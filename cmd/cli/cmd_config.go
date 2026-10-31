package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/apiclient"
	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
)

const (
	configFileName  = "nebula.toml"
	attachmentLimit = 1 << 20 // larger referenced files are not sent
	// Exit codes of `config plan --detailed-exit-code`.
	exitPlanPending = 3
)

func newConfigCmd(c *cli) *cobra.Command {
	cmd := group("config", "Manage a project from a nebula.toml file",
		newConfigInitCmd(c),
		newConfigValidateCmd(c),
		newConfigPlanCmd(c),
		newConfigApplyCmd(c),
		newConfigExportCmd(c),
		newConfigSchemaCmd(c),
		newConfigSourceCmd(c),
	)
	cmd.Long = "A nebula.toml file describes one project: its services, domains, variables (names and references, never secret values), tasks, monitors, backup schedules, scaling, volumes and per-environment overrides.\n\n" +
		"The lifecycle is validate, plan, apply. `apply` stages changes exactly like a dashboard edit, applies the settings that take effect at once, and deploys only with --deploy.\n" +
		"Start from `config export` of a live project, or `config init` for a new repository.\n\n" +
		"Exit codes: validate 0 valid, 1 invalid. plan 0 ok (with --detailed-exit-code: 0 no changes, 3 changes pending), 1 invalid or failed. apply 0 applied or nothing to do, 1 blocked, failed or partial. 2 wrong flags."
	return cmd
}

// configFlags are the flags every config command that reads the file shares.
type configFlags struct {
	file    string
	project string
	envs    []string
}

func (fl *configFlags) addFile(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&fl.file, "file", "f", "", "path of the file (default: nebula.toml in the current directory, else in a parent directory up to the git root)")
}

func (fl *configFlags) addProject(cmd *cobra.Command) {
	cmd.Flags().StringVar(&fl.project, "project", "", "project ID, slug or name (default: the `project` key of the file)")
}

func (fl *configFlags) addEnvs(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&fl.envs, "env", nil, "environment ID, slug or name; repeat to plan several (optional when the project has one standard environment)")
}

// ---- reading the file -------------------------------------------------------

// configFile is a nebula.toml read from disk with the files it refers to.
type configFile struct {
	label       string // path as shown in diagnostics
	dir         string
	text        []byte
	attachments map[string]string // relative path -> content
	project     string            // the file's `project` key, when it has one
}

// gitRoot returns the nearest directory at or above dir that holds a .git
// entry, or "".
func gitRoot(dir string) string {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// findConfigFile looks for nebula.toml in dir and then in each parent up to
// the git root. Without a git root only dir is searched.
func findConfigFile(dir string) (string, error) {
	root := gitRoot(dir)
	for d := dir; ; {
		p := filepath.Join(d, configFileName)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, nil
		}
		if root == "" || d == root {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	where := dir
	if root != "" && root != dir {
		where += " or its parents up to " + root
	}
	return "", fmt.Errorf("no %s found in %s; pass --file, or create one with `nebulactrl config init`", configFileName, where)
}

// readLimited reads at most limit bytes of path. A result longer than limit
// never happens; callers that must detect truncation pass limit+1.
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// displayPath shows path relative to the working directory when that is
// shorter than the path itself ("../nebula.toml" beats an absolute path).
func displayPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, abs); err == nil && len(rel) <= len(abs) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

// loadConfigFile reads the file (explicit path, else discovery) and the
// attachments it names.
func (c *cli) loadConfigFile(explicit string) (*configFile, error) {
	path := explicit
	if path == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		if path, err = findConfigFile(wd); err != nil {
			return nil, err
		}
	}
	// Anything larger than the format allows is rejected by the validator; the
	// read cap only keeps a wrong --file (a device, a log) from filling memory.
	text, err := readLimited(path, 2*projectconfig.MaxFileBytes)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cf := &configFile{label: displayPath(path), dir: filepath.Dir(path), text: text}
	var root map[string]any
	if toml.Unmarshal(text, &root) == nil {
		cf.project, _ = root["project"].(string)
		cf.attachments = c.collectAttachments(cf.dir, root)
	}
	return cf, nil
}

// attachmentRefs lists the relative paths a file refers to: stack.compose_file
// and files.<path>.source of every service, in the base and in environments.
func attachmentRefs(root map[string]any) []string {
	seen := map[string]bool{}
	table := func(v any) map[string]any { m, _ := v.(map[string]any); return m }
	services := func(m map[string]any) {
		for _, sv := range table(m["services"]) {
			svc := table(sv)
			if s, ok := table(svc["stack"])["compose_file"].(string); ok && s != "" {
				seen[s] = true
			}
			for _, fv := range table(svc["files"]) {
				if s, ok := table(fv)["source"].(string); ok && s != "" {
					seen[s] = true
				}
			}
		}
	}
	services(root)
	for _, ev := range table(root["environments"]) {
		services(table(ev))
	}
	refs := make([]string, 0, len(seen))
	for s := range seen {
		refs = append(refs, s)
	}
	sort.Strings(refs)
	return refs
}

