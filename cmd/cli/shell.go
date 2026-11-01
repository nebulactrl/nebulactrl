package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"
)

// shellOpts configure one terminal session.
type shellOpts struct {
	// URL is the terminal WebSocket (ws:// or wss://).
	URL        string
	Token      string
	Process    string
	Replica    int
	Command    []string
	Production bool
	Cols, Rows int
	// Resizes delivers the terminal's size ([cols, rows]) when it changes.
	Resizes <-chan [2]int
	// Notice receives session notices (idle warning); may be nil.
	Notice io.Writer
}

// shellMsg is a text message of the terminal protocol (see internal/terminal).
type shellMsg struct {
	Type        string `json:"type"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
	ExitCode    *int   `json:"exitCode,omitempty"`
	SecondsLeft int    `json:"secondsLeft,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// shellURL turns the API base URL into the terminal WebSocket URL.
func shellURL(apiBaseURL, serviceID string) (string, error) {
	u, err := url.Parse(apiBaseURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/services/" + url.PathEscape(serviceID) + "/terminal/ws"
	return u.String(), nil
}

// shellError explains a server error message in CLI terms.
func shellError(m shellMsg) error {
	switch m.Code {
	case "confirmation_required":
		return errors.New("this service runs in production: re-run with --production to confirm")
	case "too_many_sessions", "server_offline", "not_deployed", "replica_not_running", "open_failed", "invalid_request":
		return errors.New(m.Message)
	}
	if m.Message != "" {
		return errors.New(m.Message)
	}
	return fmt.Errorf("terminal error %q", m.Code)
}

// runShell opens a terminal session and relays in to the shell and the
// shell's output to out until it ends. It returns the shell's exit code.
func runShell(ctx context.Context, o shellOpts, in io.Reader, out io.Writer) (int, error) {
	dctx, dcancel := context.WithTimeout(ctx, 20*time.Second)
	ws, resp, err := websocket.Dial(dctx, o.URL, &websocket.DialOptions{ //nolint:bodyclose // Dial owns the handshake body
		HTTPHeader: http.Header{"Authorization": {"Bearer " + o.Token}, "User-Agent": {"nebulactrl-cli/" + version}},
	})
	dcancel()
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				return 0, errors.New("not authenticated: check your token")
			case http.StatusForbidden:
				return 0, errors.New("not allowed: a terminal needs the Owner, Admin or Developer role and a token with the deployments:write scope")
			case http.StatusNotFound:
				return 0, errors.New("service not found")
			case http.StatusConflict:
				return 0, errors.New("this service has no container to open a shell in")
			}
			return 0, fmt.Errorf("connect: HTTP %d", resp.StatusCode)
		}
		return 0, fmt.Errorf("connect: %w", err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)

	open := map[string]any{"type": "open", "replica": o.Replica, "cols": o.Cols, "rows": o.Rows, "confirmProduction": o.Production}
	if o.Process != "" {
		open["process"] = o.Process
	}
	if len(o.Command) > 0 {
		open["command"] = o.Command
	}
	b, _ := json.Marshal(open)
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		return 0, fmt.Errorf("open terminal: %w", err)
	}
	for opened := false; !opened; {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return 0, fmt.Errorf("open terminal: %w", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var m shellMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "opened":
			opened = true
		case "error":
			return 0, shellError(m)
		}
	}

	// Keyboard → shell.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if ws.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				_ = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"close"}`))
				return
			}
		}
	}()
	// Window size → shell.
	if o.Resizes != nil {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case sz, ok := <-o.Resizes:
					if !ok {
						return
					}
					rb, _ := json.Marshal(map[string]any{"type": "resize", "cols": sz[0], "rows": sz[1]})
					if ws.Write(ctx, websocket.MessageText, rb) != nil {
						return
					}
				}
			}
		}()
	}

	exit := 0
	var reason string
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			if reason != "" || websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				break
			}
			return exit, fmt.Errorf("connection lost: %w", err)
		}
		if typ == websocket.MessageBinary {
			if _, err := out.Write(data); err != nil {
				return exit, err
			}
			continue
		}
		var m shellMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "exit":
			if m.ExitCode != nil {
				exit = *m.ExitCode
			}
		case "idle_warning":
			if o.Notice != nil {
				fmt.Fprintf(o.Notice, "\r\n[nebulactrl] no input: this session closes in %d seconds\r\n", m.SecondsLeft)
			}
		case "error":
			return exit, shellError(m)
		case "closed":
			reason = m.Reason
		}
	}
	switch reason {
	case "idle":
		return exit, errors.New("session closed: idle for too long")
	case "agent_disconnected":
		return exit, errors.New("session ended: the server went offline")
	case "error":
		return exit, errors.New("session ended with an error")
	}
	return exit, nil
}

func newServicesShellCmd(c *cli) *cobra.Command {
	var replica int
	var process string
	var production bool
	cmd := &cobra.Command{
		Use:   "shell <service> [-- command args…]",
		Short: "Open an interactive shell in a service's container",
		Long: "Opens a terminal in a running replica of the service (bash if the image has it, else sh), or runs the command after `--` " +
			"with a terminal attached. Needs the Owner, Admin or Developer role and a token with deployments:write. Production services " +
			"need --production. Sessions are audited (never their input or output) and close after 15 minutes without input.",
		Example: "  nebulactrl services shell acme/production/web\n  nebulactrl services shell web --replica 1 -- psql -U postgres\n  nebulactrl services shell web --process worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			var command []string
			if dash >= 0 {
				command = args[dash:]
				args = args[:dash]
			}
			if len(args) != 1 {
				return usagef("expected one service\nusage: %s", cmd.UseLine())
			}
			if err := c.connect(); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			id, err := c.serviceID(ctx, args[0])
			cancel()
			if err != nil {
				return err
			}
			endpoint, err := shellURL(apiBase(c.cfg.URL), id)
			if err != nil {
				return err
			}
			tty, err := openTTY(c.in, c.out)
			if err != nil {
				return err
			}
			defer tty.restore()
			cols, rows := tty.size()
			code, err := runShell(cmd.Context(), shellOpts{
				URL: endpoint, Token: c.cfg.Token, Process: process, Replica: replica, Command: command, Production: production,
				Cols: cols, Rows: rows, Resizes: tty.resizes, Notice: c.errOut,
			}, c.in, c.out)
			tty.restore()
			if err != nil {
				return &exitError{code: max(code, 1), err: err}
			}
			if code != 0 {
				return &exitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&process, "process", "", "services with several processes: the process to open (default: the default process); --replica counts within it")
	cmd.Flags().IntVar(&replica, "replica", 0, "replica index to open (0-based)")
	cmd.Flags().BoolVar(&production, "production", false, "confirm opening a terminal in a production environment")
	return cmd
}
