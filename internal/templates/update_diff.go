package templates

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/services"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Statuses of one update change.
const (
	// ChangeApply is staged by the update.
	ChangeApply = "apply"
	// ChangeApplied is already true on the service (a retry after a partial update).
	ChangeApplied = "applied"
	// ChangeKept is left alone: the service differs from the installed template
	// in this field, so the change is the user's, not the template's.
	ChangeKept = "kept"
	// ChangeManual is reported but never done by the update: it would touch data,
	// routes or services, so the user decides.
	ChangeManual = "manual"
)

// TemplateUpdateChange is one difference between the installed template and the
// catalog's current revision.
type TemplateUpdateChange struct {
	// Service is the template service key; "" for a change of the template itself.
	Service     string `json:"service"`
	ServiceName string `json:"serviceName,omitempty"`
	// Slug is the service's slug in the environment.
	Slug string `json:"slug,omitempty"`
	// Field names what changes: image, port, healthcheck.path, memoryMb,
	// env.KEY, file:/path, volume:name, input:KEY, database.version, service.
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
	// Secret hides From and To (they stay empty).
	Secret bool   `json:"secret"`
	Status string `json:"status" enum:"apply,applied,kept,manual"`
	// Reason explains kept and manual changes.
	Reason string `json:"reason,omitempty"`
}

var shaRe = regexp.MustCompile(`@sha256:([0-9a-f]{12})[0-9a-f]{52}`)

// shortRef shortens the digest of an image reference for display.
func shortRef(s string) string { return shaRe.ReplaceAllString(s, "@sha256:$1…") }

// templateKnown is what the diff knows about the install's inputs.
type templateKnown struct {
	// Vals are the input values the install recorded (non-secret, non-generated)
	// plus the defaults of inputs that were added later.
	Vals map[string]string
	// Unknown lists inputs of the installed template whose value is not stored
	// (secret or generated): text that uses them cannot be compared or rewritten.
	Unknown map[string]bool
	// Secret marks inputs that carry credentials.
	Secret map[string]bool
}

// knownInputs derives templateKnown from the stored install inputs.
func knownInputs(old, cur *CatalogTemplate, stored map[string]string) templateKnown {
	k := templateKnown{Vals: map[string]string{}, Unknown: map[string]bool{}, Secret: map[string]bool{}}
	for key, v := range stored {
		k.Vals[key] = v
	}
	oldKeys := map[string]bool{}
	for _, in := range old.Inputs {
		oldKeys[in.Key] = true
		if in.Secret || hasGenerator(in.Default) {
			k.Secret[in.Key] = true
		}
		if _, ok := stored[in.Key]; !ok {
			k.Unknown[in.Key] = true
		}
	}
	for _, in := range cur.Inputs {
		if in.Secret || hasGenerator(in.Default) {
			k.Secret[in.Key] = true
		}
		if oldKeys[in.Key] {
			continue
		}
		if _, ok := k.Vals[in.Key]; !ok && !hasGenerator(in.Default) {
			k.Vals[in.Key] = in.Default
		}
	}
	return k
}

// usesUnknown reports whether s references an input whose value is not stored.
func usesUnknown(s string, unknown map[string]bool) bool {
	for _, m := range inputRe.FindAllStringSubmatch(s, -1) {
		if unknown[m[1]] {
			return true
		}
	}
	return false
}

func serviceByKey(t *CatalogTemplate) map[string]*TemplateService {
	m := make(map[string]*TemplateService, len(t.Services))
	for i := range t.Services {
		m[t.Services[i].Key] = &t.Services[i]
	}
	return m
}