// collectAttachments reads the files the nebula.toml refers to, keyed by the
// path as written in the file. It sends nothing that leaves dir (absolute or
// ".." paths, symlinks pointing elsewhere), is not a regular file or is too
// large; the validator then reports the reference as missing or escaping.
func (c *cli) collectAttachments(dir string, root map[string]any) map[string]string {
	refs := attachmentRefs(root)
	if len(refs) == 0 {
		return nil
	}
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		base = dir
	}
	out := map[string]string{}
	for _, ref := range refs {
		if !filepath.IsLocal(filepath.FromSlash(ref)) {
			c.note("not sending %q: a referenced file must be a relative path inside the directory of %s", ref, configFileName)
			continue
		}
		real, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(ref)))
		if err != nil {
			continue // missing: the validator reports attachment_missing
		}
		if rel, err := filepath.Rel(base, real); err != nil || !filepath.IsLocal(rel) {
			c.note("not sending %q: it resolves outside the directory of %s", ref, configFileName)
			continue
		}
		if st, err := os.Stat(real); err != nil || !st.Mode().IsRegular() {
			continue
		}
		data, err := readLimited(real, attachmentLimit+1)
		if err != nil {
			continue
		}
		if len(data) > attachmentLimit {
			c.note("not sending %q: larger than %d KiB", ref, attachmentLimit>>10)
			continue
		}
		out[ref] = string(data)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---- request bodies ---------------------------------------------------------

type cfgGitRef struct {
	Ref *string `json:"ref"`
}

type cfgSourceBody struct {
	File        string            `json:"file,omitempty"`
	Attachments map[string]string `json:"attachments,omitempty"`
	Git         *cfgGitRef        `json:"git,omitempty"`
}

// cfgRequestBody is the body of plan and apply (apply adds planId and deploy).
type cfgRequestBody struct {
	Environment string        `json:"environment,omitempty"`
	Source      cfgSourceBody `json:"source"`
	Only        []string      `json:"only,omitempty"`
	Prune       bool          `json:"prune,omitempty"`
	PlanID      string        `json:"planId,omitempty"`
	Deploy      bool          `json:"deploy,omitempty"`
}

type cfgValidateBody struct {
	File        string            `json:"file"`
	Attachments map[string]string `json:"attachments,omitempty"`
	Environment string            `json:"environment,omitempty"`
}

// ---- API errors -------------------------------------------------------------

// configFailure is a failed config call. Besides the problem it keeps the
// plan the server attached (config_blocked, plan_stale) and the scalar
// details (missing scopes, an approval link).
type configFailure struct {
	*apiError
	plan    *cfgPlan
	planRaw json.RawMessage
	extra   []string
}

func (f *configFailure) Error() string {
	s := f.apiError.Error()
	for _, e := range f.extra {
		s += "\n  " + e
	}
	return s
}

func (f *configFailure) Unwrap() error { return f.apiError }

func newConfigFailure(ae *apiError, body []byte) *configFailure {
	f := &configFailure{apiError: ae}
	var env struct {
		Details map[string]json.RawMessage `json:"details"`
	}
	if json.Unmarshal(body, &env) != nil {
		return f
	}
	keys := make([]string, 0, len(env.Details))
	for k := range env.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		raw := env.Details[k]
		if k == "plan" {
			var p cfgPlan
			if json.Unmarshal(raw, &p) == nil {
				f.plan, f.planRaw = &p, raw
			}
			continue
		}
		var s string
		var list []string
		switch {
		case json.Unmarshal(raw, &s) == nil && s != "":
			f.extra = append(f.extra, k+": "+cleanText(s))
		case json.Unmarshal(raw, &list) == nil && len(list) > 0:
			for i := range list {
				list[i] = cleanText(list[i])
			}
			f.extra = append(f.extra, k+": "+strings.Join(list, ", "))
		}
	}
	return f
}

// configCheck is check for the config endpoints: a failure keeps the plan and
// details the server sent.
func configCheck[R response](r R, err error) error {
	cerr := check(r, err)
	if cerr == nil {
		return nil
	}
	var ae *apiError
	if !errors.As(cerr, &ae) {
		return cerr
	}
	return newConfigFailure(ae, r.GetBody())
}

// ---- shared resolution ------------------------------------------------------

// configProject resolves --project, else the file's `project` key.
func (c *cli) configProject(ctx context.Context, flagRef, fileRef string, offlineHint bool) (apiclient.Project, error) {
	ref := flagRef
	if ref == "" {
		ref = fileRef
	}
	if ref == "" {
		msg := "no project: pass --project or set `project` in " + configFileName
		if offlineHint {
			msg += " (or use --offline)"
		}
		return apiclient.Project{}, usagef("%s", msg)
	}
	return c.resolveProject(ctx, ref)
}

// configEnvSlugs resolves each --env to its slug. Without --env it returns one
// empty entry: the server picks the project's only standard environment.
func (c *cli) configEnvSlugs(ctx context.Context, projectID string, refs []string) ([]string, error) {
	if len(refs) == 0 {
		return []string{""}, nil
	}
	var slugs []string
	seen := map[string]bool{}
	for _, ref := range refs {
		e, err := c.resolveEnv(ctx, projectID, ref)
		if err != nil {
			return nil, err
		}
		if !seen[e.Slug] {
			seen[e.Slug] = true
			slugs = append(slugs, e.Slug)
		}
	}
	return slugs, nil
}

// envKey is the Idempotency-Key of one environment's apply. A key given with
// --idempotency-key is made per environment when several are applied.
func (c *cli) envKey(env string, multi bool) *string {
	k := c.key()
	if multi && c.idemKey != "" && env != "" {
		s := *k + ":" + env
		return &s
	}
	return k
}

// configRun is everything plan and apply need once flags are parsed.
type configRun struct {
	cf      *configFile // nil with --from-git
	label   string
	project apiclient.Project
	envs    []string
	src     cfgSourceBody
}

