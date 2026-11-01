package main

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/templates"
)

// floatingTags mirrors the validator's list of rejected tags.
var floatingTags = map[string]bool{"latest": true, "stable": true, "main": true, "master": true, "edge": true, "nightly": true, "dev": true, "develop": true, "release": true}

func (t *translator) translateService(st *svcState) error {
	c, s := st.c, &st.svc
	p := "services." + st.key

	tag := st.tag
	if tag == "" {
		tag = "latest"
	}
	s.Image = st.repo + ":" + tag
	s.Digest = digestStub
	t.review(p+".digest", "placeholder, run tools/template-pin")
	switch {
	case floatingTags[strings.ToLower(tag)]:
		t.review(p+".image", "floating tag, pick a stable version")
	case st.tag == "":
		t.review(p+".image", "source image had no tag, pick a stable version")
	}

	for _, e := range c.Env {
		if _, _, ok := isDomainDecl(e); ok && t.src.Dok == nil {
			continue
		}
		k := e.Key
		if !envKeyRe.MatchString(k) {
			st.dropped = append(st.dropped, "env "+k)
			continue
		}
		if strings.HasPrefix(strings.ToUpper(k), "NEBULA_") || k == "PORT" {
			st.dropped = append(st.dropped, "env "+k+" (platform-managed)")
			continue
		}
		var v string
		if e.HasVal {
			v = t.expand(e.Val, st, ctxEnv)
		} else {
			v = t.resolve(k, "", "", st, ctxEnv)
		}
		v = t.hosts(v, k, st, true)
		st.setEnv(k, v)
	}

	s.Docker = t.dockerOptions(st)
	if !st.commandDone {
		for _, a := range c.Command {
			s.Command = append(s.Command, t.hosts(t.expand(a, st, ctxArg), "", st, false))
		}
	}

	if err := t.mounts(st); err != nil {
		return err
	}
	t.ports(st)

	for _, d := range c.DependsOn {
		tgt := t.byName[d.Name]
		if tgt == nil || tgt == st {
			continue
		}
		if !contains(s.DependsOn, tgt.key) {
			s.DependsOn = append(s.DependsOn, tgt.key)
		}
	}

	if r := memoryMB(c.MemLimit); r > 0 || c.Cpus > 0 {
		s.Resources = &templates.TemplateLimits{MemoryMB: r, CPUs: c.Cpus}
	}
	if d, ok := parseDuration(c.StopGrace); ok && d > 0 {
		s.StopGraceSeconds = int(d.Seconds())
	}
	if pl := scalar(c.Raw["platform"]); strings.Contains(pl, "amd64") {
		t.review("requires.arch", "service "+st.key+" is pinned to platform "+pl)
	}
	for _, k := range []string{"user", "security_opt", "ulimits", "shm_size", "sysctls", "tmpfs", "working_dir", "extra_hosts", "hostname", "init", "read_only", "dns", "stdin_open", "tty", "group_add", "cgroup_parent", "mem_reservation", "memswap_limit", "pids_limit"} {
		if c.Raw[k] != nil && !dockerKeys[k] {
			st.dropped = append(st.dropped, k)
		}
	}
	return nil
}

