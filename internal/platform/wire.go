package platform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// headscaleKeyDays is how long the Headscale API key lives; "Reconnect" mints a
// new one.
const headscaleKeyDays = 365

// WiringType values.
const WiringTailnet = "tailscale_integration"

// primary returns the first service row of a platform service.
func (s *Service) primary(ctx context.Context, row db.PlatformService) (db.Service, error) {
	if len(row.ServiceIds) == 0 {
		return db.Service{}, fatalf("The service was removed outside this page. Remove %s here and set it up again.", row.Kind)
	}
	rows, err := s.q.ListServicesIncludingDeleted(ctx, row.ServiceIds[:1])
	if err != nil {
		return db.Service{}, err
	}
	if len(rows) == 0 || rows[0].DeletedAt != nil {
		return db.Service{}, fatalf("The service was removed outside this page. Remove %s here and set it up again.", row.Kind)
	}
	if rows[0].ServerID == nil {
		return db.Service{}, fatalf("The service has no server.")
	}
	return rows[0], nil
}

// wire connects the deployed Headscale to the Tailscale integration.
func (s *Service) wire(ctx context.Context, row db.PlatformService, opts *options) error {
	svc, err := s.primary(ctx, row)
	if err != nil {
		return err
	}
	w := decodeJSON[wiring](row.Wiring)
	save := func() error {
		_, err := s.q.SetPlatformWiring(ctx, db.SetPlatformWiringParams{ID: row.ID, Wiring: encodeJSON(w)})
		return err
	}
	werr := s.wireHeadscale(ctx, row, svc, opts, &w, save)
	s.afterHeadscaleWire(ctx, row.WorkspaceID, werr)
	if werr != nil {
		return werr
	}
	s.autoBackup(ctx, row, &w)
	_ = save()
	return nil
}

// probe requests url (with basic auth when user is set) and expects status.
func (s *Service) probe(ctx context.Context, url, user, pass string, want int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	res, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != want {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return nil
}

func (s *Service) wireHeadscale(ctx context.Context, row db.PlatformService, svc db.Service, opts *options, w *wiring, save func() error) error {
	if err := s.probe(ctx, row.PublicUrl+"/health", "", "", http.StatusOK); err != nil {
		return retryLater("Waiting for Headscale to answer at %s: %v", row.PublicUrl, shorten(err))
	}
	cur, err := s.Tailnet.Get(ctx)
	if err != nil {
		return err
	}
	if cur.Configured && !(w.Type == WiringTailnet) && !opts.ReplaceIntegration {
		return fatalf("A Tailscale integration was added in the meantime. Remove it and retry, or set Headscale up again and choose to replace it.")
	}
	// The user owns the pre-auth keys; it exists once (the command fails when it
	// does, which is fine).
	if out, code, err := s.execOnce(ctx, *svc.ServerID, svc.ID, []string{"headscale", "users", "create", headscaleUser}, 60*time.Second); err != nil {
		return err
	} else if code != 0 && !strings.Contains(strings.ToLower(out), "unique") && !strings.Contains(strings.ToLower(out), "exist") {
		return retryLater("Headscale is not ready to create its first user: %s", lastLine(out))
	}
	out, code, err := s.execOnce(ctx, *svc.ServerID, svc.ID, []string{"headscale", "apikeys", "create", "--expiration", fmt.Sprintf("%dd", headscaleKeyDays)}, 60*time.Second)
	if err != nil {
		return err
	}
	key := lastLine(out)
	if code != 0 || !strings.HasPrefix(key, "hskey-api-") {
		return retryLater("Headscale did not create an API key: %s", shorten(errors.New(lastLine(out))))
	}
	// The mesh tag must be allowed too: the mesh refuses to enable (and the
	// integration refuses to change while it is in use) without it.
	if _, err := s.Tailnet.Put(ctx, tailscale.PutInput{
		Provider: tailscale.ProviderHeadscale, LoginServer: row.PublicUrl, ClientSecret: key, HeadscaleUser: headscaleUser,
		HeadscaleCredential: tailscale.HeadscaleAPIKey, DefaultTags: []string{headscaleTag}, AllowedTags: []string{headscaleTag, s.meshTag(ctx, row.WorkspaceID)},
	}); err != nil {
		return err
	}
	// The tailnet policy is NebulaCtrl's (policy.mode: database): write it before
	// any node can join. Until the first write Headscale allows everything.
	if s.Mesh != nil {
		if err := s.Mesh.ApplyPolicy(ctx, row.WorkspaceID); err != nil {
			return retryLater("Waiting to write the Headscale policy: %s", shorten(err))
		}
	}
	exp := s.Now().Add(headscaleKeyDays * 24 * time.Hour)
	opts.KeyExpiresAt = &exp
	if _, err := s.q.SetPlatformOptions(ctx, db.SetPlatformOptionsParams{ID: row.ID, Options: encodeJSON(*opts)}); err != nil {
		return err
	}
	*w = wiring{Type: WiringTailnet, ID: "tailscale", Name: "Tailscale integration", ServerID: svc.ServerID.String()}
	if err := save(); err != nil {
		return err
	}
	res, err := s.Tailnet.Test(ctx)
	if err != nil {
		return err
	}
	if !res.OK {
		var msgs []string
		for _, c := range res.Checks {
			if !c.OK {
				msgs = append(msgs, c.Message)
			}
		}
		return retryLater("The connection test failed: %s", strings.Join(msgs, "; "))
	}
	return nil
}

// autoBackup schedules a daily volume backup of Headscale's data to an existing
// external S3 destination, when the team has one. Without one nothing is
// scheduled; the page says so.
func (s *Service) autoBackup(ctx context.Context, row db.PlatformService, w *wiring) {
	if w.BackupDestinationID != "" {
		return
	}
	dests, err := s.Backups.ListDestinations(ctx)
	if err != nil {
		return
	}
	for _, d := range dests {
		if d.Kind != agentprotocol.DestS3 {
			continue
		}
		if err := s.configureBackup(ctx, row, d.ID); err == nil {
			w.BackupDestinationID = d.ID.String()
		} else {
			s.Logger.WarnContext(ctx, "platform: schedule backup", "platform_service", row.ID.String(), "err", err)
		}
		return
	}
}

// configureBackup sets a daily 03:17 UTC volume backup of every service of row.
func (s *Service) configureBackup(ctx context.Context, row db.PlatformService, dest uuid.UUID) error {
	keepD, keepW, keepM := 7, 4, 3
	for _, sid := range row.ServiceIds {
		if _, err := s.Backups.PutVolumeBackupConfig(ctx, sid, backups.VolumeBackupConfigInput{
			DestinationID: dest, Cron: "17 3 * * *", Timezone: "UTC", Enabled: true, KeepDaily: &keepD, KeepWeekly: &keepW, KeepMonthly: &keepM,
		}); err != nil {
			return err
		}
	}
	return nil
}

func shorten(err error) string {
	m := err.Error()
	if len(m) > 200 {
		m = m[:200] + "…"
	}
	return m
}
