package parser_test

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// TestDataFile contains a single unit test data.
type TestDataFile struct {
	Setup *Setup  `toml:"setup"`
	Cases []*Case `toml:"cases"`
}

// Setup defines the default configuration for a test case.
type Setup struct {
}

// Case is a single test case specification.
type Case struct {
	Description string `toml:"description,omitempty"`
	Input       string `toml:"input"`
	Dump        string `toml:"dump"`
}

func (c *Case) String() string {
	var b strings.Builder

	if c.Description != "" {
		b.WriteString(fmt.Sprintf("Test Case: %s\n", c.Description))
	}

	b.WriteString(fmt.Sprintf("Input (%d bytes):\n%s\n\n", len(c.Input), c.Input))
	b.WriteString(fmt.Sprintf("Expected Dump (%d bytes):\n%s\n", len(c.Dump), c.Dump))

	return b.String()
}

// MustReadTest reads the contents of file in path p.
func MustReadTest(p string) *TestDataFile {
	f, err := os.Open(p)
	if err != nil {
		log.Fatal(err)
	}

	var t TestDataFile

	_, err = toml.NewDecoder(f).Decode(&t)
	if err != nil {
		log.Fatal(err)
	}

	return &t
}