func (st *svcState) setEnv(k, v string) {
	for i := range st.env {
		if st.env[i].k == k {
			st.env[i].v = v
			return
		}
	}
	st.env = append(st.env, kv{k, v})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- variable expansion ---------------------------------------------------

// expand applies compose interpolation ($VAR, ${VAR}, ${VAR:-d}, ${VAR:?}, $$).
func (t *translator) expand(s string, st *svcState, ctx ctxKind) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		n := s[i+1]
		switch {
		case n == '$':
			if strings.HasPrefix(s[i+2:], "{{") {
				b.WriteString("$$") // a literal ${{ stays literal in the template grammar
			} else {
				b.WriteByte('$')
			}
			i += 2
		case n == '{':
			end := matchBrace(s, i+1)
			if end < 0 {
				b.WriteString(s[i:])
				i = len(s)
				continue
			}
			b.WriteString(t.expandBraced(s[i+2:end], st, ctx))
			i = end + 1
		case n == '_' || (n >= 'a' && n <= 'z') || (n >= 'A' && n <= 'Z'):
			j := i + 2
			for j < len(s) && (s[j] == '_' || (s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			b.WriteString(t.resolve(s[i+1:j], "", "", st, ctx))
			i = j
		default:
			b.WriteByte('$')
			i++
		}
	}
	return b.String()
}

// matchBrace returns the index of the } closing the { at s[open].
func matchBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func (t *translator) expandBraced(inner string, st *svcState, ctx ctxKind) string {
	j := 0
	for j < len(inner) && (inner[j] == '_' || (inner[j] >= 'a' && inner[j] <= 'z') || (inner[j] >= 'A' && inner[j] <= 'Z') || (j > 0 && inner[j] >= '0' && inner[j] <= '9')) {
		j++
	}
	name, rest := inner[:j], inner[j:]
	if name == "" {
		return "${" + inner + "}"
	}
	for _, op := range []string{":-", ":?", ":+", "-", "?", "+"} {
		if strings.HasPrefix(rest, op) {
			return t.resolve(name, op, rest[len(op):], st, ctx)
		}
	}
	if rest != "" {
		return "${" + inner + "}"
	}
	return t.resolve(name, "", "", st, ctx)
}

// resolve maps one compose variable to template syntax.
func (t *translator) resolve(name, op, arg string, st *svcState, ctx ctxKind) string {
	def, hasDef, required := "", false, false
	switch op {
	case ":-", "-":
		def, hasDef = t.expand(arg, st, ctx), true
	case ":?", "?":
		required = true
	case ":+", "+":
		return t.expand(arg, st, ctx)
	}
	if hasDef && hostKeyRe.MatchString(name) {
		if sib := t.byName[strings.TrimSpace(def)]; sib != nil && sib != st {
			return t.hostRef(sib)
		}
	}
	if a, ok := t.aliases[name]; ok {
		return a
	}
	if t.src.Dok == nil {
		if v, ok := t.coolifyVar(name, st); ok {
			return v
		}
	} else if v, ok := t.dokVar(name, st); ok {
		return v
	}
	return t.inputRef(name, def, required)
}

var coolifyGenRe = regexp.MustCompile(`^SERVICE_(PASSWORD|USER|BASE64|REALBASE64|HEX)_(?:(\d+)_)?(.+)$`)

// coolifyVar maps Coolify's magic variables.
func (t *translator) coolifyVar(name string, st *svcState) (string, bool) {
	if m := coolifyGenRe.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[2])
		var expr string
		switch m[1] {
		case "PASSWORD", "BASE64":
			if n == 0 {
				n = 32
			}
			expr = fmt.Sprintf("${{secret(%d)}}", n)
		case "USER":
			if n == 0 {
				n = 16
			}
			expr = fmt.Sprintf("${{secret(%d)}}", n)
		case "REALBASE64":
			if n == 0 {
				n = 32
			}
			expr = fmt.Sprintf(`${{randomBytes(%d,"base64")}}`, n)
		case "HEX":
			if n == 0 {
				n = 32
			}
			expr = fmt.Sprintf(`${{randomBytes(%d,"hex")}}`, max(n/2, 1))
		}
		t.defineGen(name, name, expr)
		return placeholder("G", name), true
	}
	if m := coolifyURLRe.FindStringSubmatch(name); m != nil {
		port, _ := strconv.Atoi(m[3])
		id := m[2]
		t.refDomain(id, port)
		kind := "U"
		if m[1] == "FQDN" {
			kind = "F"
		}
		return placeholder(kind, id), true
	}
	switch name {
	case "COOLIFY_FQDN", "COOLIFY_URL":
		id := "SELF:" + st.key
		t.declareDomain(id, st.c.Name, 0)
		if name == "COOLIFY_URL" {
			return placeholder("U", id), true
		}
		return placeholder("F", id), true
	}
	return "", false
}

// refDomain records a domain referenced (not declared) by a variable.
func (t *translator) refDomain(id string, port int) {
	if d, ok := t.domains[id]; ok {
		if d.port == 0 {
			d.port = port
		}
		return
	}
	t.domains[id] = &domainDecl{service: "", port: port}
	t.domOrder = append(t.domOrder, id)
}

func (t *translator) defineGen(name, envKey, expr string) {
	if _, ok := t.gens[name]; ok {
		return
	}
	t.gens[name] = &genVar{envKey: envKey, expr: expr}
	t.genOrder = append(t.genOrder, name)
}

// ---- Dokploy variables ----------------------------------------------------

var dokTokenRe = regexp.MustCompile(`\$\{([^}]*)\}`)

