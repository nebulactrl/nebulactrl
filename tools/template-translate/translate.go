package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/nebulactrl/nebulactrl/internal/templates"
)

const (
	maxServices = 8
	maxInputs   = 24
	digestStub  = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
)

// skipError marks a template the translator refuses to draft.
type skipError struct{ reason string }

func (e *skipError) Error() string { return e.reason }

func skipf(format string, a ...any) error { return &skipError{reason: fmt.Sprintf(format, a...)} }

type ctxKind int

const (
	ctxEnv ctxKind = iota
	ctxArg
	ctxFile
	ctxHealth
)

// Placeholders carry cross-service decisions (shared generated values, public
// URLs) through translation; finalize resolves them once every service is
// known. Format: \x00<kind>:<name>\x00 with kind G (generated value),
// U (public URL) or F (public domain).
var placeholderRe = regexp.MustCompile("\x00([GUF]):([^\x00]*)\x00")

func placeholder(kind, name string) string { return "\x00" + kind + ":" + name + "\x00" }

type kv struct{ k, v string }

type genVar struct {
	envKey  string
	expr    string
	renamed bool // envKey was taken from a config.env alias
}

type domainDecl struct {
	service string // compose service name
	port    int
}

// svcState is the translation state of one compose service.
type svcState struct {
	c       *csvc
	key     string
	svc     templates.TemplateService
	env     []kv
	db      *templates.TemplateDatabase
	dbEng   string // engine when db != nil
	oneshot bool
	dropped []string
	repo    string
	tag     string
	hasDom  bool
	// commandDone is set when the entrypoint translation already produced the start command.
	commandDone bool
}

// translator turns one source into a draft.
type translator struct {
	src      *source
	states   []*svcState
	byName   map[string]*svcState
	aliases  map[string]string
	inputs   []templates.TemplateInput
	inputIdx map[string]int
	gens     map[string]*genVar
	genOrder []string
	domains  map[string]*domainDecl
	domOrder []string
	reviews  map[string][]string
	dokEnv   map[string]string
	dokCache map[string]string
	dokBusy  map[string]bool
	dokDom   map[string]bool
	err      error
}

// draft is the translator output: the template plus the REVIEW notes keyed by
// template path (see emit.go).
type draft struct {
	T       *templates.CatalogTemplate
	Reviews map[string][]string
}

var (
	envKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	svcKeyRe    = regexp.MustCompile(`[^a-z0-9-]+`)
	volNameRe   = regexp.MustCompile(`[^a-z0-9_.-]+`)
	secretKeyRe = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|SECRET|TOKEN|PRIVATE_KEY|API_KEY|ADMIN_KEY|MASTER_KEY|ENCRYPTION_KEY|SALT)`)
	secretInRe  = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|PASS$|SECRET|TOKEN|_KEY$|^KEY$|SALT|CREDENTIAL)`)
	identRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func (t *translator) review(path, msg string) {
	if !slices.Contains(t.reviews[path], msg) {
		t.reviews[path] = append(t.reviews[path], msg)
	}
}

// translate drafts a template from one source. A *skipError means the source
// uses features templates cannot express.
func translate(src *source) (*draft, error) {
	if src.Err != nil {
		return nil, skipf("parse error: %v", src.Err)
	}
	t := &translator{
		src: src, byName: map[string]*svcState{}, aliases: map[string]string{},
		inputIdx: map[string]int{}, gens: map[string]*genVar{}, domains: map[string]*domainDecl{},
		reviews: map[string][]string{}, dokEnv: map[string]string{}, dokCache: map[string]string{},
		dokBusy: map[string]bool{}, dokDom: map[string]bool{},
	}
	if err := t.supported(); err != nil {
		return nil, err
	}
	if src.Dok != nil {
		for _, e := range src.Dok.Env {
			t.dokEnv[e.Key] = e.Val
		}
	}
	if err := t.build(); err != nil {
		return nil, err
	}
	if t.err != nil {
		return nil, t.err
	}
	return t.assemble(), nil
}

