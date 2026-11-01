package configsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
	"github.com/nebulactrl/nebulactrl/internal/auth"
	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/gitproviders"
	"github.com/nebulactrl/nebulactrl/internal/policy"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Reading the file on push, on source changes and on build checkouts.
// Every trigger ends in check: read the file, plan it as the system actor,
// store the status, optionally auto-apply (pushes only), tell the live feed.

// codeAutoApplyPrivileged marks a plan item an unattended apply must never
// write: an unrestricted-only Docker option or NET_ADMIN / SYS_ADMIN.
const codeAutoApplyPrivileged = "auto_apply_privileged"

// maxAttachmentsBytes bounds the attachments read for one file.
const maxAttachmentsBytes = 1 << 20

// ---- reading ----

// readFailure is why a source could not read.
type readFailure struct {
	status  string // EnvUnreachable or EnvError
	message string
	noAPI   bool // the connection has no API reader: the agent reads instead
}

func (e *readFailure) Error() string { return e.message }

// fileSource reads files of the bound repository.
type fileSource interface {
	// read returns the files that exist at paths and the commit they were read
	// at ("" when the source does not know it).
	read(ctx context.Context, paths []string) (files map[string][]byte, commit string, err error)
}

// noted is implemented by sources that read at the branch head instead of the
// requested commit.
type noted interface{ note() string }

// apiSource reads through a git provider. A commit that the provider does not
// accept as ref is read again at the branch head (recorded as a note).
type apiSource struct {
	open   func(ctx context.Context, ref string) (FileReader, error)
	ref    string // commit to read; "" = branch head
	branch string
	reader FileReader
	atHead bool
	nt     string
}

func (s *apiSource) note() string { return s.nt }

func (s *apiSource) read(ctx context.Context, paths []string) (map[string][]byte, string, error) {
	for {
		if s.reader == nil {
			ref := s.branch
			if s.ref != "" && !s.atHead {
				ref = s.ref
			}
			r, err := s.open(ctx, ref)
			if err != nil {
				if ae, ok := apierr.As(err); ok && ae.Code == "no_repo_api" {
					return nil, "", &readFailure{noAPI: true, status: EnvUnreachable, message: ae.Message}
				}
				return nil, "", &readFailure{status: EnvUnreachable, message: errMessage(err)}
			}
			s.reader = r
		}
		files := map[string][]byte{}
		var failed error
		for _, p := range paths {
			b, err := s.reader.Get(ctx, p)
			if errors.Is(err, gitproviders.ErrFileNotFound) {
				continue
			}
			if err != nil {
				failed = err
				break
			}
			files[p] = b
		}
		if failed == nil {
			commit := ""
			if s.ref != "" && !s.atHead {
				commit = s.ref
			}
			return files, commit, nil
		}
		if s.ref != "" && !s.atHead {
			s.atHead, s.reader, s.nt = true, nil, "read at branch head"
			continue
		}
		return nil, "", &readFailure{status: EnvUnreachable, message: errMessage(failed)}
	}
}

// agentSource reads on the server of one of the environment's git services.
type agentSource struct {
	g  *GitSync
	bi BindingInfo
}

func (s agentSource) read(ctx context.Context, paths []string) (map[string][]byte, string, error) {
	if s.g.agent == nil {
		return nil, "", &readFailure{status: EnvUnreachable, message: "this repository can only be read on a server, and no agent is available"}
	}
	var sc services.Scope
	found := false
	for _, id := range s.bi.GitServices {
		c, err := services.LoadScopeInternal(ctx, s.g.q, id)
		if err == nil && c.Service.ServerID != nil {
			sc, found = c, true
			break
		}
	}
	if !found {
		return nil, "", &readFailure{status: EnvUnreachable, message: "no git service of this environment has a server that can read the repository"}
	}
	res, err := s.g.agent.ScanConfigFiles(ctx, sc, s.bi.Branch, paths)
	if err != nil {
		msg := errMessage(err)
		if ae, ok := apierr.As(err); ok && (ae.Code == "server_offline" || ae.Code == "server_revoked") {
			msg = "server offline"
		}
		return nil, "", &readFailure{status: EnvUnreachable, message: msg}
	}
	return res.ConfigFiles, res.Commit, nil
}

