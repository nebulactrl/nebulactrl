package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// The deployment debug views: what the debug panel of the web app shows besides
// the raw timeline and the debug log stream. Both are readable by anyone who
// can read the deployment's logs (projects:read on the deployment's project)
// and never carry a secret value: command payloads and release snapshots pass
// through redactJSON, variables are listed by key only.

// maxDebugString bounds one string of a redacted payload.
const maxDebugString = 2048

// CommandView is one agent command of a deployment, redacted.
type CommandView struct {
	ID         uuid.UUID `json:"id"`
	Kind       string    `json:"kind"`
	ServerID   uuid.UUID `json:"serverId"`
	ServerName string    `json:"serverName"`
	// Status is the command's delivery state: pending, sent, acked, succeeded,
	// failed, expired or canceled.
	Status string `json:"status"`
	// Outcome is the agent's reported outcome (succeeded, failed, rolled_back,
	// ...); empty until the command has a result.
	Outcome string `json:"outcome"`
	Error   string `json:"error"`
	// Payload is the command as persisted, redacted: variable values, tokens,
	// credentials and file contents never appear.
	Payload any `json:"payload"`
	// Result is the agent's result, redacted (null until there is one).
	Result     any        `json:"result"`
	CreatedAt  time.Time  `json:"createdAt" doc:"When the control plane enqueued the command"`
	SentAt     *time.Time `json:"sentAt"`
	AckedAt    *time.Time `json:"ackedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
}

// ReleaseVariableView names one variable of a release: never its value.
type ReleaseVariableView struct {
	Key string `json:"key"`
	// Secret variables are masked in every read of the API.
	Secret bool `json:"secret"`
	// Reference is true when the value is (or contains) a ${{…}} reference.
	Reference bool `json:"reference"`
	// Platform variables are set by NebulaCtrl (PORT, NEBULA_*), not by a user.
	Platform bool `json:"platform"`
	// External variables come from an external secret manager.
	External bool `json:"external"`
	// Scope is runtime, build or both.
	Scope string `json:"scope" enum:"runtime,build,both"`
}

// ReleasePlacement says where the release was built and where it runs.
type ReleasePlacement struct {
	Strategy         string     `json:"strategy"`
	TargetServerID   *uuid.UUID `json:"targetServerId"`
	TargetServerName string     `json:"targetServerName"`
	// BuildServerID is the build server that built the image (null: built on
	// the target, or nothing was built).
	BuildServerID   *uuid.UUID `json:"buildServerId"`
	BuildServerName string     `json:"buildServerName"`
	RegistryID      *uuid.UUID `json:"registryId"`
	// RegistryRef is repository@sha256:… of an image a build server pushed.
	RegistryRef string `json:"registryRef"`
	Platform    string `json:"platform"`
}

// ReleaseDetailView is the release a deployment shipped, for the debug panel.
type ReleaseDetailView struct {
	ID           uuid.UUID `json:"id"`
	ServiceID    uuid.UUID `json:"serviceId"`
	DeploymentID uuid.UUID `json:"deploymentId"`
	// Generation is the service generation the deployment applied.
	Generation int64                    `json:"generation"`
	CreatedAt  time.Time                `json:"createdAt"`
	ImageRef   string                   `json:"imageRef"`
	Source     services.ReleaseSource   `json:"source"`
	Build      *agentprotocol.BuildInfo `json:"build"`
	Placement  ReleasePlacement         `json:"placement"`
	// Config is the configuration frozen into the release, with secrets and
	// file contents masked.
	Config any `json:"config"`
	// Variables lists the keys of the release's environment, sorted.
	Variables []ReleaseVariableView `json:"variables"`
	// VariablesPruned is true when the release's sealed environment was dropped
	// (it left the rollback window), so no variable keys are known.
	VariablesPruned bool `json:"variablesPruned"`
}

// Commands lists a deployment's agent commands, redacted. It needs the same
// access as the deployment's logs.
func (s *Service) Commands(ctx context.Context, id uuid.UUID) ([]CommandView, error) {
	dc, err := s.loadDeployment(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return nil, err
	}
	scrub, err := s.debugScrubber(ctx, dc.Dep.ServiceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListDeploymentCommandViews(ctx, &id)
	if err != nil {
		return nil, fmt.Errorf("list deployment commands: %w", err)
	}
	out := make([]CommandView, len(rows))
	for i, r := range rows {
		out[i] = commandView(r, scrub)
	}
	return out, nil
}

func commandView(c db.ListDeploymentCommandViewsRow, scrub func(string) string) CommandView {
	v := CommandView{
		ID: c.ID, Kind: c.Kind, ServerID: c.ServerID, ServerName: c.ServerName, Status: c.Status,
		Error:      scrub(c.Error),
		Payload:    redactJSON(c.Payload, scrub),
		Result:     redactJSON(c.Result, scrub),
		CreatedAt:  c.CreatedAt,
		SentAt:     c.SentAt,
		AckedAt:    c.AckedAt,
		FinishedAt: c.FinishedAt,
		ExpiresAt:  c.ExpiresAt,
	}
	var head struct {
		Outcome string `json:"outcome"`
	}
	if len(c.Result) > 0 && json.Unmarshal(c.Result, &head) == nil {
		v.Outcome = head.Outcome
	}
	return v
}

// ReleaseDetail returns the release of a deployment with its masked
// configuration snapshot and the keys of its environment.
func (s *Service) ReleaseDetail(ctx context.Context, id uuid.UUID) (ReleaseDetailView, error) {
	dc, err := s.loadDeployment(ctx, policy.ScopeProjectsRead, id)
	if err != nil {
		return ReleaseDetailView{}, err
	}
	if dc.Dep.ReleaseID == nil {
		return ReleaseDetailView{}, apierr.NotFound("release")
	}
	rel, err := s.q.GetReleaseByID(ctx, *dc.Dep.ReleaseID)
	if err != nil {
		return ReleaseDetailView{}, fmt.Errorf("get release: %w", err)
	}
	if rel.ServiceID != dc.Dep.ServiceID {
		return ReleaseDetailView{}, apierr.NotFound("release")
	}
	scrub, err := s.debugScrubber(ctx, dc.Dep.ServiceID)
	if err != nil {
		return ReleaseDetailView{}, err
	}

	src := services.DecodeSource(rel.Source)
	src.GitURL = scrub(src.GitURL)
	v := ReleaseDetailView{
		ID: rel.ID, ServiceID: rel.ServiceID, DeploymentID: dc.Dep.ID, Generation: dc.Dep.Generation,
		CreatedAt: rel.CreatedAt, ImageRef: scrub(rel.ImageRef), Source: src,
		Config:    redactJSON(rel.Config, scrub),
		Variables: []ReleaseVariableView{},
	}
	if len(dc.Dep.BuildInfo) > 0 {
		var bi agentprotocol.BuildInfo
		if json.Unmarshal(dc.Dep.BuildInfo, &bi) == nil {
			v.Build = &bi
		}
	}

	ids := []uuid.UUID{}
	if dc.Dep.ServerID != nil {
		ids = append(ids, *dc.Dep.ServerID)
	}
	if dc.Dep.BuildServerID != nil {
		ids = append(ids, *dc.Dep.BuildServerID)
	}
	names := map[uuid.UUID]string{}
	if len(ids) > 0 {
		rows, err := s.q.LoadServerRows(ctx, ids)
		if err != nil {
			return ReleaseDetailView{}, fmt.Errorf("load servers: %w", err)
		}
		for _, r := range rows {
			names[r.ID] = r.Name
		}
	}
	v.Placement = ReleasePlacement{
		Strategy: dc.Dep.Strategy, TargetServerID: dc.Dep.ServerID, BuildServerID: dc.Dep.BuildServerID,
		RegistryID: src.RegistryID, RegistryRef: src.RegistryRef, Platform: src.Platform,
	}
	if dc.Dep.ServerID != nil {
		v.Placement.TargetServerName = names[*dc.Dep.ServerID]
	}
	if dc.Dep.BuildServerID != nil {
		v.Placement.BuildServerName = names[*dc.Dep.BuildServerID]
	}

	env, err := services.ReleaseEnv(s.Box, rel)
	if err != nil {
		return ReleaseDetailView{}, fmt.Errorf("release environment: %w", err)
	}
	if env == nil {
		v.VariablesPruned = true
		return v, nil
	}
	v.Variables = releaseVariables(env)
	return v, nil
}

// releaseVariables lists the keys of a release's environment with their
// flags. Values are never read from the snapshot into the view.
func releaseVariables(env *services.EnvSnapshot) []ReleaseVariableView {
	secret := make(map[string]bool, len(env.Secret))
	for _, k := range env.Secret {
		secret[k] = true
	}
	keys := make([]string, 0, len(env.Vars))
	for k := range env.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ReleaseVariableView, 0, len(keys))
	for _, k := range keys {
		raw, user := env.Raw[k]
		_, external := env.Ext[k]
		scope := env.Scope[k]
		if scope == "" {
			scope = services.ScopeRuntime
		}
		out = append(out, ReleaseVariableView{
			Key: k, Secret: secret[k] || raw.Secret, Reference: user && strings.Contains(raw.Value, "${{"),
			Platform: !user, External: external, Scope: scope,
		})
	}
	return out
}

// debugScrubber masks the service's secret values (own variables, resolved
// references, git token, registry password) and the password of a URL, in any
// string. It fails closed: without a redactor nothing is shown.
func (s *Service) debugScrubber(ctx context.Context, serviceID uuid.UUID) (func(string) string, error) {
	if s.Services == nil {
		return nil, fmt.Errorf("no redactor configured")
	}
	redact, err := s.Services.RedactorFor(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("build redactor: %w", err)
	}
	return func(text string) string { return maskURLPasswords(redact(text)) }, nil
}

var urlUserinfo = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.\-]*://[^/\s:@]*:)[^@/\s]+@`)

