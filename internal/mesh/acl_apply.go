package mesh

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/integrations/tailscale"
	"github.com/nebulactrl/nebulactrl/internal/mesh/acl"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// tailscaleTagOwners are the owners a new tagOwners entry of the mesh tag gets
// on Tailscale (an existing entry is never changed).
var tailscaleTagOwners = []string{"autogroup:admin"}

// isHeadscale reports whether the integration is a Headscale.
func (sn *snapshot) isHeadscale() bool {
	return sn.integ.Provider == agentprotocol.TailnetProviderHeadscale
}

// canWritePolicy reports whether NebulaCtrl can write a policy at all: Tailscale
// with the OAuth client, or a Headscale with an API key.
func (sn *snapshot) canWritePolicy() bool {
	return sn.haveInteg && (!sn.isHeadscale() || sn.integ.Credential == tailscale.HeadscaleAPIKey)
}

// baseFor is the part of a managed Headscale policy that is not generated:
// the tag owners (the mesh tag and the integration's service tags) and the fixed
// rule that lets members reach the service tags.
func (s *Service) baseFor(ctx context.Context, sn *snapshot) acl.Base {
	owners := []string{}
	if r, err := s.q.GetTailnetIntegration(ctx, sn.ws); err == nil && r.HeadscaleUser != "" {
		owners = []string{r.HeadscaleUser + "@"}
	}
	b := acl.Base{TagOwners: map[string][]string{}}
	mesh := sn.meshTag()
	b.TagOwners[mesh] = owners
	var svcTags []string
	for _, t := range sn.integ.AllowedTags {
		if t == mesh {
			continue
		}
		b.TagOwners[t] = owners
		svcTags = append(svcTags, t+":*")
	}
	if len(svcTags) > 0 {
		b.Rules = append(b.Rules, acl.Rule{Action: "accept", Src: []string{"autogroup:member"}, Dst: svcTags})
	}
	return b
}

func (s *Service) setACLState(ctx context.Context, ws uuid.UUID, state, msg, hash, version string) {
	if err := s.q.SetMeshACLState(ctx, db.SetMeshACLStateParams{WorkspaceID: ws, AclState: state, AclMessage: msg, AclHash: hash, AclVersion: version}); err != nil {
		s.log.ErrorContext(ctx, "record mesh acl state", "err", err)
	}
	s.publish(ws)
}

// ApplyACL is the body of MeshACLApplyJob: it regenerates the policy from the
// tables and applies it according to the mode (managed Headscale and opt-in
// Tailscale push, advisory drift check, off). It waits while the mesh is paused.
// A returned error makes River retry.
func (s *Service) ApplyACL(ctx context.Context, ws uuid.UUID) error {
	return s.applyACL(ctx, ws, false)
}

// applyACL is ApplyACL; ignorePause lets the Headscale migration write the policy
// while the mesh is paused (that write is what ends the pause).
func (s *Service) applyACL(ctx context.Context, ws uuid.UUID, ignorePause bool) error {
	sn, err := s.load(ctx, s.q, ws)
	if err != nil {
		return err
	}
	if sn.set.Paused && !ignorePause {
		return nil
	}
	if !sn.haveInteg {
		s.setACLState(ctx, ws, StateUnknown, "Connect Tailscale or Headscale first.", "", "")
		return nil
	}
	pol := acl.Generate(sn.aclState())
	mode := sn.set.AclMode
	if mode == ModeManaged && sn.isHeadscale() && sn.integ.Credential != tailscale.HeadscaleAPIKey {
		_ = s.q.ForceMeshACLMode(ctx, db.ForceMeshACLModeParams{WorkspaceID: ws, AclMode: ModeAdvisory})
		mode = ModeAdvisory
	}
	switch mode {
	case ModeOff:
		s.setACLState(ctx, ws, StateAdvisory, "ACL management is off: the tailnet's own policy decides what the mesh may reach.", "", "")
		return nil
	case ModeManaged:
		if shared, err := s.ts.SharedTailnet(ctx, ws); err == nil && shared {
			s.setACLState(ctx, ws, StateError, "Another team uses this tailnet: NebulaCtrl will not rewrite its policy.", "", "")
			return nil
		}
		if sn.isHeadscale() {
			return s.applyManagedHeadscale(ctx, sn, pol)
		}
		return s.applyManagedTailscale(ctx, sn, pol)
	}
	return s.checkAdvisory(ctx, sn, pol)
}

