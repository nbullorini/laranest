package detect

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	_ "modernc.org/sqlite"
)

// probeTimeout bounds every external check so a firewalled host can't stall the panel.
const probeTimeout = 2 * time.Second

// CheckServices connects to what the .env points at, with its credentials:
// the database (and the tables its drivers need), Redis, Memcached, SMTP,
// Scout engines and S3 endpoints. It is slow-ish and side-effect free, so
// callers should cache it (see apps.Manager).
func CheckServices(d *Detection, env map[string]string, ctx Context) []Warning {
	var (
		mu sync.Mutex
		wg sync.WaitGroup
		ws []Warning
	)
	run := func(f func() []Warning) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := f()
			mu.Lock()
			ws = append(ws, out...)
			mu.Unlock()
		}()
	}

	run(func() []Warning { return checkDatabase(d, env) })
	if usesRedis(env, ctx) {
		run(func() []Warning { return checkRedis(d, env, ctx) })
	}
	if usesStore(env, "memcached") {
		run(func() []Warning { return checkMemcached(env, ctx) })
	}
	if env["MAIL_MAILER"] == "smtp" {
		run(func() []Warning { return checkSMTP(env) })
	}
	if driver := env["SCOUT_DRIVER"]; driver == "meilisearch" || driver == "typesense" {
		run(func() []Warning { return checkScout(driver, env) })
	}
	if env["FILESYSTEM_DISK"] == "s3" && env["AWS_ENDPOINT"] != "" {
		run(func() []Warning { return checkS3(env) })
	}
	run(func() []Warning { return checkStorageLink(d) })
	if ctx.Running && ctx.URL != "" {
		run(func() []Warning { return checkHome(d, ctx.URL) })
	}
	wg.Wait()

	sort.SliceStable(ws, func(i, j int) bool { return ws[i].Code < ws[j].Code })
	return ws
}

// ---- database ----

func checkDatabase(d *Detection, env map[string]string) []Warning {
	conn := firstNonEmpty(env["DB_CONNECTION"], "mysql")
	switch conn {
	case "sqlite":
		return checkSQLite(d, env)
	case "mysql", "mariadb", "pgsql":
		if env["DB_DATABASE"] == "" {
			return []Warning{{Level: LevelError, Code: "db_env", Message: "DB_DATABASE is not set in .env"}}
		}
		return checkSQLServer(conn, env)
	case "sqlsrv":
		host, port := firstNonEmpty(env["DB_HOST"], "127.0.0.1"), firstNonEmpty(env["DB_PORT"], "1433")
		if w := tcpDown("db", "SQL Server", host, port); w != nil {
			return []Warning{*w}
		}
	}
	return nil
}

// sqliteURI turns a path into a file: URI (escaped, and with forward slashes
// and a leading / for Windows drive paths, as SQLite expects).
func sqliteURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/x → /C:/x
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func checkSQLite(d *Detection, env map[string]string) []Warning {
	db := env["DB_DATABASE"]
	if db == ":memory:" {
		return nil
	}
	if db == "" {
		db = filepath.Join(d.Path, "database", "database.sqlite")
	} else if !filepath.IsAbs(db) {
		db = filepath.Join(d.Path, db)
	}
	if !exists(db) {
		hint := "touch " + db + " && laranest php artisan migrate"
		if runtime.GOOS == "windows" {
			hint = "laranest php artisan migrate (it offers to create the file)"
		}
		// Run from the panel (no terminal), migrate takes the default answer
		// to "create it?": yes.
		return []Warning{{Level: LevelError, Code: "db_sqlite", Message: "The SQLite database " + db + " does not exist",
			Hint: hint, Command: "php artisan migrate"}}
	}
	if !writable(filepath.Dir(db)) {
		return []Warning{{Level: LevelError, Code: "db_sqlite_perm",
			Message: "SQLite needs write access to " + filepath.Dir(db) + " (for its journal file)"}}
	}
	sqlDB, err := sql.Open("sqlite", sqliteURI(db)+"?mode=ro&_pragma=busy_timeout(1000)")
	if err != nil {
		return nil
	}
	defer sqlDB.Close()
	return checkTables(sqlDB, "sqlite", env)
}

