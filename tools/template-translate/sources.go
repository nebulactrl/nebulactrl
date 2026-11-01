package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// source is one upstream template in a catalog-neutral form.
type source struct {
	Kind    string // coolify | dokploy
	ID      string
	Path    string // path inside the upstream repository
	Commit  string
	License string
	Order   int // position in the upstream listing

	Name        string
	Summary     string
	Description string
	Docs        string
	Website     string
	Github      string
	Category    string
	Tags        []string
	Port        int
	Ignore      bool
	AmdOnly     bool

	Compose *composeDoc
	Dok     *dokConfig
	Err     error // set when the upstream files could not be parsed
}

type dokDomain struct {
	Service string
	Port    int
	Host    string
	Path    string
}

type dokMount struct {
	FilePath string
	Content  string
}

// dokConfig is the parsed template.toml.
type dokConfig struct {
	Vars    map[string]string // [variables]
	Env     []envEntry        // [config.env] (table or array form)
	Domains []dokDomain
	Mounts  []dokMount
}

type dokMeta struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Tags        []string `json:"tags"`
	Links       struct {
		Github  string `json:"github"`
		Website string `json:"website"`
		Docs    string `json:"docs"`
	} `json:"links"`
}

// resolveDir picks <dir>/<sub> when it exists, else dir itself.
func resolveDir(dir, sub string) string {
	if st, err := os.Stat(filepath.Join(dir, sub)); err == nil && st.IsDir() {
		return filepath.Join(dir, sub)
	}
	return dir
}

// loadCoolify reads templates/compose/*.yaml.
func loadCoolify(dir, commit string) ([]*source, error) {
	base := resolveDir(dir, filepath.Join("templates", "compose"))
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var out []*source
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(base, e.Name()))
		if err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(e.Name(), ".yaml")
		s, _ := parseCoolify(id, "templates/compose/"+e.Name(), commit, data)
		s.Order = len(out)
		out = append(out, s)
	}
	return out, nil
}

// parseCoolify decodes one Coolify compose template. Parse errors are kept in
// source.Err so a broken file only skips itself.
func parseCoolify(id, path, commit string, data []byte) (*source, error) {
	s := &source{Kind: "coolify", ID: id, Path: path, Commit: commit, License: "Apache-2.0"}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "#") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			break
		}
		k, v, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "#")), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "documentation":
			s.Docs = v
		case "slogan":
			s.Summary = v
		case "category":
			s.Category = v
		case "tags":
			for t := range strings.SplitSeq(v, ",") {
				if t = strings.TrimSpace(t); t != "" {
					s.Tags = append(s.Tags, t)
				}
			}
		case "port":
			s.Port, _ = strconv.Atoi(v)
		case "ignore":
			s.Ignore = v == "true"
		case "amd_only":
			s.AmdOnly = v == "true"
		}
	}
	s.Name = titleFromID(id)
	doc, err := parseCompose(data)
	if err != nil {
		s.Err = err
		return s, err
	}
	s.Compose = doc
	return s, nil
}

// loadDokploy reads blueprints/<id>/{docker-compose.yml,template.toml,meta.json}.
func loadDokploy(dir, commit string) ([]*source, error) {
	base := resolveDir(dir, "blueprints")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var out []*source
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(base, e.Name())
		compose, err1 := os.ReadFile(filepath.Join(d, "docker-compose.yml"))
		tomlData, err2 := os.ReadFile(filepath.Join(d, "template.toml"))
		meta, err3 := os.ReadFile(filepath.Join(d, "meta.json"))
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		s, _ := parseDokploy(e.Name(), "blueprints/"+e.Name(), commit, compose, tomlData, meta)
		s.Order = len(out)
		out = append(out, s)
	}
	return out, nil
}

func parseDokploy(id, path, commit string, compose, tomlData, meta []byte) (*source, error) {
	s := &source{Kind: "dokploy", ID: id, Path: path, Commit: commit, License: "MIT", Name: titleFromID(id)}
	var m dokMeta
	if err := json.Unmarshal(meta, &m); err != nil {
		s.Err = fmt.Errorf("meta.json: %w", err)
		return s, s.Err
	}
	if m.Name != "" {
		s.Name = m.Name
	}
	s.Description = withoutDokployMarketing(strings.TrimSpace(m.Description))
	s.Summary = firstSentence(s.Description)
	s.Docs, s.Website, s.Github = m.Links.Docs, m.Links.Website, m.Links.Github
	s.Tags = m.Tags
	doc, err := parseCompose(compose)
	if err != nil {
		s.Err = err
		return s, err
	}
	s.Compose = doc
	cfg, err := parseDokployConfig(tomlData)
	if err != nil {
		s.Err = fmt.Errorf("template.toml: %w", err)
		return s, s.Err
	}
	s.Dok = cfg
	return s, nil
}

func parseDokployConfig(data []byte) (*dokConfig, error) {
	root, err := parseTOML(string(data))
	if err != nil {
		return nil, err
	}
	cfg := &dokConfig{Vars: map[string]string{}}
	if vars, ok := root["variables"].(map[string]any); ok {
		for k, v := range vars {
			cfg.Vars[k] = scalar(v)
		}
	}
	c, _ := root["config"].(map[string]any)
	switch env := c["env"].(type) {
	case map[string]any:
		for _, k := range sortedKeys(env) {
			cfg.Env = append(cfg.Env, envEntry{Key: k, Val: scalar(env[k]), HasVal: true})
		}
	case []any:
		for _, it := range env {
			k, v, ok := strings.Cut(scalar(it), "=")
			cfg.Env = append(cfg.Env, envEntry{Key: strings.TrimSpace(k), Val: v, HasVal: ok})
		}
	}
	if ds, ok := c["domains"].([]any); ok {
		for _, d := range ds {
			m, _ := d.(map[string]any)
			port, _ := strconv.Atoi(scalar(m["port"]))
			cfg.Domains = append(cfg.Domains, dokDomain{Service: scalar(m["serviceName"]), Port: port, Host: scalar(m["host"]), Path: scalar(m["path"])})
		}
	}
	if ms, ok := c["mounts"].([]any); ok {
		for _, mt := range ms {
			m, _ := mt.(map[string]any)
			cfg.Mounts = append(cfg.Mounts, dokMount{FilePath: scalar(m["filePath"]), Content: scalar(m["content"])})
		}
	}
	return cfg, nil
}

// titleFromID turns "uptime-kuma" into "Uptime Kuma".
func titleFromID(id string) string {
	words := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	return s
}

// normID is the cross-catalog identity of an app: lowercase letters and digits.
func normID(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// withoutDokployMarketing drops sentences that advertise Dokploy itself.
func withoutDokployMarketing(s string) string {
	var keep []string
	for _, sent := range strings.SplitAfter(s, ". ") {
		if !strings.Contains(sent, "Dokploy") {
			keep = append(keep, sent)
		}
	}
	return strings.TrimSpace(strings.Join(keep, ""))
}