// prefetched are files an agent already read at a build checkout.
type prefetched struct {
	commit string
	// looked are the paths the agent looked at: a path missing from files does
	// not exist at commit.
	looked []string
	files  map[string][]byte
}

// prefetchSource answers from prefetched files and delegates every other path.
type prefetchSource struct {
	base fileSource
	pre  *prefetched
}

func (s prefetchSource) note() string {
	if n, ok := s.base.(noted); ok {
		return n.note()
	}
	return ""
}

func (s prefetchSource) read(ctx context.Context, paths []string) (map[string][]byte, string, error) {
	out := map[string][]byte{}
	var rest []string
	for _, p := range paths {
		if containsString(s.pre.looked, p) {
			if b, ok := s.pre.files[p]; ok {
				out[p] = b
			}
			continue
		}
		rest = append(rest, p)
	}
	commit := s.pre.commit
	if len(rest) > 0 {
		files, c, err := s.base.read(ctx, rest)
		if err != nil {
			return nil, "", err
		}
		for k, v := range files {
			out[k] = v
		}
		if commit == "" {
			commit = c
		}
	}
	return out, commit, nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// apiSourceFor reads through the binding's connection (or public URL).
func (g *GitSync) apiSourceFor(bi BindingInfo, ref string) fileSource {
	open := func(context.Context, string) (FileReader, error) {
		return nil, &readFailure{status: EnvUnreachable, message: "the git connection of this binding no longer exists"}
	}
	switch {
	case g.repos == nil:
		open = func(context.Context, string) (FileReader, error) {
			return nil, &readFailure{status: EnvUnreachable, message: "no git provider is available"}
		}
	case bi.ConnectionID != uuid.Nil:
		open = func(ctx context.Context, r string) (FileReader, error) {
			return g.repos.OpenRepo(ctx, bi.ConnectionID, bi.Repo, r)
		}
	case bi.GitURL != "":
		open = func(ctx context.Context, r string) (FileReader, error) {
			return g.repos.OpenPublicRepo(ctx, bi.GitURL, r)
		}
	}
	return &apiSource{open: open, ref: ref, branch: bi.Branch}
}

// readOutcome is what reading the file produced.
type readOutcome struct {
	// status is "" for a file that was read, else EnvNotFound, EnvUnreachable or EnvError.
	status  string
	message string
	noAPI   bool

	input    Input
	file     []byte
	path     string
	commit   string
	note     string
	tooLarge bool
	// attachNames are the attachments the file names; attachSkipped says they
	// were not read (the file is the one already checked).
	attachNames   []string
	attachSkipped bool
}

// readConfig reads the file and the attachments it names at ref ("" = branch
// head): through the provider's API, else (no API reader) on the agent.
// readAttachments, when set, decides after the file is read whether its
// attachments are read as well.
func (g *GitSync) readConfig(ctx context.Context, bi BindingInfo, ref string, pre *prefetched, readAttachments func(fileSHA string) bool) readOutcome {
	wrap := func(s fileSource) fileSource {
		if pre != nil {
			return prefetchSource{base: s, pre: pre}
		}
		return s
	}
	if bi.Mode == BindingNone || len(bi.Paths) == 0 {
		return readOutcome{status: EnvError, message: "this environment has no repository binding"}
	}
	out := g.readWith(ctx, bi, wrap(g.apiSourceFor(bi, ref)), readAttachments)
	if out.noAPI {
		out = g.readWith(ctx, bi, wrap(agentSource{g: g, bi: bi}), readAttachments)
	}
	return out
}

func (g *GitSync) readWith(ctx context.Context, bi BindingInfo, src fileSource, readAttachments func(fileSHA string) bool) readOutcome {
	fail := func(err error) readOutcome {
		var rf *readFailure
		if errors.As(err, &rf) {
			return readOutcome{status: rf.status, message: rf.message, noAPI: rf.noAPI}
		}
		return readOutcome{status: EnvError, message: errMessage(err)}
	}
	files, commit, err := src.read(ctx, bi.Paths)
	if err != nil {
		return fail(err)
	}
	out := readOutcome{}
	for _, p := range bi.Paths {
		if b, ok := files[p]; ok {
			out.path, out.file = p, b
			break
		}
	}
	if out.path == "" {
		return readOutcome{status: EnvNotFound, message: "no " + strings.Join(bi.Paths, " or ") + " in the repository", commit: commit}
	}
	out.commit = commit
	out.input = Input{
		File:        string(out.file),
		Attachments: map[string]string{},
		Source:      SourceInfo{Kind: SourceGit, Repo: bi.Repo, Branch: bi.Branch, Path: out.path, Commit: commit},
	}
	setNote := func() {
		if n, ok := src.(noted); ok {
			out.note = n.note()
		}
	}
	setNote()
	if len(out.file) > projectconfig.MaxFileBytes {
		out.tooLarge = true
		return out
	}
	// Attachments are named relative to the file's directory; they are read
	// (and tracked for pushes) by their repository path.
	dir := path.Dir(out.path)
	var names, repoPaths []string
	for _, n := range fileAttachments(out.file) {
		if rp := path.Join(dir, n); agentprotocol.ValidConfigPath(rp) {
			names, repoPaths = append(names, n), append(repoPaths, rp)
		}
	}
	out.attachNames = repoPaths
	if len(names) == 0 {
		return out
	}
	if readAttachments != nil && !readAttachments(sha256Hex(out.file)) {
		out.attachSkipped = true
		return out
	}
	afiles, c2, err := src.read(ctx, repoPaths)
	if err != nil {
		return fail(err)
	}
	setNote()
	if out.commit == "" {
		out.commit, out.input.Source.Commit = c2, c2
	}
	total := 0
	for i, n := range names {
		b, ok := afiles[repoPaths[i]]
		if !ok || len(b) > projectconfig.MaxFileBytes || total+len(b) > maxAttachmentsBytes {
			continue // reported by the plan as a missing attachment
		}
		total += len(b)
		out.input.Attachments[n] = string(b)
	}
	return out
}

// fileAttachments lists the attachments (relative paths) the file names, at
// most as many as one agent read can carry.
func fileAttachments(text []byte) []string {
	f, _, _ := projectconfig.Parse(text, nil)
	if f == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range f.AttachmentPaths() {
		if seen[p] || !agentprotocol.ValidConfigPath(p) {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) == agentprotocol.ScanMaxConfigPaths-1 {
			break
		}
	}
	return out
}

// Read implements Binding.
func (g *GitSync) Read(ctx context.Context, b BindingInfo, ref string) (Input, error) {
	if b.Mode == BindingNone {
		return Input{}, errNoBinding
	}
	out := g.readConfig(ctx, b, ref, nil, nil)
	switch out.status {
	case "":
		g.rememberAttachments(b.EnvironmentID, out.attachNames)
		return out.input, nil
	case EnvNotFound:
		return Input{}, apierr.New(404, "config_not_found", out.message)
	default:
		return Input{}, apierr.Conflict("config_unreachable", out.message)
	}
}

// ---- the check ----

// checkOpts parameterise one check.
type checkOpts struct {
	// ref is the commit to read ("" = branch head).
	ref string
	// prefetch holds files an agent read at a build checkout.
	prefetch *prefetched
	// force plans even when the file and the environment are unchanged.
	force bool
	// autoApply allows applying the plan (pushes only; never production).
	autoApply bool
	// commit names the commit for the actor label (the pushed SHA).
	commit string
	// build marks a refresh from a build checkout: attachments of an unchanged
	// file are not read again.
	build bool
}

// stored is one write of the environment's check result.
type stored struct {
	status, detail, commit, fileSHA, path string
	plan                                  json.RawMessage
}

func (g *GitSync) store(ctx context.Context, bi BindingInfo, s stored) error {
	if err := g.q.SetEnvironmentConfigStatus(ctx, db.SetEnvironmentConfigStatusParams{
		EnvironmentID: bi.EnvironmentID, WorkspaceID: bi.WorkspaceID, Status: s.status, StatusDetail: clip(s.detail, 500),
		LastCommitSha: s.commit, LastFileSha256: s.fileSHA, LastPath: s.path, LastPlan: s.plan, CheckedAt: g.now(),
	}); err != nil {
		return fmt.Errorf("store config status: %w", err)
	}
	g.publish(bi)
	return nil
}

// kept returns s with the file fields of the previous check (a failed read
// does not forget what the last good read found).
func kept(prior *db.EnvironmentConfigSetting, status, detail string) stored {
	s := stored{status: status, detail: detail}
	if prior != nil {
		s.commit, s.fileSHA, s.path, s.plan = prior.LastCommitSha, prior.LastFileSha256, prior.LastPath, prior.LastPlan
	}
	return s
}

func (g *GitSync) envLock(id uuid.UUID) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, ok := g.envMu[id]
	if !ok {
		m = new(sync.Mutex)
		g.envMu[id] = m
	}
	return m
}

