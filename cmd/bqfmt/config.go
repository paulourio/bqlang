package main

import (
	"github.com/paulourio/bqlang/bqconfig"
	"github.com/paulourio/bqlang/formatter"
)

type configLoader struct {
	inner *bqconfig.Loader
}

func newConfigLoader(explicitPath string) (*configLoader, error) {
	inner, err := bqconfig.NewLoader(explicitPath)
	if err != nil {
		return nil, err
	}

	return &configLoader{inner: inner}, nil
}

func (c *configLoader) optionsFor(filename string, isStdin bool) (*formatter.PrintOptions, error) {
	return c.inner.OptionsFor(filename, isStdin)
}
