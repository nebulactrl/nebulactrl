// Command app is the integration-test fixture: a tiny HTTP server whose
// behavior the tests steer through environment variables.
//
//	VERSION      body of GET / (default "v0")
//	FAIL_HEALTH  "1": GET /health answers 500
//	CRASH        "1": exit 1 at startup
//	CRASH_PROCESS  process name: that process' containers exit 1 at startup
//	GRACE_MS       `worker` mode: milliseconds between SIGTERM and exit (default 0)
//	PORT         listen port (default 8080)
//	SECRET_ECHO  name of an env var whose value is printed to stdout at startup
//	             (lets a test prove secrets are redacted from deployment logs)
//
// GET /hostname answers with the container's hostname (one value per replica).
//
// GET /log?msg=<line>&msg=<line> prints every msg as one line on stdout and
// answers "logged <n>" (log history and drain tests write their lines with it).
//
// `app task <exit-code> [words...]` and `app get <url>` are one-shot commands
// (the image has no shell): task and pre/post-deploy tests run them as the
// command.
//
// GET /fetch?url=<url> requests the URL from inside the container and answers
// "ok <status> <body>" (HTTP 500 "failed: <error>" if it cannot connect within
// 3 s); network tests use it to see what a container can reach.
//
// GET /db connects to Postgres with DATABASE_URL and answers "db ok <user>" (HTTP
// 500 with the driver error otherwise); GET /kv talks to the Redis-protocol
// server at REDIS_URL (AUTH, SET, GET) and answers "kv ok". Each request opens a
// fresh connection, so a rotated password shows up on the very next request.
//
// `app worker [tag]` is a process without a listener (see worker): one line per
// second, exit 0 on SIGTERM after GRACE_MS milliseconds. CRASH_PROCESS names a
// process (NEBULA_PROCESS) whose containers exit 1 at startup, like CRASH=1 does
// for every container.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

// crashes reports whether this container is told to exit at startup: CRASH=1
// for every container, CRASH_PROCESS=<name> for the containers of that process
// only (NEBULA_PROCESS, set on every container of a service with several
// processes).
func crashes() bool {
	if os.Getenv("CRASH") == "1" {
		return true
	}
	p := os.Getenv("CRASH_PROCESS")
	return p != "" && p == os.Getenv("NEBULA_PROCESS")
}

// worker is a process without a listener (see the package comment).
func worker(tag string) {
	grace := time.Duration(0)
	if ms, err := strconv.Atoi(os.Getenv("GRACE_MS")); err == nil && ms > 0 {
		grace = time.Duration(ms) * time.Millisecond
	}
	host, _ := os.Hostname()
	proc := os.Getenv("NEBULA_PROCESS")
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM, syscall.SIGINT)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	fmt.Printf("process=%s tag=%s started %s\n", proc, tag, host)
	for n := 1; ; n++ {
		select {
		case <-term:
			fmt.Printf("process=%s tag=%s stopping\n", proc, tag)
			time.Sleep(grace)
			return
		case <-tick.C:
			fmt.Printf("process=%s tag=%s tick %d %s\n", proc, tag, n, host)
		}
	}
}