// scopeMessage words the failure of a Tailscale policy call.
func scopeMessage(sn *snapshot, write bool) string {
	if sn.isHeadscale() {
		return "Headscale rejected the API key."
	}
	if write {
		return "The OAuth client lacks the policy_file scope (needed for the managed ACL push)."
	}
	return "The OAuth client lacks the policy_file:read scope, so the generated rules cannot be verified in your tailnet policy."
}

// noPolicyAccess reports whether PolicyAPI failed because the integration has
// no credential that can manage a policy (not connected, or a Headscale
// pre-auth key). Any other failure (database, secret decryption) is not a
// verdict about the credential and must surface.
func noPolicyAccess(err error) bool {
	return errors.Is(err, tailscale.ErrNotConfigured) || errors.Is(err, tailscale.ErrPolicyUnsupported)
}

func (s *Service) applyManagedHeadscale(ctx context.Context, sn *snapshot, pol acl.Policy) error {
	api, err := s.ts.PolicyAPI(ctx, sn.ws)
	if noPolicyAccess(err) {
		s.setACLState(ctx, sn.ws, StateError, "No policy access: "+err.Error(), "", "")
		return nil
	}
	if err != nil {
		return fmt.Errorf("policy api: %w", err)
	}
	doc, err := pol.Document(s.baseFor(ctx, sn))
	if err != nil {
		return err
	}
	hash := acl.Hash(doc)
	if hash == sn.set.AclHash && sn.set.AclState == StateApplied {
		return nil
	}
	s.setACLState(ctx, sn.ws, StatePending, "Applying the policy.", sn.set.AclHash, sn.set.AclVersion)
	if err := api.Check(ctx, doc); err != nil {
		s.setACLState(ctx, sn.ws, StateError, "Headscale rejected the generated policy: "+shorten(err), "", "")
		return nil
	}
	p, err := api.Set(ctx, doc, "")
	switch {
	case errors.Is(err, tailscale.ErrPolicyFileMode):
		_ = s.q.ForceMeshACLMode(ctx, db.ForceMeshACLModeParams{WorkspaceID: sn.ws, AclMode: ModeAdvisory})
		s.setACLState(ctx, sn.ws, StateAdvisory, "Headscale runs with policy.mode: file, which refuses policy updates over the API. Add the generated rules to its policy file.", "", "")
		return nil
	case errors.Is(err, tailscale.ErrAuth):
		s.setACLState(ctx, sn.ws, StateError, scopeMessage(sn, true), "", "")
		return nil
	case err != nil:
		s.setACLState(ctx, sn.ws, StateError, "Could not write the policy: "+shorten(err), "", "")
		return fmt.Errorf("write headscale policy: %w", err)
	}
	// Read it back: the state is "applied" only when the coordinator holds the rules.
	back, gerr := api.Get(ctx)
	if gerr != nil {
		s.setACLState(ctx, sn.ws, StateError, "The policy was written but could not be read back: "+shorten(gerr), "", "")
		return fmt.Errorf("read back headscale policy: %w", gerr)
	}
	if missing, merr := acl.ContainsRules([]byte(back.Raw), pol); merr != nil || len(missing) > 0 {
		s.setACLState(ctx, sn.ws, StateError, "The stored policy does not contain the generated rules.", "", "")
		return errors.New("headscale policy read back does not contain the generated rules")
	}
	if p.Version == "" {
		p.Version = back.Version
	}
	s.setACLState(ctx, sn.ws, StateApplied, "", hash, p.Version)
	return nil
}

