package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/goccy/go-zetasql"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
)

var (
	list  = flag.Bool("l", false, "list files whose formatting differs from gofmt's")
	write = flag.Bool("w", false, "write result to (source) file instead of stdout")
)

const tmpDir = "/tmp/bqfmt"

func usage() {
	fmt.Fprintf(os.Stderr, "usage: bqfmt [flags] [path ...]\n")
	flag.PrintDefaults()
}

func isBQLFile(f fs.DirEntry) bool {
	// ignore non-Go files
	name := f.Name()
	validSuffix := (strings.HasSuffix(name, ".bql") ||
		strings.HasSuffix(name, ".sql") ||
		strings.HasSuffix(name, ".bql.j2") ||
		strings.HasSuffix(name, ".sql.j2"))

	return !strings.HasPrefix(name, ".") && !f.IsDir() && validSuffix
}

func main() {
	// Arbitrarily limit in-flight work to 2MiB times the number of threads.
	//
	// The actual overhead for the parse tree and output will depend on the
	// specifics of the file, but this at least keeps the footprint of the process
	// roughly proportional to GOMAXPROCS.
	maxWeight := (2 << 20) * int64(runtime.GOMAXPROCS(0))
	s := newSequencer(maxWeight, os.Stdout, os.Stderr)

	// call gofmtMain in a separate function
	// so that it can use defer and have them
	// run before the exit.
	bqfmtMain(s)
	os.Exit(s.GetExitCode())
}

func bqfmtMain(s *sequencer) {
	flag.Usage = usage
	flag.Parse()

	if err := os.MkdirAll(tmpDir, 0777); err != nil {
		panic(err)
	}

	args := flag.Args()
	if len(args) == 0 {
		if *write {
			s.AddReport(fmt.Errorf("error: cannot use -w with standard input"))
			return
		}

		s.Add(0, func(r *reporter) error {
			return processFile("<standard input>", nil, os.Stdin, r)
		})

		return
	}

	for _, arg := range args {
		switch info, err := os.Stat(arg); {
		case err != nil:
			s.AddReport(err)
		case !info.IsDir():
			// Non-directory arguments are always formatted.
			arg := arg

			s.Add(fileWeight(arg, info), func(r *reporter) error {
				return processFile(arg, info, nil, r)
			})
		default:
			// Directories are walked, ignoring non-Go files.
			err := filepath.WalkDir(arg, func(path string, f fs.DirEntry, err error) error {
				if err != nil || !isBQLFile(f) {
					return err
				}

				info, err := f.Info()
				if err != nil {
					s.AddReport(err)
					return nil
				}

				s.Add(fileWeight(path, info), func(r *reporter) error {
					return processFile(path, info, nil, r)
				})

				return nil
			})
			if err != nil {
				s.AddReport(err)
			}
		}
	}
}

// If info == nil, we are formatting stdin instead of a file.
// If in == nil, the source is the contents of the file with the given filename.
func processFile(filename string, info fs.FileInfo, in io.Reader, r *reporter) error {
	src, err := readFile(filename, info, in)
	if err != nil {
		return err
	}

	input := string(src)

	logFile := mustGetLogFile()
	defer func() { logFile.Close() }()

	logger := log.New(logFile, "", 0)

	fmtopts := &formatter.PrintOptions{
		SoftMaxColumns:                80,
		NewlineBeforeClause:           true,
		AlignLogicalWithClauses:       true,
		AlignTrailingComments:         true,
		ColumnListTrailingComma:       formatter.Auto,
		Indentation:                   1,
		IndentCaseWhen:                true,
		IndentWithClause:              true,
		IndentWithEntries:             false,
		MinJoinsToSeparateInBlocks:    2,
		MaxColumnsForSingleLineSelect: 4,
		FunctionCatalog:               formatter.BigQueryCatalog,
		FunctionNameStyle:             formatter.AsIs,
		BoolStyle:                     formatter.UpperCase,
		BytesStyle:                    formatter.PreferSingleQuote,
		HexStyle:                      formatter.UpperCase,
		IdentifierStyle:               formatter.AsIs,
		KeywordStyle:                  formatter.UpperCase,
		NullStyle:                     formatter.UpperCase,
		StringStyle:                   formatter.PreferSingleQuote,
		TypeStyle:                     formatter.UpperCase,
	}

	formatter := formatter.NewBigQueryFormatter(
		formatter.WithLogger(logger),
		formatter.WithPrintOptions(fmtopts))

	result, err := formatter.Format(input)
	if err != nil {
		return fmt.Errorf("%s: cannot format input: %w", err)
	}

	// Check for idempotency
	result2, err := formatter.Format(result)
	if err != nil {
		return fmt.Errorf("%s: re-formatting failed: %v", err)
	}

	if result != result2 {
		return fmt.Errorf("%s: formatting is not idempotent")
	}

	if input != result {
		// formatting has changed
		if *list {
			fmt.Fprintln(r, filename)
		}

		if *write {
			if info == nil {
				panic("-w should not have been allowed with stdin")
			}

			perm := info.Mode().Perm()
			if err := writeFile(filename, src, []byte(result), perm, info.Size()); err != nil {
				return err
			}
		}
	}

	return nil
}

func mustGetLogFile() *os.File {
	t := time.Now()

	f, err := os.CreateTemp(tmpDir, t.Format("2006-01-02-*"))
	if err != nil {
		panic(fmt.Sprintf("Failed to create log file: %v", err))
	}

	return f
}

func dumpScript(input string) {
	n, err := zetasql.ParseScript(input, nil, zetasql.ErrorMessageMultiLineWithCaret)
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}

	bqd, err := bqlang.MarshalJSON(n)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(bqd))
}

func dumpStatements(input string) {
	loc := zetasql.NewParseResumeLocation(input)

	for {
		n, eoi, err := zetasql.ParseNextScriptStatement(loc, nil)
		if err != nil {
			fmt.Println(err)
			os.Exit(2)
		}

		bqd, err := bqlang.MarshalJSON(n)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println(string(bqd))

		if eoi {
			os.Exit(1)
		}

		fmt.Println("")
	}
}
