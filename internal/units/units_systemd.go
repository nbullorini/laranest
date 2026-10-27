//go:build !windows

package units

import (
	"context"
	"fmt"
	"strings"
	"time"

	sd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

type Manager struct {
	conn *sd.Conn
}

// New connects to the systemd user manager. stateDir is unused: the journal
// keeps the logs.
func New(ctx context.Context, stateDir string) (*Manager, error) {
	conn, err := sd.NewUserConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to systemd --user: %w", err)
	}
	return &Manager{conn: conn}, nil
}

func (m *Manager) Close() { m.conn.Close() }

// Start launches spec as a transient unit. Already-running units are left alone.
func (m *Manager) Start(ctx context.Context, s Spec) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if st := m.Status(ctx, s.Unit, s.Name); st.State != StateStopped && st.State != StateFailed {
		return nil
	}
	_ = m.conn.ResetFailedUnitContext(ctx, s.Unit) // a failed unit with the same name blocks the start

	props := []sd.Property{
		sd.PropDescription(s.Description),
		sd.PropExecStart(s.Cmd, false),
		sd.PropSlice(Slice),
		prop("WorkingDirectory", s.Dir),
		prop("Environment", s.Env),
		prop("Restart", "always"),
		prop("RestartUSec", usec(time.Second)),
		prop("RestartSteps", uint32(5)), // exponential backoff 1s → 30s
		prop("RestartMaxDelayUSec", usec(30*time.Second)),
		prop("StartLimitIntervalUSec", uint64(0)), // never give up restarting
		prop("TimeoutStopUSec", usec(10*time.Second)),
		prop("CollectMode", "inactive-or-failed"),
		prop("SyslogIdentifier", s.Unit),
	}
	return m.wait(ctx, func(ch chan<- string) (int, error) {
		return m.conn.StartTransientUnitContext(ctx, s.Unit, "replace", props, ch)
	})
}

// Stop stops a unit; stopping a unit that doesn't exist is not an error.
func (m *Manager) Stop(ctx context.Context, unit string) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	err := m.wait(ctx, func(ch chan<- string) (int, error) {
		return m.conn.StopUnitContext(ctx, unit, "replace", ch)
	})
	if err != nil && strings.Contains(err.Error(), "not loaded") {
		return nil
	}
	return err
}

func (m *Manager) Restart(ctx context.Context, unit string) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	return m.wait(ctx, func(ch chan<- string) (int, error) {
		return m.conn.RestartUnitContext(ctx, unit, "replace", ch)
	})
}

// StopAll stops every Laranest process.
func (m *Manager) StopAll(ctx context.Context) error {
	return m.Stop(ctx, Slice)
}

func (m *Manager) Status(ctx context.Context, unit, name string) Status {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	st := Status{Name: name, Unit: unit, State: StateStopped}
	props, err := m.conn.GetUnitPropertiesContext(ctx, unit)
	if err != nil || props["LoadState"] != "loaded" {
		return st
	}
	if svc, err := m.conn.GetUnitTypePropertiesContext(ctx, unit, "Service"); err == nil {
		st.PID, _ = svc["MainPID"].(uint32)
		st.Restarts, _ = svc["NRestarts"].(uint32)
		if ts, ok := svc["ExecMainStartTimestamp"].(uint64); ok && ts > 0 {
			st.StartedAt = time.UnixMicro(int64(ts))
		}
	}
	active, _ := props["ActiveState"].(string)
	sub, _ := props["SubState"].(string)
	switch {
	case active == "active":
		st.State = StateRunning
	case sub == "auto-restart":
		st.State = StateBackoff
	case active == "activating":
		st.State = StateStarting
	case active == "deactivating":
		st.State = StateStopping
	case active == "failed":
		st.State = StateFailed
	}
	return st
}

// wait runs a systemd job and blocks until it finishes.
func (m *Manager) wait(ctx context.Context, job func(chan<- string) (int, error)) error {
	ch := make(chan string, 1)
	if _, err := job(ch); err != nil {
		return err
	}
	select {
	case res := <-ch:
		if res != "done" {
			return fmt.Errorf("systemd job %s", res)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// withTimeout bounds each systemd call so a stuck bus can't hang the API.
func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 30*time.Second)
}

func prop(name string, v any) sd.Property {
	return sd.Property{Name: name, Value: dbus.MakeVariant(v)}
}

func usec(d time.Duration) uint64 { return uint64(d.Microseconds()) }