func (g *GitSync) rememberAttachments(id uuid.UUID, names []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.attach[id] = append([]string{}, names...)
}

// attachmentsOf returns the attachments the last read of the environment's
// file named; ok is false when this process has not read it yet.
func (g *GitSync) attachmentsOf(id uuid.UUID) (names []string, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	names, ok = g.attach[id]
	return names, ok
}

// check reads the environment's file and stores what it found. Failures are
// stored as the status, never returned: a check is a refresh, not a request
// that can fail.
func (g *GitSync) check(ctx context.Context, bi BindingInfo, o checkOpts) {
	lock := g.envLock(bi.EnvironmentID)
	lock.Lock()
	defer lock.Unlock()

	prior, err := g.settingsRow(ctx, bi.EnvironmentID)
	if err != nil {
		g.log.Warn("config check: load settings", "environment", bi.EnvironmentID.String(), "err", err)
		return
	}
	var readAtt func(string) bool
	if o.build {
		// A build checkout of the file already checked, in an environment that
		// did not move, needs no second read of its attachments.
		readAtt = func(sha string) bool {
			if prior == nil || prior.LastFileSha256 != sha || mustRead(prior.Status) {
				return true
			}
			lp, ok := decodeLastPlan(prior.LastPlan)
			gens, err := g.generations(ctx, bi.EnvironmentID)
			pgens, _, _ := strings.Cut(lp.Generations, ".")
			return !ok || err != nil || gens != pgens
		}
	}
	out := g.readConfig(ctx, bi, o.ref, o.prefetch, readAtt)
	switch out.status {
	case EnvNotFound:
		g.logStoreErr(g.store(ctx, bi, stored{status: EnvNotFound, detail: out.message, commit: out.commit}), bi)
		return
	case EnvUnreachable, EnvError:
		g.logStoreErr(g.store(ctx, bi, kept(prior, out.status, out.message)), bi)
		return
	}
	g.rememberAttachments(bi.EnvironmentID, out.attachNames)
	fileSHA := sha256Hex(out.file)
	if out.tooLarge {
		g.logStoreErr(g.store(ctx, bi, stored{
			status: EnvInvalid, detail: fmt.Sprintf("%s is %d KiB, over the %d KiB limit", out.path, len(out.file)>>10, projectconfig.MaxFileBytes>>10),
			commit: out.commit, fileSHA: fileSHA, path: out.path,
		}), bi)
		return
	}

	gens, err := g.generations(ctx, bi.EnvironmentID)
	if err != nil {
		g.log.Warn("config check: generations", "environment", bi.EnvironmentID.String(), "err", err)
		return
	}
	digest := gens + "." + digestAttachments(out.input.Attachments)
	if !o.force && prior != nil && unchanged(prior, out, fileSHA, digest) {
		if err := g.q.TouchEnvironmentConfigChecked(ctx, db.TouchEnvironmentConfigCheckedParams{
			EnvironmentID: bi.EnvironmentID, CheckedAt: g.now(), LastCommitSha: firstNonEmpty(out.commit, prior.LastCommitSha),
		}); err != nil {
			g.log.Warn("config check: touch", "environment", bi.EnvironmentID.String(), "err", err)
		}
		return
	}
	if out.attachSkipped {
		return // the checked file at another path: the next push or check reads it whole
	}

	label := actorLabel(firstNonEmpty(o.commit, out.commit))
	plan, err := g.plan(ctx, bi, out.input, label)
	if err != nil {
		g.logStoreErr(g.store(ctx, bi, kept(prior, EnvError, "planning the file failed: "+errMessage(err))), bi)
		return
	}
	status, detail := planStatus(plan, out.path)
	rec := stored{
		status: status, detail: joinDetail(detail, out.note), commit: out.commit, fileSHA: fileSHA, path: out.path,
		plan: encodeLastPlan(plan, digest),
	}
	if err := g.store(ctx, bi, rec); err != nil {
		g.log.Warn("config check: store", "environment", bi.EnvironmentID.String(), "err", err)
		return
	}
	if o.autoApply && status == EnvDiffers && prior != nil && prior.AutoApply {
		g.autoApply(ctx, bi, out, plan, rec, label)
	}
}

