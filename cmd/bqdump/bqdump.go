package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/goccy/go-googlesql"

	"github.com/paulourio/bqlang"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := bqlang.Init(); err != nil {
		fmt.Fprintf(stderr, "bqdump: %v\n", err)

		return 2
	}

	flagSet := flag.NewFlagSet("bqdump", flag.ContinueOnError)
	flagSet.SetOutput(stderr)

	fileFlag := flagSet.String("file", "", "filename")
	modeFlag := flagSet.String("mode", "script", "mode: script or statement")

	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2
	}

	input, err := readInput(*fileFlag, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "bqdump: %v\n", err)

		return 1
	}

	switch *modeFlag {
	case "script":
		return dumpScript(input, stdout, stderr)
	case "statement":
		return dumpStatements(input, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "bqdump: mode must be script or statement, got %s\n", *modeFlag)

		return 2
	}
}

func readInput(filename string, stdin io.Reader) (string, error) {
	if filename == "" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}

		return string(data), nil
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", filename, err)
	}

	return string(data), nil
}

func parseScript(input string, stderr io.Writer) (*googlesql.ASTScript, int) {
	out, err := googlesql.ParseScript(
		input,
		bqlang.DefaultParserOptions(),
		bqlang.DefaultErrorMessageOptions(),
	)
	if err != nil {
		fmt.Fprintln(stderr, err)

		return nil, 2
	}

	script, err := out.Script()
	if err != nil {
		fmt.Fprintf(stderr, "bqdump: extract script: %v\n", err)

		return nil, 1
	}

	return script, 0
}

func dumpScript(input string, stdout, stderr io.Writer) int {
	script, code := parseScript(input, stderr)
	if code != 0 {
		return code
	}

	bqd, err := bqlang.MarshalJSON(script)
	if err != nil {
		fmt.Fprintf(stderr, "bqdump: marshal json: %v\n", err)

		return 1
	}

	fmt.Fprintln(stdout, string(bqd))

	return 0
}

func dumpStatements(input string, stdout, stderr io.Writer) int {
	script, code := parseScript(input, stderr)
	if code != 0 {
		return code
	}

	stmtList, err := script.StatementListNode()
	if err != nil {
		fmt.Fprintf(stderr, "bqdump: statement list: %v\n", err)

		return 1
	}

	if stmtList == nil {
		return 0
	}

	num, err := stmtList.NumChildren()
	if err != nil {
		fmt.Fprintf(stderr, "bqdump: statement count: %v\n", err)

		return 1
	}

	for i := range int(num) {
		stmt, cerr := stmtList.Child(int32(i))
		if cerr != nil {
			fmt.Fprintf(stderr, "bqdump: statement %d: %v\n", i, cerr)

			return 1
		}

		bqd, merr := bqlang.MarshalJSON(stmt)
		if merr != nil {
			fmt.Fprintf(stderr, "bqdump: marshal statement %d: %v\n", i, merr)

			return 1
		}

		if i > 0 {
			fmt.Fprintln(stdout)
		}

		fmt.Fprintln(stdout, string(bqd))
	}

	return 0
}