// dokVar resolves a compose variable through config.env / [variables].
func (t *translator) dokVar(name string, st *svcState) (string, bool) {
	if raw, ok := t.dokEnv[name]; ok {
		if !strings.Contains(raw, "${") {
			return t.inputRef(name, raw, false), true
		}
		return t.dokEval(name, raw, st), true
	}
	if raw, ok := t.src.Dok.Vars[name]; ok {
		if !strings.Contains(raw, "${") {
			return raw, true
		}
		return t.dokEval(name, raw, st), true
	}
	return "", false
}

// dokEval evaluates a Dokploy value containing ${helper} tokens.
func (t *translator) dokEval(name, raw string, st *svcState) string {
	if v, ok := t.dokCache[name]; ok {
		return v
	}
	if t.dokBusy[name] {
		return raw
	}
	t.dokBusy[name] = true
	defer delete(t.dokBusy, name)
	whole := dokTokenRe.FindStringIndex(raw)
	single := len(whole) > 0 && whole[0] == 0 && whole[1] == len(raw)
	n := 0
	out := dokTokenRe.ReplaceAllStringFunc(raw, func(tok string) string {
		inner := tok[2 : len(tok)-1]
		gname := name
		if !single {
			n++
			gname = fmt.Sprintf("%s_%d", name, n)
		}
		if v, ok := t.dokToken(inner, gname, st); ok {
			return v
		}
		return tok
	})
	if single {
		if m := placeholderRe.FindStringSubmatch(out); len(m) > 2 && m[0] == out && m[1] == "G" {
			if g := t.gens[m[2]]; g != nil && !g.renamed && g.envKey == strings.ToUpper(m[2]) && !strings.EqualFold(name, m[2]) && envKeyRe.MatchString(name) {
				g.envKey, g.renamed = name, true
			}
		}
	}
	t.dokCache[name] = out
	return out
}

// dokToken evaluates one ${...} token: a helper or a known variable.
func (t *translator) dokToken(inner, name string, st *svcState) (string, bool) {
	helper, arg, _ := strings.Cut(inner, ":")
	n, _ := strconv.Atoi(arg)
	gen := func(expr string) string {
		t.defineGen(name, strings.ToUpper(name), expr)
		return placeholder("G", name)
	}
	switch helper {
	case "password":
		if n == 0 {
			n = 16
		}
		return gen(fmt.Sprintf("${{secret(%d)}}", n)), true
	case "base64":
		if n == 0 {
			n = 32
		}
		return gen(fmt.Sprintf(`${{randomBytes(%d,"base64")}}`, n)), true
	case "hash":
		if n == 0 {
			n = 32
		}
		return gen(fmt.Sprintf(`${{randomBytes(%d,"hex")}}`, max((n+1)/2, 1))), true
	case "uuid":
		return gen("${{uuid()}}"), true
	case "randomPort":
		return gen("${{randomInt(20000,60000)}}"), true
	case "domain":
		id := name
		if strings.Contains(name, "_") && !t.dokDom[name] {
			id = "domain"
		}
		t.refDomain(id, 0)
		return placeholder("F", id), true
	case "email":
		return t.inputRef("ADMIN_EMAIL", "", true), true
	case "username":
		return t.inputRef("ADMIN_USERNAME", "admin", false), true
	case "jwt", "timestamp", "timestamps":
		t.err = skipf("unsupported Dokploy helper ${%s}", helper)
		return "", true
	}
	if arg == "" {
		if raw, ok := t.dokEnv[helper]; ok || t.src.Dok.Vars[helper] != "" {
			_ = raw
			return t.dokVar(helper, st)
		}
	}
	return "", false
}

// dokContent substitutes only known Dokploy variables inside mount content
// (shell/config syntax such as ${HOME} must stay untouched).
func (t *translator) dokContent(s string, st *svcState) string {
	return dokTokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		inner := tok[2 : len(tok)-1]
		if v, ok := t.dokToken(inner, "FILE_"+strings.ReplaceAll(inner, ":", "_"), st); ok {
			return v
		}
		return tok
	})
}

// ---- inputs -----------------------------------------------------------------

var inputKeyFix = regexp.MustCompile(`[^A-Z0-9_]+`)

func inputKey(name string) string {
	k := inputKeyFix.ReplaceAllString(strings.ToUpper(name), "_")
	k = strings.TrimLeft(k, "_0123456789")
	if len(k) > 64 {
		k = k[:64]
	}
	return k
}

func inputLabel(key string) string {
	s := strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(key, "_", " ")), " "))
	return strings.ToUpper(s[:1]) + s[1:]
}