func (g *GitSync) logStoreErr(err error, bi BindingInfo) {
	if err != nil {
		g.log.Warn("config check: store", "environment", bi.EnvironmentID.String(), "err", err)
	}
}

// unchanged reports whether a read file and environment are the ones the last
// check planned, so that only the time (and commit) moves.
func unchanged(prior *db.EnvironmentConfigSetting, out readOutcome, fileSHA, digest string) bool {
	switch prior.Status {
	case EnvInSync, EnvDiffers, EnvInvalid:
	default:
		return false
	}
	if prior.LastFileSha256 != fileSHA || prior.LastPath != out.path {
		return false
	}
	lp, ok := decodeLastPlan(prior.LastPlan)
	if !ok {
		return false
	}
	gens, atts, _ := strings.Cut(digest, ".")
	pgens, patts, _ := strings.Cut(lp.Generations, ".")
	if gens != pgens {
		return false
	}
	return out.attachSkipped || atts == patts
}

// plan plans the file against the environment as the system actor (read only).
func (g *GitSync) plan(ctx context.Context, bi BindingInfo, in Input, label string) (Plan, error) {
	pctx := auth.WithPrincipal(ctx, auth.System(bi.WorkspaceID, bi.ProjectID, label, policy.ScopeProjectsRead))
	return g.planner.Plan(pctx, bi.ProjectID, PlanRequest{Environment: bi.EnvSlug, Input: in})
}

