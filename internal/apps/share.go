package apps

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nbullorini/laranest/internal/units"
)

// Sharing: a running app can be reached from the internet through a
// Cloudflare quick tunnel (no account): cloudflared runs as the app's "share"
// process and prints a random https://<words>.trycloudflare.com address.
// Requests arrive at the proxy's HTTP port with that host, which is routed to
// the app as if it were served over HTTPS, so Laravel builds its URLs with it.

var tunnelURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Share starts the app's tunnel (or returns the running one's address).
func (m *Manager) Share(id string) (View, error) {
	m.mu.Lock()
	a := m.store.apps[id]
	if a == nil {
		m.mu.Unlock()
		return View{}, ErrNotFound
	}
	running, shared := m.isRunning(a), m.shares[id]
	m.mu.Unlock()
	if shared != "" {
		return m.view(a), nil
	}
	if !running {
		return View{}, errors.New("start the app before sharing it")
	}
	bin, err := m.rt.Tools.Cloudflared(context.Background())
	if err != nil {
		return View{}, err
	}
	// An empty config of our own: a ~/.cloudflared/config.yml for a named
	// tunnel would make the quick tunnel fail.
	conf := filepath.Join(m.rt.DataDir, "cloudflared", "quick-tunnel.yml")
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		return View{}, err
	}
	if err := os.WriteFile(conf, nil, 0o644); err != nil {
		return View{}, err
	}
	unit := unitName(id, "share")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	found := make(chan string, 1)
	var (
		mu   sync.Mutex
		last []string // to explain a failure
	)
	go units.Follow(ctx, unit, 0, func(l units.Line) {
		if u := tunnelURL.FindString(l.Text); u != "" {
			select {
			case found <- u:
			default:
			}
		}
		mu.Lock()
		last = append(last[max(0, len(last)-4):], l.Text)
		mu.Unlock()
	})
	time.Sleep(200 * time.Millisecond) // let the follower attach before the first line
	err = m.units.Start(context.Background(), units.Spec{
		Unit: unit, Name: "share", Description: fmt.Sprintf("Laranest %s (share)", id), Dir: a.Path,
		Cmd: []string{bin, "tunnel", "--config", conf, "--no-autoupdate", "--url", "http://" + m.proxyListen()},
	})
	if err != nil {
		return View{}, err
	}
	select {
	case u := <-found:
		m.mu.Lock()
		m.shares[id] = u
		m.applyProxyLocked()
		m.mu.Unlock()
		return m.view(a), nil
	case <-ctx.Done():
		_ = m.units.Stop(context.Background(), unit)
		mu.Lock()
		defer mu.Unlock()
		return View{}, fmt.Errorf("the tunnel did not start: %s", strings.Join(last, " / "))
	}
}

// Unshare stops the app's tunnel.
func (m *Manager) Unshare(id string) (View, error) {
	m.mu.Lock()
	a := m.store.apps[id]
	if a == nil {
		m.mu.Unlock()
		return View{}, ErrNotFound
	}
	m.stopShareLocked(a.ID)
	m.applyProxyLocked()
	m.mu.Unlock()
	return m.view(a), nil
}

func (m *Manager) stopShareLocked(id string) {
	_ = m.units.Stop(context.Background(), unitName(id, "share"))
	delete(m.shares, id)
}

// shareHost is the public host of the app's tunnel ("" if not shared).
func (m *Manager) shareHostLocked(id string) string {
	if u, err := url.Parse(m.shares[id]); err == nil {
		return u.Host
	}
	return ""
}
