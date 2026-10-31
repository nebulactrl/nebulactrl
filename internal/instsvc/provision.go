package instsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/secretgen"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Names of the rows a team's slices are wired into. They are read-only for the
// team: the control plane resolves everything else from the slice.
const (
	destinationName = "Built-in backup storage"
	registryName    = "Built-in registry"
	drainName       = "Built-in central logs"
)

const (
	accessKeyAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	maxMessage        = 300
)

// Ensure makes the instance services ready: it records each kind's row and
// health, creates the system bucket and its key, keeps the slices' team names
// current, and provisions every team that is missing a slice (EnsureTeam). It is
// idempotent and runs at boot and every minute; a service that does not answer
// yet is shown as failed with the reason and tried again on the next run.
func (s *Service) Ensure(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()

	var errs []error
	healthy := map[string]bool{}
	for _, kind := range kinds {
		if err := s.ensureRow(ctx, kind); err != nil {
			return err
		}
	}
	for _, kind := range kinds {
		be := s.backends[kind]
		if be == nil {
			if err := s.setServiceStatus(ctx, kind, StateFailed, "Not set up on this installation."); err != nil {
				return err
			}
			continue
		}
		err := s.checkBackend(ctx, be)
		status, msg := StateReady, ""
		if err != nil {
			status, msg = StateFailed, truncate(err.Error())
			errs = append(errs, fmt.Errorf("%s: %w", kind, err))
		}
		healthy[kind] = err == nil
		if err := s.setServiceStatus(ctx, kind, status, msg); err != nil {
			return err
		}
	}
	if healthy[KindS3] {
		if err := s.ensureSystemSlice(ctx, s.backends[KindS3]); err != nil {
			errs = append(errs, fmt.Errorf("system bucket: %w", err))
		}
	}
	if err := s.q.SyncSliceTeamNames(ctx); err != nil {
		return fmt.Errorf("sync team names: %w", err)
	}
	teams, err := s.q.ListTeamsWithoutSlices(ctx, s.wantedKinds())
	if err != nil {
		return fmt.Errorf("list teams without slices: %w", err)
	}
	for _, t := range teams {
		if err := s.ensureTeam(ctx, t.ID); err != nil {
			errs = append(errs, fmt.Errorf("team %s: %w", t.ID, err))
		}
	}
	return errors.Join(errs...)
}

// checkBackend runs the backend's optional preparation and health check.
func (s *Service) checkBackend(ctx context.Context, be Backend) error {
	if p, ok := be.(Preparer); ok {
		if err := p.Prepare(ctx); err != nil {
			return err
		}
	}
	if h, ok := be.(Healther); ok {
		return h.Health(ctx)
	}
	return nil
}

// ensureRow creates the instance_services row of a kind when it is missing, so
// the backends can store their options and secrets on it.
func (s *Service) ensureRow(ctx context.Context, kind string) error {
	_, err := s.q.GetInstanceService(ctx, kind)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("get %s service: %w", kind, err)
	}
	if _, err := s.q.UpsertInstanceService(ctx, db.UpsertInstanceServiceParams{
		Kind: kind, Status: StateProvisioning, Endpoint: s.endpoints[kind],
	}); err != nil {
		return fmt.Errorf("create %s service: %w", kind, err)
	}
	return nil
}

// setServiceStatus records a kind's health; it writes only a change, so a
// healthy service does not touch its row every minute.
func (s *Service) setServiceStatus(ctx context.Context, kind, status, message string) error {
	row, err := s.q.GetInstanceService(ctx, kind)
	if err != nil {
		return fmt.Errorf("get %s service: %w", kind, err)
	}
	if row.Status == status && row.Message == message && row.Endpoint == s.endpoints[kind] {
		return nil
	}
	if _, err := s.q.UpsertInstanceService(ctx, db.UpsertInstanceServiceParams{
		Kind: kind, Status: status, Message: message, Endpoint: s.endpoints[kind],
	}); err != nil {
		return fmt.Errorf("record %s status: %w", kind, err)
	}
	return nil
}

