// Package units runs app processes and keeps them running.
//
// On Linux they are transient systemd *user* units: systemd handles restarts
// with backoff, kills whole cgroups on stop (no orphans), stores logs in the
// journal and keeps apps running while the Laranest daemon itself restarts or
// is upgraded. Windows has no equivalent, so there the daemon supervises them
// itself (see supervisor_windows.go).
package units

import "time"

// Slice groups every Laranest process, so they can be stopped together.
const Slice = "laranest.slice"

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateBackoff  State = "backoff" // exited, will be restarted
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

// Spec describes one process.
type Spec struct {
	Unit        string // laranest-miapp-web.service
	Name        string // web
	Description string
	Cmd         []string
	Dir         string
	Env         []string
}

// Line is one log line of a process.
type Line struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"` // "out" (the process) or "sys" (the supervisor: started, restarting…)
	Text   string    `json:"text"`
}

// Status is a point-in-time view of a unit, safe to serialize.
type Status struct {
	Name      string    `json:"name"`
	Unit      string    `json:"unit"`
	State     State     `json:"state"`
	PID       uint32    `json:"pid,omitempty"`
	Restarts  uint32    `json:"restarts"`
	StartedAt time.Time `json:"started_at,omitzero"`
	Command   []string  `json:"command,omitempty"`
}
