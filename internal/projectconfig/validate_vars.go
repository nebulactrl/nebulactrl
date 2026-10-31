package projectconfig

import (
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// varRef is a reference from a variable of one service to another service.
type varRef struct {
	name string // the referencing variable
	slug string // the referenced service
	path string // file key path of the referencing variable
}

// hasOnlyReferences reports whether a value consists only of references,
// generators and transforms (no literal text): such a value is secret by
// derivation, so listing the name in secrets.keys is not a conflict.
func hasOnlyReferences(value string) bool {
	t, err := varexpr.Parse(value)
	if err != nil || len(t.Exprs()) == 0 {
		return false
	}
	for _, p := range t.Parts {
		if p.Expr == nil && strings.TrimSpace(p.Text) != "" {
			return false
		}
	}
	return true
}

func reservedVariable(name string) bool {
	return name == "PORT" || strings.HasPrefix(name, "NEBULA_")
}

// checkServiceVars validates the service's variables and secrets.
func (c *svcCtx) checkServiceVars() {
	c.v.checkVars(c.def.Variables, c.abs, "variables", c.def.Secrets, "secrets", c)
	c.checkVarCycles()
}

// checkVars validates one layer of variables and the secret names declared
// next to it. abs maps a path relative to the owner (a service, the root or an
// environment block) to a file key path; c is the service, nil for shared layers.
func (v *view) checkVars(vars map[string]Variable, abs func(rel ...string) string, varsKey string, secrets *Secrets, secretsKey string, c *svcCtx) {
	s := v.s
	if len(vars) > MaxVariablesPerLayer {
		s.errorf(CodeLimitExceeded, abs(varsKey), "%d variables; at most %d per layer", len(vars), MaxVariablesPerLayer)
	}
	secretSet := map[string]bool{}
	if secrets != nil {
		if len(secrets.Keys) > MaxSecretKeys {
			s.errorf(CodeLimitExceeded, abs(secretsKey, "keys"), "%d secret names; at most %d per layer", len(secrets.Keys), MaxSecretKeys)
		}
		for i, k := range secrets.Keys {
			kp := abs(secretsKey, "keys", "["+itoa(i)+"]")
			switch {
			case !varNameRe.MatchString(k):
				s.errorf(CodeInvalidValue, kp, "%q is not a valid variable name (letters, digits and underscores, not starting with a digit)", k)
			case c != nil && reservedVariable(k):
				s.errorf(CodeReservedVariable, kp, "%q is reserved by the platform and cannot be declared", k)
			case secretSet[k]:
				s.errorf(CodeInvalidValue, kp, "secret %q is listed twice", k)
			}
			secretSet[k] = true
		}
		if secrets.Scope != nil {
			switch *secrets.Scope {
			case "runtime", "build", "both":
				if c != nil && *secrets.Scope != "runtime" && c.kind != KindGit {
					s.errorf(CodeInvalidValue, abs(secretsKey, "scope"), "scope %q needs a git service (builds only exist for git sources)", *secrets.Scope)
				}
			default:
				s.errorf(CodeInvalidValue, abs(secretsKey, "scope"), "scope must be runtime, build or both")
			}
		}
	}
	names := sortedKeys(vars)
	for _, name := range names {
		val := vars[name]
		np := abs(varsKey, name)
		vp := abs(varsKey, name, "value")
		if !varNameRe.MatchString(name) {
			s.errorf(CodeInvalidValue, np, "%q is not a valid variable name: letters, digits and underscores, not starting with a digit, at most 255 characters", name)
			continue
		}
		if c != nil && reservedVariable(name) {
			g := s.errorf(CodeReservedVariable, np, "%q is reserved by the platform (PORT and NEBULA_*) and cannot be set", name)
			g.Hint = "remove it; the platform provides it"
		}
		switch val.Scope {
		case "", "runtime":
		case "build", "both":
			if c != nil && c.kind != KindGit {
				s.errorf(CodeInvalidValue, abs(varsKey, name, "scope"), "scope %q needs a git service (builds only exist for git sources)", val.Scope)
			}
		default:
			s.errorf(CodeInvalidValue, abs(varsKey, name, "scope"), "scope must be runtime, build or both")
		}
		if len(val.Comment) > 1000 {
			s.errorf(CodeInvalidValue, abs(varsKey, name, "comment"), "comment is longer than 1000 characters")
		}
		if val.RotateEveryDays != nil && *val.RotateEveryDays < 0 {
			s.errorf(CodeInvalidValue, abs(varsKey, name, "rotate_every_days"), "rotate_every_days must be 0 or more")
		}
		tpl, err := varexpr.Parse(val.Value)
		if err == nil {
			_, err = varexpr.Validate(tpl)
		}
		if err != nil {
			msg := err.Error()
			if se, ok := err.(*varexpr.SyntaxError); ok {
				msg = se.Message
			}
			g := s.errorf(CodeInvalidTemplate, vp, "variable %q is not a valid value: %s", name, msg)
			g.Hint = "write a literal $ as $${{ and use ${{KEY}}, ${{service.KEY}}, ${{shared.KEY}} or ${{secret(32)}}"
			continue
		}
		hasExpr := len(tpl.Exprs()) > 0
		if !hasExpr && val.Value != "" && !val.Public && credentialName(name) && !secretSet[name] {
			g := s.errorf(CodeLiteralSecret, vp, "variable %q looks like a credential but has a literal value (%s); a credential is never written in the file", name, maskLiteral(val.Value))
			g.Hint = "list it in secrets.keys and set the value in the dashboard or CLI, use a generator such as ${{secret(32)}} or a reference, or mark the entry { public = true } if it is not a credential"
		}
		if secretSet[name] && !hasOnlyReferences(val.Value) {
			g := s.errorf(CodeSecretHasValue, np, "%q is declared secret in secrets.keys and also has a value in variables; the file never holds a secret's value", name)
			g.Hint = "remove it from variables, or remove it from secrets.keys"
		}
		v.checkRefs(tpl, name, vp, abs, varsKey, secretSet, vars, c)
	}
}

// checkRefs validates the references of one variable value.
func (v *view) checkRefs(tpl *varexpr.Template, name, vp string, abs func(rel ...string) string, varsKey string, secretSet map[string]bool, vars map[string]Variable, c *svcCtx) {
	s := v.s
	for _, r := range tpl.Refs() {
		switch {
		case r.NS == "":
			if c == nil {
				continue
			}
			if _, ok := vars[r.Key]; !ok && !secretSet[r.Key] && !reservedVariable(r.Key) && !c.declaresSecret(r.Key) {
				s.warnf(CodeUnresolvedReference, vp, "${{%s}} in %q is not defined in this service in the file; it must exist on the service already", r.Key, name)
			}
		case r.NS == "team":
		case r.NS == "shared":
			if _, ok := v.res.EnvShared[r.Key]; ok {
				continue
			}
			if _, ok := v.res.Shared[r.Key]; ok {
				continue
			}
			if containsStr(v.res.EnvSharedSecrets, r.Key) || containsStr(v.res.SharedSecrets, r.Key) {
				continue
			}
			s.warnf(CodeUnresolvedReference, vp, "${{shared.%s}} in %q is not defined in the file's shared variables; it must exist in the environment or project layer", r.Key, name)
		case !varexpr.IsNamespace(r.NS):
			s.errorf(CodeInvalidReference, vp, "%q is not a valid reference namespace in ${{%s}}", r.NS, r.String())
		default:
			if c == nil {
				s.errorf(CodeInvalidReference, vp, "shared variables cannot reference a service: ${{%s}}", r.String())
				continue
			}
			if r.NS == c.slug {
				if _, ok := vars[r.Key]; !ok && !secretSet[r.Key] && !reservedVariable(r.Key) {
					s.warnf(CodeUnresolvedReference, vp, "${{%s}} in %q is not defined in this service in the file", r.String(), name)
				}
				continue
			}
			c.refs = append(c.refs, varRef{name: name, slug: r.NS, path: vp})
		}
	}
}

func (c *svcCtx) declaresSecret(key string) bool {
	return c.def.Secrets != nil && containsStr(c.def.Secrets.Keys, key)
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// checkVarCycles finds references between the variables of one service that
// form a cycle (A = ${{B}}, B = ${{A}}).
func (c *svcCtx) checkVarCycles() {
	vars := c.def.Variables
	edges := map[string][]string{}
	for name, val := range vars {
		tpl, err := varexpr.Parse(val.Value)
		if err != nil {
			continue
		}
		for _, r := range tpl.Refs() {
			if r.NS == "" || r.NS == c.slug {
				if _, ok := vars[r.Key]; ok {
					edges[name] = append(edges[name], r.Key)
				}
			}
		}
	}
	state := map[string]int{}
	var stack []string
	reported := map[string]bool{}
	var dfs func(n string)
	dfs = func(n string) {
		state[n] = 1
		stack = append(stack, n)
		next := append([]string(nil), edges[n]...)
		sort.Strings(next)
		for _, m := range next {
			switch state[m] {
			case 0:
				dfs(m)
			case 1:
				i := len(stack) - 1
				for i >= 0 && stack[i] != m {
					i--
				}
				cyc := append(append([]string(nil), stack[i:]...), m)
				key := strings.Join(cyc, ">")
				if !reported[key] {
					reported[key] = true
					c.err(CodeReferenceCycle, rp("variables", m, "value"), "variables reference each other in a cycle: %s", strings.Join(cyc, " -> "))
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = 2
	}
	for _, n := range sortedKeys(vars) {
		if state[n] == 0 {
			dfs(n)
		}
	}
}

// crossService checks what spans services of one view: dependencies, cycles,
// upstream targets and host collisions.
func (v *view) crossService() {
	s := v.s
	edges := map[string][]string{}
	edgePath := map[string]string{}
	for _, slug := range v.slugs {
		c := v.svcs[slug]
		if c.def == nil {
			continue
		}
		for i, dep := range c.def.DependsOn {
			dp := c.abs("depends_on", "["+itoa(i)+"]")
			switch {
			case dep == slug:
				s.errorf(CodeReferenceCycle, dp, "service %q depends on itself", slug)
			case v.svcs[dep] == nil:
				s.warnf(CodeUnresolvedReference, dp, "service %q is not in the file for this environment; it must exist already", dep)
			default:
				edges[slug] = append(edges[slug], dep)
				if _, ok := edgePath[slug+">"+dep]; !ok {
					edgePath[slug+">"+dep] = dp
				}
			}
		}
		for _, r := range c.refs {
			switch {
			case v.svcs[r.slug] == nil:
				s.warnf(CodeUnresolvedReference, r.path, "${{%s.…}} in %q refers to service %q, which is not in the file for this environment; it must exist already", r.slug, r.name, r.slug)
			default:
				edges[slug] = append(edges[slug], r.slug)
				if _, ok := edgePath[slug+">"+r.slug]; !ok {
					edgePath[slug+">"+r.slug] = r.path
				}
			}
		}
	}
	state := map[string]int{}
	var stack []string
	reported := map[string]bool{}
	var dfs func(n string)
	dfs = func(n string) {
		state[n] = 1
		stack = append(stack, n)
		next := append([]string(nil), edges[n]...)
		sort.Strings(next)
		for _, m := range next {
			switch state[m] {
			case 0:
				dfs(m)
			case 1:
				i := len(stack) - 1
				for i >= 0 && stack[i] != m {
					i--
				}
				cyc := append(append([]string(nil), stack[i:]...), m)
				key := strings.Join(cyc, ">")
				if !reported[key] {
					reported[key] = true
					last := cyc[len(cyc)-2]
					s.errorf(CodeReferenceCycle, edgePath[last+">"+m], "services depend on each other in a cycle: %s", strings.Join(cyc, " -> "))
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = 2
	}
	for _, slug := range v.slugs {
		if state[slug] == 0 {
			dfs(slug)
		}
	}
	v.checkUpstreamTargets()
	v.checkHostCollisions()
}

// checkUpstreamTargets validates route upstreams that name services.
func (v *view) checkUpstreamTargets() {
	for _, slug := range v.slugs {
		c := v.svcs[slug]
		if c.def == nil || c.def.Route == nil {
			continue
		}
		for i, u := range c.def.Route.Upstreams {
			if u.Service == nil {
				continue
			}
			tp := c.abs("route", "upstreams", "["+itoa(i)+"]", "service")
			t := v.svcs[*u.Service]
			switch {
			case *u.Service == slug:
				v.s.errorf(CodeInvalidUpstream, tp, "a route cannot target itself")
			case t == nil:
				v.s.warnf(CodeUnresolvedReference, tp, "service %q is not in the file for this environment; it must exist on the same server", *u.Service)
			case t.kind == KindRoute || t.kind == KindStack:
				v.s.errorf(CodeInvalidUpstream, tp, "service %q is a %s service; an upstream must be an image, git or database service", *u.Service, t.kind)
			case t.rs.ServerName != "" && c.rs.ServerName != "" && t.rs.ServerName != c.rs.ServerName:
				v.s.errorf(CodeInvalidUpstream, tp, "service %q runs on server %q, the route on %q; a managed upstream must be on the route's server", *u.Service, t.rs.ServerName, c.rs.ServerName)
			}
		}
	}
}

// checkHostCollisions reports a host/path claimed by two services of the file.
func (v *view) checkHostCollisions() {
	owner := map[string]string{}
	for _, slug := range v.slugs {
		c := v.svcs[slug]
		if c.def == nil {
			continue
		}
		for _, key := range sortedKeys(c.def.Domains) {
			host, pfx, ok := splitDomainKey(key)
			if !ok {
				continue
			}
			k := host + pfx
			if prev, dup := owner[k]; dup && prev != slug {
				v.s.errorf(CodeHostnameInUse, c.abs("domains", key), "%s is already claimed by service %q in this file; a hostname and path belong to one service", k, prev)
				continue
			}
			owner[k] = slug
		}
	}
}