func checkSQLServer(conn string, env map[string]string) []Warning {
	host := firstNonEmpty(env["DB_HOST"], "127.0.0.1")
	user, pass, name := env["DB_USERNAME"], env["DB_PASSWORD"], env["DB_DATABASE"]
	label := map[string]string{"mysql": "MySQL", "mariadb": "MariaDB", "pgsql": "PostgreSQL"}[conn]

	var driver, dsn, port string
	switch conn {
	case "pgsql":
		port = firstNonEmpty(env["DB_PORT"], "5432")
		driver = "pgx"
		dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s connect_timeout=2 sslmode=prefer",
			pgQuote(host), port, pgQuote(user), pgQuote(pass), pgQuote(name))
	default:
		port = firstNonEmpty(env["DB_PORT"], "3306")
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.DBName = user, pass, name
		cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(host, port)
		if sock := env["DB_SOCKET"]; sock != "" {
			cfg.Net, cfg.Addr = "unix", sock
		}
		cfg.Timeout, cfg.ReadTimeout = probeTimeout, probeTimeout
		cfg.AllowNativePasswords = true
		driver, dsn = "mysql", cfg.FormatDSN()
	}

	if w := dockerHostname("db", label, host); w != nil {
		return []Warning{*w}
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	where := net.JoinHostPort(host, port)
	if err := db.PingContext(ctx); err != nil {
		return []Warning{dbError(label, where, user, name, err)}
	}
	return checkTables(db, driver, env)
}

// dbError turns driver errors into something actionable.
func dbError(label, where, user, name string, err error) Warning {
	var my *mysql.MySQLError
	var pg *pgconn.PgError
	switch {
	case errors.As(err, &my) && (my.Number == 1045 || my.Number == 1698),
		errors.As(err, &pg) && pg.Code == "28P01",
		errors.As(err, &pg) && pg.Code == "28000":
		return Warning{Level: LevelError, Code: "db_auth",
			Message: fmt.Sprintf("%s at %s rejects user %q: invalid credentials", label, where, user),
			Hint:    "Check DB_USERNAME and DB_PASSWORD in .env"}
	case errors.As(err, &my) && my.Number == 1049, errors.As(err, &pg) && pg.Code == "3D000":
		return Warning{Level: LevelError, Code: "db_missing",
			Message: fmt.Sprintf("Database %q does not exist on %s", name, where),
			Hint:    fmt.Sprintf("Create it (CREATE DATABASE %s) and run laranest php artisan migrate", name)}
	case errors.As(err, &my) && my.Number == 1044:
		return Warning{Level: LevelError, Code: "db_auth",
			Message: fmt.Sprintf("User %q has no access to database %q on %s", user, name, where)}
	}
	return Warning{Level: LevelWarning, Code: "db_down",
		Message: fmt.Sprintf("%s is not responding at %s", label, where),
		Hint:    "Laranest does not manage services: start it yourself (systemd, Docker, etc.). " + shortErr(err)}
}

