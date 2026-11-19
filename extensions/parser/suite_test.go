package parser_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"
)

// TestDataFile contains a single unit test data.
type TestDataFile struct {
	Setup *Setup  `toml:"setup"`
	Cases []*Case `toml:"cases"`
}

// Setup defines the default configuration for a test case.
type Setup struct{}

// Case is a single test case specification.
type Case struct {
	Description string `toml:"description,omitempty"`
	Input       string `toml:"input"`
	Dump        string `toml:"dump"`
}

func (c *Case) String() string {
	var b strings.Builder

	if c.Description != "" {
		fmt.Fprintf(&b, "Test Case: %s\n", c.Description)
	}

	fmt.Fprintf(&b, "Input (%d bytes):\n%s\n\n", len(c.Input), c.Input)
	fmt.Fprintf(&b, "Expected Dump (%d bytes):\n%s\n", len(c.Dump), c.Dump)

	return b.String()
}

// MustReadTest reads the contents of file in path p.
func MustReadTest(tb testing.TB, p string) *TestDataFile {
	tb.Helper()

	f, err := os.Open(p)
	require.NoError(tb, err)

	defer func() {
		_ = f.Close()
	}()

	var td TestDataFile

	_, err = toml.NewDecoder(f).Decode(&td)
	require.NoError(tb, err)

	return &td
}
