package deployments

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const logPollInterval = 500 * time.Millisecond

// LogLine is one SSE message of a followed deployment log.
type LogLine struct {
	Stream string    `json:"stream"`
	Line   string    `json:"line"`
	TS     time.Time `json:"ts"`
}

// DefaultLogStreams are the streams of the ordinary deployment log. The debug
// stream (the engine's verbose trace) is left out unless asked for.
var DefaultLogStreams = []string{agentprotocol.StreamBuild, agentprotocol.StreamDeploy, agentprotocol.StreamRuntime}

// AllLogStreams is every stream a deployment stores, the debug stream included.
var AllLogStreams = append(slices.Clone(DefaultLogStreams), agentprotocol.StreamDebug)

// ParseLogStreams reads the `streams` query parameter: empty selects the
// ordinary streams, `all` every stream, otherwise a comma-separated list of
// build, deploy, runtime and debug.
func ParseLogStreams(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	switch raw {
	case "":
		return DefaultLogStreams, nil
	case "all":
		return AllLogStreams, nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if !slices.Contains(AllLogStreams, part) {
			return nil, apierr.Unprocessable("validation_failed", "streams must be all, or a comma-separated list of build, deploy, runtime and debug")
		}
		if !slices.Contains(out, part) {
			out = append(out, part)
		}
	}
	return out, nil
}

// LogLines returns persisted lines of the given streams after the given line id.
func (s *Service) LogLines(ctx context.Context, id uuid.UUID, afterID int64, limit int, streams []string) ([]LogLineView, error) {
	if _, err := s.loadDeployment(ctx, policy.ScopeLogsRead, id); err != nil {
		return nil, err
	}
	return s.readLines(ctx, id, afterID, limit, streams)
}

func (s *Service) readLines(ctx context.Context, id uuid.UUID, afterID int64, limit int, streams []string) ([]LogLineView, error) {
	rows, err := s.q.ListDeploymentLogLines(ctx, db.ListDeploymentLogLinesParams{DeploymentID: id, AfterID: afterID, Streams: streams, RowLimit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list deployment logs: %w", err)
	}
	out := make([]LogLineView, len(rows))
	for i, r := range rows {
		out[i] = LogLineView{ID: r.ID, Stream: r.Stream, Line: r.Line, CreatedAt: r.CreatedAt}
	}
	return out, nil
}

// FollowLogs authorizes access and returns the SSE body that streams a
// deployment's log lines until it finishes, then sends `event: end`. The
// database is the source of truth (lines are already redacted when
// persisted); the hub's live feed only wakes the loop early.
func (s *Service) FollowLogs(ctx context.Context, id uuid.UUID, streams []string) (*huma.StreamResponse, error) {
	if _, err := s.loadDeployment(ctx, policy.ScopeLogsRead, id); err != nil {
		return nil, err
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		rctx := hctx.Context()
		st, err := services.NewStream(hctx)
		if err != nil {
			return
		}
		var wake <-chan agentprotocol.LogFrame
		if s.Follower != nil {
			var stop func()
			wake, stop = s.Follower.FollowDeploymentLogs(rctx, id)
			defer stop()
		}
		tick := time.NewTicker(logPollInterval)
		defer tick.Stop()
		ping := time.NewTicker(15 * time.Second)
		defer ping.Stop()
		var last int64
		for {
			// Read the status first: lines written before a terminal status
			// are then guaranteed to be flushed below.
			cur, err := s.q.GetDeploymentScopeInternal(rctx, id) // id was authorized by loadDeployment above
			if err != nil {
				return
			}
			finished := Status(cur.Deployment.Status).IsTerminal()
			for {
				lines, err := s.readLines(rctx, id, last, 500, streams)
				if err != nil {
					return
				}
				for _, l := range lines {
					last = l.ID
					if st.Data(LogLine{Stream: l.Stream, Line: l.Line, TS: l.CreatedAt}) != nil {
						return
					}
				}
				if len(lines) < 500 {
					break
				}
			}
			if finished {
				_ = st.Event("end", struct {
					Status string `json:"status"`
				}{Status: cur.Deployment.Status})
				return
			}
			select {
			case <-rctx.Done():
				return
			case <-ping.C:
				if st.Comment("ping") != nil {
					return
				}
			case _, ok := <-wake:
				if !ok {
					wake = nil
				}
			case <-tick.C:
			}
		}
	}}, nil
}
