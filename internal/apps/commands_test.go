package apps

import (
	"net"
	"slices"
	"testing"
)

func TestParseCommand(t *testing.T) {
	ok := map[string][]string{
		"php artisan migrate":                    {"php", "artisan", "migrate"},
		"laranest npm run build":                 {"npm", "run", "build"},
		`php artisan tinker --execute "echo 1;"`: {"php", "artisan", "tinker", "--execute", "echo 1;"},
		"  composer require 'predis/predis'  ":   {"composer", "require", "predis/predis"},
	}
	for line, want := range ok {
		got, err := ParseCommand(line)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("ParseCommand(%q) = %q, %v; want %q", line, got, err, want)
		}
	}
	// Only project tooling: never Laranest's own commands, nor arbitrary programs.
	for _, line := range []string{"", "laranest", "uninstall", "laranest serve", "rm -rf /", `php "unterminated`} {
		if args, err := ParseCommand(line); err == nil {
			t.Errorf("ParseCommand(%q) = %q, want an error", line, args)
		}
	}
}

func TestQuoteArgs(t *testing.T) {
	got := quoteArgs([]string{"php", "-r", "echo 1;", ""})
	want := []string{"php", "-r", `"echo 1;"`, `""`}
	if !slices.Equal(got, want) {
		t.Errorf("quoteArgs = %q, want %q", got, want)
	}
}

func TestPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if !portInUse(port) {
		t.Errorf("port %d is listening but portInUse says free", port)
	}
	ln.Close()
	if portInUse(port) {
		t.Errorf("port %d was closed but portInUse says busy", port)
	}
}