// maskURLPasswords replaces the password of scheme://user:password@host URLs.
func maskURLPasswords(text string) string {
	if !strings.Contains(text, "@") {
		return text
	}
	return urlUserinfo.ReplaceAllString(text, "${1}"+services.Mask+"@")
}

// sensitiveKey matches the JSON keys whose values are credentials.
var sensitiveKey = regexp.MustCompile(`(?i)(password|passwd|passphrase|secret|token|authkey|auth_key|authorization|credential|private|apikey|api_key|htpasswd|bearer|cookie|signature|sshkey|ssh_key|hash)`)

// hiddenContentKeys hold whole documents (file contents, a compose file) that
// may contain literal secrets the redactor does not know: only their size shows.
var hiddenContentKeys = map[string]bool{"content": true, "contents": true, "compose": true}

// envMapKeys hold key/value maps of environment-like data: the keys stay, the
// values are masked.
var envMapKeys = map[string]bool{"env": true, "vars": true, "environment": true, "secrets": true, "variables": true}

// redactJSON decodes raw and returns it with every credential masked:
//
//   - the value of a key that looks like a credential (password, token, secret, …)
//     becomes the mask, except a list of key names (secretEnvKeys, envKeys), a
//     number or a boolean (hasToken);
//   - the values of an env/vars map are masked, keeping the keys;
//   - file contents and compose files are replaced by their size;
//   - every remaining string passes through scrub (the service's secret values
//     and URL passwords) and is bounded.
//
// It never returns raw: an input that is not valid JSON yields nil.
func redactJSON(raw json.RawMessage, scrub func(string) string) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	return redactValue("", v, false, scrub)
}

