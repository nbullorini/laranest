// Package proxy runs an embedded Caddy that routes <app>.<domain> to each
// app's PHP-FPM pool or php-cgi workers (and Reverb), and laranest.<domain>
// to the panel.
//
// HTTPS uses Caddy's own local CA ("tls internal"): certificates for
// *.localhost are issued on the fly; browsers trust them once the CA root
// (RootCert) is added to the OS store.
package proxy

import (
	"encoding/json"
	"fmt"
	"html"
	"path/filepath"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/fileserver"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy/fastcgi"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"
	_ "github.com/caddyserver/caddy/v2/modules/caddypki"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"
)

// Site is one app as the proxy sees it.
type Site struct {
	Host string // miapp.localhost
	Root string // /path/to/app/public
	// FastCGI are the app's PHP upstreams as Caddy dial addresses: the FPM
	// socket ("unix//run/…/app.sock") or php-cgi workers ("127.0.0.1:19100").
	// Empty when the app is stopped.
	FastCGI []string
	// Upstream is an HTTP server that serves the whole app instead (a web
	// command listening on a port: Octane, a custom server).
	Upstream   string
	ReverbHost string // reverb-miapp.localhost; "" if Reverb is off
	ReverbAddr string // 127.0.0.1:8xxx
	HTTPS      bool   // served over TLS; plain HTTP redirects to it
}

type Proxy struct {
	mu        sync.Mutex
	listen    string // HTTP address
	listenTLS string // HTTPS address
	panelHost string
	panelAddr string
	panelURL  string
	storage   string
	lastErr   error
	tlsErr    error
}

func New(listenAddr, listenTLS, panelHost, panelAddr, panelURL, storageDir string) *Proxy {
	return &Proxy{listen: listenAddr, listenTLS: listenTLS, panelHost: panelHost, panelAddr: panelAddr, panelURL: panelURL, storage: storageDir}
}

// RootCert is the local CA certificate to trust (created on the first Apply).
func (p *Proxy) RootCert() string {
	return filepath.Join(p.storage, "pki", "authorities", "local", "root.crt")
}

// Apply (re)loads Caddy with the given sites. Caddy swaps configs gracefully.
// If only the HTTPS listener fails (e.g. port 443 not allowed), plain HTTP
// keeps working and the failure is reported by TLSErr.
func (p *Proxy) Apply(sites []Site) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tlsErr = nil
	err := p.load(sites, true)
	if err != nil && strings.Contains(err.Error(), p.listenTLS) {
		p.tlsErr = fmt.Errorf("HTTPS on %s: %w", p.listenTLS, err)
		err = p.load(sites, false)
	}
	if err != nil {
		err = fmt.Errorf("proxy on %s: %w", p.listen, err)
	}
	p.lastErr = err
	return err
}

func (p *Proxy) load(sites []Site, withTLS bool) error {
	cfg, err := json.Marshal(p.config(sites, withTLS))
	if err != nil {
		return err
	}
	return caddy.Load(cfg, true)
}

// SetListen changes the addresses used by the next Apply.
func (p *Proxy) SetListen(addr, addrTLS string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listen, p.listenTLS = addr, addrTLS
}

// Err is the last error from Apply, or nil if the proxy is serving.
func (p *Proxy) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastErr
}

// TLSErr is set when HTTPS couldn't start (HTTP may still be up).
func (p *Proxy) TLSErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tlsErr
}

func (p *Proxy) Stop() error { return caddy.Stop() }

type obj = map[string]any

