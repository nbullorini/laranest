//go:build windows || darwin

package units

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Manager supervises processes as children of the daemon, where there is no
// systemd (Windows, macOS). Each one runs in a group of its own (a Job Object
// on Windows, a process group on macOS), so stopping it stops its whole
// process tree; it is restarted with backoff when it exits, and its output
// goes to a log file plus an in-memory backlog for the panel's live logs.
// Processes don't outlive the daemon: it starts the apps again when it boots.
type Manager struct {
	mu     sync.Mutex
	procs  map[string]*proc
	logDir string
}

func New(ctx context.Context, stateDir string) (*Manager, error) {
	dir := filepath.Join(stateDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	reapOrphans(stateDir)
	return &Manager{procs: map[string]*proc{}, logDir: dir}, nil
}

// Close stops every process: they don't outlive the daemon here.
func (m *Manager) Close() { _ = m.StopAll(context.Background()) }

// Start launches spec and keeps it running. Already-running processes are left alone.
func (m *Manager) Start(ctx context.Context, s Spec) error {
	m.mu.Lock()
	if p := m.procs[s.Unit]; p != nil {
		if st := p.status(); st != StateStopped && st != StateFailed {
			m.mu.Unlock()
			return nil
		}
	}
	p := &proc{spec: s, state: StateStarting, stop: make(chan struct{}), done: make(chan struct{})}
	m.procs[s.Unit] = p
	m.mu.Unlock()

	started := make(chan error, 1)
	go p.run(logFor(s.Unit, m.logDir), started)
	return <-started
}

// Stop stops a process and its children; stopping one that isn't running is not an error.
func (m *Manager) Stop(ctx context.Context, unit string) error {
	m.mu.Lock()
	p := m.procs[unit]
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.terminate()
}

func (m *Manager) Restart(ctx context.Context, unit string) error {
	m.mu.Lock()
	p := m.procs[unit]
	m.mu.Unlock()
	if p == nil {
		return fmt.Errorf("unit %s not loaded", unit)
	}
	if err := p.terminate(); err != nil {
		return err
	}
	return m.Start(ctx, p.spec)
}

// StopAll stops every Laranest process.
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	procs := make([]*proc, 0, len(m.procs))
	for _, p := range m.procs {
		procs = append(procs, p)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.terminate() }()
	}
	wg.Wait()
	return nil
}

