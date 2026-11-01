package templates

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/apierr"
)

// maxInputLen bounds one install input value.
const maxInputLen = 4096

// Materializer evaluates the generators (${{secret(32)}}, …) of a string once
// and reports whether the result is secret. It is satisfied by the variables
// package.
type Materializer func(template string) (stored string, secret bool, err error)

// ResolvedInputs are the final input values of an install.
type ResolvedInputs struct {
	// Values maps input key → final value (generators evaluated).
	Values map[string]string
	// Secret marks the inputs whose value is a credential.
	Secret map[string]bool
	// Generated marks inputs whose value was generated because the user left
	// the field empty.
	Generated map[string]bool
}

// hasGenerator reports whether v contains a ${{fn(...)}} generator call.
func hasGenerator(v string) bool {
	return strings.Contains(v, "${{") && strings.Contains(v, "(")
}

// ResolveInputs applies defaults and checks the user's values. With a nil
// materialize, generator defaults stay unevaluated (plan mode); Generated
// still reports them.
func ResolveInputs(t *CatalogTemplate, given map[string]string, materialize Materializer) (ResolvedInputs, error) {
	out := ResolvedInputs{Values: map[string]string{}, Secret: map[string]bool{}, Generated: map[string]bool{}}
	known := map[string]TemplateInput{}
	for _, in := range t.Inputs {
		known[in.Key] = in
	}
	var unknown []string
	for k := range given {
		if _, ok := known[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return out, apierr.Unprocessable("unknown_input", "unknown input(s): "+strings.Join(unknown, ", "))
	}
	var missing []string
	for _, in := range t.Inputs {
		v, supplied := given[in.Key]
		v = strings.TrimSpace(v)
		if len(v) > maxInputLen {
			return out, apierr.Unprocessable("invalid_input", fmt.Sprintf("input %s is too long", in.Key))
		}
		if strings.ContainsRune(v, 0) {
			return out, apierr.Unprocessable("invalid_input", fmt.Sprintf("input %s contains a NUL byte", in.Key))
		}
		if !supplied || v == "" {
			v = in.Default
			if hasGenerator(v) {
				out.Generated[in.Key] = true
				if materialize != nil {
					stored, secret, err := materialize(v)
					if err != nil {
						return out, apierr.Unprocessable("invalid_input", fmt.Sprintf("input %s: %v", in.Key, err))
					}
					v = stored
					if secret {
						out.Secret[in.Key] = true
					}
				}
			}
		}
		if in.Secret {
			out.Secret[in.Key] = true
		}
		if msg := CheckInputFormat(in.Format, v); msg != "" && !out.Generated[in.Key] {
			return out, apierr.Unprocessable("invalid_input", fmt.Sprintf("input %s %s", in.Key, msg))
		}
		if v == "" && in.Required {
			missing = append(missing, in.Key)
		}
		if len(in.Options) > 0 && v != "" && !slices.Contains(in.Options, v) {
			return out, apierr.Unprocessable("invalid_input", fmt.Sprintf("input %s must be one of %s", in.Key, strings.Join(in.Options, ", ")))
		}
		out.Values[in.Key] = v
	}
	if len(missing) > 0 {
		return out, apierr.Unprocessable("missing_input", "required input(s) missing: "+strings.Join(missing, ", "))
	}
	return out, nil
}

// expandInputs replaces ${input:KEY}. With escapeRefs the substituted text has
// its `${{` escaped (`$${{`) so an input can never become a variable
// reference or generator.
func expandInputs(s string, vals map[string]string, escapeRefs bool) string {
	if !strings.Contains(s, "${input:") {
		return s
	}
	return inputRe.ReplaceAllStringFunc(s, func(m string) string {
		key := inputRe.FindStringSubmatch(m)[1]
		v := vals[key]
		if escapeRefs {
			v = strings.ReplaceAll(v, "${{", "$${{")
		}
		return v
	})
}

// usesSecretInput reports whether s references a secret input.
func usesSecretInput(s string, secret map[string]bool) bool {
	for _, m := range inputRe.FindAllStringSubmatch(s, -1) {
		if secret[m[1]] {
			return true
		}
	}
	return false
}

// rewriteRefs turns ${{key.VAR}} into ${{slug.VAR}} using slugs (template
// service key → real slug). Namespaces that are not template keys (shared,
// vault) are left alone.
func rewriteRefs(s string, slugs map[string]string) string {
	if !strings.Contains(s, "${{") {
		return s
	}
	return refRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := refRe.FindStringSubmatch(m)
		slug, ok := slugs[sub[2]]
		if !ok {
			return m
		}
		return sub[1] + "${{" + slug + "." + sub[3] + sub[4] + "}}"
	})
}

// isCredentialKey reports whether an env key looks like a credential.
func isCredentialKey(k string) bool { return secretKeyRe.MatchString(k) }

// envIsSecret decides whether an env variable of s is stored as a secret.
func envIsSecret(s *TemplateService, key, value string, inputSecret map[string]bool) bool {
	return slices.Contains(s.Secrets, key) || isCredentialKey(key) || usesSecretInput(value, inputSecret)
}
