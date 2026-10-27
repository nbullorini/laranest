package main

import (
	"fmt"
	"os"

	"github.com/nbullorini/laranest/internal/cli"
)

func main() {
	// Invoked as the optional `php` / `composer` shim.
	if shim, err := cli.RunShim(); shim {
		fmt.Fprintln(os.Stderr, "laranest:", err)
		os.Exit(1)
	}
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