func dbCheck(ctx context.Context) (string, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return "", fmt.Errorf("DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	var user string
	if err := conn.QueryRow(ctx, "SELECT current_user").Scan(&user); err != nil {
		return "", err
	}
	return "db ok " + user, nil
}

// respCmd sends one RESP command and returns the first reply line.
func respCmd(c net.Conn, r *bufio.Reader, args ...string) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&sb, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := c.Write([]byte(sb.String())); err != nil {
		return "", err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if strings.HasPrefix(line, "-") {
		return "", fmt.Errorf("%s", line[1:])
	}
	if strings.HasPrefix(line, "$") { // bulk reply: read the payload line
		payload, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimRight(payload, "\r\n"), nil
	}
	return line, nil
}

func kvCheck(ctx context.Context) (string, error) {
	raw := os.Getenv("REDIS_URL")
	if raw == "" {
		return "", fmt.Errorf("REDIS_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(c)
	if pw, ok := u.User.Password(); ok {
		if _, err := respCmd(c, r, "AUTH", pw); err != nil {
			return "", err
		}
	}
	if _, err := respCmd(c, r, "SET", "nebula:itest", "1"); err != nil {
		return "", err
	}
	if v, err := respCmd(c, r, "GET", "nebula:itest"); err != nil || v != "1" {
		return "", fmt.Errorf("GET returned %q: %v", v, err)
	}
	return "kv ok", nil
}

func check(fn func(context.Context) (string, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		out, err := fn(ctx)
		if err != nil {
			http.Error(w, "failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, out)
	}
}

func main() {
	// `app task <exit-code> [words...]`: print the words (and the value of the
	// variable named by TASK_ECHO), exit with the code. Task and pre-deploy tests
	// run it as the command (the image has no shell).
	if len(os.Args) > 2 && os.Args[1] == "task" {
		fmt.Println(strings.Join(os.Args[3:], " "))
		if name := os.Getenv("TASK_ECHO"); name != "" {
			fmt.Printf("%s=%s\n", name, os.Getenv(name))
		}
		code := 0
		_, _ = fmt.Sscanf(os.Args[2], "%d", &code)
		os.Exit(code)
	}
	// `app get <url>`: print the status and body of a GET, exit 0 on 2xx. The
	// post-deploy test runs it as the command to see which release is live.
	if len(os.Args) > 2 && os.Args[1] == "get" {
		res, err := (&http.Client{Timeout: 5 * time.Second}).Get(os.Args[2])
		if err != nil {
			fmt.Println("get failed:", err)
			os.Exit(1)
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		fmt.Printf("GET %s -> %d %s\n", os.Args[2], res.StatusCode, strings.TrimSpace(string(body)))
		if res.StatusCode/100 != 2 {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if crashes() {
		fmt.Fprintln(os.Stderr, "CRASH=1: exiting at startup")
		os.Exit(1)
	}
	// `app worker [tag]`: a process without a listener. It prints one line per
	// second, "process=<NEBULA_PROCESS> tag=<tag> tick <n> <hostname>", and on
	// SIGTERM waits GRACE_MS milliseconds (default 0) before exiting 0, so tests
	// can tell a stop that honors its grace period from a kill.
	if len(os.Args) > 1 && os.Args[1] == "worker" {
		worker(strings.Join(os.Args[2:], " "))
		return
	}
	version := os.Getenv("VERSION")
	if version == "" {
		version = "v0"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if name := os.Getenv("SECRET_ECHO"); name != "" {
		fmt.Printf("startup: %s=%s\n", name, os.Getenv(name))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if os.Getenv("FAIL_HEALTH") == "1" {
			http.Error(w, "unhealthy", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/db", check(dbCheck))
	mux.HandleFunc("/kv", check(kvCheck))
	mux.HandleFunc("/hostname", func(w http.ResponseWriter, _ *http.Request) {
		// Distinct per container: lets a test see which replica answered.
		name, _ := os.Hostname()
		fmt.Fprint(w, name)
	})
	mux.HandleFunc("/log", func(w http.ResponseWriter, r *http.Request) {
		msgs := r.URL.Query()["msg"]
		for _, m := range msgs {
			fmt.Println(m)
		}
		fmt.Fprintf(w, "logged %d", len(msgs))
	})
	// /pgq?env=NAME&q=SQL runs one single-value query against the Postgres URL
	// in env var NAME (default DATABASE_URL) and prints the value; the replica
	// tests use it to see which server a connection string reaches
	// (select pg_is_in_recovery()) and to write through DATABASE_WRITE_URL.
	mux.HandleFunc("/pgq", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		name := r.URL.Query().Get("env")
		if name == "" {
			name = "DATABASE_URL"
		}
		conn, err := pgx.Connect(ctx, os.Getenv(name))
		if err != nil {
			http.Error(w, "failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer conn.Close(ctx)
		var out string
		if err := conn.QueryRow(ctx, r.URL.Query().Get("q")).Scan(&out); err != nil {
			http.Error(w, "failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, out)
	})
	mux.HandleFunc("/fetch", func(w http.ResponseWriter, r *http.Request) {
		c := &http.Client{Timeout: 3 * time.Second}
		res, err := c.Get(r.URL.Query().Get("url"))
		if err != nil {
			http.Error(w, "failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		fmt.Fprintf(w, "ok %d %s", res.StatusCode, strings.TrimSpace(string(b)))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, version)
	})
	fmt.Printf("listening on :%s version=%s\n", port, version)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
