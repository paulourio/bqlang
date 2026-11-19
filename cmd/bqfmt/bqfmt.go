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
	"strings"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
)

type cliRunner struct {
	list   bool
	write  bool
	debug  bool
	fdSem  chan struct{}
	stdin  io.Reader
	stderr io.Writer
}

const maxOpenFiles = 200

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := bqlang.Init(); err != nil {
		fmt.Fprintf(stderr, "bqfmt: %v\n", err)

		return 2
	}

	flagSet := flag.NewFlagSet("bqfmt", flag.ContinueOnError)
	flagSet.SetOutput(stderr)

	listFlag := flagSet.Bool("l", false, "list files whose formatting differs from bqfmt's")
	writeFlag := flagSet.Bool("w", false, "write result to (source) file instead of stdout")
	debugFlag := flagSet.Bool("debug", false, "emit formatter debug trace to stderr")

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

	runner := &cliRunner{
		list:   *listFlag,
		write:  *writeFlag,
		debug:  *debugFlag,
		fdSem:  make(chan struct{}, maxOpenFiles),
		stdin:  stdin,
		stderr: stderr,
	}

	maxWeight := (2 << 20) * int64(runtime.GOMAXPROCS(0))
	seq := newSequencer(maxWeight, stdout, stderr)
	runner.execute(seq, flagSet.Args())

	return seq.GetExitCode()
}

func (c *cliRunner) execute(s *sequencer, paths []string) {
	if len(paths) == 0 {
		if c.write {
			s.AddReport(errors.New("error: cannot use -w with standard input"))

			return
		}

		s.Add(0, func(r *reporter) error {
			return c.processFile("<standard input>", nil, c.stdin, r)
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
			fileArg := target

			s.Add(fileWeight(fileArg, info), func(r *reporter) error {
				return c.processFile(fileArg, info, nil, r)
			})

			continue
		}

		walkErr := filepath.WalkDir(target, func(path string, entry fs.DirEntry, werr error) error {
			if werr != nil || !isBQLFile(entry) {
				return werr
			}

			entryInfo, ierr := entry.Info()
			if ierr != nil {
				s.AddReport(ierr)

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
}

func isBQLFile(f fs.DirEntry) bool {
	name := f.Name()
	validSuffix := strings.HasSuffix(name, ".bql") ||
		strings.HasSuffix(name, ".sql") ||
		strings.HasSuffix(name, ".bql.j2") ||
		strings.HasSuffix(name, ".sql.j2")

	return !strings.HasPrefix(name, ".") && !f.IsDir() && validSuffix
}

// processFile formats a single file or standard input (when info == nil).
func (c *cliRunner) processFile(filename string, info fs.FileInfo, in io.Reader, r *reporter) error {
	src, err := c.readFile(filename, info, in)
	if err != nil {
		return err
	}

	printOpts := formatter.DefaultPrintOptions()

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

	debugBuf.Reset()

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
	if !c.list && !c.write {
		if _, werr := r.Write([]byte(result)); werr != nil {
			return fmt.Errorf("%s: write stdout: %w", filename, werr)
		}

		return nil
	}

	if input == result {
		return nil
	}

	if c.list {
		fmt.Fprintln(r, filename)
	}

	if c.write {
		if info == nil {
			return errors.New("error: cannot use -w with standard input")
		}

		perm := info.Mode().Perm()
		if werr := c.writeFile(filename, src, []byte(result), perm, info.Size()); werr != nil {
			return werr
		}
	}

	return nil
}
