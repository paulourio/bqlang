package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
	"github.com/paulourio/bqlang/internal/bqconfig"
)

var version = "dev"

type cliRunner struct {
	list          bool
	write         bool
	diff          bool
	check         bool
	debug         bool
	stdinFilename string
	cfg           *configLoader
	fdSem         chan struct{}
	stdin         io.Reader
}

const maxOpenFiles = 200

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flagSet := flag.NewFlagSet("bqfmt", flag.ContinueOnError)
	flagSet.SetOutput(stderr)

	listFlag := flagSet.Bool("l", false, "list files whose formatting differs from bqfmt's")
	writeFlag := flagSet.Bool("w", false, "write result to (source) file instead of stdout")
	diffFlag := flagSet.Bool("d", false, "display diffs instead of rewriting files")
	checkFlag := flagSet.Bool("check", false, "exit with code 1 if any file's formatting differs from bqfmt's")
	configFlag := flagSet.String("config", "", "path to .bqfmt.toml configuration file")
	stdinFilenameFlag := flagSet.String(
		"stdin-filename",
		"",
		"filename for config discovery, style matching, and diagnostics when reading standard input",
	)
	debugFlag := flagSet.Bool("debug", false, "emit formatter debug trace to stderr")
	versionFlag := flagSet.Bool("version", false, "print bqfmt version and exit")

	flagSet.Usage = func() {
		fmt.Fprintf(stderr, "usage: bqfmt [flags] [path ...]\n")
		flagSet.PrintDefaults()
	}

	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2
	}

	if *versionFlag {
		fmt.Fprintf(stdout, "bqfmt %s\n", versionString())

		return 0
	}

	if *checkFlag && *writeFlag {
		fmt.Fprintln(stderr, "bqfmt: cannot use -check with -w")

		return 2
	}

	if *stdinFilenameFlag != "" && flagSet.NArg() > 0 {
		fmt.Fprintln(stderr, "bqfmt: cannot use -stdin-filename with file arguments")

		return 2
	}

	if err := bqlang.Init(); err != nil {
		fmt.Fprintf(stderr, "bqfmt: %v\n", err)

		return 2
	}

	cfgLoader, err := newConfigLoader(*configFlag)
	if err != nil {
		fmt.Fprintf(stderr, "bqfmt: %v\n", err)

		return 2
	}

	runner := &cliRunner{
		list:          *listFlag,
		write:         *writeFlag,
		diff:          *diffFlag,
		check:         *checkFlag,
		debug:         *debugFlag,
		stdinFilename: *stdinFilenameFlag,
		cfg:           cfgLoader,
		fdSem:         make(chan struct{}, maxOpenFiles),
		stdin:         stdin,
	}

	maxWeight := (2 << 20) * int64(runtime.GOMAXPROCS(0))
	seq := newSequencer(maxWeight, stdout, stderr)
	runner.execute(seq, flagSet.Args())

	return seq.GetExitCode()
}

func versionString() string {
	if version != "" && version != "dev" {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}

	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	var (
		rev      string
		modified bool
	)

	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}

	if rev == "" {
		return "dev"
	}

	if len(rev) > 12 {
		rev = rev[:12]
	}

	if modified {
		return "dev+" + rev + "-dirty"
	}

	return "dev+" + rev
}

func (c *cliRunner) execute(s *sequencer, paths []string) {
	if len(paths) == 0 {
		if c.write {
			s.AddReport(errors.New("error: cannot use -w with standard input"))

			return
		}

		name := "<standard input>"
		if c.stdinFilename != "" {
			name = c.stdinFilename
		}

		s.Add(0, func(r *reporter) error {
			return c.processFile(name, nil, c.stdin, r)
		})

		return
	}

	for _, target := range paths {
		info, err := os.Stat(target)
		if err != nil {
			s.AddReport(err)

			continue
		}

		if !info.IsDir() {
			s.Add(fileWeight(target, info), func(r *reporter) error {
				return c.processFile(target, info, nil, r)
			})

			continue
		}

		c.walkDirectory(s, target)
	}
}