// diffTemplates lists what changed between the installed template and the
// current one. It is pure: every change starts as apply or manual and
// classifyChanges refines the apply ones against the live services.
func diffTemplates(old, cur *CatalogTemplate, k templateKnown) []TemplateUpdateChange {
	var out []TemplateUpdateChange

	oldIn := map[string]TemplateInput{}
	for _, in := range old.Inputs {
		oldIn[in.Key] = in
	}
	curIn := map[string]TemplateInput{}
	for _, in := range cur.Inputs {
		curIn[in.Key] = in
		if _, ok := oldIn[in.Key]; !ok {
			reason := "New input: asked when you stage the update."
			if !in.Required || in.Default != "" {
				reason = "New input: optional, it has a default."
			}
			out = append(out, TemplateUpdateChange{Field: "input:" + in.Key, To: in.Label, Status: ChangeApply, Reason: reason})
		}
	}
	for _, in := range old.Inputs {
		if _, ok := curIn[in.Key]; !ok {
			out = append(out, TemplateUpdateChange{Field: "input:" + in.Key, From: in.Label, Status: ChangeManual, Reason: "The template no longer asks for this input."})
		}
	}

	oldSvc, curSvc := serviceByKey(old), serviceByKey(cur)
	for i := range cur.Services {
		cs := &cur.Services[i]
		os := oldSvc[cs.Key]
		if os == nil {
			out = append(out, TemplateUpdateChange{
				Service: cs.Key, ServiceName: cs.Name, Field: "service", To: cs.Name, Status: ChangeManual,
				Reason: "The new revision adds this service. The update does not create services: install the template again in a new group, or add the service by hand.",
			})
			continue
		}
		out = append(out, diffService(os, cs, k)...)
	}
	for i := range old.Services {
		os := &old.Services[i]
		if curSvc[os.Key] == nil {
			out = append(out, TemplateUpdateChange{
				Service: os.Key, ServiceName: os.Name, Field: "service", From: os.Name, Status: ChangeManual,
				Reason: "The new revision no longer includes this service. It stays as it is; delete it yourself if you don't need it.",
			})
		}
	}
	return out
}

// diffConfig is services.DiffConfig with the Docker options collapsed into one
// "<prefix>.docker" change. The block is one setting, and its maps (sysctls,
// hostSysctls, labels) have dotted keys that the path-based patch cannot address.
func diffConfig(prefix string, from, to services.Config) []services.FieldChange {
	var out []services.FieldChange
	seen := false
	for _, d := range services.DiffConfig(prefix, from, to) {
		if d.Field != prefix+".docker" && !strings.HasPrefix(d.Field, prefix+".docker.") {
			out = append(out, d)
			continue
		}
		if !seen {
			seen = true
			out = append(out, services.FieldChange{Field: prefix + ".docker", From: dockerText(from.Docker), To: dockerText(to.Docker)})
		}
	}
	return out
}

// dockerText renders a Docker option block as compact JSON ("" when empty).
func dockerText(o *agentprotocol.DockerOptions) string {
	if o == nil || o.IsZero() {
		return ""
	}
	b, _ := json.Marshal(o)
	return string(b)
}

// configLeaf is the config diff of one service: the fields whose value differs
// between the two revisions, excluding files (handled per path).
func configLeaf(old, cur *TemplateService, vals map[string]string) []services.FieldChange {
	oc := serviceConfig(old, vals).WithDefaults()
	nc := serviceConfig(cur, vals).WithDefaults()
	var out []services.FieldChange
	for _, d := range diffConfig("config", oc, nc) {
		if d.Field == "config.files" {
			continue
		}
		out = append(out, d)
	}
	return out
}