// prepareRun reads the file, connects and resolves project and environments.
func (c *cli) prepareRun(cmd *cobra.Command, fl *configFlags, fromGit bool) (*configRun, error) {
	run := &configRun{label: configFileName}
	if fromGit {
		if fl.file != "" {
			return nil, usagef("--from-git reads the file through the environment's binding; drop --file")
		}
		if fl.project == "" {
			return nil, usagef("--from-git needs --project (there is no local file to take it from)")
		}
		run.src = cfgSourceBody{Git: &cfgGitRef{}}
	} else {
		cf, err := c.loadConfigFile(fl.file)
		if err != nil {
			return nil, err
		}
		run.cf, run.label = cf, cf.label
		run.src = cfgSourceBody{File: string(cf.text), Attachments: cf.attachments}
	}
	if err := c.connect(); err != nil {
		return nil, err
	}
	ctx, cancel := c.ctx(cmd.Context())
	defer cancel()
	fileProject := ""
	if run.cf != nil {
		fileProject = run.cf.project
	}
	p, err := c.configProject(ctx, fl.project, fileProject, false)
	if err != nil {
		return nil, err
	}
	run.project = p
	if run.envs, err = c.configEnvSlugs(ctx, p.Id, fl.envs); err != nil {
		return nil, err
	}
	return run, nil
}

// printRawJSON prints an API body as received, indented.
func (c *cli) printRawJSON(raw []byte) error {
	return c.printJSON(json.RawMessage(raw))
}

// printEnvJSON prints the body of one environment, or an array for several.
func (c *cli) printEnvJSON(bodies []json.RawMessage) error {
	switch len(bodies) {
	case 0:
		return nil
	case 1:
		return c.printRawJSON(bodies[0])
	}
	return c.printJSON(bodies)
}

// finishEnvs turns the per-environment errors into the command's result.
func (c *cli) finishEnvs(envs []string, errs map[string]error, code int) error {
	switch {
	case len(errs) == 1 && len(envs) == 1:
		for _, err := range errs {
			return err
		}
	case len(errs) > 0:
		for _, env := range envs {
			if err := errs[env]; err != nil {
				fmt.Fprintf(c.errOut, "Error: environment %s: %v\n", env, err)
			}
		}
		return &exitError{code: 1}
	}
	if code != 0 {
		return &exitError{code: code}
	}
	return nil
}

// ---- validate ---------------------------------------------------------------

func newConfigValidateCmd(c *cli) *cobra.Command {
	var fl configFlags
	var env string
	var offline bool
	cmd := &cobra.Command{
		Use: "validate", Short: "Check a nebula.toml", Args: noArgs,
		Long: "Sends the file to the control plane, which checks it against the project's real servers, connections and services, and prints every diagnostic as file:line:column: severity[code] path: message. Exit 1 when there is any error.\n" +
			"With --offline nothing is sent: the file is parsed, merged and checked statically on this machine (strict keys, types, references, limits), without names or live state.",
		Example: "nebulactrl config validate\nnebulactrl config validate --env staging\nnebulactrl config validate --offline --json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cf, err := c.loadConfigFile(fl.file)
			if err != nil {
				return err
			}
			var raw []byte
			var res cfgValidateResult
			if offline {
				res = offlineValidate(cf.text, cf.attachments, env)
				if raw, err = json.Marshal(res); err != nil {
					return err
				}
			} else {
				if err := c.connect(); err != nil {
					return err
				}
				ctx, cancel := c.ctx(cmd.Context())
				defer cancel()
				p, err := c.configProject(ctx, fl.project, cf.project, true)
				if err != nil {
					return err
				}
				slug := ""
				if env != "" {
					e, err := c.resolveEnv(ctx, p.Id, env)
					if err != nil {
						return err
					}
					slug = e.Slug
				}
				body, _ := json.Marshal(cfgValidateBody{File: string(cf.text), Attachments: cf.attachments, Environment: slug})
				r, err := c.api.ValidateProjectConfigWithBodyWithResponse(ctx, p.Id, "application/json", bytes.NewReader(body))
				if err := configCheck(r, err); err != nil {
					return err
				}
				raw = r.GetBody()
				if err := json.Unmarshal(raw, &res); err != nil {
					return fmt.Errorf("decode the validate response: %w", err)
				}
			}
			if c.asJSON {
				if err := c.printRawJSON(raw); err != nil {
					return err
				}
			} else {
				writeValidate(c.out, cf.label, &res)
			}
			if !res.Valid || res.Diagnostics.HasErrors() {
				return &exitError{code: 1}
			}
			return nil
		},
	}
	fl.addFile(cmd)
	fl.addProject(cmd)
	f := cmd.Flags()
	f.StringVar(&env, "env", "", "also check the file as resolved for this environment (slug; with --offline the key of an [environments.<slug>] block)")
	f.BoolVar(&offline, "offline", false, "check on this machine only: parse, strict keys, merge and static validation; no names or live state")
	return cmd
}