// autoApply stages the plan like a dashboard edit would (no deploy, no prune)
// when it is safe to do unattended, then refreshes the status.
func (g *GitSync) autoApply(ctx context.Context, bi BindingInfo, out readOutcome, plan Plan, rec stored, label string) {
	refuse := func(why string) {
		rec.detail = joinDetail(why, out.note)
		g.logStoreErr(g.store(ctx, bi, rec), bi)
	}
	// The flag is read again: an environment that became production meanwhile never applies.
	env, err := g.q.GetEnvironmentInternal(ctx, bi.EnvironmentID)
	if err != nil {
		g.log.Warn("config auto-apply: load environment", "environment", bi.EnvironmentID.String(), "err", err)
		return
	}
	switch {
	case env.Environment.IsProduction:
		return
	case !plan.Valid:
		refuse("auto-apply refused: the file has errors")
		return
	case privilegedPlan(plan):
		refuse("auto-apply refused: the file asks for host-level capabilities; a person applies it")
		return
	}
	sys := auth.WithPrincipal(ctx, auth.System(bi.WorkspaceID, bi.ProjectID, label,
		policy.ScopeProjectsRead, policy.ScopeProjectsWrite, policy.ScopeDeploymentsWrite))
	in := out.input
	in.Source.Origin = OriginGit
	res, err := g.planner.Apply(sys, bi.ProjectID, ApplyRequest{
		PlanRequest: PlanRequest{Environment: bi.EnvSlug, Input: in},
		PlanID:      plan.PlanID,
	})
	if err != nil {
		refuse(autoApplyRefusal(err))
		return
	}
	// The apply changed the environment: plan again for the status it leaves.
	gens, err := g.generations(ctx, bi.EnvironmentID)
	if err != nil {
		g.log.Warn("config auto-apply: generations", "environment", bi.EnvironmentID.String(), "err", err)
		return
	}
	after, err := g.plan(ctx, bi, out.input, label)
	if err != nil {
		refuse("auto-applied, but planning again failed: " + errMessage(err))
		return
	}
	status, detail := planStatus(after, out.path)
	note := "applied automatically"
	if res.Status == StatusPartial || res.Status == StatusFailed {
		note = "auto-apply did not finish: " + failedSteps(res)
	}
	g.logStoreErr(g.store(ctx, bi, stored{
		status: status, detail: joinDetail(detail, note, out.note), commit: rec.commit, fileSHA: rec.fileSHA, path: rec.path,
		plan: encodeLastPlan(after, gens+"."+digestAttachments(out.input.Attachments)),
	}), bi)
}