func diffService(old, cur *TemplateService, k templateKnown) []TemplateUpdateChange {
	var out []TemplateUpdateChange
	add := func(c TemplateUpdateChange) {
		c.Service, c.ServiceName = cur.Key, cur.Name
		out = append(out, c)
	}

	// Managed databases only ever change through the databases API.
	if old.Database != nil || cur.Database != nil {
		od, cd := old.Database, cur.Database
		switch {
		case od == nil || cd == nil || od.Engine != cd.Engine:
			add(TemplateUpdateChange{Field: "database.engine", From: dbText(od), To: dbText(cd), Status: ChangeManual, Reason: "A database's engine cannot change in place."})
		case od.Version != cd.Version:
			add(TemplateUpdateChange{Field: "database.version", From: od.Version, To: cd.Version, Status: ChangeManual,
				Reason: "The update never upgrades a database. Use the database's Upgrade action when you are ready."})
		}
	} else {
		for _, d := range configLeaf(old, cur, k.Vals) {
			f := strings.TrimPrefix(d.Field, "config.")
			c := TemplateUpdateChange{Field: f, From: d.From, To: d.To, Status: ChangeApply}
			if f == "image" {
				c.From, c.To = shortRef(c.From), shortRef(c.To)
			}
			add(c)
		}
		out = append(out, diffFiles(old, cur, k)...)
	}

	// Volumes.
	oldVol, curVol := map[string]string{}, map[string]string{}
	for _, v := range old.Volumes {
		oldVol[v.Name] = v.Path
	}
	for _, v := range cur.Volumes {
		curVol[v.Name] = v.Path
	}
	for _, name := range sortedKeys(oldVol, curVol) {
		op, inOld := oldVol[name]
		cp, inCur := curVol[name]
		switch {
		case !inOld:
			add(TemplateUpdateChange{Field: "volume:" + name, To: cp, Status: ChangeApply})
		case !inCur:
			add(TemplateUpdateChange{Field: "volume:" + name, From: op, Status: ChangeManual, Reason: "A removed volume is kept: deleting it would delete its data."})
		case op != cp:
			add(TemplateUpdateChange{Field: "volume:" + name, From: op, To: cp, Status: ChangeManual, Reason: "A volume's mount path is changed on the service's Volumes tab."})
		}
	}

	// Environment.
	for _, key := range sortedKeys(old.Env, cur.Env) {
		ov, inOld := old.Env[key]
		cv, inCur := cur.Env[key]
		secret := envIsSecret(old, key, ov, k.Secret) || envIsSecret(cur, key, cv, k.Secret)
		show := func(v string) string {
			if secret {
				return ""
			}
			return v
		}
		switch {
		case !inOld:
			add(TemplateUpdateChange{Field: "env." + key, To: show(cv), Secret: secret, Status: ChangeApply})
		case !inCur:
			add(TemplateUpdateChange{Field: "env." + key, From: show(ov), Secret: secret, Status: ChangeManual,
				Reason: "The template no longer sets this variable. It is left in place; delete it if the app does not need it."})
		case ov != cv:
			add(TemplateUpdateChange{Field: "env." + key, From: show(ov), To: show(cv), Secret: secret, Status: ChangeApply})
		}
	}

	// Published ports and domains change routes: reported, never applied.
	if a, b := portsText(old.Ports), portsText(cur.Ports); a != b {
		add(TemplateUpdateChange{Field: "ports", From: a, To: b, Status: ChangeManual, Reason: "Published ports change the server's proxy routes. Edit them under Networking."})
	}
	if a, b := domainsText(old.Domains), domainsText(cur.Domains); a != b {
		add(TemplateUpdateChange{Field: "domains", From: a, To: b, Status: ChangeManual, Reason: "Domains are managed under Networking."})
	}
	return out
}

func dbText(d *TemplateDatabase) string {
	if d == nil {
		return ""
	}
	return d.Engine + " " + d.Version
}