// offlineValidate runs the format's own checks without the API: Parse, then
// Resolve for the base and every environment block (or only env).
func offlineValidate(text []byte, att map[string]string, env string) cfgValidateResult {
	attachments := make(projectconfig.Attachments, len(att))
	for k, v := range att {
		attachments[k] = []byte(v)
	}
	res := cfgValidateResult{Environments: []string{}, Services: []cfgValidateService{}, Diagnostics: projectconfig.Diagnostics{}}
	f, idx, ds := projectconfig.Parse(text, attachments)
	seen := map[string]bool{}
	add := func(list projectconfig.Diagnostics) {
		for _, d := range list {
			key := fmt.Sprintf("%s|%s|%s|%d|%d|%s|%s", d.Severity, d.Code, d.Path, d.Line, d.Column, d.Environment, d.Message)
			if !seen[key] {
				seen[key] = true
				res.Diagnostics = append(res.Diagnostics, d)
			}
		}
	}
	add(ds)
	if f != nil {
		res.Schema = projectconfig.SchemaVersion
		if envs := f.Environments(); len(envs) > 0 {
			res.Environments = envs
		}
		targets := []string{""}
		if env != "" {
			targets = []string{env}
		} else {
			targets = append(targets, res.Environments...)
		}
		for i, t := range targets {
			rs, rds := f.Resolve(t, idx)
			add(rds)
			if i == 0 && rs != nil {
				slugs := make([]string, 0, len(rs.Services))
				for slug := range rs.Services {
					slugs = append(slugs, slug)
				}
				sort.Strings(slugs)
				for _, slug := range slugs {
					res.Services = append(res.Services, cfgValidateService{Slug: slug, Kind: rs.Services[slug].Kind})
				}
			}
		}
	}
	res.Valid = !res.Diagnostics.HasErrors()
	return res
}

// ---- plan -------------------------------------------------------------------

func newConfigPlanCmd(c *cli) *cobra.Command {
	var fl configFlags
	var only []string
	var planOut string
	var detailed, fromGit, prune, verbose bool
	cmd := &cobra.Command{
		Use: "plan", Short: "Show what applying a nebula.toml would change", Args: noArgs,
		Long: "Reads nothing but the file and the live project and changes nothing. Every change is labelled staged (takes effect on the next deploy) or applies at once. Secret values never appear: they are masked.\n" +
			"With --detailed-exit-code the exit code is 0 for no changes, 3 for pending changes and 1 for an invalid plan. Several --env flags print one plan per environment; --plan-out then inserts the environment slug before the file extension.",
		Example: "nebulactrl config plan --env production\nnebulactrl config plan --env staging --plan-out plan.json\nnebulactrl config plan --env production --detailed-exit-code --json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			run, err := c.prepareRun(cmd, &fl, fromGit)
			if err != nil {
				return err
			}
			multi := len(run.envs) > 1
			var bodies []json.RawMessage
			errs := map[string]error{}
			code := 0
			for i, env := range run.envs {
				body, _ := json.Marshal(cfgRequestBody{Environment: env, Source: run.src, Only: only, Prune: prune})
				ctx, cancel := c.ctx(cmd.Context())
				r, err := c.api.PlanProjectConfigWithBodyWithResponse(ctx, run.project.Id, "application/json", bytes.NewReader(body))
				cancel()
				if err := configCheck(r, err); err != nil {
					errs[env] = c.reportFailure(err, run.label, verbose, &bodies)
					continue
				}
				var p cfgPlan
				if err := json.Unmarshal(r.GetBody(), &p); err != nil {
					errs[env] = fmt.Errorf("decode the plan: %w", err)
					continue
				}
				if c.asJSON {
					bodies = append(bodies, json.RawMessage(r.GetBody()))
				} else {
					if i > 0 {
						fmt.Fprintln(c.out)
					}
					writePlan(c.out, &p, run.label, verbose)
				}
				if planOut != "" {
					if err := writePlanFile(planOut, env, multi, r.GetBody()); err != nil {
						return err
					}
					c.note("Wrote the plan to %s", planFilePath(planOut, env, multi))
				}
				switch {
				case !p.ok():
					code = 1
				case detailed && p.hasChanges() && code == 0:
					code = exitPlanPending
				}
			}
			if c.asJSON {
				if err := c.printEnvJSON(bodies); err != nil {
					return err
				}
			}
			return c.finishEnvs(run.envs, errs, code)
		},
	}
	fl.addFile(cmd)
	fl.addProject(cmd)
	fl.addEnvs(cmd)
	f := cmd.Flags()
	f.StringVar(&planOut, "plan-out", "", "write the plan (JSON) to this file; pass it to `config apply --plan`")
	f.BoolVar(&detailed, "detailed-exit-code", false, "exit 0 when there are no changes, 3 when changes are pending, 1 when the plan is invalid")
	f.StringArrayVar(&only, "only", nil, "restrict the plan to this file key path, e.g. services.web or services.web.variables.NODE_ENV (repeatable)")
	f.BoolVar(&fromGit, "from-git", false, "read the file through the environment's repository binding instead of the local file (needs --project)")
	f.BoolVar(&prune, "prune", false, "also plan deleting sub-resources the file owned before and no longer lists")
	f.BoolVar(&verbose, "verbose", false, "list every field of new services and the live members the file does not mention")
	return cmd
}

// reportFailure prints what a failed call attached and returns the error to
// report. In JSON mode the attached plan is collected for stdout.
func (c *cli) reportFailure(err error, label string, verbose bool, bodies *[]json.RawMessage) error {
	var cf *configFailure
	if !errors.As(err, &cf) || cf.plan == nil {
		return err
	}
	if c.asJSON {
		*bodies = append(*bodies, cf.planRaw)
	} else {
		writePlan(c.out, cf.plan, label, verbose)
		fmt.Fprintln(c.out)
	}
	return err
}

// planFilePath is where --plan-out writes the plan of env.
func planFilePath(path, env string, multi bool) string {
	if !multi || env == "" {
		return path
	}
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "." + env + ext
}

func writePlanFile(path, env string, multi bool, raw []byte) error {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		buf.Reset()
		buf.Write(raw)
	}
	buf.WriteByte('\n')
	// A plan names servers and connections: keep it private to the user.
	return os.WriteFile(planFilePath(path, env, multi), buf.Bytes(), 0o600)
}

// ---- apply ------------------------------------------------------------------