// inputRef returns ${input:KEY} (declaring the input) or the inline default
// when an input cannot carry the value.
func (t *translator) inputRef(name, def string, required bool) string {
	key := inputKey(name)
	if key == "" || strings.Contains(def, "\x00") || strings.Contains(def, "${{") {
		return def
	}
	if i, ok := t.inputIdx[key]; ok {
		if required && t.inputs[i].Default == "" {
			t.inputs[i].Required = true
		}
		return "${input:" + key + "}"
	}
	if len(t.inputs) >= maxInputs {
		t.review("inputs", fmt.Sprintf("the source has more than %d variables; those beyond the limit were inlined with their defaults", maxInputs))
		return def
	}
	in := templates.TemplateInput{Key: key, Label: inputLabel(key), Default: def, Required: required && def == "", Secret: secretInRe.MatchString(key)}
	if strings.HasPrefix(name, "SERVICE_") || strings.HasPrefix(name, "COOLIFY_") {
		t.review("inputs."+key, "unknown Coolify magic variable "+name)
	}
	t.inputIdx[key] = len(t.inputs)
	t.inputs = append(t.inputs, in)
	return "${input:" + key + "}"
}

// ---- hostnames ---------------------------------------------------------------

var hostKeyRe = regexp.MustCompile(`(?i)(HOST|SERVER|ADDR|ENDPOINT|URL|URI|DOMAIN)`)

// hosts rewrites sibling compose hostnames to template references and
// collapses full database connection strings to ${{db.DATABASE_URL}}.
func (t *translator) hosts(v, key string, self *svcState, env bool) string {
	if v == "" {
		return v
	}
	var names []string
	for n, st := range t.byName {
		if st != self {
			names = append(names, regexp.QuoteMeta(n))
		}
	}
	if len(names) == 0 {
		return v
	}
	sortLongestFirst(names)
	wholeOK := env && hostKeyRe.MatchString(key)
	re := regexp.MustCompile(`(^|://|@|,)(` + strings.Join(names, "|") + `)(:[0-9]+|/|\?|,|$)`)
	out := re.ReplaceAllStringFunc(v, func(m string) string {
		sub := re.FindStringSubmatch(m)
		pre, name, post := sub[1], sub[2], sub[3]
		// A hostname at the very start only counts as host:port or, for
		// host-like keys, as the whole value.
		if pre == "" && !strings.HasPrefix(post, ":") && !(wholeOK && post == "") {
			return m
		}
		return pre + t.hostRef(t.byName[name]) + post
	})
	return t.collapseURL(out)
}

func sortLongestFirst(s []string) {
	sort.Slice(s, func(i, j int) bool {
		if len(s[i]) != len(s[j]) {
			return len(s[i]) > len(s[j])
		}
		return s[i] < s[j]
	})
}

func (t *translator) hostRef(st *svcState) string {
	switch {
	case st.db != nil && (st.dbEng == "redis" || st.dbEng == "valkey"):
		return "${{" + st.key + ".REDIS_HOST}}"
	case st.db != nil:
		return "${{" + st.key + ".DATABASE_HOST}}"
	}
	return "${{" + st.key + ".NEBULA_PRIVATE_HOST}}"
}

func (t *translator) collapseURL(v string) string {
	for _, st := range t.states {
		if st.db == nil {
			continue
		}
		k := regexp.QuoteMeta(st.key)
		if st.dbEng == "redis" || st.dbEng == "valkey" {
			re := regexp.MustCompile(`^rediss?://[^:@/]*:` + `\$\{\{` + k + `\.REDIS_PASSWORD\}\}@\$\{\{` + k + `\.REDIS_HOST\}\}(:[0-9]+)?(/[0-9]*)?$`)
			if re.MatchString(v) {
				return "${{" + st.key + ".REDIS_URL}}"
			}
			continue
		}
		re := regexp.MustCompile(`^(?:postgres|postgresql|mysql|mariadb|mongodb)://[^:@/]*:\$\{\{` + k + `\.DATABASE_PASSWORD\}\}@\$\{\{` + k + `\.DATABASE_HOST\}\}(?::[0-9]+)?/[^?]*(\?.*)?$`)
		if m := re.FindStringSubmatch(v); m != nil {
			return "${{" + st.key + ".DATABASE_URL}}" + m[1]
		}
	}
	return v
}

// ---- mounts -----------------------------------------------------------------