// ensureSystemSlice creates the system bucket's key once and makes the bucket.
func (s *Service) ensureSystemSlice(ctx context.Context, be Backend) error {
	sys, err := s.systemKey(ctx)
	if errors.Is(err, ErrNoSlice) {
		if sys.AccessKey, err = secretgen.Secret(20, accessKeyAlphabet); err != nil {
			return err
		}
		if sys.Secret, err = secretgen.Secret(40, secretgen.AlphaAlnum); err != nil {
			return err
		}
		raw, err := json.Marshal(sys)
		if err != nil {
			return err
		}
		enc, err := s.box.Seal(secretbox.InstanceServicesSecrets, KindS3, raw)
		secretbox.Wipe(raw)
		if err != nil {
			return fmt.Errorf("seal system key: %w", err)
		}
		if err := s.q.SetInstanceServiceSecrets(ctx, db.SetInstanceServiceSecretsParams{Kind: KindS3, SecretsEnc: enc}); err != nil {
			return fmt.Errorf("store system key: %w", err)
		}
	} else if err != nil {
		return err
	}
	return be.EnsureSlice(ctx, Slice{Kind: KindS3, Ref: SystemBucket, AccessKey: sys.AccessKey}, 0)
}

// EnsureTeam gives a team its slices and the rows that use them: a bucket with a
// key that can only touch it, a repository prefix and a log tenant, plus the
// team's "Built-in backup storage" destination, "Built-in registry" and the
// central-logs drain (created off: the team turns it on). It is idempotent, so
// it is safe to run at creation, from the periodic Ensure and by hand; a team
// that is being deleted gets nothing.
func (s *Service) EnsureTeam(ctx context.Context, teamID uuid.UUID) error {
	if !s.Enabled() {
		return nil
	}
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	return s.ensureTeam(ctx, teamID)
}

func (s *Service) ensureTeam(ctx context.Context, teamID uuid.UUID) error {
	ws, err := s.q.GetWorkspace(ctx, teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get team: %w", err)
	}
	if ws.DeletionState != "" {
		return nil
	}
	def, err := defaultQuota(ctx, s.q)
	if err != nil {
		return err
	}
	var errs []error
	usable := map[string]bool{}
	for _, kind := range s.wantedKinds() {
		row, err := s.ensureSlice(ctx, ws, kind, def)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", kind, err))
		}
		usable[kind] = err == nil && (row.State == StateReady || row.State == StateFull)
	}
	if err := s.wireTeam(ctx, ws, usable); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ensureSlice creates the slice row when missing and has the backend make the
// slice real. A failure is stored on the slice (state failed, with the reason).
func (s *Service) ensureSlice(ctx context.Context, ws db.Workspace, kind string, def int64) (db.TeamServiceSlice, error) {
	row, err := s.q.GetTeamSlice(ctx, db.GetTeamSliceParams{TeamID: ws.ID, Kind: kind})
	if errors.Is(err, pgx.ErrNoRows) {
		if row, err = s.createSlice(ctx, ws, kind); err != nil {
			return row, err
		}
	} else if err != nil {
		return row, fmt.Errorf("get slice: %w", err)
	}
	if row.DeletedAt != nil {
		return row, nil
	}
	sl := sliceOf(row, def)
	if err := s.backends[kind].EnsureSlice(ctx, sl, sl.QuotaBytes); err != nil {
		if err := s.q.SetSliceState(ctx, setState(row.ID, StateFailed, truncate(err.Error()))); err != nil {
			s.log.WarnContext(ctx, "record slice failure", "team_id", ws.ID, "kind", kind, "err", err)
		}
		row.State = StateFailed
		return row, err
	}
	if row.State == StateProvisioning || row.State == StateFailed {
		if err := s.q.SetSliceState(ctx, setState(row.ID, StateReady, "")); err != nil {
			return row, fmt.Errorf("record slice state: %w", err)
		}
		row.State = StateReady
	}
	return row, nil
}