func newConfigApplyCmd(c *cli) *cobra.Command {
	var fl configFlags
	var only []string
	var planID, planFile string
	var deploy, prune, yes, fromGit, wait, verbose bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "apply", Short: "Apply a nebula.toml to an environment", Args: noArgs,
		Long: "Plans again inside one transaction and applies: changes that need a deploy are staged like a dashboard edit, settings that take effect at once run afterwards. Nothing is deployed unless you pass --deploy.\n" +
			"Pass --plan-id (or --plan <file> from `config plan --plan-out`) to fail with plan_stale when anything changed since that plan; use the same --only and --prune as when planning. Without it the apply plans and applies atomically.\n" +
			"--prune also deletes sub-resources the file owned before and no longer lists (variables, domains, volume attachments, networks, links, tasks, backup schedules); services, databases and stored data are never deleted. It needs --yes.\n" +
			"Exit 0 when applied or nothing to do; 1 when blocked, failed or partial (the result lists the failed steps; apply again to converge).",
		Example: "nebulactrl config apply --env staging\nnebulactrl config apply --env production --plan plan.json --deploy --wait",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if wait && !deploy {
				return usagef("--wait waits for the deployments --deploy starts; add --deploy")
			}
			if prune && !yes {
				return usagef("--prune deletes what the file owned and no longer lists; pass --yes to confirm")
			}
			if planID != "" && planFile != "" {
				return usagef("use either --plan-id or --plan, not both")
			}
			planEnv := ""
			if planFile != "" {
				raw, err := os.ReadFile(planFile)
				if err != nil {
					return fmt.Errorf("read the plan: %w", err)
				}
				var p cfgPlan
				if err := json.Unmarshal(raw, &p); err != nil || p.PlanID == "" {
					return fmt.Errorf("%s is not a plan written by `config plan --plan-out`", planFile)
				}
				if !p.ok() {
					return &exitError{code: 1, err: fmt.Errorf("the plan in %s is not valid; fix the file and plan again", planFile)}
				}
				planID, planEnv = p.PlanID, p.Environment.Slug
			}
			run, err := c.prepareRun(cmd, &fl, fromGit)
			if err != nil {
				return err
			}
			if planID != "" {
				if len(run.envs) > 1 {
					return usagef("a plan id belongs to one environment; pass one --env")
				}
				if planEnv != "" && run.envs[0] == "" {
					run.envs[0] = planEnv
				}
				if planEnv != "" && run.envs[0] != planEnv {
					return usagef("%s was planned for environment %s, not %s", planFile, planEnv, run.envs[0])
				}
			}
			multi := len(run.envs) > 1
			var bodies []json.RawMessage
			errs := map[string]error{}
			code := 0
			for i, env := range run.envs {
				body, _ := json.Marshal(cfgRequestBody{Environment: env, Source: run.src, Only: only, Prune: prune, PlanID: planID, Deploy: deploy})
				ctx, cancel := c.ctx(cmd.Context())
				r, err := c.api.ApplyProjectConfigWithBodyWithResponse(ctx, run.project.Id,
					&apiclient.ApplyProjectConfigParams{IdempotencyKey: c.envKey(env, multi)}, "application/json", bytes.NewReader(body))
				cancel()
				if err := configCheck(r, err); err != nil {
					errs[env] = c.reportFailure(err, run.label, verbose, &bodies)
					continue
				}
				var res cfgApplyResult
				if err := json.Unmarshal(r.GetBody(), &res); err != nil {
					errs[env] = fmt.Errorf("decode the apply result: %w", err)
					continue
				}
				if c.asJSON {
					bodies = append(bodies, json.RawMessage(r.GetBody()))
				} else {
					if i > 0 {
						fmt.Fprintln(c.out)
					}
					writeApplyResult(c.out, &res, run.label, verbose)
				}
				if res.Status == "partial" || res.Status == "failed" {
					errs[env] = &exitError{code: 1, err: fmt.Errorf("apply %s: %s", res.Status, plural(max(res.stepsFailed(), 1), "step")+" failed")}
					continue
				}
				if res.Status == "staged" && len(res.Deployments) == 0 && len(res.Plan.Summary.RequiresDeploy) > 0 {
					c.note("Staged changes take effect on the next deploy of %s: re-run with --deploy, or `nebulactrl services deploy <project>/<env>/<service>`.",
						strings.Join(res.Plan.Summary.RequiresDeploy, ", "))
				}
				if wait && len(res.Deployments) > 0 {
					if err := c.waitDeployments(cmd.Context(), res.Deployments, timeout); err != nil {
						errs[env] = err
					}
				}
			}
			if c.asJSON {
				if err := c.printEnvJSON(bodies); err != nil {
					return err
				}
			}
			return c.finishEnvs(run.envs, errs, code)
		},
	}
	fl.addFile(cmd)
	fl.addProject(cmd)
	fl.addEnvs(cmd)
	f := cmd.Flags()
	f.StringVar(&planID, "plan-id", "", "fail with plan_stale unless the plan is still this one (pl_…)")
	f.StringVar(&planFile, "plan", "", "take the plan id and the environment from a file written by `config plan --plan-out`")
	f.BoolVar(&deploy, "deploy", false, "deploy the services this apply touched that now have staged changes")
	f.BoolVar(&prune, "prune", false, "also delete sub-resources the file owned before and no longer lists (needs --yes)")
	f.BoolVar(&yes, "yes", false, "confirm --prune")
	f.StringArrayVar(&only, "only", nil, "restrict the apply to this file key path (repeatable); use the same value as when planning")
	f.BoolVar(&fromGit, "from-git", false, "read the file through the environment's repository binding instead of the local file (needs --project)")
	f.BoolVar(&wait, "wait", false, "with --deploy: wait for the deployments; exit non-zero unless they all succeeded")
	f.DurationVar(&timeout, "timeout", 30*time.Minute, "give up waiting after this long (with --wait)")
	f.BoolVar(&verbose, "verbose", false, "list every field of new services and the live members the file does not mention")
	return cmd
}