func portsText(ps []TemplatePort) string {
	var parts []string
	for _, p := range ps {
		s := fmt.Sprintf("%d/%s", p.Port, p.Protocol)
		if p.Publish {
			s += " published"
		}
		parts = append(parts, s)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func domainsText(ds []TemplateDomain) string {
	var parts []string
	for _, d := range ds {
		if d.Generate {
			parts = append(parts, "generated")
		} else {
			parts = append(parts, d.Host)
		}
	}
	return strings.Join(parts, ", ")
}

// diffFiles compares the template's files by path.
func diffFiles(old, cur *TemplateService, k templateKnown) []TemplateUpdateChange {
	var out []TemplateUpdateChange
	oldF, curF := map[string]TemplateFile{}, map[string]TemplateFile{}
	for _, f := range old.Files {
		oldF[f.Path] = f
	}
	for _, f := range cur.Files {
		curF[f.Path] = f
	}
	paths := map[string]bool{}
	for p := range oldF {
		paths[p] = true
	}
	for p := range curF {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	for _, p := range sorted {
		of, inOld := oldF[p]
		cf, inCur := curF[p]
		c := TemplateUpdateChange{Service: cur.Key, ServiceName: cur.Name, Field: "file:" + p, Status: ChangeApply}
		switch {
		case !inOld:
			c.To = fileText(cf, k.Vals)
		case !inCur:
			c.From = fileText(of, k.Vals)
		case fileText(of, k.Vals) != fileText(cf, k.Vals):
			c.From, c.To = fileText(of, k.Vals), fileText(cf, k.Vals)
		default:
			continue
		}
		out = append(out, c)
	}
	return out
}

// fileText is the comparable form of a template file (mode and content).
func fileText(f TemplateFile, vals map[string]string) string {
	return fileMode(f.Mode) + "\n" + expandInputs(f.Content, vals, false)
}

func fileMode(m string) string {
	if m == "" {
		return "0644"
	}
	return m
}

func sortedKeys[V any](a, b map[string]V) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- classification against the live services ----

// LiveVariable is a stored variable as the template update reads it: secrets
// carry no value.
type LiveVariable struct {
	Value  string
	Secret bool
}

// liveService is a service of the stack as it is now.
type liveService struct {
	Slug    string
	Cfg     services.Config
	Volumes map[string]string // name → mount path
	Vars    map[string]LiveVariable
}

// classifyInput is everything classifyChanges needs besides the changes.
type classifyInput struct {
	Old, Cur *CatalogTemplate
	Known    templateKnown
	// Slugs maps template service key → the service's slug.
	Slugs map[string]string
	Live  map[string]*liveService
}

// classifyChanges refines every apply change against the live service: a field
// the user changed is kept, one that already has the new value is applied.
func classifyChanges(in classifyInput, changes []TemplateUpdateChange) []TemplateUpdateChange {
	oldSvc, curSvc := serviceByKey(in.Old), serviceByKey(in.Cur)
	type cfgSets struct{ vsOld, vsNew map[string]bool }
	cache := map[string]cfgSets{}
	sets := func(key string) cfgSets {
		if c, ok := cache[key]; ok {
			return c
		}
		live := in.Live[key]
		oc := serviceConfig(oldSvc[key], in.Known.Vals).WithDefaults()
		nc := serviceConfig(curSvc[key], in.Known.Vals).WithDefaults()
		lc := live.Cfg.WithDefaults()
		c := cfgSets{vsOld: fieldSet(diffConfig("config", lc, oc)), vsNew: fieldSet(diffConfig("config", lc, nc))}
		cache[key] = c
		return c
	}

	out := make([]TemplateUpdateChange, len(changes))
	copy(out, changes)
	for i := range out {
		c := &out[i]
		if c.Service != "" {
			c.Slug = in.Slugs[c.Service]
		}
		if c.Status != ChangeApply || c.Service == "" {
			continue
		}
		live := in.Live[c.Service]
		if live == nil {
			c.Status, c.Reason = ChangeManual, "This service is no longer part of the stack."
			continue
		}
		os, cs := oldSvc[c.Service], curSvc[c.Service]
		switch {
		case strings.HasPrefix(c.Field, "env."):
			classifyEnv(c, strings.TrimPrefix(c.Field, "env."), os, cs, in, live)
		case strings.HasPrefix(c.Field, "file:"):
			classifyFile(c, strings.TrimPrefix(c.Field, "file:"), os, cs, in.Known.Vals, live)
		case strings.HasPrefix(c.Field, "volume:"):
			name := strings.TrimPrefix(c.Field, "volume:")
			if path, ok := live.Volumes[name]; ok {
				if path == c.To {
					c.Status = ChangeApplied
				} else {
					c.Status, c.Reason = ChangeKept, "The service already has a volume with this name at "+path+"."
				}
			}
		default:
			cs := sets(c.Service)
			f := "config." + c.Field
			switch {
			case !cs.vsOld[f]:
			case !cs.vsNew[f]:
				c.Status = ChangeApplied
			default:
				c.Status, c.Reason = ChangeKept, "You changed this on the service, so the update leaves your value."
			}
		}
	}
	return out
}

func fieldSet(ds []services.FieldChange) map[string]bool {
	m := make(map[string]bool, len(ds))
	for _, d := range ds {
		m[d.Field] = true
	}
	return m
}

// envValue is the text an install would have stored for a template variable.
func envValue(raw string, vals map[string]string, slugs map[string]string) string {
	return rewriteRefs(expandInputs(raw, vals, true), slugs)
}

func classifyEnv(c *TemplateUpdateChange, key string, os, cs *TemplateService, in classifyInput, live *liveService) {
	cur, hasCur := live.Vars[key]
	oldRaw, inOld := os.Env[key]
	newRaw := cs.Env[key]
	if usesUnknown(newRaw, in.Known.Unknown) {
		c.Status, c.Reason = ChangeManual, "This value uses an input whose value is stored only as a secret. Set the variable by hand."
		return
	}
	if !inOld {
		// A variable the template adds: written unless the service already has the key.
		if !hasCur {
			return
		}
		if !cur.Secret && cur.Value == envValue(newRaw, in.Known.Vals, in.Slugs) {
			c.Status = ChangeApplied
			return
		}
		c.Status, c.Reason = ChangeKept, "The service already has a variable with this name."
		return
	}
	switch {
	case !hasCur:
		c.Status, c.Reason = ChangeKept, "You removed this variable, so the update does not bring it back."
	case cur.Secret || c.Secret:
		c.Status, c.Reason = ChangeKept, "Secret values are never overwritten by an update."
	case cur.Value == envValue(newRaw, in.Known.Vals, in.Slugs):
		c.Status = ChangeApplied
	case cur.Value == envValue(oldRaw, in.Known.Vals, in.Slugs) && !usesUnknown(oldRaw, in.Known.Unknown):
	default:
		c.Status, c.Reason = ChangeKept, "You changed this variable, so the update leaves your value."
	}
}

func classifyFile(c *TemplateUpdateChange, path string, os, cs *TemplateService, vals map[string]string, live *liveService) {
	liveText := ""
	have := false
	for _, f := range live.Cfg.Files {
		if f.Path == path {
			liveText, have = fileMode(f.Mode)+"\n"+f.Content, true
		}
	}
	oldText, newText := "", ""
	for _, f := range os.Files {
		if f.Path == path {
			oldText = fileText(f, vals)
		}
	}
	for _, f := range cs.Files {
		if f.Path == path {
			newText = fileText(f, vals)
		}
	}
	switch {
	case c.To == "": // removed from the template
		switch {
		case !have:
			c.Status = ChangeApplied
		case liveText != oldText:
			c.Status, c.Reason = ChangeKept, "You edited this file, so the update keeps it."
		}
	case c.From == "": // added by the template
		switch {
		case !have:
		case liveText == newText:
			c.Status = ChangeApplied
		default:
			c.Status, c.Reason = ChangeKept, "The service already has a different file at this path."
		}
	default: // changed
		switch {
		case !have:
			c.Status, c.Reason = ChangeKept, "You removed this file, so the update does not bring it back."
		case liveText == newText:
			c.Status = ChangeApplied
		case liveText != oldText:
			c.Status, c.Reason = ChangeKept, "You edited this file, so the update keeps it."
		}
	}
}

// ---- applying ----

// patchConfig copies the named config fields (paths from DiffConfig without the
// "config." prefix) from next into live and returns the result. A field that is
// absent from next is removed, which restores its default.
func patchConfig(live, next services.Config, fields []string) (services.Config, error) {
	lm, err := toMap(live)
	if err != nil {
		return live, err
	}
	nm, err := toMap(next)
	if err != nil {
		return live, err
	}
	for _, f := range fields {
		path := strings.Split(f, ".")
		if v, ok := getPath(nm, path); ok {
			setPath(lm, path, v)
		} else {
			delPath(lm, path)
		}
	}
	raw, err := json.Marshal(lm)
	if err != nil {
		return live, err
	}
	var out services.Config
	if err := json.Unmarshal(raw, &out); err != nil {
		return live, fmt.Errorf("patched config: %w", err)
	}
	return out.WithDefaults(), nil
}

func toMap(c services.Config) (map[string]any, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func getPath(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, p := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func setPath(m map[string]any, path []string, v any) {
	for _, p := range path[:len(path)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[path[len(path)-1]] = v
}

func delPath(m map[string]any, path []string) {
	for _, p := range path[:len(path)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			return
		}
		m = next
	}
	delete(m, path[len(path)-1])
}

// patchFiles applies the file changes (add, change or remove by path) to files.
func patchFiles(files []services.FileMount, changes []TemplateUpdateChange, next []services.FileMount) []services.FileMount {
	out := slices.Clone(files)
	for _, c := range changes {
		path, ok := strings.CutPrefix(c.Field, "file:")
		if !ok || c.Status != ChangeApply {
			continue
		}
		idx := slices.IndexFunc(out, func(f services.FileMount) bool { return f.Path == path })
		nidx := slices.IndexFunc(next, func(f services.FileMount) bool { return f.Path == path })
		switch {
		case nidx < 0 && idx >= 0:
			out = slices.Delete(out, idx, idx+1)
		case nidx >= 0 && idx >= 0:
			out[idx] = next[nidx]
		case nidx >= 0:
			out = append(out, next[nidx])
		}
	}
	return out
}

// countStatus counts the changes with status.
func countStatus(changes []TemplateUpdateChange, status string) int {
	n := 0
	for _, c := range changes {
		if c.Status == status {
			n++
		}
	}
	return n
}