// supported rejects compose features a template cannot express.
func (t *translator) supported() error {
	doc := t.src.Compose
	if len(doc.Services) == 0 {
		return skipf("no services")
	}
	if len(doc.Services) > maxServices {
		return skipf("more than %d services (%d)", maxServices, len(doc.Services))
	}
	for _, c := range doc.Services {
		raw := c.Raw
		switch {
		case raw["build"] != nil:
			return skipf("build: directive (service %s)", c.Name)
		case raw["privileged"] == true:
			return skipf("privileged (service %s)", c.Name)
		case raw["network_mode"] != nil:
			return skipf("network_mode: %s (service %s)", scalar(raw["network_mode"]), c.Name)
		case raw["devices"] != nil:
			return skipf("devices (service %s)", c.Name)
		case unsafeCapAdd(raw["cap_add"]):
			return skipf("cap_add %s needs an unrestricted server (service %s)", strings.Join(strList(raw["cap_add"]), ","), c.Name)
		case raw["volumes_from"] != nil:
			return skipf("volumes_from (service %s)", c.Name)
		case raw["env_file"] != nil:
			return skipf("env_file (service %s)", c.Name)
		case raw["configs"] != nil || raw["secrets"] != nil:
			return skipf("compose configs/secrets (service %s)", c.Name)
		case raw["pid"] != nil || raw["ipc"] != nil:
			return skipf("pid/ipc namespace (service %s)", c.Name)
		case raw["gpus"] != nil || raw["runtime"] != nil:
			return skipf("gpu/runtime (service %s)", c.Name)
		case raw["extends"] != nil:
			return skipf("extends (service %s)", c.Name)
		}
		if c.Image == "" {
			return skipf("no image (service %s)", c.Name)
		}
		switch n := raw["networks"].(type) {
		case []any:
			for _, x := range n {
				if !defaultNetwork(scalar(x)) {
					return skipf("custom network %s (service %s)", scalar(x), c.Name)
				}
			}
		case map[string]any:
			for name := range n {
				if !defaultNetwork(name) {
					return skipf("custom network %s (service %s)", name, c.Name)
				}
			}
		}
		for _, m := range c.Volumes {
			if m.Type == "bind" && !m.HasBody && (strings.HasPrefix(m.Source, "/") || strings.HasPrefix(m.Source, "~")) {
				return skipf("host bind mount without content: %s (service %s)", m.Source, c.Name)
			}
		}
	}
	return nil
}

func defaultNetwork(n string) bool {
	return n == "default" || n == "coolify" || n == "dokploy-network"
}

// build runs every translation phase up to (not including) header assembly.
func (t *translator) build() error {
	if err := t.makeStates(); err != nil {
		return err
	}
	t.classifyDatabases()
	t.declareDomains()
	t.markOneShots()
	for _, st := range t.states {
		if st.db != nil {
			continue
		}
		if err := t.translateService(st); err != nil {
			return err
		}
	}
	return t.finalize()
}

func (t *translator) makeStates() error {
	used := map[string]bool{}
	for _, c := range t.src.Compose.Services {
		key := serviceKey(c.Name)
		for base, n := key, 2; used[key]; n++ {
			key = fmt.Sprintf("%s-%d", base[:min(len(base), 28)], n)
		}
		used[key] = true
		image, err := t.literalImage(c)
		if err != nil {
			return err
		}
		st := &svcState{c: c, key: key}
		st.repo, st.tag = templates.SplitImage(image)
		st.svc = templates.TemplateService{Key: key}
		t.states = append(t.states, st)
		t.byName[c.Name] = st
		if c.ContainerNm != "" {
			t.byName[c.ContainerNm] = st
		}
	}
	return nil
}

func serviceKey(name string) string {
	k := svcKeyRe.ReplaceAllString(strings.ToLower(strings.ReplaceAll(name, "_", "-")), "-")
	k = strings.Trim(k, "-")
	if k == "" {
		k = "app"
	}
	if len(k) > 31 {
		k = strings.TrimRight(k[:31], "-")
	}
	return k
}

