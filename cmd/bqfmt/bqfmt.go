package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path"

	"github.com/goccy/go-zetasql"

	"github.com/paulourio/bqlang"
	"github.com/paulourio/bqlang/formatter"
)

func main() {
	var input string
	var err error

	logName := mustGetLogFile()

	logFile, err := os.OpenFile(logName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0664)
	if err != nil {
		panic(err)
	}
	defer func() { logFile.Close() }()

	logger := log.New(logFile, "", 0)

	file := flag.String("file", "", "filename")
	// mode := flag.String("mode", "script", "mode, script or statement")
	flag.Parse()

	switch *file {
	case "":
		d, err := io.ReadAll(os.Stdin)
		if err != nil {
			log.Fatal(err)
		}

		input = string(d)
	default:
		d, err := os.ReadFile(*file)
		if err != nil {
			log.Fatal(err)
		}

		input = string(d)
	}

	fmtopts := &formatter.PrintOptions{
		SoftMaxColumns:                80,
		NewlineBeforeClause:           true,
		AlignLogicalWithClauses:       true,
		AlignTrailingComments:         true,
		ColumnListTrailingComma:       formatter.Auto,
		Indentation:                   1,
		IndentCaseWhen:                true,
		IndentWithClause:              true,
		IndentWithEntries:             true,
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
		formatter.WithLogger(logger), formatter.WithPrintOptions(fmtopts))

	result, err := formatter.Format(input)
	if err != nil {
		log.Fatal("Cannot format input: ", err)
	}

	fmt.Println(result)

	// Check for idempotency
	result2, err := formatter.Format(result)
	if err != nil {
		log.Fatalf("Failed to validate formatted output when re-formatting: %v",
			err)
	}

	if result != result2 {
		log.Fatal("Failed to validate: format is not idempotent")
	}

	os.Exit(0)
}

func mustGetLogFile() string {
	h, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Sprintf("Failed to get user home directory: %v", err))
	}

	return path.Join(h, "formatter.log")
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
