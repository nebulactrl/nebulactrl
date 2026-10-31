package coolify

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

var sharedRefRe = regexp.MustCompile(`\{\{\s*(team|project|environment)\.[^}]+\}\}`)

// envItem is one variable on its way to the plan.
type envItem struct {
	key     string
	val     string
	hasVal  bool
	env     Env
	choice  CoolifyEnvChoice
	skipWhy string
	note    string
}

// buildEnv turns the variables of an application into PlanEnv entries: it skips
// what NebulaCtrl provides itself, rewrites references to other Coolify
// resources, and decides which variables are secrets.
func (a *analyzer) buildEnv(r *CoolifyPlanResource, c *refCtx) {
	app := r.app
	items := make([]envItem, 0, len(app.Envs))
	index := map[string]int{}
	previews := 0
	for _, e := range app.Envs {
		if e.Preview {
			previews++
			continue
		}
		it := envItem{key: e.Key, env: e, choice: r.choice.Env[e.Key]}
		it.val, it.hasVal = e.Value, e.HasValue
		if e.Shared || sharedRefRe.MatchString(e.Value) {
			switch {
			case e.RealValue != "" && e.RealValue != e.Value:
				it.val, it.hasVal = e.RealValue, true
				it.note = "A Coolify shared variable was resolved to its value (NebulaCtrl has shared variables per project and environment: move it there if several services use it)."
			case sharedRefRe.MatchString(e.Value):
				it.note = "The value refers to a Coolify shared variable that could not be resolved; check it."
			}
		}
		if i, dup := index[e.Key]; dup {
			items[i] = it
			continue
		}
		index[e.Key] = len(items)
		items = append(items, it)
	}
	if previews > 0 {
		r.addNote("preview variables", SevInfo, fmt.Sprintf("%d variable(s) are only for Coolify preview deployments and were not imported.", previews))
	}

	// Pass 1: which databases does this application use?
	for _, it := range items {
		v := valueOf2(it)
		hits, _ := c.scan(it.key, v, r, true)
		for _, h := range hits {
			if h.target.isDB() && (h.kind == "host" || h.kind == "password" || h.kind == "url") {
				c.linked[h.target] = true
			}
		}
	}

	depends := map[string]bool{}
	buildVars := 0
	for _, it := range items {
		pe := CoolifyPlanEnv{Key: it.key, Scope: "runtime", Note: it.note}
		finish := func() { r.Env = append(r.Env, pe) }
		skip := func(why string) {
			pe.Skipped, pe.Note = true, why
			finish()
		}
		switch {
		case it.choice.Skip != nil && *it.choice.Skip:
			skip("Left out in the import.")
			continue
		case !varexprKey(it.key):
			skip("The name is not a valid variable name here.")
			continue
		case services.IsPlatformKey(it.key):
			skip(it.key + " is provided by the platform (PORT and NEBULA_* are reserved). The service port is set in the service settings.")
			continue
		case coolifyInjected(it.key):
			skip("Injected by Coolify, not part of the application's configuration.")
			continue
		}
		val, has := it.val, it.hasVal
		if it.choice.Value != nil {
			val, has = *it.choice.Value, true
		}
		a.envTotal++
		if !has || (it.env.ShownOnce && val == "" && it.choice.Value == nil) {
			if !it.env.ShownOnce {
				a.envUnreadable++
			}
			pe.NeedsValue, pe.Secret, pe.SecretReason = true, true, SecretLocked
			skip("Coolify does not return the value of a locked variable: supply it in the import or set it on the service.")
			r.secretValues = append(r.secretValues, it.key)
			continue
		}

		// scope
		switch {
		case it.env.Buildtime && it.env.Runtime:
			pe.Scope = services.ScopeBoth
		case it.env.Buildtime:
			pe.Scope = services.ScopeBuild
		}
		if pe.Scope != services.ScopeRuntime && r.Source.Type != "git" {
			pe.Scope = services.ScopeRuntime
			if it.env.Buildtime && !it.env.Runtime {
				pe.Note = joinNote(pe.Note, "Build-time only in Coolify; image services have no build, so it is a runtime variable.")
			}
		}
		if pe.Scope != services.ScopeRuntime {
			buildVars++
		}

		// references
		rewriteOn := it.choice.Rewrite == nil || *it.choice.Rewrite
		var rendered string
		var hits []hit
		if rewriteOn {
			var notes []string
			hits, notes = c.scan(it.key, val, r, false)
			for _, n := range notes {
				pe.Note = joinNote(pe.Note, n)
			}
		}
		rendered = apply(val, hits).render()
		if _, err := validTemplate(rendered); err != nil {
			rendered, hits = segs{{lit: val}}.render(), nil
			pe.Note = joinNote(pe.Note, "A reference could not be written for this value; it was kept as it is.")
		}
		secretByRef := false
		for _, h := range hits {
			pe.Rewritten = true
			r.rewrites = append(r.rewrites, CoolifyPlanRewrite{Resource: r.Name, Key: it.key, Kind: h.kind, To: "${{" + h.ref + "}}"})
			depends[h.target.res.Slug] = true
			if h.kind == "password" || h.kind == "url" {
				secretByRef = true
			}
		}
		pe.value = rendered

		// secrecy
		switch {
		case it.choice.Secret != nil:
			pe.Secret, pe.SecretReason = *it.choice.Secret, SecretChoice
		case it.env.ShownOnce:
			pe.Secret, pe.SecretReason = true, SecretLocked
		case isSecretKey(it.key):
			pe.Secret, pe.SecretReason = true, SecretName
		case valueHasCredentials(val):
			pe.Secret, pe.SecretReason = true, SecretValue
		case secretByRef:
			pe.Secret, pe.SecretReason = true, SecretReference
		}
		if !pe.Secret {
			pe.SecretReason = ""
		}
		finish()
	}
	for slug := range depends {
		r.DependsOn = append(r.DependsOn, slug)
	}
	sort.Strings(r.DependsOn)
	if r.Source.Type == "git" && buildVars > 0 {
		r.addNote("build variables", SevInfo, fmt.Sprintf("%d variable(s) are available at build time, as in Coolify. Secret ones reach the build as BuildKit secrets (RUN --mount=type=secret) and need Docker 23+ on the server.", buildVars))
	}
	if len(r.secretValues) > 0 {
		r.addNote("locked variables", SevWarning, fmt.Sprintf("Coolify does not return the value of %d locked variable(s) (%s): supply them in the import, or set them on the service before deploying.", len(r.secretValues), strings.Join(r.secretValues, ", ")))
	}

	// Build settings that Railpack takes as variables.
	if g := r.Source.Git; g != nil && g.BuildType == agentprotocol.BuildRailpack {
		have := map[string]bool{}
		for _, e := range r.Env {
			have[e.Key] = true
		}
		add := func(key, val, why string) {
			if strings.TrimSpace(val) == "" || have[key] {
				return
			}
			pe := CoolifyPlanEnv{Key: key, Scope: services.ScopeBuild, Note: why, value: segs{{lit: val}}.render()}
			r.Env = append(r.Env, pe)
		}
		add("RAILPACK_INSTALL_CMD", r.app.InstallCommand, "From Coolify's install command.")
		add("RAILPACK_BUILD_CMD", r.app.BuildCommand, "From Coolify's build command.")
		add("RAILPACK_START_CMD", r.app.StartCommand, "From Coolify's start command.")
		if r.app.BuildPack == "static" {
			add("RAILPACK_SPA_OUTPUT_DIR", strings.TrimPrefix(r.app.PublishDirectory, "/"), "From Coolify's publish directory; check the build preview.")
		}
	}
}

func valueOf2(it envItem) string {
	if it.choice.Value != nil {
		return *it.choice.Value
	}
	return it.val
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + " " + b
}

func varexprKey(k string) bool { return varexpr.IsKey(k) && len(k) <= 255 }

func validTemplate(v string) (*varexpr.Template, error) {
	t, err := varexpr.Parse(v)
	if err != nil {
		return nil, err
	}
	if _, err := varexpr.Validate(t); err != nil {
		return nil, err
	}
	return t, nil
}