// literalImage resolves ${VAR:-default} in an image reference to its default.
func (t *translator) literalImage(c *csvc) (string, error) {
	img := c.Image
	for range 4 {
		if !strings.Contains(img, "$") {
			break
		}
		img = defaultRe.ReplaceAllString(img, "$1")
	}
	if strings.Contains(img, "$") {
		return "", skipf("image reference depends on a variable: %s (service %s)", c.Image, c.Name)
	}
	if i := strings.Index(img, "@"); i >= 0 {
		img = img[:i]
	}
	return img, nil
}

// defaultRe matches ${VAR:-default} / ${VAR-default} without nested braces.
var defaultRe = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*:?-([^}$]*)\}`)

// ---- managed databases ----------------------------------------------------

var defaultDBVersion = map[string]string{"postgres": "17", "mysql": "8", "mariadb": "11", "redis": "7", "valkey": "9", "mongodb": "8"}

// officialEngine maps an official database image repository to a managed engine.
func officialEngine(repo string) string {
	r := strings.TrimPrefix(repo, "docker.io/")
	r = strings.TrimPrefix(r, "registry-1.docker.io/")
	r = strings.TrimPrefix(r, "library/")
	switch r {
	case "postgres":
		return "postgres"
	case "mysql":
		return "mysql"
	case "mariadb":
		return "mariadb"
	case "redis":
		return "redis"
	case "valkey/valkey":
		return "valkey"
	case "mongo":
		return "mongodb"
	}
	return ""
}

var singleVarRe = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)(?::?-([^}]*))?\}?$`)

func (t *translator) classifyDatabases() {
	for _, st := range t.states {
		eng := officialEngine(st.repo)
		if eng == "" {
			continue
		}
		ver, guessed := dbVersion(eng, st.tag)
		st.dbEng = eng
		st.db = &templates.TemplateDatabase{Engine: eng, Version: ver}
		st.svc.Name = "" // set in assemble
		st.svc.Kind = "database"
		if guessed {
			t.review("services."+st.key+".database.version", fmt.Sprintf("version guessed (source tag %q)", st.tag))
		} else if st.tag != "" {
			t.review("services."+st.key+".database.version", fmt.Sprintf("managed database; source image %s:%s", st.repo, st.tag))
		}
		t.dbCreds(st)
		t.review("services."+st.key, "converted to a managed database; check the app's credentials/connection env uses ${{"+st.key+".DATABASE_*}}")
		if len(st.c.Command) > 0 {
			st.dropped = append(st.dropped, "command")
		}
		if len(st.c.Volumes) > 0 {
			st.dropped = append(st.dropped, "volumes")
		}
	}
}

func dbVersion(engine, tag string) (string, bool) {
	i := 0
	for i < len(tag) && tag[i] >= '0' && tag[i] <= '9' {
		i++
	}
	if i > 0 {
		return tag[:i], false
	}
	return defaultDBVersion[engine], true
}

// dbCreds registers aliases so that app services referencing the database's
// credential variables use the managed database's exported variables.
func (t *translator) dbCreds(st *svcState) {
	k := st.key
	user := []string{"POSTGRES_USER", "MYSQL_USER", "MARIADB_USER", "MONGO_INITDB_ROOT_USERNAME"}
	pass := []string{"POSTGRES_PASSWORD", "MYSQL_PASSWORD", "MARIADB_PASSWORD", "MONGO_INITDB_ROOT_PASSWORD", "REDIS_PASSWORD", "MYSQL_ROOT_PASSWORD", "MARIADB_ROOT_PASSWORD"}
	name := []string{"POSTGRES_DB", "MYSQL_DATABASE", "MARIADB_DATABASE", "MONGO_INITDB_DATABASE"}
	passVar := "DATABASE_PASSWORD"
	if st.dbEng == "redis" || st.dbEng == "valkey" {
		passVar = "REDIS_PASSWORD"
	}
	set := func(list []string, target string, onlyIfAbsent bool) {
		for _, e := range st.c.Env {
			if !slices.Contains(list, e.Key) {
				continue
			}
			m := singleVarRe.FindStringSubmatch(e.Val)
			if m == nil {
				if target == "DATABASE_NAME" && e.Val != "" && st.db.DatabaseName == "" && !strings.Contains(e.Val, "$") {
					st.db.DatabaseName = e.Val
				}
				continue
			}
			if _, ok := t.aliases[m[1]]; ok && onlyIfAbsent {
				continue
			}
			t.aliases[m[1]] = "${{" + k + "." + target + "}}"
			if target == "DATABASE_NAME" && m[2] != "" && st.db.DatabaseName == "" {
				st.db.DatabaseName = m[2]
			}
		}
	}
	set(user, "DATABASE_USER", false)
	set(pass[:5], passVar, false)
	set(pass[5:], passVar, true)
	set(name, "DATABASE_NAME", false)
	if passVar == "REDIS_PASSWORD" {
		args := st.c.Command
		for i, a := range args {
			if a == "--requirepass" && i+1 < len(args) {
				if m := singleVarRe.FindStringSubmatch(args[i+1]); m != nil {
					t.aliases[m[1]] = "${{" + k + ".REDIS_PASSWORD}}"
				}
			}
		}
	} else if st.db.DatabaseName == "" {
		st.db.DatabaseName = strings.ReplaceAll(t.src.ID, "-", "_")
		t.review("services."+st.key+".database.databaseName", "database name guessed from the template id")
	}
	if st.dbEng == "mongodb" {
		t.review("services."+st.key+".database", "mongodb: check that DATABASE_* variables are exported for this engine")
	}
}