func (c *cliRunner) walkDirectory(s *sequencer, target string) {
	walkErr := filepath.WalkDir(target, func(path string, entry fs.DirEntry, werr error) error {
		if werr != nil {
			s.AddReport(werr)

			return nil
		}

		if entry.IsDir() {
			if path != target && isSkippedDir(entry.Name()) {
				return filepath.SkipDir
			}

			return nil
		}

		if !isBQLFile(entry) {
			return nil
		}

		entryInfo, ok, ierr := resolveWalkEntry(path, entry)
		if ierr != nil {
			s.AddReport(ierr)

			return nil
		}

		if !ok {
			return nil
		}

		s.Add(fileWeight(path, entryInfo), func(r *reporter) error {
			return c.processFile(path, entryInfo, nil, r)
		})

		return nil
	})
	if walkErr != nil {
		s.AddReport(walkErr)
	}
}

func resolveWalkEntry(path string, entry fs.DirEntry) (fs.FileInfo, bool, error) {
	if entry.Type() == fs.ModeSymlink {
		info, err := os.Stat(path)
		if err != nil {
			return nil, false, err
		}

		if info.IsDir() {
			return nil, false, nil
		}

		return info, true, nil
	}

	info, err := entry.Info()
	if err != nil {
		return nil, false, err
	}

	return info, true, nil
}

func isSkippedDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules"
}

func isBQLFile(f fs.DirEntry) bool {
	name := f.Name()

	return !strings.HasPrefix(name, ".") && !f.IsDir() && bqconfig.IsBQLFilename(name)
}

// processFile formats a single file or standard input (when info == nil).
func (c *cliRunner) processFile(filename string, info fs.FileInfo, in io.Reader, r *reporter) error {
	src, err := c.readFile(filename, info, in)
	if err != nil {
		return err
	}

	useStdinDiscovery := info == nil && c.stdinFilename == ""

	printOpts, err := c.cfg.optionsFor(filename, useStdinDiscovery)
	if err != nil {
		return err
	}

	input := string(src)

	var (
		debugBuf bytes.Buffer
		fmtOpts  = []formatter.Option{formatter.WithPrintOptions(printOpts)}
	)

	if c.debug {
		logger := log.New(&debugBuf, "[bqfmt] ", 0)
		fmtOpts = append(fmtOpts, formatter.WithLogger(logger))
	}

	bqFormatter := formatter.NewBigQueryFormatter(fmtOpts...)

	result, err := bqFormatter.Format(input)

	if debugBuf.Len() > 0 {
		r.Warnf("%s", debugBuf.String())
	}

	if err != nil {
		return fmt.Errorf("%s: cannot format input: %w", filename, err)
	}

	bqFormatter.Logger = nil

	result2, err := bqFormatter.Format(result)
	if err != nil {
		return fmt.Errorf("%s: re-formatting failed: %w", filename, err)
	}

	if result != result2 {
		return fmt.Errorf("%s: formatting is not idempotent", filename)
	}

	return c.emitResult(filename, info, src, input, result, r)
}

func (c *cliRunner) emitResult(
	filename string,
	info fs.FileInfo,
	src []byte,
	input string,
	result string,
	r *reporter,
) error {
	if !c.list && !c.write && !c.diff && !c.check {
		if _, werr := r.Write([]byte(result)); werr != nil {
			return fmt.Errorf("%s: write stdout: %w", filename, werr)
		}

		return nil
	}

	if input == result {
		return nil
	}

	if c.check {
		r.MarkDiff()
	}

	if c.list {
		fmt.Fprintln(r, filename)
	}

	if c.diff {
		if _, werr := r.Write([]byte(unifiedDiff(filename, input, result))); werr != nil {
			return fmt.Errorf("%s: write diff: %w", filename, werr)
		}
	}

	if c.write {
		if info == nil {
			return errors.New("error: cannot use -w with standard input")
		}

		perm := info.Mode().Perm()
		if werr := c.writeFile(filename, src, []byte(result), perm, r); werr != nil {
			return werr
		}
	}

	return nil
}