// checkTables looks for the tables Laravel's database drivers rely on: a
// connected but unmigrated database is the most common "500 on first load".
func checkTables(db *sql.DB, driver string, env map[string]string) []Warning {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var q string
	switch driver {
	case "sqlite":
		q = "SELECT name FROM sqlite_master WHERE type = 'table'"
	case "pgx":
		q = "SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()"
	default:
		q = "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()"
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	tables := map[string]bool{}
	for rows.Next() {
		var t string
		if rows.Scan(&t) == nil {
			tables[strings.ToLower(t)] = true
		}
	}
	prefix := strings.ToLower(env["DB_PREFIX"])
	has := func(t string) bool { return tables[prefix+strings.ToLower(t)] }

	if !has("migrations") {
		return []Warning{{Level: LevelError, Code: "db_unmigrated", Message: "The database is empty: migrations have not been run",
			Hint: "laranest php artisan migrate", Command: "php artisan migrate"}}
	}
	var missing []string
	need := func(used bool, table string) {
		if used && !has(table) {
			missing = append(missing, table)
		}
	}
	need(env["QUEUE_CONNECTION"] == "database", firstNonEmpty(env["DB_QUEUE_TABLE"], "jobs"))
	need(env["SESSION_DRIVER"] == "database", firstNonEmpty(env["SESSION_TABLE"], "sessions"))
	need(usesStore(env, "database"), firstNonEmpty(env["DB_CACHE_TABLE"], "cache"))
	if len(missing) == 0 {
		return nil
	}
	return []Warning{{Level: LevelError, Code: "db_tables",
		Message: "Missing tables needed by the \"database\" drivers in .env: " + strings.Join(missing, ", "),
		Hint:    "laranest php artisan migrate (or make:queue-table / make:session-table / make:cache-table on Laravel 10 and older)",
		Command: "php artisan migrate"}}
}

// ---- redis & friends ----

func checkRedis(d *Detection, env map[string]string, ctx Context) []Warning {
	var ws []Warning
	// Laravel's default client is phpredis, which needs the redis extension.
	switch client := firstNonEmpty(env["REDIS_CLIENT"], "phpredis"); client {
	case "phpredis":
		if ctx.Modules != nil && !ctx.Modules["redis"] {
			ws = append(ws, Warning{Level: LevelError, Code: "redis_client",
				Message: "REDIS_CLIENT=phpredis but PHP " + ctx.PHPVersion + " lacks the redis extension",
				Hint:    "Use REDIS_CLIENT=predis (composer require predis/predis) or a PHP with the extension"})
		}
	case "predis":
		if lockedVersion(d.Path, "predis/predis") == "" {
			ws = append(ws, Warning{Level: LevelError, Code: "redis_client",
				Message: "REDIS_CLIENT=predis but the predis/predis package is not installed",
				Hint:    "laranest composer require predis/predis", Command: "composer require predis/predis"})
		}
	}

	opts := &redis.Options{
		Addr:        net.JoinHostPort(firstNonEmpty(nullable(env["REDIS_HOST"]), "127.0.0.1"), firstNonEmpty(env["REDIS_PORT"], "6379")),
		Username:    nullable(env["REDIS_USERNAME"]),
		Password:    nullable(env["REDIS_PASSWORD"]),
		DialTimeout: probeTimeout, ReadTimeout: probeTimeout, MaxRetries: -1,
	}
	if u := env["REDIS_URL"]; u != "" {
		if parsed, err := redis.ParseURL(u); err == nil {
			parsed.DialTimeout, parsed.ReadTimeout, parsed.MaxRetries = probeTimeout, probeTimeout, -1
			opts = parsed
		}
	}
	if db, err := strconv.Atoi(env["REDIS_DB"]); err == nil {
		opts.DB = db
	}
	host, _, _ := net.SplitHostPort(opts.Addr)
	if w := dockerHostname("redis", "Redis", host); w != nil {
		return append(ws, *w)
	}

	client := redis.NewClient(opts)
	defer client.Close()
	c, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	err := client.Ping(c).Err()
	switch {
	case err == nil:
	case strings.Contains(err.Error(), "NOAUTH"), strings.Contains(err.Error(), "WRONGPASS"),
		strings.Contains(err.Error(), "invalid password"), strings.Contains(err.Error(), "invalid username-password"):
		ws = append(ws, Warning{Level: LevelError, Code: "redis_auth",
			Message: "Redis at " + opts.Addr + " rejects the authentication", Hint: "Check REDIS_PASSWORD (and REDIS_USERNAME) in .env"})
	case strings.Contains(err.Error(), "DB index is out of range"):
		ws = append(ws, Warning{Level: LevelError, Code: "redis_db", Message: "REDIS_DB=" + env["REDIS_DB"] + " does not exist on " + opts.Addr})
	default:
		ws = append(ws, Warning{Level: LevelWarning, Code: "redis_down", Message: "Redis is not responding at " + opts.Addr,
			Hint: "Laranest does not manage services: start it yourself (systemd, Docker, etc.). " + shortErr(err)})
	}
	return ws
}

func checkMemcached(env map[string]string, ctx Context) []Warning {
	var ws []Warning
	if ctx.Modules != nil && !ctx.Modules["memcached"] {
		ws = append(ws, Warning{Level: LevelError, Code: "memcached_ext",
			Message: "The cache uses memcached but PHP " + ctx.PHPVersion + " lacks the memcached extension",
			Hint:    "Use CACHE_STORE=redis/database, or a PHP with the extension"})
	}
	if w := tcpDown("memcached", "Memcached", firstNonEmpty(env["MEMCACHED_HOST"], "127.0.0.1"), firstNonEmpty(env["MEMCACHED_PORT"], "11211")); w != nil {
		ws = append(ws, *w)
	}
	return ws
}

// checkSMTP connects and reads the greeting: a port that answers but isn't
// an SMTP server (a typo'd port) is as broken as a closed one.
func checkSMTP(env map[string]string) []Warning {
	host, port := firstNonEmpty(env["MAIL_HOST"], "127.0.0.1"), firstNonEmpty(env["MAIL_PORT"], "2525")
	if w := dockerHostname("mail", "The SMTP server", host); w != nil {
		return []Warning{*w}
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), probeTimeout)
	if err != nil {
		return []Warning{{Level: LevelWarning, Code: "mail_down",
			Message: "The SMTP server is not responding at " + net.JoinHostPort(host, port),
			Hint:    "For development, use Mailpit (port 1025) or MAIL_MAILER=log"}}
	}
	defer conn.Close()
	if port == "465" {
		return nil // implicit TLS: no plaintext greeting to read
	}
	_ = conn.SetReadDeadline(time.Now().Add(probeTimeout))
	line, _ := bufio.NewReader(conn).ReadString('\n')
	if !strings.HasPrefix(line, "220") {
		return []Warning{{Level: LevelWarning, Code: "mail_protocol",
			Message: net.JoinHostPort(host, port) + " answers but is not an SMTP server", Hint: "Check MAIL_HOST and MAIL_PORT"}}
	}
	return nil
}