func (t *translator) mounts(st *svcState) error {
	s, p := &st.svc, "services."+st.key
	seenPath := map[string]bool{}
	for _, m := range st.c.Volumes {
		target := strings.TrimRight(m.Target, "/")
		if target == "" || !path.IsAbs(target) {
			st.dropped = append(st.dropped, "volume "+m.Target)
			continue
		}
		var dokFiles []templates.TemplateFile
		if m.Type == "bind" && !m.HasBody && t.src.Dok != nil {
			dokFiles = t.dokFiles(st, m)
		}
		switch {
		case m.Type == "tmpfs":
			continue
		case target == "/etc/localtime" || target == "/etc/timezone":
			st.dropped = append(st.dropped, "volume "+target)
		case m.HasBody:
			content := t.expand(m.Content, st, ctxFile)
			s.Files = append(s.Files, templates.TemplateFile{Path: target, Mode: fileMode(target, content), Content: content})
			t.review(p+".files."+target, "inline content from the source; check it has no secrets")
		case len(dokFiles) > 0:
			s.Files = append(s.Files, dokFiles...)
			for _, f := range dokFiles {
				t.review(p+".files."+f.Path, "from Dokploy [[config.mounts]]")
			}
		default:
			if seenPath[target] {
				continue
			}
			seenPath[target] = true
			name := uniqueVolume(s.Volumes, volumeName(m.Source, target))
			if m.Type == "bind" {
				t.review(p+".volumes."+name, "relative bind mount "+m.Source+" turned into a named volume")
			}
			s.Volumes = append(s.Volumes, templates.TemplateVolume{Name: name, Path: target})
		}
	}
	return nil
}

var volumeFix = regexp.MustCompile(`[^a-z0-9_.-]+`)

func volumeName(source, target string) string {
	base := source
	if base == "" || strings.HasPrefix(base, ".") || strings.HasPrefix(base, "/") {
		base = strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(base, "./"), "../"), "./")
		if base == "" {
			base = strings.Trim(target, "/")
		}
	}
	n := volumeFix.ReplaceAllString(strings.ToLower(base), "-")
	n = strings.Trim(n, "-_.")
	if n == "" {
		n = "data"
	}
	if len(n) > 63 {
		n = n[:63]
	}
	return n
}

func fileMode(target, content string) string {
	if strings.HasPrefix(content, "#!") || strings.HasSuffix(target, ".sh") {
		return "0755"
	}
	return ""
}

// dokFiles maps a Dokploy `../files/x:/dst` bind to the matching mounts.
func (t *translator) dokFiles(st *svcState, m mount) []templates.TemplateFile {
	rel := strings.TrimPrefix(strings.TrimPrefix(m.Source, "../files"), "./")
	rel = strings.Trim(strings.TrimPrefix(rel, "files"), "/")
	rel = strings.Trim(path.Clean("/"+rel), "/")
	var out []templates.TemplateFile
	for _, mt := range t.src.Dok.Mounts {
		fp := strings.Trim(path.Clean("/"+mt.FilePath), "/")
		var dst string
		switch {
		case fp == rel:
			dst = strings.TrimRight(m.Target, "/")
		case rel != "" && strings.HasPrefix(fp, rel+"/"):
			dst = strings.TrimRight(m.Target, "/") + "/" + strings.TrimPrefix(fp, rel+"/")
		default:
			continue
		}
		content := t.dokContent(mt.Content, st)
		out = append(out, templates.TemplateFile{Path: dst, Mode: fileMode(dst, content), Content: content})
	}
	return out
}

// ---- ports ------------------------------------------------------------------

func (t *translator) ports(st *svcState) {
	s, p := &st.svc, "services."+st.key
	seen := map[string]bool{}
	for _, pm := range st.c.Ports {
		c := t.literalValue(pm.Container)
		if i := strings.Index(c, "-"); i > 0 { // port range: keep the first
			c = c[:i]
		}
		n, err := strconv.Atoi(strings.TrimSpace(c))
		if err != nil || n < 1 || n > 65535 {
			st.dropped = append(st.dropped, "ports "+pm.Container)
			continue
		}
		proto := strings.ToLower(pm.Proto)
		if proto != "udp" {
			proto = "tcp"
		}
		name := fmt.Sprintf("%s-%d", proto, n)
		if seen[name] {
			continue
		}
		seen[name] = true
		s.Ports = append(s.Ports, templates.TemplatePort{Name: name, Port: n, Protocol: proto})
		if pm.Host != "" {
			t.review(p+".ports."+name, fmt.Sprintf("compose published host port %s; publish is false, set true for internet access", pm.Host))
		}
	}
}