// privilegedPlan reports whether the plan holds anything an unattended apply
// must leave to a person.
func privilegedPlan(p Plan) bool {
	if p.Diagnostics.HasCode(codeAutoApplyPrivileged) {
		return true
	}
	for _, s := range p.Services {
		if containsString(s.Blockers, codeAutoApplyPrivileged) {
			return true
		}
	}
	return false
}

func autoApplyRefusal(err error) string {
	if ae, ok := apierr.As(err); ok {
		switch ae.Code {
		case CodeInsufficientScope:
			return "auto-apply refused: the file needs permissions a push cannot grant; a person applies it"
		case CodePlanStale:
			return "auto-apply skipped: the environment changed meanwhile; the next push checks again"
		case CodeConfigBlocked:
			return "auto-apply refused: the file has errors"
		}
		return "auto-apply refused: " + ae.Message
	}
	return "auto-apply failed: " + errMessage(err)
}

func failedSteps(r ApplyResult) string {
	var failed []string
	for _, s := range r.Steps {
		if s.Status == StepFailed {
			failed = append(failed, s.Step)
		}
	}
	if len(failed) == 0 {
		return r.Status
	}
	return "failed: " + strings.Join(failed, ", ")
}

// planStatus is the environment status a plan leaves, with its reason.
func planStatus(p Plan, file string) (status, detail string) {
	if !p.Valid || p.Diagnostics.HasErrors() {
		return EnvInvalid, firstProblem(p, file)
	}
	if n := differingServices(p); n > 0 || sharedDiffers(p) {
		switch {
		case n == 1:
			return EnvDiffers, "1 service differs from the file"
		case n > 1:
			return EnvDiffers, fmt.Sprintf("%d services differ from the file", n)
		}
		return EnvDiffers, "shared variables differ from the file"
	}
	return EnvInSync, ""
}

func firstProblem(p Plan, file string) string {
	for _, d := range p.Diagnostics {
		if d.Severity == projectconfig.SeverityError {
			return d.Render(file)
		}
	}
	return "the file has errors"
}

func differingServices(p Plan) int {
	n := 0
	for _, s := range p.Services {
		if s.Action == ActionNotInFile || s.Action == ActionBlocked {
			continue
		}
		if s.Action == ActionCreate || len(s.Released) > 0 {
			n++
			continue
		}
		for _, c := range s.Changes {
			if c.Op != OpKeep {
				n++
				break
			}
		}
	}
	return n
}

func sharedDiffers(p Plan) bool {
	for _, list := range [][]PlanChange{p.Shared.Project, p.Shared.Environment} {
		for _, c := range list {
			if c.Op != OpKeep {
				return true
			}
		}
	}
	return false
}

// encodeLastPlan is the stored slim plan with the digest of the state it was
// computed from.
func encodeLastPlan(p Plan, digest string) json.RawMessage {
	lp := NewLastPlan(p)
	lp.Generations = digest
	raw, err := json.Marshal(lp)
	if err != nil {
		return nil
	}
	return raw
}

func decodeLastPlan(raw json.RawMessage) (LastPlan, bool) {
	if len(raw) == 0 {
		return LastPlan{}, false
	}
	var lp LastPlan
	if json.Unmarshal(raw, &lp) != nil {
		return LastPlan{}, false
	}
	return lp, true
}

// generations digests the generations of the environment's live services: a
// dashboard edit moves it, so a check re-plans.
func (g *GitSync) generations(ctx context.Context, environmentID uuid.UUID) (string, error) {
	rows, err := g.q.ListEnvironmentServiceGenerations(ctx, environmentID)
	if err != nil {
		return "", fmt.Errorf("list service generations: %w", err)
	}
	h := sha256.New()
	for _, r := range rows {
		fmt.Fprintf(h, "%s:%d\n", r.ID, r.Generation)
	}
	return hex.EncodeToString(h.Sum(nil)[:16]), nil
}