func checkScout(driver string, env map[string]string) []Warning {
	url, name := strings.TrimRight(firstNonEmpty(env["MEILISEARCH_HOST"], "http://127.0.0.1:7700"), "/")+"/health", "Meilisearch"
	if driver == "typesense" {
		url = fmt.Sprintf("%s://%s:%s/health", firstNonEmpty(env["TYPESENSE_PROTOCOL"], "http"),
			firstNonEmpty(env["TYPESENSE_HOST"], "localhost"), firstNonEmpty(env["TYPESENSE_PORT"], "8108"))
		name = "Typesense"
	}
	client := http.Client{Timeout: probeTimeout}
	resp, err := client.Get(url)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode < 500 {
			return nil
		}
	}
	return []Warning{{Level: LevelWarning, Code: "scout_down", Message: name + " (SCOUT_DRIVER) is not responding at " + url}}
}

func checkS3(env map[string]string) []Warning {
	endpoint := strings.TrimPrefix(strings.TrimPrefix(env["AWS_ENDPOINT"], "https://"), "http://")
	host, port, err := net.SplitHostPort(strings.SplitN(endpoint, "/", 2)[0])
	if err != nil {
		host, port = strings.SplitN(endpoint, "/", 2)[0], "443"
		if strings.HasPrefix(env["AWS_ENDPOINT"], "http://") {
			port = "80"
		}
	}
	if w := tcpDown("s3", "The S3 endpoint (AWS_ENDPOINT)", host, port); w != nil {
		return []Warning{*w}
	}
	return nil
}

func checkStorageLink(d *Detection) []Warning {
	public := filepath.Join(d.Path, "storage", "app", "public")
	if _, err := os.Lstat(filepath.Join(d.Path, "public", "storage")); err == nil || !exists(public) {
		return nil
	}
	return []Warning{{Level: LevelInfo, Code: "storage_link",
		Message: "public/storage does not exist: files uploaded to the \"public\" disk will not be served",
		Hint:    "laranest php artisan storage:link", Command: "php artisan storage:link"}}
}

// ---- the app itself ----

