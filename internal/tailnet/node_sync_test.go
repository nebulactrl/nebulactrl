package tailnet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/events"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/tailnet/tailnetcfg"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// failDisp refuses to enqueue commands.
type failDisp struct{ *fakeDisp }

func (failDisp) Enqueue(context.Context, pgx.Tx, agents.NewCommand) (uuid.UUID, error) {
	return uuid.Nil, errors.New("dispatcher down")
}

// applied returns a service with an applied tailnet node on f.server.
func (f *fixture) appliedNode(slug string, tn *tailnetcfg.TailnetConfig) (db.Service, agentprotocol.ServiceTailnetSpec) {
	f.t.Helper()
	sv := f.service(slug, tn)
	sc := f.scope(sv.ID)
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(f.t, err)
	f.apply(*spec, online, f.server.ID)
	return sv, *spec
}

func (f *fixture) command(kind string, serverID uuid.UUID, deployment *uuid.UUID, payload any) db.AgentCommand {
	f.t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(f.t, err)
	id := uuid.New()
	cmd, err := f.q.InsertAgentCommand(bg, db.InsertAgentCommandParams{
		ID: id, ServerID: serverID, DeploymentID: deployment, Kind: kind, Payload: raw, IdempotencyKey: id.String(), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(f.t, err)
	return cmd
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.pool.Exec(bg, sql, args...)
	require.NoError(f.t, err)
}

// tailscaleAPI replaces the fake Tailscale API; fail decides which calls get a 500.
func (f *fixture) tailscaleAPI(fail func(*http.Request) bool) *[]string {
	f.t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v2/oauth/token" {
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		if fail(r) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"kMINT2","key":"` + mintedKey + `"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	f.t.Cleanup(srv.Close)
	f.ts.SetBaseURL(srv.URL, srv.Client())
	return &calls
}

func TestSupportsServiceTailnetReadsTheAdvertisedCapability(t *testing.T) {
	for name, tc := range map[string]struct {
		facts string
		want  bool
	}{
		"never connected":               {"", true},
		"unreadable facts":              {"not json", true},
		"facts without an OS":           {`{}`, true},
		"an agent without the feature":  {`{"os":"linux","capabilities":["proxy.v2"]}`, false},
		"an agent with no capabilities": {`{"os":"linux"}`, false},
		"an agent with the feature":     {`{"os":"linux","capabilities":["` + agentprotocol.CapServiceTailnet + `"]}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			var facts json.RawMessage
			if tc.facts != "" {
				facts = json.RawMessage(tc.facts)
			}
			require.Equal(t, tc.want, SupportsServiceTailnet(db.Server{Facts: facts}))
		})
	}
	require.True(t, Handles(agentprotocol.KindSyncTailnet))
	require.True(t, Handles(agentprotocol.KindRemoveTailnet))
	require.False(t, Handles(agentprotocol.KindDeploy))
}

func nextEvent(t *testing.T, feed <-chan events.Event) events.Event {
	t.Helper()
	select {
	case ev := <-feed:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event was published")
		return events.Event{}
	}
}

func TestAnOutdatedAgentCannotBeGivenATailnetNode(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	old := f.server
	old.Facts = json.RawMessage(`{"os":"linux","capabilities":["proxy.v2"]}`)

	_, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), old, false)
	require.Equal(t, "agent_too_old", apiCode(t, err))

	// Removing a node needs the sidecar's agent too, and only when there is a node.
	off := sc.Config()
	off.Tailnet = nil
	spec, _, err := f.svc.Spec(bg, f.q, sc, off, old, false)
	require.NoError(t, err)
	require.Nil(t, spec, "no node, nothing to remove")

	node, nodeSpec := f.appliedNode("api", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	nsc := f.scope(node.ID)
	_, _, err = f.svc.Spec(bg, f.q, nsc, off, old, false)
	require.Equal(t, "agent_too_old", apiCode(t, err))
	_, _, err = f.svc.Spec(bg, f.q, nsc, nsc.Config(), old, true)
	require.Equal(t, "agent_too_old", apiCode(t, err), "keeping the node needs the sidecar's agent too")
	require.NotNil(t, nodeSpec)
}

func TestSpecForAnExistingNodeThatIsBeingTurnedOff(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, _ := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	off := sc.Config()
	off.Tailnet = nil

	spec, hide, err := f.svc.Spec(bg, f.q, sc, off, f.server, false)
	require.NoError(t, err)
	require.False(t, hide)
	require.Equal(t, &agentprotocol.ServiceTailnetSpec{Mode: agentprotocol.TailnetRemove, ServiceID: sv.ID.String()}, spec)

	// Once it is on its way out there is nothing more to ask for.
	f.exec(`UPDATE service_tailnet SET state = 'removing'`)
	spec, _, err = f.svc.Spec(bg, f.q, sc, off, f.server, false)
	require.NoError(t, err)
	require.Nil(t, spec)
}

func TestSpecKeepsTheNodeAsLastApplied(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, applied := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true, HidePublic: true})
	sc := f.scope(sv.ID)
	cfg := services.Config{Port: 8080, Tailnet: nil}

	// The configuration being deployed says nothing about the tailnet: the applied node is kept, with the port of this release.
	spec, hide, err := f.svc.Spec(bg, f.q, sc, cfg, f.server, true)
	require.NoError(t, err)
	require.True(t, hide, "tailnet only stays on")
	require.False(t, spec.Provision, "same server, nothing to re-create")
	require.Equal(t, applied.Hostname, spec.Hostname)
	require.Equal(t, 8080, spec.Port)

	// On another server the node is created again.
	moved, _, err := f.svc.Spec(bg, f.q, sc, cfg, f.other, true)
	require.NoError(t, err)
	require.True(t, moved.Provision)

	// A node that needs attention is re-created too.
	f.exec(`UPDATE service_tailnet SET needs_reprovision = true`)
	again, _, err := f.svc.Spec(bg, f.q, sc, cfg, f.server, true)
	require.NoError(t, err)
	require.True(t, again.Provision)

	// Nothing to keep: no node, a node on its way out, a torn or a remove spec.
	plain := f.service("plain", nil)
	none, hide, err := f.svc.Spec(bg, f.q, f.scope(plain.ID), cfg, f.server, true)
	require.NoError(t, err)
	require.Nil(t, none)
	require.False(t, hide)

	for name, mutate := range map[string]string{
		"removing":    `UPDATE service_tailnet SET state = 'removing'`,
		"torn":        `UPDATE service_tailnet SET applied = '"torn"'::jsonb`,
		"empty":       `UPDATE service_tailnet SET applied = '{}'::jsonb`,
		"remove mode": `UPDATE service_tailnet SET applied = jsonb_set(applied, '{mode}', '"remove"')`,
	} {
		f.exec(mutate)
		got, _, err := f.svc.Spec(bg, f.q, sc, cfg, f.server, true)
		require.NoError(t, err, name)
		require.Nil(t, got, name)
		f.exec(`UPDATE service_tailnet SET state = 'online', applied = $1::jsonb`, mustJSON(t, applied))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func TestPlanCheckExplainsRemovalAndRecreation(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, _ := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	off := sc.Config()
	off.Tailnet = nil

	// A node without a name from the tailnet is announced by its hostname.
	f.exec(`UPDATE service_tailnet SET fqdn = ''`)
	warns, probs, err := f.svc.PlanCheck(bg, f.q, sc, off, &f.server)
	require.NoError(t, err)
	require.Empty(t, probs)
	require.Len(t, warns, 1)
	require.Contains(t, warns[0], "removes device web-production from your tailnet")

	old := f.server
	old.Facts = json.RawMessage(`{"os":"linux"}`)
	_, probs, err = f.svc.PlanCheck(bg, f.q, sc, off, &old)
	require.NoError(t, err)
	require.Len(t, probs, 1)
	require.Equal(t, "agent_too_old", probs[0].Code)
	require.Contains(t, probs[0].Message, "remove the tailnet sidecar")

	// A node that is already being removed announces nothing.
	f.exec(`UPDATE service_tailnet SET state = 'removing'`)
	warns, probs, err = f.svc.PlanCheck(bg, f.q, sc, off, &f.server)
	require.NoError(t, err)
	require.Empty(t, warns)
	require.Empty(t, probs)
	f.exec(`UPDATE service_tailnet SET state = 'online'`)

	// The same configuration on the same server changes nothing...
	warns, _, err = f.svc.PlanCheck(bg, f.q, sc, sc.Config(), &f.server)
	require.NoError(t, err)
	require.Empty(t, warns)

	// ...but another hostname, a node that needs attention, or another server re-creates the device.
	renamed := sc.Config()
	renamed.Tailnet.Hostname = "api"
	warns, _, err = f.svc.PlanCheck(bg, f.q, sc, renamed, &f.server)
	require.NoError(t, err)
	require.Len(t, warns, 1)
	require.Contains(t, warns[0], "re-creates device api")

	warns, _, err = f.svc.PlanCheck(bg, f.q, sc, sc.Config(), &f.other)
	require.NoError(t, err)
	require.Len(t, warns, 1)
	require.Contains(t, warns[0], "re-creates device web-production")

	f.exec(`UPDATE service_tailnet SET needs_reprovision = true`)
	warns, _, err = f.svc.PlanCheck(bg, f.q, sc, sc.Config(), &f.server)
	require.NoError(t, err)
	require.Len(t, warns, 1)
	require.Contains(t, warns[0], "re-creates device")

	// A service without a node and with the tailnet off has nothing to say.
	plain := f.service("plain", nil)
	pc := f.scope(plain.ID)
	warns, probs, err = f.svc.PlanCheck(bg, f.q, pc, pc.Config(), &f.server)
	require.NoError(t, err)
	require.Empty(t, warns)
	require.Empty(t, probs)
}

func TestPlanCheckWarnsThatHeadscaleServesWebOverPlainHTTP(t *testing.T) {
	f := newFixture(t)
	f.connectHeadscale()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)

	warns, probs, err := f.svc.PlanCheck(bg, f.q, sc, sc.Config(), &f.server)
	require.NoError(t, err)
	require.Empty(t, probs)
	require.Len(t, warns, 2)
	require.Contains(t, warns[1], "plain HTTP over WireGuard on port 80")

	tcpOnly := sc.Config()
	tcpOnly.Tailnet = &tailnetcfg.TailnetConfig{Enabled: true, TCP: []tailnetcfg.TCPForward{{Listen: 5432, Target: 5432}}}
	warns, _, err = f.svc.PlanCheck(bg, f.q, sc, tcpOnly, &f.server)
	require.NoError(t, err)
	require.Len(t, warns, 1, "no web exposure, no HTTP warning")
}

func TestSecretsRefusesWhenNoIntegrationIsConnected(t *testing.T) {
	f := newFixture(t)
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	spec := agentprotocol.ServiceTailnetSpec{Mode: agentprotocol.TailnetEnsure, Provision: true, ServiceID: sv.ID.String(), Tags: []string{"tag:nebula-svc"}}

	_, err := f.svc.Secrets(bg, sv.ID, spec)
	require.Equal(t, "tailnet_not_configured", apiCode(t, err))

	none, err := f.svc.Secrets(bg, sv.ID, agentprotocol.ServiceTailnetSpec{Mode: agentprotocol.TailnetRemove, Provision: true})
	require.NoError(t, err)
	require.Nil(t, none, "a remove spec carries no key")

	_, err = f.svc.Secrets(bg, uuid.New(), spec)
	require.ErrorContains(t, err, "load service")
}

func TestSecretsSurfacesAFailureToMintTheKey(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	f.tailscaleAPI(func(r *http.Request) bool { return r.Method == http.MethodPost })

	_, err = f.svc.Secrets(bg, sv.ID, *spec)
	require.ErrorContains(t, err, "mint a tailnet auth key")
}

func TestSecretsReplacesThePreviousDeviceAndRevokesItsKey(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, applied := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	applied.Provision = true
	calls := f.tailscaleAPI(func(*http.Request) bool { return false })

	// The first re-provisioning remembers the id of the key it minted...
	_, err := f.svc.Secrets(bg, sv.ID, applied)
	require.NoError(t, err)
	require.Equal(t, "kMINT2", f.row(sv.ID).AuthKeyID)
	require.Equal(t, []string{"DELETE /api/v2/device/nNODE1CNTRL", "POST /api/v2/tailnet/-/keys"}, *calls)

	// ...so the next one revokes it.
	*calls = nil
	f.exec(`UPDATE service_tailnet SET device_id = 'nNODE2'`)
	_, err = f.svc.Secrets(bg, sv.ID, applied)
	require.NoError(t, err)
	require.Equal(t, []string{"DELETE /api/v2/device/nNODE2", "DELETE /api/v2/tailnet/-/keys/kMINT2", "POST /api/v2/tailnet/-/keys"}, *calls)
}

func TestSecretsStillMintsWhenTheOldKeyCannotBeRevoked(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, applied := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	applied.Provision = true
	f.exec(`UPDATE service_tailnet SET auth_key_id = 'kOLD'`)
	f.tailscaleAPI(func(r *http.Request) bool {
		return r.Method == http.MethodDelete && r.URL.Path == "/api/v2/tailnet/-/keys/kOLD"
	})
	var logs bytes.Buffer
	f.svc.log = slog.New(slog.NewTextHandler(&logs, nil))

	secrets, err := f.svc.Secrets(bg, sv.ID, applied)
	require.NoError(t, err, "a key that is already gone must not block the deployment")
	require.Equal(t, mintedKey, secrets[agentprotocol.SecretServiceTailnetKey])
	require.Contains(t, logs.String(), "could not revoke the previous node key")
	require.Contains(t, logs.String(), sv.ID.String())
}

func TestSecretsDoesNotMintWhenTheOldDeviceCannotBeRemoved(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, applied := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	applied.Provision = true
	calls := f.tailscaleAPI(func(r *http.Request) bool { return r.Method == http.MethodDelete })

	_, err := f.svc.Secrets(bg, sv.ID, applied)
	require.ErrorContains(t, err, "remove the previous tailnet device")
	require.Equal(t, []string{"DELETE /api/v2/device/nNODE1CNTRL"}, *calls, "no key is minted for a node that would get a second name")
	require.Equal(t, "nNODE1CNTRL", f.row(sv.ID).DeviceID, "the device stays known so the next attempt removes it")
}

func TestSecretsForCommandRejectsAMalformedSyncCommand(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.SecretsForCommand(bg, agentprotocol.KindSyncTailnet, json.RawMessage(`"garbage"`))
	require.ErrorContains(t, err, "decode sync_tailnet payload")

	_, err = f.svc.SecretsForCommand(bg, agentprotocol.KindSyncTailnet, json.RawMessage(`{"tailnet":{"serviceId":"nope"}}`))
	require.ErrorContains(t, err, "sync_tailnet service id")
}

func TestRecordDeployStoresTheNodeTheDeploymentCarried(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	dep, err := f.q.InsertDeployment(bg, db.InsertDeploymentParams{ServiceID: sv.ID, ServerID: &f.server.ID, Trigger: "manual", ActorType: "user", Strategy: "rolling"})
	require.NoError(t, err)
	record := func(st *agentprotocol.ServiceTailnetStatus) error {
		return db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.RecordDeploy(bg, tx, f.ws, dep, st) })
	}

	require.NoError(t, record(&online), "a deployment without a recorded command has nothing to record")
	_, err = f.q.GetServiceTailnet(bg, sv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	f.command(agentprotocol.KindDeploy, f.server.ID, &dep.ID, "garbage")
	require.NoError(t, record(&online), "a payload that cannot be read never rolls the deployment back")
	_, err = f.q.GetServiceTailnet(bg, sv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	f.exec(`DELETE FROM agent_commands`)
	f.command(agentprotocol.KindDeploy, f.server.ID, &dep.ID, agentprotocol.DeployPayload{ServiceID: sv.ID.String()})
	require.NoError(t, record(&online))
	_, err = f.q.GetServiceTailnet(bg, sv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "a deployment without a tailnet spec records nothing")

	f.exec(`DELETE FROM agent_commands`)
	f.command(agentprotocol.KindDeploy, f.server.ID, &dep.ID, agentprotocol.DeployPayload{ServiceID: sv.ID.String(), Tailnet: spec})
	require.NoError(t, record(&online))
	row := f.row(sv.ID)
	require.Equal(t, services.TailnetOnline, row.State)
	require.Equal(t, f.server.ID, *row.ServerID)
	require.Equal(t, "web-production", row.Hostname)
}

func TestApplyOfARemoveSpec(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, _ := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	remove := agentprotocol.ServiceTailnetSpec{Mode: agentprotocol.TailnetRemove, ServiceID: sv.ID.String()}

	// The agent could not remove it and says why: the node needs attention but stays.
	f.apply(remove, agentprotocol.ServiceTailnetStatus{Message: "docker refused"}, f.server.ID)
	row := f.row(sv.ID)
	require.Equal(t, services.TailnetNeedsAttn, row.State)
	require.Equal(t, "docker refused", row.Message)

	// Not removed and nothing to say: unchanged.
	f.apply(remove, agentprotocol.ServiceTailnetStatus{}, f.server.ID)
	require.Equal(t, "docker refused", f.row(sv.ID).Message)

	// Removed: the row goes, the device is deleted on the tailnet, and the sidecar
	// is not told again because the deployment that removed it ran on that server.
	f.apply(remove, agentprotocol.ServiceTailnetStatus{Removed: true}, f.server.ID)
	_, err := f.q.GetServiceTailnet(bg, sv.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.Empty(t, f.disp.kinds())
	require.Len(t, f.riverArgs(), 1)

	// A second report finds nothing to remove.
	f.apply(remove, agentprotocol.ServiceTailnetStatus{Removed: true}, f.server.ID)
	require.Len(t, f.riverArgs(), 1)
}

func TestApplyRemovedOnAnotherServerAlsoRemovesTheSidecarWhereItRuns(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, _ := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	remove := agentprotocol.ServiceTailnetSpec{Mode: agentprotocol.TailnetRemove, ServiceID: sv.ID.String()}

	f.apply(remove, agentprotocol.ServiceTailnetStatus{Removed: true}, f.other.ID)
	require.Equal(t, []string{agentprotocol.KindRemoveTailnet}, f.disp.kinds())
	require.Equal(t, f.server.ID, f.disp.cmds[0].ServerID, "the sidecar is removed where the node lived")
}

func TestApplyIgnoresWhatCannotBeAttributedAndNormalisesTheState(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.apply(agentprotocol.ServiceTailnetSpec{Mode: agentprotocol.TailnetEnsure, ServiceID: "not-a-uuid"}, online, f.server.ID)
	require.Zero(t, f.countRows())

	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	f.apply(*spec, agentprotocol.ServiceTailnetStatus{State: "something new"}, f.server.ID)
	row := f.row(sv.ID)
	require.Equal(t, services.TailnetProvisioning, row.State, "an unknown agent state is shown as provisioning")
	require.Empty(t, row.Ips)
	require.False(t, row.NeedsReprovision)
}

func (f *fixture) countRows() int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.pool.QueryRow(bg, `SELECT count(*) FROM service_tailnet`).Scan(&n))
	return n
}

func TestServiceDeletionFailsWhenTheSidecarRemovalCannotBeQueued(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, _ := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	f.svc.disp = failDisp{f.disp}

	err := db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.ServiceDeleted(bg, tx, f.scope(sv.ID)) })
	require.ErrorContains(t, err, "enqueue remove_tailnet")
	require.Equal(t, 1, f.countRows(), "the failed transaction left the node")
}

func TestServiceDeletionOfANodeWithoutAServerOnlyRemovesTheDevice(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, _ := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	f.exec(`UPDATE service_tailnet SET server_id = NULL`)

	require.NoError(t, db.InTx(bg, f.pool, func(tx pgx.Tx) error { return f.svc.ServiceDeleted(bg, tx, f.scope(sv.ID)) }))
	require.Empty(t, f.disp.kinds())
	require.Len(t, f.riverArgs(), 1)
}

func TestConfigChangedOnlyAuditsRealTransitions(t *testing.T) {
	f := newFixture(t)
	sv := f.service("web", nil)
	sc := f.scope(sv.ID)
	web := &tailnetcfg.TailnetConfig{Enabled: true, Web: true}
	funnel := &tailnetcfg.TailnetConfig{Enabled: true, Web: true, Funnel: true}
	change := func(from, to *tailnetcfg.TailnetConfig) {
		require.NoError(t, db.InTx(f.admin, f.pool, func(tx pgx.Tx) error { return f.svc.ConfigChanged(f.admin, tx, sc, from, to) }))
	}
	actions := func() []string {
		rows, err := f.pool.Query(bg, `SELECT action FROM audit_events WHERE action LIKE 'tailnet.%' ORDER BY id`)
		require.NoError(f.t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var a string
			require.NoError(f.t, rows.Scan(&a))
			out = append(out, a)
		}
		return out
	}

	change(nil, nil)
	change(web, web)
	require.Empty(t, actions(), "nothing changed, nothing audited")

	change(web, funnel)
	change(funnel, funnel)
	change(funnel, web)
	require.Equal(t, []string{"tailnet.funnel_enabled"}, actions(), "only turning Funnel on is audited, once")
}

func TestHandleResultAppliesTheAgentsAnswerToASyncCommand(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv := f.service("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	spec, _, err := f.svc.Spec(bg, f.q, sc, sc.Config(), f.server, false)
	require.NoError(t, err)
	cmd := f.command(agentprotocol.KindSyncTailnet, f.server.ID, nil, agentprotocol.SyncTailnetPayload{Tailnet: *spec})
	feed := f.svc.bus.Subscribe(bg)

	data, err := json.Marshal(online)
	require.NoError(t, err)
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, cmd.ID, agentprotocol.ResultFrame{CommandID: cmd.ID.String(), Outcome: agentprotocol.OutcomeSucceeded, Data: data}))
	row := f.row(sv.ID)
	require.Equal(t, services.TailnetOnline, row.State)
	require.Equal(t, "web-production.tail1234.ts.net", row.Fqdn)
	require.Equal(t, f.server.ID, *row.ServerID)
	require.Equal(t, events.Event{Type: services.EventServiceUpdated, ProjectID: sc.ProjectID.String(), EnvironmentID: sc.EnvironmentID.String(), ResourceID: sv.ID.String(), WorkspaceID: f.ws.String()}, nextEvent(t, feed))

	// A failure, with or without the agent's words, needs attention.
	for name, tc := range map[string]struct {
		res  agentprotocol.ResultFrame
		want string
	}{
		"failed with a reason": {agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "login failed"}, "login failed"},
		"failed silently":      {agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed}, "The tailnet sidecar could not be provisioned."},
		"succeeded without a state": {agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: json.RawMessage(`{}`)},
			"The tailnet sidecar could not be provisioned."},
		"succeeded with unreadable data": {agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeSucceeded, Data: json.RawMessage(`"x"`)},
			"The tailnet sidecar could not be provisioned."},
	} {
		f.exec(`UPDATE service_tailnet SET state = 'online', message = ''`)
		require.NoError(t, f.svc.HandleResult(bg, f.server.ID, cmd.ID, tc.res), name)
		row := f.row(sv.ID)
		require.Equal(t, services.TailnetNeedsAttn, row.State, name)
		require.Equal(t, tc.want, row.Message, name)
		require.True(t, row.NeedsReprovision, name)
		require.Equal(t, events.Event{Type: services.EventServiceUpdated, ProjectID: sc.ProjectID.String(), EnvironmentID: sc.EnvironmentID.String(), ResourceID: sv.ID.String(), WorkspaceID: f.ws.String()}, nextEvent(t, feed), name)
	}
}

func TestHandleResultIgnoresWhatItCannotAttribute(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, spec := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	failed := agentprotocol.ResultFrame{Outcome: agentprotocol.OutcomeFailed, Error: "x"}

	// Unknown command.
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, uuid.New(), failed))
	// A remove_tailnet command: the node was forgotten when it was enqueued.
	remove := f.command(agentprotocol.KindRemoveTailnet, f.server.ID, nil, agentprotocol.RemoveTailnetPayload{ServiceID: sv.ID.String()})
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, remove.ID, failed))
	// Payloads that name no service.
	garbage := f.command(agentprotocol.KindSyncTailnet, f.server.ID, nil, "garbage")
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, garbage.ID, failed))
	nonUUID := f.command(agentprotocol.KindSyncTailnet, f.server.ID, nil, agentprotocol.SyncTailnetPayload{Tailnet: agentprotocol.ServiceTailnetSpec{ServiceID: "nope"}})
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, nonUUID.ID, failed))
	// The service was deleted while the command ran.
	ghost := spec
	ghost.ServiceID = uuid.NewString()
	gone := f.command(agentprotocol.KindSyncTailnet, f.server.ID, nil, agentprotocol.SyncTailnetPayload{Tailnet: ghost})
	require.NoError(t, f.svc.HandleResult(bg, f.server.ID, gone.ID, failed))

	require.Equal(t, services.TailnetOnline, f.row(sv.ID).State, "none of those touched the node")
}

func TestHandleUndeliveredMarksTheNodeAsNeedingAttention(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, spec := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	feed := f.svc.bus.Subscribe(bg)
	sync := f.command(agentprotocol.KindSyncTailnet, f.server.ID, nil, agentprotocol.SyncTailnetPayload{Tailnet: spec})

	require.NoError(t, f.svc.HandleUndelivered(bg, sync.ID, "the agent was offline for too long"))
	row := f.row(sv.ID)
	require.Equal(t, services.TailnetNeedsAttn, row.State)
	require.Equal(t, "the agent was offline for too long", row.Message)
	require.Equal(t, services.EventServiceUpdated, nextEvent(t, feed).Type)

	// Everything else is left alone without an error, so the expiry job does not retry it forever.
	f.exec(`UPDATE service_tailnet SET state = 'online', message = ''`)
	other := f.command(agentprotocol.KindRemoveTailnet, f.server.ID, nil, agentprotocol.RemoveTailnetPayload{ServiceID: sv.ID.String()})
	garbage := f.command(agentprotocol.KindSyncTailnet, f.server.ID, nil, "garbage")
	nonUUID := f.command(agentprotocol.KindSyncTailnet, f.server.ID, nil, agentprotocol.SyncTailnetPayload{Tailnet: agentprotocol.ServiceTailnetSpec{ServiceID: "nope"}})
	for name, id := range map[string]uuid.UUID{"unknown": uuid.New(), "other kind": other.ID, "garbage": garbage.ID, "non-uuid": nonUUID.ID} {
		require.NoError(t, f.svc.HandleUndelivered(bg, id, "late"), name)
	}
	require.Equal(t, services.TailnetOnline, f.row(sv.ID).State)

	// A service without a node, or one that was deleted: still no error.
	ghost := spec
	ghost.ServiceID = uuid.NewString()
	gone := f.command(agentprotocol.KindSyncTailnet, f.server.ID, nil, agentprotocol.SyncTailnetPayload{Tailnet: ghost})
	require.NoError(t, f.svc.HandleUndelivered(bg, gone.ID, "late"))
}

func TestHandleObservedRecordsChangesAndSkipsRepeats(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, _ := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	feed := f.svc.bus.Subscribe(bg)
	observe := func(st *agentprotocol.ServiceTailnetStatus) {
		require.NoError(t, f.svc.HandleObserved(bg, f.server.ID, agentprotocol.Observed{Services: []agentprotocol.ObservedService{{ServiceID: sv.ID.String(), Tailnet: st}}}))
	}
	clock := time.Now()
	f.svc.now = func() time.Time { return clock }

	// A state the control plane does not know is shown as provisioning, and announced.
	observe(&agentprotocol.ServiceTailnetStatus{State: "from the future", Message: "starting"})
	row := f.row(sv.ID)
	require.Equal(t, services.TailnetProvisioning, row.State)
	require.Equal(t, "starting", row.Message)
	require.Equal(t, services.EventServiceUpdated, nextEvent(t, feed).Type)

	// The same report again within a minute is not written (the observation time stays) nor announced.
	first := *f.row(sv.ID).ObservedAt
	observe(&agentprotocol.ServiceTailnetStatus{State: "from the future", Message: "starting"})
	require.True(t, f.row(sv.ID).ObservedAt.Equal(first))
	select {
	case ev := <-feed:
		t.Fatalf("unexpected event %v", ev)
	default:
	}

	// After a minute it is written again, still without an announcement: nothing changed.
	clock = clock.Add(2 * time.Minute)
	observe(&agentprotocol.ServiceTailnetStatus{State: "from the future", Message: "starting"})
	require.True(t, f.row(sv.ID).ObservedAt.After(first))
	select {
	case ev := <-feed:
		t.Fatalf("unexpected event %v", ev)
	default:
	}

	// A change of address is recorded.
	observe(&agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetOnline, FQDN: "web.tail1.ts.net", IPs: []string{"100.64.0.9"}, DeviceID: "nNEW"})
	row = f.row(sv.ID)
	require.Equal(t, services.TailnetOnline, row.State)
	require.Equal(t, []string{"100.64.0.9"}, row.Ips)
	require.Equal(t, "nNEW", row.DeviceID)
	require.Equal(t, services.EventServiceUpdated, nextEvent(t, feed).Type)
}

func TestHandleObservedIgnoresWhatDoesNotBelongToTheReportingServer(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, _ := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	plain := f.service("plain", nil)
	report := func(id string, st *agentprotocol.ServiceTailnetStatus) error {
		return f.svc.HandleObserved(bg, f.server.ID, agentprotocol.Observed{Services: []agentprotocol.ObservedService{{ServiceID: id, Tailnet: st}}})
	}
	bad := &agentprotocol.ServiceTailnetStatus{State: agentprotocol.ServiceTailnetNeedsAttn, Message: "nope"}

	require.NoError(t, report("not-a-uuid", bad))
	require.NoError(t, report(plain.ID.String(), bad), "a service without a node")
	f.exec(`UPDATE service_tailnet SET state = 'removing'`)
	require.NoError(t, report(sv.ID.String(), bad), "a node on its way out")
	f.exec(`UPDATE service_tailnet SET state = 'online', server_id = NULL`)
	require.NoError(t, report(sv.ID.String(), bad), "a node that has no server")
	require.Equal(t, services.TailnetOnline, f.row(sv.ID).State)
}

func TestDatabaseFailuresSurfaceWithTheirContext(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, applied := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	sc := f.scope(sv.ID)
	cmd := f.command(agentprotocol.KindSyncTailnet, f.server.ID, nil, agentprotocol.SyncTailnetPayload{Tailnet: applied})
	dead, cancel := context.WithCancel(bg)
	cancel()

	_, _, err := f.svc.PlanCheck(dead, f.q, sc, sc.Config(), &f.server)
	require.ErrorContains(t, err, "get service tailnet")
	_, _, err = f.svc.Spec(dead, f.q, sc, sc.Config(), f.server, false)
	require.ErrorContains(t, err, "get service tailnet")
	_, err = f.svc.Secrets(dead, sv.ID, applied)
	require.ErrorContains(t, err, "load service")
	require.ErrorContains(t, f.svc.HandleResult(dead, f.server.ID, cmd.ID, agentprotocol.ResultFrame{}), "get command")
	require.ErrorContains(t, f.svc.HandleUndelivered(dead, cmd.ID, "x"), "get command")

	obs := agentprotocol.Observed{Services: []agentprotocol.ObservedService{{ServiceID: sv.ID.String(), Tailnet: &online}}}
	err = f.svc.HandleObserved(dead, f.server.ID, obs)
	require.ErrorIs(t, err, context.Canceled, "the first failure is returned after every service was tried")
}

func TestReprovisionRefusals(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, applied := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	restore := func() {
		f.exec(`UPDATE service_tailnet SET state = 'online', server_id = $1, applied = $2::jsonb`, f.server.ID, mustJSON(t, applied))
		f.exec(`UPDATE servers SET status = 'online', facts = NULL`)
	}

	for name, tc := range map[string]struct {
		mutate string
		code   string
	}{
		"node on its way out": {`UPDATE service_tailnet SET state = 'removing'`, "not_applicable"},
		"node without server": {`UPDATE service_tailnet SET server_id = NULL`, "no_server"},
		"revoked server":      {`UPDATE servers SET status = 'revoked'`, "server_revoked"},
		"outdated agent":      {`UPDATE servers SET facts = '{"os":"linux","capabilities":["proxy.v2"]}'`, "agent_too_old"},
		"never applied":       {`UPDATE service_tailnet SET applied = '{}'::jsonb`, "not_applicable"},
		"torn applied":        {`UPDATE service_tailnet SET applied = '"torn"'::jsonb`, "not_applicable"},
		"remove applied":      {`UPDATE service_tailnet SET applied = jsonb_set(applied, '{mode}', '"remove"')`, "not_applicable"},
	} {
		f.exec(tc.mutate)
		_, err := f.svc.Reprovision(f.admin, sv.ID)
		require.Equal(t, tc.code, apiCode(t, err), name)
		restore()
	}
	require.Empty(t, f.disp.kinds(), "no refused request queued a command")
	require.False(t, f.row(sv.ID).NeedsReprovision)
}

func TestReprovisionFailsCleanlyWhenTheCommandCannotBeQueued(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, _ := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})
	f.svc.disp = failDisp{f.disp}

	_, err := f.svc.Reprovision(f.admin, sv.ID)
	require.ErrorContains(t, err, "reprovision tailnet")
	require.ErrorContains(t, err, "enqueue sync_tailnet")
	require.False(t, f.row(sv.ID).NeedsReprovision, "the mark was rolled back with the command")
}

func TestReprovisionOfAnUnknownServiceIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Reprovision(f.admin, uuid.New())
	require.Equal(t, "not_found", apiCode(t, err))
}

// ---- HTTP ----

func TestHTTPReprovision(t *testing.T) {
	f := newFixture(t)
	f.connect()
	sv, _ := f.appliedNode("web", &tailnetcfg.TailnetConfig{Enabled: true, Web: true})

	serve := func(ctx context.Context) http.Handler {
		p, _ := auth.PrincipalFrom(ctx)
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
			})
		})
		cfg := huma.DefaultConfig("test", "1")
		cfg.CreateHooks = nil
		Register(humachi.New(r, cfg), f.svc)
		return r
	}
	post := func(h http.Handler, id string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/services/"+id+"/tailnet/reprovision", nil))
		return rec
	}

	rec := post(serve(f.viewer), sv.ID.String())
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Empty(t, f.disp.kinds())

	rec = post(serve(f.admin), uuid.NewString())
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = post(serve(f.admin), sv.ID.String())
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var body struct {
		CommandID string `json:"commandId"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEqual(t, uuid.Nil.String(), body.CommandID)
	require.Equal(t, []string{agentprotocol.KindSyncTailnet}, f.disp.kinds())
}
