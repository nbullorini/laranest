package detect

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// ProjectFile is a project's own Laranest settings, committed with its code
// so the whole team runs it the same way. Everything is optional: what is
// missing stays automatic. What only makes sense on one machine (the app's
// name, its group, dumps, Xdebug) is not part of it.
const ProjectFile = "laranest.json"

type ProjectConfig struct {
	PHP   *string `json:"php,omitempty"`   // "8.4"; "" = from composer.json
	HTTPS *bool   `json:"https,omitempty"` // default true
	// Processes turns built-in processes on or off; omitted ones keep
	// Laranest's recommendation.
	Processes map[string]bool `json:"processes,omitempty"`
	// Commands, Custom and WebMode are the advanced settings (see the
	// panel): command overrides, extra processes, and the web on a port.
	Commands map[string]string `json:"commands,omitempty"`
	Custom   []CustomCommand   `json:"custom,omitempty"`
	WebMode  *string           `json:"web_mode,omitempty"`
}

type CustomCommand struct {
	Name     string `json:"name"`
	Command  string `json:"command"`
	Disabled bool   `json:"disabled,omitempty"`
}

var processKeys = []string{"queue", "horizon", "scheduler", "reverb", "vite"}

// ReadProject reads the project's laranest.json: nil (and no error) if it
// has none. sum identifies the content, to notice when it changes.
func ReadProject(dir string) (cfg *ProjectConfig, sum string, err error) {
	data, err := os.ReadFile(filepath.Join(dir, ProjectFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	h := sha256.Sum256(data)
	sum = hex.EncodeToString(h[:])
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // a typo would be silently ignored otherwise
	cfg = &ProjectConfig{}
	if err := dec.Decode(cfg); err != nil {
		return nil, sum, fmt.Errorf("%s: %w", ProjectFile, err)
	}
	for k := range cfg.Processes {
		if !slices.Contains(processKeys, k) {
			return nil, sum, fmt.Errorf("%s: unknown process %q (queue, horizon, scheduler, reverb or vite)", ProjectFile, k)
		}
	}
	return cfg, sum, nil
}

// ApplyProcesses turns on or off the processes the file names.
func (c *ProjectConfig) ApplyProcesses(p Processes) Processes {
	for k, on := range c.Processes {
		switch k {
		case "queue":
			p.Queue = on
		case "horizon":
			p.Horizon = on
		case "scheduler":
			p.Scheduler = on
		case "reverb":
			p.Reverb = on
		case "vite":
			p.Vite = on
		}
	}
	return p
}

// ProcessMap is p in the file's format.
func ProcessMap(p Processes) map[string]bool {
	return map[string]bool{"queue": p.Queue, "horizon": p.Horizon, "scheduler": p.Scheduler, "reverb": p.Reverb, "vite": p.Vite}
}