func (s *Service) applyManagedTailscale(ctx context.Context, sn *snapshot, pol acl.Policy) error {
	api, err := s.ts.PolicyAPI(ctx, sn.ws)
	if noPolicyAccess(err) {
		s.setACLState(ctx, sn.ws, StateError, "No policy access: "+err.Error(), "", "")
		return nil
	}
	if err != nil {
		return fmt.Errorf("policy api: %w", err)
	}
	cur, err := api.Get(ctx)
	if errors.Is(err, tailscale.ErrAuth) {
		s.setACLState(ctx, sn.ws, StateError, scopeMessage(sn, true), "", "")
		return nil
	}
	if err != nil {
		s.setACLState(ctx, sn.ws, StateError, "Could not read the tailnet policy: "+shorten(err), "", "")
		return fmt.Errorf("read tailscale policy: %w", err)
	}
	next, err := acl.PatchHuJSON([]byte(cur.Raw), pol, sn.meshTag(), tailscaleTagOwners, sn.meshIPs())
	if err != nil {
		s.setACLState(ctx, sn.ws, StateError, shorten(err), "", "")
		return nil
	}
	hash := acl.Hash(next)
	if string(next) == cur.Raw {
		s.setACLState(ctx, sn.ws, StateApplied, "", hash, cur.Version)
		return nil
	}
	s.setACLState(ctx, sn.ws, StatePending, "Applying the policy.", sn.set.AclHash, sn.set.AclVersion)
	if err := api.Check(ctx, next); err != nil {
		s.setACLState(ctx, sn.ws, StateError, "Tailscale rejected the patched policy, nothing was written: "+shorten(err), "", "")
		return nil
	}
	p, err := api.Set(ctx, next, cur.Version)
	switch {
	case errors.Is(err, tailscale.ErrPolicyConflict):
		return fmt.Errorf("the tailnet policy changed while it was patched: %w", err) // retried with a fresh read
	case errors.Is(err, tailscale.ErrAuth):
		s.setACLState(ctx, sn.ws, StateError, scopeMessage(sn, true), "", "")
		return nil
	case err != nil:
		s.setACLState(ctx, sn.ws, StateError, "Could not write the policy: "+shorten(err), "", "")
		return fmt.Errorf("write tailscale policy: %w", err)
	}
	s.setACLState(ctx, sn.ws, StateApplied, "", hash, p.Version)
	return nil
}

// checkAdvisory records whether the generated rules are present in the tailnet
// policy. It only ever reads. Without read access the state is "advisory": the
// connectivity check (mesh_check) is the ground truth.
func (s *Service) checkAdvisory(ctx context.Context, sn *snapshot, pol acl.Policy) error {
	if len(pol.Rules) == 0 {
		s.setACLState(ctx, sn.ws, StateApplied, "No access rules are needed yet.", "", "")
		return nil
	}
	hint := "Add the generated rules to your tailnet policy (Team settings → Server mesh, Preview rules). The connectivity test shows whether they work."
	api, err := s.ts.PolicyAPI(ctx, sn.ws)
	if noPolicyAccess(err) {
		s.setACLState(ctx, sn.ws, StateAdvisory, hint, "", "")
		return nil
	}
	if err != nil {
		return fmt.Errorf("policy api: %w", err)
	}
	cur, err := api.Get(ctx)
	switch {
	case errors.Is(err, tailscale.ErrPolicyNotFound):
		s.setACLState(ctx, sn.ws, StateDrift, fmt.Sprintf("%d generated rule(s) are missing: the tailnet has no policy yet. %s", len(pol.Rules), hint), "", "")
		return nil
	case errors.Is(err, tailscale.ErrAuth):
		s.setACLState(ctx, sn.ws, StateAdvisory, scopeMessage(sn, false)+" "+hint, "", "")
		return nil
	case err != nil:
		s.setACLState(ctx, sn.ws, StateAdvisory, "Could not read the tailnet policy ("+shorten(err)+"). "+hint, "", "")
		return nil
	}
	missing, err := acl.ContainsRules([]byte(cur.Raw), pol)
	if err != nil {
		s.setACLState(ctx, sn.ws, StateAdvisory, shorten(err)+". "+hint, "", "")
		return nil
	}
	if len(missing) > 0 {
		s.setACLState(ctx, sn.ws, StateDrift, fmt.Sprintf("%d generated rule(s) are missing from your tailnet policy. %s", len(missing), hint), "", cur.Version)
		return nil
	}
	s.setACLState(ctx, sn.ws, StateApplied, "All generated rules are present in your tailnet policy (a broader rule may also exist).", "", cur.Version)
	return nil
}