func digestAttachments(att map[string]string) string {
	names := make([]string, 0, len(att))
	for n := range att {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		sum := sha256.Sum256([]byte(att[n]))
		fmt.Fprintf(h, "%s:%x\n", n, sum)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func actorLabel(commit string) string {
	if commit == "" {
		return "nebula.toml"
	}
	if len(commit) > 7 {
		commit = commit[:7]
	}
	return "nebula.toml @" + commit
}

func joinDetail(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func errMessage(err error) string {
	if ae, ok := apierr.As(err); ok && ae.Message != "" {
		return ae.Message
	}
	return err.Error()
}

// ---- background work ----

// async runs fn detached from the caller, bounded, and recovers panics.
func (g *GitSync) async(fn func(ctx context.Context)) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				g.log.Error("config check panicked", "panic", fmt.Sprint(r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		fn(ctx)
	}()
}

// Refresh implements Binding: it checks an environment's file soon (debounced),
// the status after a binding or source change or a dashboard apply. It writes
// nothing but the status.
func (g *GitSync) Refresh(environmentID uuid.UUID) {
	if g.planner == nil || g.q == nil {
		return
	}
	now := g.now()
	g.mu.Lock()
	if t, ok := g.pending[environmentID]; ok && now.Sub(t) < g.scanDelay {
		g.mu.Unlock()
		return
	}
	for k, t := range g.pending {
		if now.Sub(t) > time.Minute {
			delete(g.pending, k)
		}
	}
	g.pending[environmentID] = now
	g.mu.Unlock()
	g.async(func(ctx context.Context) {
		if g.scanDelay > 0 {
			select {
			case <-time.After(g.scanDelay):
			case <-ctx.Done():
				return
			}
		}
		bi, err := g.Resolve(ctx, environmentID)
		if err != nil || bi.Mode == BindingNone {
			return
		}
		g.check(ctx, bi, checkOpts{})
	})
}

// GitSourceChanged implements services.SourceHook: a git service was created
// or its repository, branch, build context, server or token changed, so the
// file may now live somewhere else. It only schedules a status refresh.
func (g *GitSync) GitSourceChanged(_ context.Context, sc services.Scope) {
	g.Refresh(sc.EnvironmentID)
}

// ---- pushes ----

// BeforePush implements gitproviders.PushHook: for every environment bound to
// the pushed repository and branch, read the file at the pushed commit, store
// the status and (auto-apply on, not production) stage it, before the push
// deploys anything.
func (g *GitSync) BeforePush(ctx context.Context, ev gitproviders.PushEvent) {
	if g.planner == nil || g.q == nil || ev.CommitSHA == "" || strings.Trim(ev.CommitSHA, "0") == "" {
		return // not configured, or the branch was deleted
	}
	infos, err := g.environmentsForPush(ctx, ev)
	if err != nil {
		g.log.Warn("config push: find environments", "repo", ev.RepoFullName, "err", err)
		return
	}
	for _, bi := range infos {
		g.checkPush(ctx, bi, ev)
	}
}

// environmentsForPush finds the standard environments whose binding is the
// pushed repository and branch: explicit rows plus derived bindings.
func (g *GitSync) environmentsForPush(ctx context.Context, ev gitproviders.PushEvent) ([]BindingInfo, error) {
	var ids []uuid.UUID
	seen := map[uuid.UUID]bool{}
	add := func(id uuid.UUID) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	explicit, err := g.q.ListExplicitConfigEnvironmentsForRepo(ctx, db.ListExplicitConfigEnvironmentsForRepoParams{
		ConnectionID: ev.ConnectionID, Repo: ev.RepoFullName, Branch: ev.Branch,
	})
	if err != nil {
		return nil, fmt.Errorf("list explicit bindings: %w", err)
	}
	for _, r := range explicit {
		add(r.EnvironmentConfigSetting.EnvironmentID)
	}
	derived, err := g.q.ListGitEnvironmentsForRepo(ctx, db.ListGitEnvironmentsForRepoParams{ConnectionID: ev.ConnectionID, Repo: ev.RepoFullName})
	if err != nil {
		return nil, fmt.Errorf("list git environments: %w", err)
	}
	for _, id := range derived {
		add(id)
	}
	var out []BindingInfo
	for _, id := range ids {
		bi, err := g.Resolve(ctx, id)
		if err != nil {
			g.log.Warn("config push: resolve", "environment", id.String(), "err", err)
			continue
		}
		if bi.Mode == BindingNone || bi.ConnectionID != ev.ConnectionID || !strings.EqualFold(bi.Repo, ev.RepoFullName) || bi.Branch != ev.Branch {
			continue
		}
		out = append(out, bi)
	}
	return out, nil
}

// checkPush is the check of one environment for a push.
func (g *GitSync) checkPush(ctx context.Context, bi BindingInfo, ev gitproviders.PushEvent) {
	if ev.PathsComplete {
		prior, err := g.settingsRow(ctx, bi.EnvironmentID)
		if err == nil && prior != nil && !mustRead(prior.Status) && !g.pushTouches(bi, ev.ChangedPaths) {
			return // the cheap path: the push touches neither the file nor its attachments
		}
	}
	g.check(ctx, bi, checkOpts{ref: ev.CommitSHA, autoApply: true, commit: ev.CommitSHA})
}

// mustRead reports whether a status means the file must be read again whatever
// the push changed.
func mustRead(status string) bool {
	switch status {
	case EnvInSync, EnvDiffers, EnvInvalid, EnvNotFound:
		return false
	}
	return true // unknown, error, unreachable
}

// pushTouches reports whether the changed files include a candidate path or an
// attachment of the file. An environment whose file this process has not read
// yet counts as touched: its attachments are not known.
func (g *GitSync) pushTouches(bi BindingInfo, changed []string) bool {
	names, known := g.attachmentsOf(bi.EnvironmentID)
	if !known {
		return true
	}
	for _, c := range changed {
		if containsString(bi.Paths, c) || containsString(names, c) {
			return true
		}
	}
	return false
}

// ---- build checkouts ----

// HandleSourceScan implements agents.SourceScanSink: the agent reports the
// project file of a commit it checked out to build. The status of the
// environment is refreshed at that commit; the release being built is never
// touched, and nothing is applied.
func (g *GitSync) HandleSourceScan(ctx context.Context, serverID uuid.UUID, scan agentprotocol.SourceScan) error {
	if g.planner == nil || g.q == nil || len(scan.ConfigPaths) == 0 {
		return nil
	}
	id, err := uuid.Parse(scan.ServiceID)
	if err != nil {
		return nil //nolint:nilerr // not a service of ours: drop the scan
	}
	sc, err := services.LoadScopeInternal(ctx, g.q, id)
	if err != nil || sc.Service.SourceType != services.SourceGit {
		return nil //nolint:nilerr // deleted service: nothing to refresh
	}
	if sc.Service.ServerID == nil || *sc.Service.ServerID != serverID {
		// A build server scans the checkout of the deployments it builds.
		built, err := g.q.ServerBuiltForService(ctx, db.ServerBuiltForServiceParams{ServerID: serverID, ServiceID: id})
		if err != nil || !built {
			return nil //nolint:nilerr // not this server's service: drop the scan
		}
	}
	if !commitShaRe.MatchString(scan.Commit) {
		return nil
	}
	bi, err := g.Resolve(ctx, sc.EnvironmentID)
	if err != nil || bi.Mode == BindingNone || bi.Branch != sc.Config().GitBranch {
		return nil //nolint:nilerr // no binding, or the build is not of the bound branch
	}
	// The agent looked at the root and the build context: it must have looked at
	// every candidate for the absence of a file to mean anything.
	for _, p := range bi.Paths {
		if !containsString(scan.ConfigPaths, p) {
			return nil
		}
	}
	pre := &prefetched{commit: scan.Commit, files: map[string][]byte{}}
	for _, p := range scan.ConfigPaths {
		if agentprotocol.ValidConfigPath(p) && len(pre.looked) < agentprotocol.ScanMaxConfigPaths {
			pre.looked = append(pre.looked, p)
		}
	}
	for p, b := range scan.ConfigFiles {
		if containsString(pre.looked, p) && len(b) <= agentprotocol.ScanMaxConfigFileBytes {
			pre.files[p] = b
		}
	}
	g.async(func(ctx context.Context) {
		g.check(ctx, bi, checkOpts{ref: scan.Commit, prefetch: pre, build: true})
	})
	return nil
}
