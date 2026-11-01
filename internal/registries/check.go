package registries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/audit"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

const (
	// defaultCheckWait is how long a check request waits for the agent's answer.
	defaultCheckWait = 25 * time.Second
	// checkRepository is the probe repository a managed registry's check asks a
	// push token for.
	checkRepository = "nebulactrl-check"
	// checkCredentialTTL covers the agent's check (a few requests).
	checkCredentialTTL = 10 * time.Minute
	maxCheckMessage    = 1000
)

// checkPayload is the persisted payload of a registry_check command: the agent's
// payload plus the registry id the control plane resolves the credentials from at
// send time. It carries no credential.
type checkPayload struct {
	agentprotocol.RegistryCheckPayload
	RegistryID string `json:"registryId"`
}

// Handles reports whether a command kind belongs to this package.
func Handles(kind string) bool { return kind == agentprotocol.KindRegistryCheck }

// Check asks the agent on serverId to test the registry: reachability and the
// credentials, without building. It waits up to 25 seconds for the answer; the
// result is also stored in the registry's lastCheck.
func (s *Service) Check(ctx context.Context, id, serverID uuid.UUID) (RegistryCheck, error) {
	ws, err := s.workspace(ctx, policy.ScopeAdmin)
	if err != nil {
		return RegistryCheck{}, err
	}
	r, err := s.load(ctx, ws, id)
	if err != nil {
		return RegistryCheck{}, err
	}
	srv, err := s.q.LoadServerRow(ctx, db.LoadServerRowParams{ID: serverID, WorkspaceID: &ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return RegistryCheck{}, apierr.Unprocessable("server_not_found", "the selected server does not exist")
	}
	if err != nil {
		return RegistryCheck{}, fmt.Errorf("load server: %w", err)
	}
	if srv.Status != "online" {
		return RegistryCheck{}, apierr.Conflict("server_offline", srv.Name+" is not online, so the registry can not be tested from it")
	}
	info, err := s.info(ctx, r)
	if err != nil {
		return RegistryCheck{}, err
	}
	if r.Kind == KindBuiltin && !info.Healthy {
		return RegistryCheck{}, apierr.Conflict("registry_unavailable", info.HealthMessage)
	}
	payload := checkPayload{
		RegistryCheckPayload: agentprotocol.RegistryCheckPayload{Host: r.Host, Insecure: info.Insecure, Repository: probeRepository(r)},
		RegistryID:           r.ID.String(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return RegistryCheck{}, err
	}
	started := s.Now()
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := s.Dispatcher.Enqueue(ctx, tx, agents.NewCommand{ServerID: serverID, Kind: agentprotocol.KindRegistryCheck, Payload: raw}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{
			Action: AuditCheck, ResourceType: "registry", ResourceID: id.String(),
			Metadata: map[string]any{"name": r.Name, "server": srv.Name},
		})
	})
	if err != nil {
		return RegistryCheck{}, fmt.Errorf("enqueue registry check: %w", err)
	}
	deadline := time.NewTimer(s.checkWait)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return RegistryCheck{Pending: true, At: started, ServerID: &serverID}, nil
		case <-deadline.C:
			return RegistryCheck{Pending: true, At: started, ServerID: &serverID}, nil
		case <-tick.C:
			cur, err := s.q.GetRegistryInternal(ctx, id)
			if err != nil {
				continue
			}
			var all map[string]RegistryCheck
			if json.Unmarshal(cur.LastCheck, &all) != nil {
				continue
			}
			if c, ok := all[serverID.String()]; ok && !c.At.Before(started.Add(-time.Second)) {
				return c, nil
			}
		}
	}
}

// probeRepository is the repository a check asks the registry about: the team's
// own prefix for the built-in registry (its namespace is the team id); an external
// one gets it only when the user chose a namespace (a bare top-level name is not
// theirs to push to on most providers).
func probeRepository(r db.Registry) string {
	switch {
	case r.Namespace != "":
		return r.Namespace + "/" + checkRepository
	}
	return ""
}