func (s *Service) createSlice(ctx context.Context, ws db.Workspace, kind string) (db.TeamServiceSlice, error) {
	p := db.InsertSliceParams{ID: uuid.New(), TeamID: ws.ID, TeamName: ws.Name, Kind: kind}
	switch kind {
	case KindS3:
		p.Ref = "nc-" + ws.ID.String()
		key, err := secretgen.Secret(20, accessKeyAlphabet)
		if err != nil {
			return db.TeamServiceSlice{}, err
		}
		secret, err := secretgen.Secret(40, secretgen.AlphaAlnum)
		if err != nil {
			return db.TeamServiceSlice{}, err
		}
		p.AccessKey = key
		if p.SecretEnc, err = s.box.SealString(secretbox.TeamSliceSecrets, p.ID.String(), secret); err != nil {
			return db.TeamServiceSlice{}, fmt.Errorf("seal slice secret: %w", err)
		}
	case KindRegistry:
		p.Ref = ws.ID.String() + "/"
	case KindLoki:
		p.Ref = ws.ID.String()
	default:
		return db.TeamServiceSlice{}, fmt.Errorf("unknown kind %q", kind)
	}
	row, err := s.q.InsertSlice(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) { // created by someone else meanwhile
		return s.q.GetTeamSlice(ctx, db.GetTeamSliceParams{TeamID: ws.ID, Kind: kind})
	}
	if err != nil {
		return row, fmt.Errorf("create slice: %w", err)
	}
	return row, nil
}

// wireTeam creates the team's built-in rows for the slices that are usable. The
// rows hold no credentials; each is created once and left alone afterwards.
func (s *Service) wireTeam(ctx context.Context, ws db.Workspace, usable map[string]bool) error {
	var errs []error
	if usable[KindS3] {
		_, err := s.q.GetBuiltinDestination(ctx, ws.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tryNames(destinationName, func(name string) error {
				_, err := s.q.InsertBuiltinDestination(ctx, db.InsertBuiltinDestinationParams{WorkspaceID: ws.ID, Name: name})
				return ignoreNoRows(err)
			})
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("backup destination: %w", err))
		}
	}
	if usable[KindRegistry] {
		_, err := s.q.GetBuiltinRegistry(ctx, ws.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tryNames(registryName, func(name string) error {
				_, err := s.q.InsertBuiltinRegistry(ctx, db.InsertBuiltinRegistryParams{
					WorkspaceID: ws.ID, Name: name, Host: fmt.Sprintf("127.0.0.1:%d", agentprotocol.LocalPortRegistry), Namespace: ws.ID.String(),
				})
				return ignoreNoRows(err)
			})
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("registry: %w", err))
		}
	}
	if usable[KindLoki] {
		_, err := s.q.GetBuiltinDrain(ctx, ws.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			cfg, merr := json.Marshal(map[string]any{"loki": agentprotocol.LokiDrainConfig{
				URL: fmt.Sprintf("http://127.0.0.1:%d/loki/api/v1/push", agentprotocol.LocalPortLoki),
			}})
			if merr != nil {
				return merr
			}
			err = tryNames(drainName, func(name string) error {
				_, err := s.q.InsertBuiltinDrain(ctx, db.InsertBuiltinDrainParams{WorkspaceID: ws.ID, Name: name, Config: cfg})
				return ignoreNoRows(err)
			})
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("log drain: %w", err))
		}
	}
	return errors.Join(errs...)
}

// tryNames inserts under name and, when the team already uses it for something
// else, under "name (NebulaCtrl)".
func tryNames(name string, insert func(name string) error) error {
	err := insert(name)
	if isUniqueViolation(err) {
		return insert(name + " (NebulaCtrl)")
	}
	return err
}

func ignoreNoRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func truncate(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > maxMessage {
		return msg[:maxMessage] + "…"
	}
	return msg
}

// MarkDeleted starts the 30-day clock of a deleted team's slices: they are no
// longer served (the forwarder and the gate see no slice), and the daily purge
// removes bucket, key, logs and registry images after PurgeAfter. The instance
// administrator can purge earlier. Idempotent: a second call keeps the dates.
func (s *Service) MarkDeleted(ctx context.Context, teamID uuid.UUID) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error { return s.MarkDeletedTx(ctx, tx, teamID) })
}

// MarkDeletedTx is MarkDeleted inside the caller's transaction (the team's own
// deletion), so the slices and the team go together.
func (s *Service) MarkDeletedTx(ctx context.Context, tx pgx.Tx, teamID uuid.UUID) error {
	_, err := db.New(tx).MarkTeamSlicesDeleted(ctx, db.MarkTeamSlicesDeletedParams{
		TeamID: teamID, PurgeAt: s.now().Add(PurgeAfter),
	})
	if err != nil {
		return fmt.Errorf("mark slices deleted: %w", err)
	}
	return nil
}
