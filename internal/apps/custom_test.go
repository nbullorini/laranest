package apps

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nbullorini/laranest/internal/config"
	"github.com/nbullorini/laranest/internal/detect"
	"github.com/nbullorini/laranest/internal/php"
)

func TestValidateAdvanced(t *testing.T) {
	ok := []struct {
		commands map[string]string
		custom   []CustomProcess
		webMode  string
	}{
		{map[string]string{"queue": "php artisan queue:work --queue=high,default"}, nil, ""},
		{nil, []CustomProcess{{Name: "stripe", Command: "stripe listen --forward-to {url}/stripe/webhook"}}, ""},
		{map[string]string{"web": "php artisan octane:start --port={port}"}, nil, WebModePort},
		{map[string]string{"reverb": "php artisan reverb:start --port={reverb_port} --debug"}, nil, ""},
	}
	for _, c := range ok {
		if err := validateAdvanced(c.commands, c.custom, c.webMode); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	bad := map[string]struct {
		commands map[string]string
		custom   []CustomProcess
		webMode  string
	}{
		"unknown process":     {map[string]string{"cron": "x"}, nil, ""},
		"web without command": {nil, nil, WebModePort},
		"web without {port}":  {map[string]string{"web": "php artisan octane:start"}, nil, WebModePort},
		"{port} outside web":  {map[string]string{"queue": "php artisan queue:work --port={port}"}, nil, ""},
		"unknown placeholder": {nil, []CustomProcess{{Name: "x", Command: "echo {nope}"}}, ""},
		"built-in name":       {nil, []CustomProcess{{Name: "queue", Command: "php artisan queue:work"}}, ""},
		"duplicate name":      {nil, []CustomProcess{{Name: "a", Command: "x"}, {Name: "a", Command: "y"}}, ""},
		"invalid name":        {nil, []CustomProcess{{Name: "My Process", Command: "x"}}, ""},
		"empty command":       {nil, []CustomProcess{{Name: "a", Command: "  "}}, ""},
		"unterminated quote":  {nil, []CustomProcess{{Name: "a", Command: `php -r "echo`}}, ""},
		"unknown web mode":    {nil, nil, "octane"},
	}
	for name, c := range bad {
		if err := validateAdvanced(c.commands, c.custom, c.webMode); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestEnabledProcessesIncludeCustom(t *testing.T) {
	a := &App{Processes: detect.Processes{Queue: true}, Custom: []CustomProcess{
		{Name: "stripe", Command: "stripe listen"}, {Name: "pulse", Command: "php artisan pulse:check", Disabled: true},
	}}
	if got := enabledProcesses(a); !slices.Equal(got, []string{"web", "queue", "stripe"}) {
		t.Errorf("enabled = %v", got)
	}
	if names := processNames(a); !slices.Contains(names, "pulse") {
		t.Errorf("processNames must include disabled custom processes (to stop them): %v", names)
	}
}

func TestResolveCommand(t *testing.T) {
	m := &Manager{cfg: config.Config{Domain: "localhost", HTTPSPort: 443}}
	a := &App{ID: "shop", Path: "/srv/shop", HTTPS: true, WebPort: 18123, ReverbPort: 18080}
	prep := prepared{rt: php.Runtime{CLI: "/opt/php/8.4/php"}, nodeBin: "/opt/node/bin", composer: "/opt/composer.phar"}

	got, err := m.resolveCommand(a, "php artisan octane:start --port={port}", prep, "")
	if err != nil || !slices.Equal(got, []string{"/opt/php/8.4/php", "artisan", "octane:start", "--port=18123"}) {
		t.Errorf("php: %q %v", got, err)
	}
	got, _ = m.resolveCommand(a, "composer run dev", prep, "")
	if !slices.Equal(got, []string{"/opt/php/8.4/php", "/opt/composer.phar", "run", "dev"}) {
		t.Errorf("composer: %q", got)
	}
	if got, err := m.resolveCommand(a, `stripe listen`, prep, t.TempDir()); err == nil {
		t.Errorf("a program that isn't installed must fail, got %q", got)
	}

	// Programs are looked up in the processes' PATH.
	dir := t.TempDir()
	name := "stripe"
	if runtime.GOOS == "windows" {
		name = "stripe.exe"
	}
	os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755)
	got, err = m.resolveCommand(a, `stripe listen --forward-to "{url}/stripe/webhook"`, prep, dir)
	if err != nil || got[0] != filepath.Join(dir, name) || !strings.HasSuffix(got[3], "https://shop.localhost/stripe/webhook") {
		t.Errorf("stripe: %q %v", got, err)
	}
}