// waitDeployments waits for deployments an apply started. Progress goes to
// stderr so --json keeps stdout to the apply result.
func (c *cli) waitDeployments(parent context.Context, ids []string, timeout time.Duration) error {
	out, asJSON := c.out, c.asJSON
	c.out, c.asJSON = c.errOut, false
	defer func() { c.out, c.asJSON = out, asJSON }()
	var first error
	for _, id := range ids {
		if err := c.waitAndReport(parent, id, "", timeout); err != nil {
			if first == nil {
				first = err
			}
			if parent.Err() != nil {
				break
			}
		}
	}
	return first
}

// ---- export -----------------------------------------------------------------

func newConfigExportCmd(c *cli) *cobra.Command {
	var fl configFlags
	var env, base, out string
	var all, defaults bool
	cmd := &cobra.Command{
		Use: "export", Short: "Write the live state of a project as a nebula.toml", Args: noArgs,
		Long: "Reads the live project and prints a nebula.toml that plans to no changes against it. Only keys that differ from their defaults or that a file already owns are written; secrets are written as names only. The output is deterministic, so diffs stay small.\n" +
			"With --all every standard environment is exported and factored into base definitions plus [environments.<slug>] overrides. Things the format cannot express are listed in a trailing comment and as warnings on stderr.",
		Example: "nebulactrl config export --project acme-shop --env production -o nebula.toml\nnebulactrl config export --project acme-shop --all",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all && env != "" {
				return usagef("use either --env or --all")
			}
			if base != "" && !all {
				return usagef("--base only applies with --all")
			}
			fileProject := ""
			if fl.project == "" {
				fileProject = localFileProject()
			}
			return c.runExport(cmd.Context(), fl.project, fileProject, env, base, out, all, defaults)
		},
	}
	fl.addProject(cmd)
	f := cmd.Flags()
	f.StringVar(&env, "env", "", "environment ID, slug or name to export")
	f.BoolVar(&all, "all", false, "export every standard environment, factored into base definitions and [environments.<slug>] overrides")
	f.StringVar(&base, "base", "", "with --all: the environment slug the base definitions come from (default: the production environment, else the first)")
	f.BoolVar(&defaults, "defaults", false, "also write keys that equal their default")
	f.StringVarP(&out, "out", "o", "", "write to this file instead of stdout")
	return cmd
}