func (m *Manager) Status(ctx context.Context, unit, name string) Status {
	m.mu.Lock()
	p := m.procs[unit]
	m.mu.Unlock()
	st := Status{Name: name, Unit: unit, State: StateStopped}
	if p == nil {
		return st
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st.State, st.PID, st.Restarts, st.StartedAt = p.state, p.pid, p.restarts, p.startedAt
	return st
}

type proc struct {
	spec Spec
	stop chan struct{} // closed by terminate
	done chan struct{} // closed when run returns

	mu        sync.Mutex
	state     State
	pid       uint32
	restarts  uint32
	startedAt time.Time
	group     procGroup // of the current run; zero between runs
	stopOnce  sync.Once
}

func (p *proc) status() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

func (p *proc) set(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f()
}

// run starts the process and restarts it whenever it exits, with an
// exponential backoff (1s → 30s) like the systemd units on Linux. The first
// start's result is sent on started.
func (p *proc) run(log *logBuf, started chan<- error) {
	defer close(p.done)
	delay := time.Second
	for attempt := 0; ; attempt++ {
		t0 := time.Now()
		cmd, group, err := spawn(p.spec, log)
		if err != nil {
			log.sys("Failed to start: " + err.Error())
			if attempt == 0 {
				p.set(func() { p.state = StateFailed })
				started <- err
				return
			}
		} else {
			p.set(func() {
				p.state, p.pid, p.startedAt, p.group = StateRunning, uint32(cmd.Process.Pid), t0, group
			})
			if attempt == 0 {
				started <- nil
			}
			log.sys(fmt.Sprintf("Started %s (pid %d)", p.spec.Description, cmd.Process.Pid))
			state, _ := cmd.Process.Wait()
			// The process is gone; take whatever it left behind with it.
			p.set(func() { p.group, p.pid = 0, 0 })
			killGroup(group)
			releaseGroup(group)
			if stopped(p.stop) {
				break
			}
			if state != nil {
				log.sys(fmt.Sprintf("Exited with code %d", state.ExitCode()))
			}
		}
		if stopped(p.stop) {
			break
		}
		if time.Since(t0) > time.Minute {
			delay = time.Second // it ran fine for a while: not a crash loop
		}
		p.set(func() { p.state = StateBackoff; p.restarts++ })
		log.sys(fmt.Sprintf("Restarting in %s", delay))
		select {
		case <-p.stop:
		case <-time.After(delay):
		}
		if stopped(p.stop) {
			break
		}
		delay = min(delay*2, 30*time.Second)
	}
	p.set(func() { p.state = StateStopped })
	log.sys("Stopped")
}

// terminate stops the process tree and waits for the run loop to finish.
func (p *proc) terminate() error {
	p.stopOnce.Do(func() {
		p.set(func() {
			if p.state != StateStopped && p.state != StateFailed {
				p.state = StateStopping
			}
			close(p.stop)
			if p.group != 0 {
				stopGroup(p.group)
			}
		})
	})
	select {
	case <-p.done:
		return nil
	case <-time.After(15 * time.Second):
		return errors.New("timed out stopping " + p.spec.Unit)
	}
}

func stopped(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// ---- logs ----

const backlogLines = 1000

// logBuf keeps a unit's recent output, appends it to a log file and fans it
// out to live followers.
type logBuf struct {
	mu    sync.Mutex
	lines []Line
	subs  map[chan Line]bool
	path  string
}

var logBufs = struct {
	sync.Mutex
	m map[string]*logBuf
}{m: map[string]*logBuf{}}

// logFor returns the unit's buffer, created on first use (followers can
// subscribe before the process starts).
func logFor(unit, dir string) *logBuf {
	logBufs.Lock()
	defer logBufs.Unlock()
	b := logBufs.m[unit]
	if b == nil {
		b = &logBuf{subs: map[chan Line]bool{}}
		logBufs.m[unit] = b
	}
	if dir != "" && b.path == "" {
		b.path = filepath.Join(dir, strings.TrimSuffix(unit, ".service")+".log")
		if info, err := os.Stat(b.path); err == nil && info.Size() > 10<<20 {
			_ = os.Rename(b.path, b.path+".1")
		}
	}
	return b
}

func (b *logBuf) sys(text string) { b.add(Line{Time: time.Now(), Stream: "sys", Text: text}) }

func (b *logBuf) add(l Line) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, l)
	if len(b.lines) > backlogLines {
		b.lines = append(b.lines[:0:0], b.lines[len(b.lines)-backlogLines:]...)
	}
	for ch := range b.subs {
		select {
		case ch <- l:
		default: // a slow follower loses lines rather than blocking the process
		}
	}
	if b.path != "" {
		if f, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			fmt.Fprintf(f, "%s [%s] %s\n", l.Time.Format(time.RFC3339), l.Stream, l.Text)
			f.Close()
		}
	}
}

func (b *logBuf) copy(r *os.File) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		b.add(Line{Time: time.Now(), Stream: "out", Text: strings.TrimRight(sc.Text(), "\r")})
	}
}

// Follow sends the last backlog lines of unit's output and then new ones,
// until ctx is cancelled.
func Follow(ctx context.Context, unit string, backlog int, fn func(Line)) error {
	b := logFor(unit, "")
	ch := make(chan Line, 256)
	b.mu.Lock()
	recent := b.lines[max(0, len(b.lines)-backlog):]
	recent = append([]Line(nil), recent...)
	b.subs[ch] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}()
	for _, l := range recent {
		fn(l)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case l := <-ch:
			fn(l)
		}
	}
}