// literalValue renders ${VAR:-default} as its default text.
func (t *translator) literalValue(s string) string {
	for range 4 {
		if !strings.Contains(s, "$") {
			break
		}
		s = defaultRe.ReplaceAllString(s, "$1")
	}
	return s
}

// ---- health -----------------------------------------------------------------

var (
	httpProbeRe = regexp.MustCompile(`(?:curl|wget)\b[^|;&]*?(https?://[^\s'"|;&)]+)`)
	trivialCmd  = map[string]bool{"echo ok": true, "true": true, "exit 0": true, ":": true, "echo": true}
)

func (t *translator) health(st *svcState) {
	c, s, p := st.c, &st.svc, "services."+st.key
	h := c.Health
	if h == nil || h.Disabled || len(h.Test) == 0 {
		if h != nil && h.Disabled {
			t.review(p+".healthcheck", "compose disables the healthcheck")
		}
		return
	}
	test := make([]string, len(h.Test))
	for i, a := range h.Test {
		test[i] = t.expand(a, st, ctxHealth)
	}
	mode, args := test[0], test[1:]
	var cmdline string
	switch mode {
	case "CMD":
		cmdline = strings.Join(args, " ")
	case "CMD-SHELL":
		cmdline = strings.Join(args, " ")
	default: // bare string form already normalised to CMD-SHELL
		cmdline = strings.Join(test, " ")
		mode, args = "CMD-SHELL", []string{cmdline}
	}
	if c := strings.TrimSpace(cmdline); trivialCmd[c] || strings.HasPrefix(c, "echo ") {
		t.review(p+".healthcheck", "compose healthcheck is a no-op ("+cmdline+")")
		return
	}
	hc := &templates.TemplateHealth{}
	if m := httpProbeRe.FindStringSubmatch(cmdline); m != nil {
		if u, err := url.Parse(m[1]); err == nil && localHost(u.Hostname(), st) {
			port := 80
			if u.Port() != "" {
				port, _ = strconv.Atoi(u.Port())
			} else if u.Scheme == "https" {
				port = 443
			}
			hc.Type = "http"
			hc.Path = u.EscapedPath()
			if hc.Path == "" {
				hc.Path = "/"
			}
			if u.RawQuery != "" {
				hc.Path += "?" + u.RawQuery
			}
			if port != s.Port {
				hc.Port = port
			}
			if u.Scheme == "https" {
				t.review(p+".healthcheck", "compose probes https; probed over http")
			}
		}
	}
	if hc.Type == "" {
		hc.Type = "exec"
		hc.Command = append([]string{mode}, args...)
	}
	if d, ok := parseDuration(h.Interval); ok {
		hc.IntervalSeconds = int(d.Seconds())
	}
	if d, ok := parseDuration(h.Timeout); ok {
		hc.TimeoutSeconds = int(d.Seconds())
	}
	start := 0
	if d, ok := parseDuration(h.StartPeriod); ok {
		start = int(d.Seconds())
		hc.StartPeriodSeconds = start
	}
	if h.Retries > 0 {
		hc.DeadlineSeconds = max(60, start+max(hc.IntervalSeconds, 5)*h.Retries)
	}
	s.Healthcheck = hc
}

func localHost(h string, st *svcState) bool {
	switch h {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1", "[::1]", st.c.Name, st.c.ContainerNm:
		return true
	}
	return false
}

func parseDuration(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, true
	}
	d, err := time.ParseDuration(s)
	return d, err == nil
}

var memRe = regexp.MustCompile(`(?i)^\s*([0-9.]+)\s*([kmg]?)b?\s*$`)

func memoryMB(s string) int {
	m := memRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	switch strings.ToLower(m[2]) {
	case "g":
		f *= 1024
	case "k":
		f /= 1024
	case "":
		f /= 1024 * 1024
	}
	if f < 6 {
		return 0
	}
	return int(f)
}

// uniqueVolume avoids reusing a volume name for a second mount path.
func uniqueVolume(have []templates.TemplateVolume, name string) string {
	taken := func(n string) bool {
		for _, v := range have {
			if v.Name == n {
				return true
			}
		}
		return false
	}
	out := name
	for i := 2; taken(out); i++ {
		out = fmt.Sprintf("%s-%d", name, i)
	}
	return out
}