// checkHome requests the app's home page through the proxy and, on a 5xx,
// surfaces the last error Laravel logged: the actual reason a page is blank.
func checkHome(d *Detection, url string) []Warning {
	port := map[bool]string{true: "443", false: "80"}[strings.HasPrefix(url, "https://")]
	if i := strings.LastIndex(url, ":"); i > len("https:") {
		port = url[i+1:]
	}
	client := http.Client{
		Timeout: 3 * probeTimeout,
		Transport: &http.Transport{
			// *.localhost may not resolve inside Go: always dial the local proxy.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: probeTimeout}).DialContext(ctx, network, "127.0.0.1:"+port)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // our own proxy on loopback
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	resp, err := client.Get(url + "/")
	if err != nil {
		return nil // proxy/FPM problems are reported elsewhere
	}
	resp.Body.Close()
	// 502/504 come from the proxy while FPM is still starting, and 503 means
	// stopped or in maintenance: only other 5xx are the app's own errors.
	switch {
	case resp.StatusCode < 500, resp.StatusCode == 502, resp.StatusCode == 503, resp.StatusCode == 504:
		return nil
	}
	w := Warning{Level: LevelError, Code: "app_error", Message: fmt.Sprintf("The app answers %d at %s", resp.StatusCode, url)}
	if last := lastLoggedError(d.Path); last != "" {
		w.Message += ": " + last
	}
	w.Hint = "Full details in storage/logs/ (or in the Logs → web tab)"
	return []Warning{w}
}

var logErrorRe = regexp.MustCompile(`\] [\w-]+\.(?:ERROR|CRITICAL|ALERT|EMERGENCY): (.+?)(?: \{| \[stacktrace\]|$)`)

// lastLoggedError returns the message of the newest error in storage/logs.
func lastLoggedError(root string) string {
	files, _ := filepath.Glob(filepath.Join(root, "storage", "logs", "*.log"))
	var newest string
	var newestTime time.Time
	for _, f := range files {
		if info, err := os.Stat(f); err == nil && info.ModTime().After(newestTime) {
			newest, newestTime = f, info.ModTime()
		}
	}
	if newest == "" {
		return ""
	}
	f, err := os.Open(newest)
	if err != nil {
		return ""
	}
	defer f.Close()
	const tail = 512 << 10
	if info, err := f.Stat(); err == nil && info.Size() > tail {
		f.Seek(-tail, io.SeekEnd)
	}
	data, _ := io.ReadAll(f)
	var last string
	for _, line := range strings.Split(string(data), "\n") {
		if m := logErrorRe.FindStringSubmatch(line); m != nil {
			last = m[1]
		}
	}
	if len(last) > 300 {
		last = last[:300] + "…"
	}
	return last
}

func init() {
	// go-redis logs failed dials to stderr; the outcome is reported as a warning.
	redis.SetLogger(silentLogger{})
}

type silentLogger struct{}

func (silentLogger) Printf(context.Context, string, ...any) {}

// ---- helpers ----

// dockerHostname catches Sail/docker-compose service names (DB_HOST=mysql)
// used outside Docker, where they don't resolve.
func dockerHostname(code, label, host string) *Warning {
	if host == "" || strings.Contains(host, ".") || host == "localhost" || net.ParseIP(host) != nil {
		return nil
	}
	if _, err := net.LookupHost(host); err == nil {
		return nil
	}
	return &Warning{Level: LevelWarning, Code: code + "_down",
		Message: fmt.Sprintf("%s points to %q, a name that only exists inside Docker/Sail", label, host),
		Hint:    "If the service runs on your machine or on a published Docker port, use 127.0.0.1"}
}

func tcpDown(code, label, host, port string) *Warning {
	if w := dockerHostname(code, label, host); w != nil {
		return w
	}
	if reachable(host, port) {
		return nil
	}
	return &Warning{Level: LevelWarning, Code: code + "_down",
		Message: fmt.Sprintf("%s is not responding at %s", label, net.JoinHostPort(host, port)),
		Hint:    "Laranest does not manage services: start it yourself (systemd, Docker, etc.)."}
}

func usesStore(env map[string]string, store string) bool {
	return firstNonEmpty(env["CACHE_STORE"], env["CACHE_DRIVER"]) == store
}

// nullable maps Laravel's "null" placeholder (REDIS_PASSWORD=null) to "".
func nullable(v string) string {
	if strings.EqualFold(v, "null") {
		return ""
	}
	return v
}

func pgQuote(v string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), "'", `\'`) + "'"
}

func shortErr(err error) string {
	msg := err.Error()
	if len(msg) > 120 {
		msg = msg[:120] + "…"
	}
	return "(" + msg + ")"
}
