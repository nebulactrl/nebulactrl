package databases

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var _ agents.SecretSource = (*Service)(nil)

// Handles reports whether the command kind belongs to this package.
func Handles(kind string) bool {
	return kind == agentprotocol.KindDBAdmin || kind == agentprotocol.KindDBUpgrade || kind == agentprotocol.KindDBReplicaClone
}

// SecretsForCommand attaches the send-time credentials of a db_admin command:
// the current password (and root password) for authentication and, for a
// rotation, the new password. They are never persisted.
func (s *Service) SecretsForCommand(ctx context.Context, _ uuid.UUID, kind string, payload json.RawMessage) (map[string]string, error) {
	if kind == agentprotocol.KindDBReplicaClone {
		out := map[string]string{}
		if err := s.replicationSecrets(ctx, kind, payload, out); err != nil {
			return nil, err
		}
		return out, nil
	}
	if kind != agentprotocol.KindDBAdmin {
		return nil, nil
	}
	var p agentprotocol.DBAdminPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("decode db_admin payload: %w", err)
	}
	svcID, err := uuid.Parse(p.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("db_admin service id: %w", err)
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, svcID)
	if err != nil {
		return nil, fmt.Errorf("load service: %w", err)
	}
	sc, spec, eng, err := s.asManaged(sc)
	if err != nil {
		return nil, err
	}
	creds, _, err := s.credentials(ctx, s.q, sc, spec, eng)
	if err != nil {
		return nil, fmt.Errorf("resolve credentials: %w", err)
	}
	out := map[string]string{}
	if creds.Password != "" {
		out[agentprotocol.SecretDBAdminPassword] = creds.Password
	}
	if creds.RootPassword != "" {
		out[agentprotocol.SecretDBAdminRootPassword] = creds.RootPassword
	}
	if p.Action == agentprotocol.DBActionRotatePassword {
		id, err := rotationIDOf(payload)
		if err != nil {
			return nil, err
		}
		rot, err := s.q.GetDatabaseRotation(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("get rotation: %w", err)
		}
		pw, err := s.box.OpenString(secretbox.DatabaseRotationsNewPassword, rot.ID.String(), rot.NewPasswordEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt rotation password: %w", err)
		}
		out[agentprotocol.SecretDBAdminNewPassword] = pw
	}
	if err := s.replicationSecrets(ctx, kind, payload, out); err != nil {
		return nil, err
	}
	return out, nil
}

// HandleResult records the outcome of a db_admin or db_upgrade command. Other
// kinds are ignored.
func (s *Service) HandleResult(ctx context.Context, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	cmd, err := s.q.GetAgentCommand(ctx, commandID)
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load command: %w", err)
	}
	defer s.kickForCommand(ctx, cmd)
	switch cmd.Kind {
	case agentprotocol.KindDBAdmin:
		var p agentprotocol.DBAdminPayload
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil
		}
		switch p.Action {
		case agentprotocol.DBActionRotatePassword:
			id, err := rotationIDOf(cmd.Payload)
			if err != nil {
				return nil
			}
			return s.applyRotationResult(ctx, id, res)
		case agentprotocol.DBActionInfo:
			return s.applyInfoResult(ctx, p.ServiceID, commandID, res)
		}
	case agentprotocol.KindDBUpgrade:
		var p agentprotocol.DBUpgradePayload
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil
		}
		id, err := uuid.Parse(p.UpgradeID)
		if err != nil {
			return nil
		}
		return s.applyUpgradeResult(ctx, id, res)
	}
	return nil
}

// HandleUndelivered fails the operation of a db_admin or db_upgrade command
// that could not be delivered (the server stayed offline until it expired), so
// nothing waits forever and nothing was changed.
func (s *Service) HandleUndelivered(ctx context.Context, commandID uuid.UUID, msg string) error {
	if msg == "" {
		msg = "Not delivered: server offline. No changes were made."
	}
	return s.HandleResult(ctx, commandID, agentprotocol.ResultFrame{CommandID: commandID.String(), Outcome: agentprotocol.OutcomeFailed, Error: msg})
}

// applyInfoResult stores the snapshot an info command returned.
func (s *Service) applyInfoResult(ctx context.Context, serviceID string, commandID uuid.UUID, res agentprotocol.ResultFrame) error {
	id, err := uuid.Parse(serviceID)
	if err != nil {
		return nil
	}
	sc, err := services.LoadScopeInternal(ctx, s.q, id)
	if err != nil {
		return nil
	}
	var info json.RawMessage
	errText := ""
	if res.Outcome == agentprotocol.OutcomeSucceeded {
		var out agentprotocol.DBAdminResult
		if err := json.Unmarshal(res.Data, &out); err == nil && out.Info != nil {
			info = mustJSON(out.Info)
		}
	} else {
		errText = res.Error
		if errText == "" {
			errText = "the agent could not read the database"
		}
	}
	if err := s.q.UpsertDatabaseInfo(ctx, db.UpsertDatabaseInfoParams{ServiceID: id, Info: info, Error: errText, CommandID: &commandID}); err != nil {
		return fmt.Errorf("store database info: %w", err)
	}
	s.publish(sc)
	return nil
}