// ApplyPolicy writes the managed Headscale policy of ws right now (base rules
// plus the generated mesh rules) and switches the team to managed mode. The
// platform Headscale wiring calls it once the API key exists, before any node can
// join; the Headscale migration job calls it after the redeploy.
func (s *Service) ApplyPolicy(ctx context.Context, ws uuid.UUID) error {
	if err := s.q.EnsureMeshSettings(ctx, ws); err != nil {
		return err
	}
	if _, err := s.q.UpdateMeshSettings(ctx, db.UpdateMeshSettingsParams{WorkspaceID: ws, AclMode: new(ModeManaged)}); err != nil {
		return err
	}
	if err := s.applyACL(ctx, ws, true); err != nil {
		return err
	}
	r, err := s.q.GetMeshSettings(ctx, ws)
	if err != nil {
		return err
	}
	if r.AclState != StateApplied {
		return fmt.Errorf("the Headscale policy was not applied (%s): %s", r.AclState, r.AclMessage)
	}
	return nil
}

func shorten(err error) string {
	m := err.Error()
	if len(m) > 200 {
		m = m[:200] + "…"
	}
	return strings.TrimSpace(m)
}

// ACLPreview is the answer of PreviewACL.
type ACLPreview struct {
	Rules  []RuleView `json:"rules"`
	Policy string     `json:"policy" doc:"What NebulaCtrl owns (managed Headscale: the whole policy) or the snippet to paste (HuJSON with comments)"`
	Diff   *ACLDiff   `json:"diff,omitempty" doc:"Against the policy currently stored in the tailnet, when it could be read"`
}

// ACLDiff lists the generated rules missing from the stored policy and the
// NebulaCtrl-owned rules that would be removed.
type ACLDiff struct {
	Add    []acl.Rule `json:"add"`
	Remove []acl.Rule `json:"remove"`
}

// diffFor reads the stored policy and compares it with the generated rules.
func (s *Service) diffFor(ctx context.Context, sn *snapshot, pol acl.Policy) *ACLDiff {
	api, err := s.ts.PolicyAPI(ctx, sn.ws)
	if err != nil {
		return nil
	}
	cur, err := api.Get(ctx)
	if errors.Is(err, tailscale.ErrPolicyNotFound) {
		return &ACLDiff{Add: append([]acl.Rule{}, pol.Rules...), Remove: []acl.Rule{}}
	}
	if err != nil {
		return nil
	}
	missing, err := acl.ContainsRules([]byte(cur.Raw), pol)
	if err != nil {
		return nil
	}
	stale, err := acl.StaleRules([]byte(cur.Raw), pol, sn.meshIPs())
	if err != nil {
		return nil
	}
	if missing == nil {
		missing = []acl.Rule{}
	}
	if stale == nil {
		stale = []acl.Rule{}
	}
	return &ACLDiff{Add: missing, Remove: stale}
}

// errPolicy maps a failed Tailscale policy call onto the API error for a missing scope.
func (s *Service) errPolicy(err error, sn *snapshot) error {
	if errors.Is(err, tailscale.ErrAuth) {
		return apierr.Unprocessable(apierr.CodeMeshPolicyScopeMissing, scopeMessage(sn, true))
	}
	return err
}
