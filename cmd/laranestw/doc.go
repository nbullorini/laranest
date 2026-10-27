//go:build !windows

// Command laranestw is the Windows sign-in launcher (see main.go).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "laranestw only exists on Windows")
	os.Exit(1)
}