func (p *Proxy) config(sites []Site, withTLS bool) obj {
	panel := obj{
		"match":    []obj{{"host": []string{p.panelHost}}},
		"handle":   []obj{reverseProxy(p.panelAddr)},
		"terminal": true,
	}
	notFound := obj{"handle": []obj{staticPage(404, "Unknown app",
		"This domain is not registered in Laranest.", p.panelURL)}}

	plain := []obj{panel}
	secure := []obj{panel}
	var tlsHosts []string
	for _, s := range sites {
		hosts := []string{s.Host}
		var routes []obj
		if s.ReverbHost != "" {
			hosts = append(hosts, s.ReverbHost)
			if len(s.FastCGI) > 0 || s.Upstream != "" {
				routes = append(routes, obj{
					"match":    []obj{{"host": []string{s.ReverbHost}}},
					"handle":   []obj{reverseProxy(s.ReverbAddr)},
					"terminal": true,
				})
			}
		}
		routes = append(routes, obj{
			"match":    []obj{{"host": []string{s.Host}}},
			"handle":   []obj{{"handler": "subroute", "routes": siteRoutes(s, p.panelURL)}},
			"terminal": true,
		})
		if s.HTTPS && withTLS {
			secure = append(secure, routes...)
			tlsHosts = append(tlsHosts, hosts...)
			plain = append(plain, obj{
				"match":    []obj{{"host": hosts}},
				"handle":   []obj{redirect("https://{http.request.host}" + portSuffix(p.listenTLS, "443") + "{http.request.uri}")},
				"terminal": true,
			})
		} else {
			plain = append(plain, routes...)
		}
	}

	servers := obj{"laranest": obj{
		"listen":          []string{p.listen},
		"routes":          append(plain, notFound),
		"automatic_https": obj{"disable": true},
	}}
	apps := obj{
		"http": obj{"servers": servers},
		// Our own CA; never let Caddy try to install it into the system
		// store (that needs root): Laranest asks the user to trust it.
		"pki": obj{"certificate_authorities": obj{"local": obj{"name": "Laranest Local CA", "install_trust": false}}},
	}
	if len(tlsHosts) > 0 {
		servers["laranest_tls"] = obj{
			"listen":          []string{p.listenTLS},
			"routes":          append(secure, notFound),
			"automatic_https": obj{"disable_redirects": true},
		}
		apps["tls"] = obj{"automation": obj{"policies": []obj{{
			"subjects": append(tlsHosts, p.panelHost),
			"issuers":  []obj{{"module": "internal", "ca": "local"}},
		}}}}
	}

	return obj{
		"admin":   obj{"disabled": true, "config": obj{"persist": false}},
		"logging": obj{"logs": obj{"default": obj{"level": "ERROR"}}},
		"storage": obj{"module": "file_system", "root": p.storage},
		"apps":    apps,
	}
}

func redirect(location string) obj {
	return obj{
		"handler":     "static_response",
		"status_code": 308,
		"headers":     obj{"Location": []string{location}},
	}
}

// portSuffix returns ":port" unless addr uses the scheme's default port.
func portSuffix(addr, defaultPort string) string {
	i := strings.LastIndex(addr, ":")
	if i < 0 || addr[i+1:] == defaultPort {
		return ""
	}
	return addr[i:]
}

// siteRoutes mimics Laravel's public/ front controller: existing static files
// are served directly, everything else goes to index.php over FastCGI.
func siteRoutes(s Site, panelURL string) []obj {
	if s.Upstream != "" {
		return []obj{{"handle": []obj{reverseProxy(s.Upstream)}}}
	}
	if len(s.FastCGI) == 0 {
		return []obj{{"handle": []obj{staticPage(503, "App stopped",
			"The app "+s.Host+" is stopped. Start it from the panel.", panelURL)}}}
	}
	return []obj{
		{
			"match": []obj{{
				"file": obj{"root": s.Root, "try_files": []string{"{http.request.uri.path}"}},
				"not":  []obj{{"path": []string{"*.php", "*/"}}},
			}},
			"handle":   []obj{{"handler": "file_server", "root": s.Root}},
			"terminal": true,
		},
		{
			"handle": []obj{
				{"handler": "rewrite", "uri": "/index.php?{http.request.uri.query}"},
				fastCGI(s),
			},
		},
	}
}

func fastCGI(s Site) obj {
	upstreams := make([]obj, len(s.FastCGI))
	for i, dial := range s.FastCGI {
		upstreams[i] = obj{"dial": dial}
	}
	h := obj{
		"handler": "reverse_proxy",
		"transport": obj{
			"protocol":   "fastcgi",
			"root":       s.Root,
			"split_path": []string{".php"},
		},
		"upstreams": upstreams,
	}
	if len(upstreams) > 1 {
		// php-cgi workers serve one request at a time: send each request to
		// the least busy one, and retry another while one restarts.
		h["load_balancing"] = obj{
			"selection_policy": obj{"policy": "least_conn"},
			"try_duration":     "5s",
		}
	}
	return h
}

func reverseProxy(addr string) obj {
	return obj{"handler": "reverse_proxy", "upstreams": []obj{{"dial": addr}}}
}

func staticPage(status int, title, msg, panelURL string) obj {
	body := fmt.Sprintf(`<!doctype html><meta charset="utf-8"><title>%[1]s</title>
<body style="font-family:system-ui;display:grid;place-items:center;height:100vh;margin:0;background:#141417;color:#ececf1">
<div style="text-align:center"><h1 style="color:#f05340">%[1]s</h1><p>%[2]s</p><p><a style="color:#f05340" href="%[3]s">Open Laranest</a></p></div>`,
		html.EscapeString(title), html.EscapeString(msg), html.EscapeString(panelURL))
	return obj{
		"handler":     "static_response",
		"status_code": status,
		"headers":     obj{"Content-Type": []string{"text/html; charset=utf-8"}},
		"body":        body,
	}
}