// SecretsForCommand attaches the send-time credentials of a registry_check
// command: the push credential as registry_push_auth and, when the registry has
// a separate read-only credential, that one as registry_auth. They never enter
// the persisted payload.
func (s *Service) SecretsForCommand(ctx context.Context, kind string, payload json.RawMessage) (map[string]string, error) {
	if !Handles(kind) {
		return nil, nil
	}
	var p checkPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("decode registry_check payload: %w", err)
	}
	id, err := uuid.Parse(p.RegistryID)
	if err != nil {
		return nil, fmt.Errorf("registry id: %w", err)
	}
	r, err := s.q.GetRegistryInternal(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}
	repo := strings.TrimPrefix(p.Repository, r.Host+"/")
	push, err := s.Credentials(ctx, id, Scope{Repository: repo, Actions: []string{ActionPush, ActionPull}}, checkCredentialTTL)
	if err != nil {
		return nil, err
	}
	out := map[string]string{agentprotocol.SecretRegistryPushAuth: push}
	if len(r.PullAuthEnc) > 0 {
		pull, err := s.Credentials(ctx, id, Scope{Repository: repo, Actions: []string{ActionPull}}, checkCredentialTTL)
		if err != nil {
			return nil, err
		}
		out[agentprotocol.SecretRegistryAuth] = pull
	}
	return out, nil
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxCheckMessage {
		s = s[:maxCheckMessage] + "…"
	}
	return s
}

// HandleResult stores the outcome of a registry_check command. Commands of
// other kinds are ignored.
func (s *Service) HandleResult(ctx context.Context, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load command: %w", err)
	}
	if !Handles(cmd.Kind) {
		return nil
	}
	var data agentprotocol.RegistryCheckResult
	_ = json.Unmarshal(res.Data, &data)
	out := RegistryCheck{Push: data.Push, Pull: data.Pull, Error: data.Error, At: s.Now()}
	if out.Error == "" {
		out.Error = res.Error
	}
	switch {
	case res.Outcome != agentprotocol.OutcomeSucceeded:
		if out.Error == "" {
			out.Error = "the agent could not run the check"
		}
	case out.Error == "" && !data.Push:
		out.Error = "the registry did not accept the push credential"
	case out.Error == "" && !data.Pull:
		out.Error = "the registry did not accept the pull credential"
	}
	out.Ok = res.Outcome == agentprotocol.OutcomeSucceeded && data.Push && data.Pull && out.Error == ""
	out.Error = clip(s.scrub(ctx, cmd, out.Error))
	return s.recordCheck(ctx, cmd, out)
}

// HandleUndelivered records a registry_check that never reached its agent.
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load command: %w", err)
	}
	if !Handles(cmd.Kind) {
		return nil
	}
	return s.recordCheck(ctx, cmd, RegistryCheck{Error: clip(msg), At: s.Now()})
}

func (s *Service) recordCheck(ctx context.Context, cmd db.AgentCommand, out RegistryCheck) error {
	var p checkPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		s.Logger.WarnContext(ctx, "registry check result ignored: the stored payload cannot be parsed", "command_id", cmd.ID, "err", err)
		return nil
	}
	id, err := uuid.Parse(p.RegistryID)
	if err != nil {
		s.Logger.WarnContext(ctx, "registry check result ignored: the stored payload has no valid registry id", "command_id", cmd.ID, "err", err)
		return nil
	}
	out.ServerID = &cmd.ServerID
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if err := s.q.SetRegistryLastCheck(ctx, db.SetRegistryLastCheckParams{ID: id, ServerID: cmd.ServerID.String(), Result: raw}); err != nil {
		return fmt.Errorf("record registry check: %w", err)
	}
	if r, err := s.q.GetRegistryInternal(ctx, id); err == nil {
		s.publish(r.WorkspaceID, id)
	}
	return nil
}

var minted = regexp.MustCompile(`ncr1\.[A-Za-z0-9_-]+`)

// scrub masks the credentials of a registry in an agent-provided message: minted
// managed credentials and the stored external passwords.
func (s *Service) scrub(ctx context.Context, cmd db.AgentCommand, msg string) string {
	msg = minted.ReplaceAllString(msg, "••••••")
	var p checkPayload
	if json.Unmarshal(cmd.Payload, &p) != nil {
		return msg
	}
	id, err := uuid.Parse(p.RegistryID)
	if err != nil {
		return msg
	}
	r, err := s.q.GetRegistryInternal(ctx, id)
	if err != nil {
		return msg
	}
	for _, a := range []struct {
		col secretbox.Column
		enc []byte
	}{{secretbox.RegistriesPushAuth, r.PushAuthEnc}, {secretbox.RegistriesPullAuth, r.PullAuthEnc}} {
		if c, err := s.open(a.col, r.ID, a.enc); err == nil && len(c.Password) >= 4 {
			msg = strings.ReplaceAll(msg, c.Password, "••••••")
		}
	}
	return msg
}