// ---- domains ---------------------------------------------------------------

var (
	coolifyURLRe = regexp.MustCompile(`^SERVICE_(URL|FQDN)_(.+?)(?:_(\d+))?$`)
	hostVarRe    = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
)

func isDomainDecl(e envEntry) (id string, port int, ok bool) {
	m := coolifyURLRe.FindStringSubmatch(e.Key)
	if m == nil || (e.HasVal && e.Val != "" && !strings.HasPrefix(e.Val, "/")) {
		return "", 0, false
	}
	fmt.Sscanf(m[3], "%d", &port)
	return m[2], port, true
}

func (t *translator) declareDomain(id, service string, port int) {
	if d, ok := t.domains[id]; ok {
		if d.port == 0 && port != 0 {
			d.port = port
		}
		return
	}
	t.domains[id] = &domainDecl{service: service, port: port}
	t.domOrder = append(t.domOrder, id)
}

// declareDomains registers explicit domain declarations: Coolify's
// SERVICE_URL_*/SERVICE_FQDN_* environment entries and Dokploy's
// [[config.domains]].
func (t *translator) declareDomains() {
	if t.src.Dok != nil {
		for i, d := range t.src.Dok.Domains {
			id := "host" + fmt.Sprint(i)
			if m := hostVarRe.FindStringSubmatch(d.Host); m != nil {
				id = m[1]
				t.dokDom[id] = true
			}
			if _, ok := t.byName[d.Service]; !ok {
				t.err = skipf("config.domains references unknown service %q", d.Service)
				return
			}
			if d.Path != "" && d.Path != "/" {
				t.review("services."+t.byName[d.Service].key+".domains", "upstream routes path "+d.Path+"; path routing is not supported")
			}
			t.declareDomain(id, d.Service, d.Port)
		}
		return
	}
	for _, st := range t.states {
		for _, e := range st.c.Env {
			if id, port, ok := isDomainDecl(e); ok {
				t.declareDomain(id, st.c.Name, port)
			}
		}
	}
}

// markOneShots flags services that other services wait on until they exit,
// plus `restart: no` jobs without ports.
func (t *translator) markOneShots() {
	for _, st := range t.states {
		for _, d := range st.c.DependsOn {
			if d.Condition == "service_completed_successfully" {
				if tgt := t.byName[d.Name]; tgt != nil && tgt.db == nil {
					tgt.oneshot = true
				}
			}
		}
	}
	for _, st := range t.states {
		if st.db == nil && (st.c.Restart == "no" || st.c.Restart == "false") && len(st.c.Ports) == 0 && !t.ownsDeclaredDomain(st) {
			st.oneshot = true
		}
	}
}

func (t *translator) ownsDeclaredDomain(st *svcState) bool {
	for _, d := range t.domains {
		if d.service == st.c.Name {
			return true
		}
	}
	return false
}