func redactValue(key string, v any, inEnvMap bool, scrub func(string) string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		isEnv := envMapKeys[strings.ToLower(key)]
		for k, child := range t {
			out[k] = redactMember(k, child, isEnv, scrub)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = redactValue(key, child, false, scrub)
		}
		return out
	case string:
		if inEnvMap {
			return maskIfSet(t)
		}
		return boundString(scrub(t))
	default:
		return v // numbers, booleans, null
	}
}

// redactMember redacts one member of an object.
func redactMember(key string, v any, inEnvMap bool, scrub func(string) string) any {
	if inEnvMap {
		if _, isString := v.(string); isString {
			return maskIfSet(v.(string))
		}
		return redactValue(key, v, true, scrub)
	}
	lower := strings.ToLower(key)
	if s, ok := v.(string); ok && hiddenContentKeys[lower] {
		return fmt.Sprintf("[%d bytes hidden]", len(s))
	}
	if sensitiveKey.MatchString(key) && !strings.HasSuffix(lower, "keys") {
		switch t := v.(type) {
		case string:
			return maskIfSet(t)
		case map[string]any, []any:
			return services.Mask
		}
	}
	return redactValue(key, v, false, scrub)
}

func maskIfSet(s string) string {
	if s == "" {
		return ""
	}
	return services.Mask
}

func boundString(s string) string {
	if len(s) <= maxDebugString {
		return s
	}
	cut := maxDebugString
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
