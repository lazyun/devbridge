package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/lazyun/devbridge/internal/runtimeaudit"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ledger <internal-root>")
		os.Exit(2)
	}
	entries, err := runtimeaudit.Scan(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(entries); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
