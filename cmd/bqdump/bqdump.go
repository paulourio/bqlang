package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/goccy/go-zetasql"

	"github.com/paulourio/bqlang"
)

func main() {
	var input string

	file := flag.String("file", "", "filename")
	mode := flag.String("mode", "script", "mode, script or statement")
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

	switch *mode {
	case "script":
		dumpScript(input)
	case "statement":
		dumpStatements(input)
	default:
		log.Fatalf("mode must be script or statement, got %s\n", *mode)
	}

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
