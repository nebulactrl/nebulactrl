package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// execOnce runs argv inside the running container of a service and returns its
// output (stdout and stderr, as a terminal merges them) and exit code. It uses the
// agent's exec stream like the web terminal, without a person on the other end:
// nothing is recorded, and the command's arguments never carry a secret.
func (s *Service) execOnce(ctx context.Context, serverID, serviceID uuid.UUID, argv []string, timeout time.Duration) (string, int, error) {
	if s.Agents == nil {
		return "", 0, errors.New("no agent connection available")
	}
	sid := uuid.NewString()
	st, err := s.Agents.OpenStream(serverID, sid)
	if err != nil {
		if errors.Is(err, agents.ErrNotConnected) {
			return "", 0, retryLater("The server is offline")
		}
		return "", 0, err
	}
	defer st.Close()
	if err := st.Send(agentprotocol.TypeExecOpen, agentprotocol.ExecOpen{
		SessionID: sid, ServiceID: serviceID.String(), Command: argv, Cols: 200, Rows: 24,
	}); err != nil {
		return "", 0, retryLater("The server is offline")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var out bytes.Buffer
	opened := false
	for {
		select {
		case f := <-st.Frames():
			switch f.Type {
			case agentprotocol.TypeExecOpened:
				var o agentprotocol.ExecOpened
				if err := f.Into(&o); err != nil {
					return "", 0, err
				}
				if o.Error != "" {
					if o.Code == "not_found" {
						return "", 0, retryLater("The container is not running yet")
					}
					return "", 0, fmt.Errorf("could not run %s: %s", argv[0], o.Error)
				}
				opened = true
			case agentprotocol.TypeExecOutput:
				var o agentprotocol.ExecOutput
				if err := f.Into(&o); err != nil {
					return "", 0, err
				}
				out.Write(o.Data)
				_ = st.Send(agentprotocol.TypeStreamAck, agentprotocol.StreamAck{StreamID: sid, Bytes: len(o.Data)})
			case agentprotocol.TypeExecExit:
				var e agentprotocol.ExecExit
				if err := f.Into(&e); err != nil {
					return "", 0, err
				}
				if e.Error != "" {
					return "", 0, fmt.Errorf("the command was interrupted: %s", e.Error)
				}
				return cleanOutput(out.String()), e.ExitCode, nil
			}
		case <-st.Done():
			return "", 0, retryLater("The connection to the server dropped")
		case <-timer.C:
			_ = st.Send(agentprotocol.TypeExecClose, agentprotocol.ExecClose{SessionID: sid})
			if !opened {
				return "", 0, retryLater("The server did not answer")
			}
			return "", 0, fmt.Errorf("%s did not finish within %s", argv[0], timeout)
		case <-ctx.Done():
			_ = st.Send(agentprotocol.TypeExecClose, agentprotocol.ExecClose{SessionID: sid})
			return "", 0, ctx.Err()
		}
	}
}

// cleanOutput strips terminal escapes and carriage returns.
func cleanOutput(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "")
}

// lastLine returns the last non-empty line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