// runExport fetches the export of a project and writes it to out ("" = stdout).
func (c *cli) runExport(parent context.Context, projectFlag, fileProject, env, base, out string, all, defaults bool) error {
	if err := c.connect(); err != nil {
		return err
	}
	ctx, cancel := c.ctx(parent)
	defer cancel()
	p, err := c.configProject(ctx, projectFlag, fileProject, false)
	if err != nil {
		return err
	}
	params := &apiclient.ExportProjectConfigParams{}
	switch {
	case all:
		a := "all"
		params.Environment = &a
		if base != "" {
			params.Base = &base
		}
	case env != "":
		e, err := c.resolveEnv(ctx, p.Id, env)
		if err != nil {
			return err
		}
		params.Environment = &e.Slug
	}
	if defaults {
		params.Defaults = &defaults
	}
	wantJSON := func(_ context.Context, req *http.Request) error {
		req.Header.Set("Accept", "application/json")
		return nil
	}
	r, err := c.api.ExportProjectConfigWithResponse(ctx, p.Id, params, wantJSON)
	if err := configCheck(r, err); err != nil {
		return err
	}
	body := r.GetBody()
	isJSON := strings.Contains(r.HTTPResponse.Header.Get("Content-Type"), "json")
	var res cfgExportResult
	if isJSON {
		if err := json.Unmarshal(body, &res); err != nil {
			return fmt.Errorf("decode the export: %w", err)
		}
	} else {
		res.File = string(body)
	}
	file := res.File
	if file != "" && !strings.HasSuffix(file, "\n") {
		file += "\n"
	}
	if out != "" {
		if err := os.WriteFile(out, []byte(file), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
		c.note("Wrote %s", out)
	}
	if c.asJSON {
		if isJSON {
			return c.printRawJSON(body)
		}
		return c.printJSON(res)
	}
	if out == "" {
		if _, err := io.WriteString(c.out, file); err != nil {
			return err
		}
	}
	writeDiagnostics(c.errOut, configFileName, "", res.Warnings)
	return nil
}

// ---- schema -----------------------------------------------------------------

func newConfigSchemaCmd(c *cli) *cobra.Command {
	var remote bool
	cmd := &cobra.Command{
		Use: "schema", Short: "Print the JSON Schema of nebula.toml", Args: noArgs,
		Long: "Prints the JSON Schema this CLI was built with. With --remote it fetches /schema/nebula.toml.json from the control plane you are logged in to, which may be newer. Point an editor at it with a `#:schema <url>` first line.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			schema := projectconfig.Schema()
			if remote {
				var err error
				if schema, err = c.fetchRemoteSchema(cmd.Context()); err != nil {
					return err
				}
			}
			if _, err := c.out.Write(schema); err != nil {
				return err
			}
			if !bytes.HasSuffix(schema, []byte("\n")) {
				_, err := io.WriteString(c.out, "\n")
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&remote, "remote", false, "fetch the schema from the control plane instead of printing the embedded one")
	return cmd
}

// controlPlaneOrigin is the control plane URL without the /api/v1 suffix.
func controlPlaneOrigin(url string) string {
	return strings.TrimSuffix(apiBase(url), "/api/v1")
}

func (c *cli) fetchRemoteSchema(parent context.Context) ([]byte, error) {
	cfg := c.cfg
	if cfg.URL == "" {
		var err error
		if cfg, err = loadConfig(); err != nil {
			return nil, err
		}
	}
	if cfg.URL == "" {
		return nil, errors.New("not logged in: run `nebulactrl login --url <url> --token <token>`")
	}
	ctx, cancel := c.ctx(parent)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, controlPlaneOrigin(cfg.URL)+"/schema/nebula.toml.json", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "nebulactrl-cli/"+version)
	res, err := (&http.Client{Timeout: requestTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch the schema: HTTP %d", res.StatusCode)
	}
	return body, nil
}

// ---- source -----------------------------------------------------------------

func newConfigSourceCmd(c *cli) *cobra.Command {
	cmd := group("source", "Show or change where an environment reads its nebula.toml",
		newConfigSourceShowCmd(c), newConfigSourceSetCmd(c), newConfigSourceClearCmd(c), newConfigSourceCheckCmd(c))
	cmd.Long = "An environment can be bound to a repository file (connection, repository, branch, path). On every push the control plane reads the file at the pushed commit and flags services whose settings differ from it; with auto-apply on, a non-production environment stages the file by itself. Production never auto-applies. Auto-apply is a control-plane setting, not a key of the file."
	return cmd
}

// sourceTarget resolves --project and --env of a source command. Without
// --env the project must have exactly one standard environment.
func (c *cli) sourceTarget(cmd *cobra.Command, projectFlag, envFlag string) (apiclient.Project, apiclient.Environment, error) {
	if err := c.connect(); err != nil {
		return apiclient.Project{}, apiclient.Environment{}, err
	}
	fileProject := ""
	if projectFlag == "" {
		fileProject = localFileProject()
	}
	ctx, cancel := c.ctx(cmd.Context())
	defer cancel()
	p, err := c.configProject(ctx, projectFlag, fileProject, false)
	if err != nil {
		return p, apiclient.Environment{}, err
	}
	if envFlag != "" {
		e, err := c.resolveEnv(ctx, p.Id, envFlag)
		return p, e, err
	}
	all, err := c.environments(ctx, p.Id)
	if err != nil {
		return p, apiclient.Environment{}, err
	}
	var std []apiclient.Environment
	for _, e := range all {
		if e.Kind == apiclient.EnvironmentKindStandard {
			std = append(std, e)
		}
	}
	if len(std) != 1 {
		return p, apiclient.Environment{}, usagef("pass --env: %s has %d standard environments", p.Slug, len(std))
	}
	return p, std[0], nil
}

// localFileProject is the `project` key of the nebula.toml found from the
// working directory, "" when there is none or it has no such key.
func localFileProject() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	p, err := findConfigFile(wd)
	if err != nil {
		return ""
	}
	text, err := readLimited(p, 2*projectconfig.MaxFileBytes)
	if err != nil {
		return ""
	}
	var root map[string]any
	if toml.Unmarshal(text, &root) != nil {
		return ""
	}
	project, _ := root["project"].(string)
	return project
}

func addSourceTargetFlags(cmd *cobra.Command, project, env *string) {
	cmd.Flags().StringVar(project, "project", "", "project ID, slug or name (default: the `project` key of the nebula.toml found from here)")
	cmd.Flags().StringVar(env, "env", "", "environment ID, slug or name (optional when the project has one standard environment)")
}

// emitSource prints an environment source: JSON as received, else text.
func (c *cli) emitSource(raw []byte, p apiclient.Project, e apiclient.Environment) error {
	if c.asJSON {
		return c.printRawJSON(raw)
	}
	var s cfgEnvSource
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("decode the config source: %w", err)
	}
	c.writeEnvSource(p, e, &s)
	return nil
}

func newConfigSourceShowCmd(c *cli) *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use: "show", Short: "Show the file binding and sync state of an environment", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, e, err := c.sourceTarget(cmd, project, env)
			if err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.GetEnvironmentConfigSourceWithResponse(ctx, e.Id)
			if err := configCheck(r, err); err != nil {
				return err
			}
			return c.emitSource(r.GetBody(), p, e)
		},
	}
	addSourceTargetFlags(cmd, &project, &env)
	return cmd
}

