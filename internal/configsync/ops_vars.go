package configsync

import (
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/projectconfig"
	"github.com/nebulactrl/nebulactrl/internal/varexpr"
)

// varOps handles variables.<KEY> (a value with its metadata) and
// variables.<KEY>#secret (the declaration that a name is secret).
type varOps struct{}

const secretSuffix = "#secret"

// varKey returns the variable name of a field id and whether it is the secret declaration.
func varKey(id string) (key string, isSecretDecl bool) {
	k := strings.TrimPrefix(id, "variables.")
	if strings.HasSuffix(k, secretSuffix) {
		return strings.TrimSuffix(k, secretSuffix), true
	}
	return k, false
}

func normScope(s string) string {
	if s == "" {
		return "runtime"
	}
	return s
}

// templateSecret reports whether the text contains a secret-class generator
// (secret, randomBytes): the platform stores such a variable as a secret.
func templateSecret(t *varexpr.Template) bool {
	for _, p := range t.Parts {
		if p.Expr != nil && p.Expr.Call != nil && (p.Expr.Call.Fn == varexpr.FnSecret || p.Expr.Call.Fn == varexpr.FnRandomBytes) {
			return true
		}
	}
	return false
}

// describeTemplate is the plan text of a variable template: the text itself,
// or the generator descriptors (never the literal text around a generator).
func describeTemplate(text string) (display string, masked bool) {
	t, err := varexpr.Parse(text)
	if err != nil || !t.HasGenerators() {
		return text, false
	}
	var calls []string
	for _, e := range t.Exprs() {
		if e.Call != nil && varexpr.IsGenerator(e.Call.Fn) {
			calls = append(calls, varexpr.Describe(e.Call).String())
		}
	}
	return "generated(" + strings.Join(calls, ", ") + ")", true
}

// varFP is the fingerprint of a variable as the file can express it:
// the template text, the secret flag and the scope, plus the metadata the
// file declares (parts).
func varFP(text string, secret bool, scope string, comment string, rotate *int, parts string) string {
	m := map[string]any{"t": text, "secret": secret, "scope": normScope(scope)}
	if strings.Contains(parts, "comment") {
		m["comment"] = comment
	}
	if strings.Contains(parts, "rotate") {
		if rotate != nil {
			m["rotate"] = *rotate
		} else {
			m["rotate"] = -1
		}
	}
	return projectconfig.Fingerprint(m)
}

func (varOps) live(pc *planCtx, ls *liveService, id, proj string) (fieldView, bool) {
	key, isDecl := varKey(id)
	lv, ok := ls.Vars[key]
	if !ok {
		return fieldView{}, false
	}
	if isDecl {
		return fieldView{FP: projectconfig.Fingerprint(map[string]any{"secret": lv.Secret, "scope": normScope(lv.Scope)}), Display: secretText(lv.Secret)}, true
	}
	return liveVarView(lv, proj), true
}

func secretText(secret bool) string {
	if secret {
		return "secret"
	}
	return "not secret"
}

func liveVarView(lv liveVar, proj string) fieldView {
	fp := varFP(lv.Template, lv.Secret, lv.Scope, lv.Comment, lv.Rotate, proj)
	switch {
	case lv.Opaque:
		return fieldView{FP: fp, Display: Mask, Masked: true, Proj: proj}
	case lv.Secret:
		d, masked := describeTemplate(lv.Template)
		return fieldView{FP: fp, Display: d, Masked: masked || lv.Template == "", Proj: proj}
	}
	d, masked := describeTemplate(lv.Template)
	return fieldView{FP: fp, Display: d, Masked: masked, Proj: proj}
}

func (varOps) desired(pc *planCtx, ls *liveService, fv projectconfig.FieldValue) (fieldView, error) {
	key, isDecl := varKey(fv.ID)
	if isDecl {
		d, _ := fv.Value.(projectconfig.SecretDecl)
		return fieldView{FP: projectconfig.Fingerprint(map[string]any{"secret": true, "scope": normScope(d.Scope)}), Display: "secret"}, nil
	}
	v, ok := fv.Value.(projectconfig.Variable)
	if !ok {
		return fieldView{}, refErr("wrong_type", "variable %s has an unexpected value", key)
	}
	tpl, err := varexpr.Parse(v.Value)
	if err != nil {
		return fieldView{}, refErr("invalid_template", "variable %s: %v", key, err)
	}
	// The flag follows the template (a generator of secret class forces it), the
	// secret declaration (written separately as variables.<KEY>#secret) and the
	// stored flag: writing a value never turns a stored secret into a plain variable.
	secret := templateSecret(tpl)
	if ls != nil {
		if lv, ok := ls.Vars[key]; ok {
			secret = secret || lv.Secret
		} else if pc.rs != nil {
			_, declared := pc.rs.Fields["variables."+key+secretSuffix]
			secret = secret || declared
		}
	} else if pc.rs != nil {
		_, declared := pc.rs.Fields["variables."+key+secretSuffix]
		secret = secret || declared
	}
	scope := normScope(v.Scope)
	var parts []string
	comment := v.Comment
	if v.Comment != "" {
		parts = append(parts, "comment")
	}
	rotate := v.RotateEveryDays
	if rotate != nil {
		parts = append(parts, "rotate")
	}
	proj := strings.Join(parts, ",")
	display, masked := describeTemplate(v.Value)
	return fieldView{FP: varFP(v.Value, secret, scope, comment, rotate, proj), Display: display, Masked: masked || secret && tpl.HasGenerators(), Proj: proj}, nil
}
