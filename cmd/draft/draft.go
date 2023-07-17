package main

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

func main() {
	f, err := os.Open("testdata/case_as_is.toml")
	if err != nil {
		panic(err)
	}

	var d any

	x, err := toml.NewDecoder(f).Decode(&d)
	if err != nil {
		panic(err)
	}

	fmt.Printf("decode ret: %#v\n", x)
	fmt.Printf("decode data: %#v\n", d)
}