func newConfigSourceSetCmd(c *cli) *cobra.Command {
	var project, env, repo, branch, path, connection string
	var autoApply, noAutoApply bool
	cmd := &cobra.Command{
		Use: "set", Short: "Bind an environment to a nebula.toml in a repository", Args: noArgs,
		Long: "Only the flags you pass change; the rest of the binding stays. --auto-apply needs the admin scope and is refused for production environments.",
		Example: "nebulactrl config source set --env staging --repo acme/shop --branch develop\n" +
			"nebulactrl config source set --env staging --connection github-acme --path deploy/nebula.toml --auto-apply",
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := cmd.Flags()
			if autoApply && noAutoApply {
				return usagef("use either --auto-apply or --no-auto-apply")
			}
			body := map[string]any{}
			if f.Changed("repo") {
				body["repo"] = repo
			}
			if f.Changed("branch") {
				body["branch"] = branch
			}
			if f.Changed("path") {
				body["path"] = path
			}
			if autoApply || noAutoApply {
				body["autoApply"] = autoApply
			}
			if len(body) == 0 && !f.Changed("connection") {
				return usagef("nothing to set: pass --repo, --branch, --path, --connection, --auto-apply or --no-auto-apply")
			}
			p, e, err := c.sourceTarget(cmd, project, env)
			if err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			if connection != "" {
				id, err := c.resolveGitConnection(ctx, connection)
				if err != nil {
					return err
				}
				body["connectionId"] = id
			}
			raw, _ := json.Marshal(body)
			r, err := c.api.UpdateEnvironmentConfigSourceWithBodyWithResponse(ctx, e.Id, "application/json", bytes.NewReader(raw))
			if err := configCheck(r, err); err != nil {
				return err
			}
			if len(bytes.TrimSpace(r.GetBody())) == 0 {
				c.info("Updated the config source of %s/%s", p.Slug, e.Slug)
				return nil
			}
			return c.emitSource(r.GetBody(), p, e)
		},
	}
	addSourceTargetFlags(cmd, &project, &env)
	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "repository as owner/name")
	f.StringVar(&branch, "branch", "", "branch the file is read from")
	f.StringVar(&path, "path", "", "path of the file in the repository (default: nebula.toml)")
	f.StringVar(&connection, "connection", "", "git connection ID or name that can read the repository")
	f.BoolVar(&autoApply, "auto-apply", false, "stage the file on every push (not for production; needs the admin scope)")
	f.BoolVar(&noAutoApply, "no-auto-apply", false, "switch auto-apply off")
	return cmd
}

func newConfigSourceClearCmd(c *cli) *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use: "clear", Short: "Remove the binding of an environment", Args: noArgs,
		Long: "Clears the explicit binding and switches auto-apply off. What the file already owns stays owned.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, e, err := c.sourceTarget(cmd, project, env)
			if err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.DeleteEnvironmentConfigSourceWithResponse(ctx, e.Id)
			if err := configCheck(r, err); err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(map[string]any{"cleared": true, "project": p.Slug, "environment": e.Slug})
			}
			c.info("Cleared the config source of %s/%s", p.Slug, e.Slug)
			return nil
		},
	}
	addSourceTargetFlags(cmd, &project, &env)
	return cmd
}

func newConfigSourceCheckCmd(c *cli) *cobra.Command {
	var project, env, sha string
	cmd := &cobra.Command{
		Use: "check", Short: "Read the bound file now and compare it with the environment", Args: noArgs,
		Long: "Reads the file at the branch head (or at --sha) and updates the sync state. Exit 1 when the file is invalid, not found, unreachable or could not be read; a file that differs is reported, not an error.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, e, err := c.sourceTarget(cmd, project, env)
			if err != nil {
				return err
			}
			body := map[string]any{}
			if sha != "" {
				body["sha"] = sha
			}
			raw, _ := json.Marshal(body)
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.CheckEnvironmentConfigSourceWithBodyWithResponse(ctx, e.Id, "application/json", bytes.NewReader(raw))
			if err := configCheck(r, err); err != nil {
				return err
			}
			if err := c.emitSource(r.GetBody(), p, e); err != nil {
				return err
			}
			var s cfgEnvSource
			_ = json.Unmarshal(r.GetBody(), &s)
			switch s.Status {
			case "invalid", "not_found", "unreachable", "error":
				return &exitError{code: 1}
			}
			return nil
		},
	}
	addSourceTargetFlags(cmd, &project, &env)
	cmd.Flags().StringVar(&sha, "sha", "", "read the file at this commit instead of the branch head")
	return cmd
}

// writeEnvSource prints the binding and sync state of an environment.
func (c *cli) writeEnvSource(p apiclient.Project, e apiclient.Environment, s *cfgEnvSource) {
	w := c.out
	fmt.Fprintf(w, "Config source of %s / %s\n", cleanText(p.Slug), cleanText(e.Slug))
	switch s.Mode {
	case "none", "":
		fmt.Fprintln(w, "  Binding        none (no repository file is bound; bind one with `nebulactrl config source set`)")
	default:
		binding := s.Mode + ": " + orDash(cleanText(s.Repo))
		if s.Branch != "" {
			binding += "@" + cleanText(s.Branch)
		}
		binding += ", path " + orDash(cleanText(s.Path))
		fmt.Fprintf(w, "  Binding        %s\n", binding)
		if s.ResolvedPath != "" && s.ResolvedPath != s.Path {
			fmt.Fprintf(w, "  Resolved path  %s\n", cleanText(s.ResolvedPath))
		}
	}
	fmt.Fprintf(w, "  Auto-apply     %s\n", onOff(s.AutoApply))
	status := orDash(cleanText(s.Status))
	if s.Commit != "" {
		status += " @ " + cleanText(shortSHA(s.Commit))
	}
	if s.CheckedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, s.CheckedAt); err == nil {
			status += " (checked " + ts(t) + ")"
		}
	}
	fmt.Fprintf(w, "  Status         %s\n", status)
	if s.StatusDetail != "" {
		fmt.Fprintf(w, "  Detail         %s\n", cleanText(s.StatusDetail))
	}
	if s.FileSHA256 != "" {
		fmt.Fprintf(w, "  File SHA-256   %s\n", cleanText(s.FileSHA256))
	}
	if len(s.Services) > 0 {
		fmt.Fprintln(w)
		rows := make([][]string, len(s.Services))
		for i, sv := range s.Services {
			rows[i] = []string{cleanText(sv.Slug), cleanText(sv.State), fmt.Sprint(sv.Changes)}
		}
		c.table([]string{"SERVICE", "STATE", "CHANGES"}, rows)
	}
}
