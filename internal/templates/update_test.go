package templates

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	catalogfs "github.com/nebulactrl/nebulactrl/templates"

	"github.com/nebulactrl/nebulactrl/internal/services"
)

func dig(c string) string { return "sha256:" + strings.Repeat(c, 64) }

// baseTemplate is a two-service template (app plus managed database) at 1.0.0.
func baseTemplate() *CatalogTemplate {
	return &CatalogTemplate{
		Schema: 1, ID: "demo", Revision: 1, Name: "Demo", Summary: "Demo app.", Category: CategoryDevtools,
		Links:    TemplateLinks{Website: "https://example.org"},
		Upstream: TemplateUpstream{License: "MIT", Class: LicensePermissive},
		Inputs:   []TemplateInput{{Key: "TITLE", Label: "Title", Default: "Demo"}, {Key: "ADMIN_PASSWORD", Label: "Admin password", Secret: true, Default: "${{secret(20)}}"}},
		Services: []TemplateService{
			{
				Key: "app", Name: "Demo", Kind: "app", Image: "ghcr.io/acme/demo:1.0.0", Digest: dig("a"), Port: 8080,
				Healthcheck: &TemplateHealth{Type: "http", Path: "/health"},
				Env: map[string]string{
					"DATABASE_URL": "${{db.DATABASE_URL}}", "TITLE": "${input:TITLE}", "ADMIN_PASSWORD": "${input:ADMIN_PASSWORD}",
					"LOG_LEVEL": "info", "SESSION_SECRET": "${{secret(32)}}",
				},
				Volumes:   []TemplateVolume{{Name: "data", Path: "/data"}},
				Files:     []TemplateFile{{Path: "/etc/demo/app.conf", Content: "listen = 8080\n"}},
				DependsOn: []string{"db"},
				Resources: &TemplateLimits{MemoryMB: 512},
			},
			{Key: "db", Name: "Demo DB", Kind: "database", Database: &TemplateDatabase{Engine: "postgres", Version: "17", DatabaseName: "demo"}},
		},
	}
}

func clone(t *CatalogTemplate) *CatalogTemplate {
	raw, err := encodeSnapshot(t)
	if err != nil {
		panic(err)
	}
	out, err := decodeSnapshot(raw)
	if err != nil {
		panic(err)
	}
	return out
}

func changeFor(changes []TemplateUpdateChange, field string) *TemplateUpdateChange {
	for i := range changes {
		if changes[i].Field == field {
			return &changes[i]
		}
	}
	return nil
}

func known(old, cur *CatalogTemplate) templateKnown {
	return knownInputs(old, cur, map[string]string{"TITLE": "Demo"})
}

func TestAppVersion(t *testing.T) {
	tpl := baseTemplate()
	require.Equal(t, "1.0.0", AppVersion(tpl))
	// A managed database first, a one-shot job second: the real app wins.
	tpl.Services = []TemplateService{
		{Key: "db", Kind: "database", Database: &TemplateDatabase{Engine: "postgres", Version: "17"}},
		{Key: "migrate", Image: "ghcr.io/acme/demo:0.9", Digest: dig("b"), OneShot: true},
		{Key: "app", Image: "ghcr.io/acme/demo:v2.1.0-alpine", Digest: dig("c")},
	}
	require.Equal(t, "v2.1.0-alpine", AppVersion(tpl))
	require.Equal(t, "", AppVersion(&CatalogTemplate{Services: []TemplateService{{Key: "db", Database: &TemplateDatabase{Engine: "redis", Version: "8"}}}}))
	// Registry ports are not tags.
	require.Equal(t, "", AppVersion(&CatalogTemplate{Services: []TemplateService{{Key: "a", Image: "host:5000/app"}}}))
}

func TestSnapshotRoundTrip(t *testing.T) {
	tpl := baseTemplate()
	raw, err := encodeSnapshot(tpl)
	require.NoError(t, err)
	// The snapshot keeps placeholders and generators, never an input value.
	require.Contains(t, string(raw), "${input:ADMIN_PASSWORD}")
	back, err := decodeSnapshot(raw)
	require.NoError(t, err)
	require.Equal(t, tpl, back)
	_, err = decodeSnapshot([]byte("{"))
	require.Error(t, err)
}

func TestDiffTemplatesNoChange(t *testing.T) {
	old := baseTemplate()
	cur := clone(old)
	cur.Revision = 2 // a metadata-only revision
	cur.Summary = "A different summary."
	cur.Category = CategoryWeb
	cur.Tags = []string{"new"}
	require.Empty(t, diffTemplates(old, cur, known(old, cur)))
}

func TestDiffTemplatesConfig(t *testing.T) {
	old := baseTemplate()
	cur := clone(old)
	cur.Revision = 2
	app := &cur.Services[0]
	app.Image, app.Digest = "ghcr.io/acme/demo:1.1.0", dig("b")
	app.Healthcheck.Path = "/healthz"
	app.Resources.MemoryMB = 1024
	app.Env["LOG_LEVEL"] = "warn"
	app.Env["NEW_FLAG"] = "on"
	delete(app.Env, "TITLE")
	app.Env["SESSION_SECRET"] = "${{secret(64)}}"
	app.Files[0].Content = "listen = 9090\n"
	app.Volumes = append(app.Volumes, TemplateVolume{Name: "cache", Path: "/cache"})

	got := diffTemplates(old, cur, known(old, cur))

	c := changeFor(got, "image")
	require.NotNil(t, c)
	require.Equal(t, ChangeApply, c.Status)
	require.Equal(t, "ghcr.io/acme/demo:1.0.0@sha256:aaaaaaaaaaaa…", c.From)
	require.Equal(t, "ghcr.io/acme/demo:1.1.0@sha256:bbbbbbbbbbbb…", c.To)
	require.Equal(t, "app", c.Service)

	require.Equal(t, "/health", changeFor(got, "healthcheck.path").From)
	require.Equal(t, "/healthz", changeFor(got, "healthcheck.path").To)
	require.Equal(t, "1024", changeFor(got, "memoryMb").To)

	require.Equal(t, ChangeApply, changeFor(got, "env.LOG_LEVEL").Status)
	require.Equal(t, "warn", changeFor(got, "env.LOG_LEVEL").To)
	require.Equal(t, ChangeApply, changeFor(got, "env.NEW_FLAG").Status)
	require.Equal(t, ChangeManual, changeFor(got, "env.TITLE").Status, "a removed variable is never deleted")

	// A secret's text is masked in the diff.
	ss := changeFor(got, "env.SESSION_SECRET")
	require.True(t, ss.Secret)
	require.Empty(t, ss.From)
	require.Empty(t, ss.To)

	f := changeFor(got, "file:/etc/demo/app.conf")
	require.Equal(t, ChangeApply, f.Status)
	require.Contains(t, f.To, "listen = 9090")

	require.Equal(t, ChangeApply, changeFor(got, "volume:cache").Status)
	// The database did not change.
	for _, c := range got {
		require.NotEqual(t, "db", c.Service, c.Field)
	}
}

func TestDiffTemplatesManualItems(t *testing.T) {
	old := baseTemplate()
	cur := clone(old)
	cur.Revision = 2
	cur.Services[1].Database.Version = "18"
	cur.Services[0].Ports = []TemplatePort{{Name: "tcp", Port: 9000, Protocol: "tcp", Publish: true}}
	cur.Services[0].Volumes[0].Path = "/var/data"
	cur.Services = append(cur.Services, TemplateService{Key: "cache", Name: "Cache", Kind: "database", Database: &TemplateDatabase{Engine: "valkey", Version: "9"}})
	cur.Inputs = append(cur.Inputs, TemplateInput{Key: "SMTP_HOST", Label: "SMTP host", Required: true})
	cur.Inputs = cur.Inputs[1:] // TITLE removed

	got := diffTemplates(old, cur, known(old, cur))
	for field, svc := range map[string]string{"database.version": "db", "ports": "app", "volume:data": "app", "service": "cache"} {
		c := changeFor(got, field)
		require.NotNil(t, c, field)
		require.Equal(t, ChangeManual, c.Status, field)
		require.Equal(t, svc, c.Service, field)
		require.NotEmpty(t, c.Reason, field)
	}
	require.Equal(t, ChangeApply, changeFor(got, "input:SMTP_HOST").Status)
	require.Contains(t, changeFor(got, "input:SMTP_HOST").Reason, "asked when you stage")
	require.Equal(t, ChangeManual, changeFor(got, "input:TITLE").Status)

	// A service the new revision dropped.
	cur2 := clone(old)
	cur2.Services = cur2.Services[:1]
	cur2.Services[0].DependsOn = nil
	cur2.Services[0].Env["DATABASE_URL"] = "sqlite:///data/app.db"
	got = diffTemplates(old, cur2, known(old, cur2))
	var removed *TemplateUpdateChange
	for i := range got {
		if got[i].Field == "service" {
			removed = &got[i]
		}
	}
	require.NotNil(t, removed)
	require.Equal(t, "db", removed.Service)
	require.Equal(t, ChangeManual, removed.Status)
}

// live builds the live state of the app service as an install of old would have
// left it, with the users' edits applied by mutate.
func live(old *CatalogTemplate, mutate func(*liveService)) map[string]*liveService {
	app := &old.Services[0]
	vals := map[string]string{"TITLE": "Demo"}
	ls := &liveService{
		Slug: "demo", Cfg: serviceConfig(app, vals).WithDefaults(), Volumes: map[string]string{"data": "/data"},
		Vars: map[string]LiveVariable{
			"DATABASE_URL": {Value: "${{demo-db.DATABASE_URL}}"}, "TITLE": {Value: "Demo"}, "LOG_LEVEL": {Value: "info"},
			"ADMIN_PASSWORD": {Secret: true}, "SESSION_SECRET": {Secret: true},
		},
	}
	if mutate != nil {
		mutate(ls)
	}
	return map[string]*liveService{"app": ls, "db": {Slug: "demo-db"}}
}

func classify(old, cur *CatalogTemplate, l map[string]*liveService) []TemplateUpdateChange {
	k := known(old, cur)
	return classifyChanges(classifyInput{Old: old, Cur: cur, Known: k, Slugs: map[string]string{"app": "demo", "db": "demo-db"}, Live: l},
		diffTemplates(old, cur, k))
}

func upgraded() (old, cur *CatalogTemplate) {
	old = baseTemplate()
	cur = clone(old)
	cur.Revision = 2
	app := &cur.Services[0]
	app.Image, app.Digest = "ghcr.io/acme/demo:1.1.0", dig("b")
	app.Healthcheck.Path = "/healthz"
	app.Env["LOG_LEVEL"] = "warn"
	app.Env["NEW_FLAG"] = "on"
	app.Env["SESSION_SECRET"] = "${{secret(64)}}"
	app.Files[0].Content = "listen = 9090\n"
	return old, cur
}

func TestClassifyUntouchedServiceAppliesEverything(t *testing.T) {
	old, cur := upgraded()
	got := classify(old, cur, live(old, nil))
	for _, field := range []string{"image", "healthcheck.path", "env.LOG_LEVEL", "env.NEW_FLAG", "file:/etc/demo/app.conf"} {
		c := changeFor(got, field)
		require.NotNil(t, c, field)
		require.Equal(t, ChangeApply, c.Status, field+": "+c.Reason)
		require.Equal(t, "demo", c.Slug)
	}
	// A secret already stored is never overwritten.
	require.Equal(t, ChangeKept, changeFor(got, "env.SESSION_SECRET").Status)
	require.Contains(t, changeFor(got, "env.SESSION_SECRET").Reason, "never overwritten")
}

func TestClassifyKeepsWhatTheUserChanged(t *testing.T) {
	old, cur := upgraded()
	got := classify(old, cur, live(old, func(ls *liveService) {
		ls.Cfg.Image = "ghcr.io/me/fork:9@sha256:" + strings.Repeat("e", 64)
		ls.Cfg.Healthcheck.Path = "/ping"
		ls.Vars["LOG_LEVEL"] = LiveVariable{Value: "debug"}
		ls.Cfg.Files[0].Content = "listen = 1\n"
	}))
	for _, field := range []string{"image", "healthcheck.path", "env.LOG_LEVEL", "file:/etc/demo/app.conf"} {
		c := changeFor(got, field)
		require.Equal(t, ChangeKept, c.Status, field)
		require.NotEmpty(t, c.Reason, field)
	}
	// Untouched fields still apply.
	require.Equal(t, ChangeApply, changeFor(got, "env.NEW_FLAG").Status)
}

func TestClassifyAlreadyApplied(t *testing.T) {
	old, cur := upgraded()
	// A retry after a partial update: the service already has the new values.
	got := classify(old, cur, live(old, func(ls *liveService) {
		ls.Cfg = serviceConfig(&cur.Services[0], map[string]string{"TITLE": "Demo"}).WithDefaults()
		ls.Vars["LOG_LEVEL"] = LiveVariable{Value: "warn"}
		ls.Vars["NEW_FLAG"] = LiveVariable{Value: "on"}
	}))
	for _, field := range []string{"image", "healthcheck.path", "env.LOG_LEVEL", "env.NEW_FLAG", "file:/etc/demo/app.conf"} {
		require.Equal(t, ChangeApplied, changeFor(got, field).Status, field)
	}
}

func TestClassifyVariables(t *testing.T) {
	old, cur := upgraded()
	// The user deleted LOG_LEVEL and already created NEW_FLAG with another value.
	got := classify(old, cur, live(old, func(ls *liveService) {
		delete(ls.Vars, "LOG_LEVEL")
		ls.Vars["NEW_FLAG"] = LiveVariable{Value: "off"}
	}))
	require.Equal(t, ChangeKept, changeFor(got, "env.LOG_LEVEL").Status)
	require.Contains(t, changeFor(got, "env.LOG_LEVEL").Reason, "removed")
	require.Equal(t, ChangeKept, changeFor(got, "env.NEW_FLAG").Status)
}

func TestClassifyFilesAndVolumes(t *testing.T) {
	old := baseTemplate()
	cur := clone(old)
	cur.Revision = 2
	app := &cur.Services[0]
	app.Files = append(app.Files, TemplateFile{Path: "/etc/demo/extra.conf", Content: "x"})
	app.Volumes = append(app.Volumes, TemplateVolume{Name: "cache", Path: "/cache"})
	got := classify(old, cur, live(old, nil))
	require.Equal(t, ChangeApply, changeFor(got, "file:/etc/demo/extra.conf").Status)
	require.Equal(t, ChangeApply, changeFor(got, "volume:cache").Status)

	got = classify(old, cur, live(old, func(ls *liveService) {
		ls.Cfg.Files = append(ls.Cfg.Files, services.FileMount{Path: "/etc/demo/extra.conf", Mode: "0644", Content: "mine"})
		ls.Volumes["cache"] = "/elsewhere"
	}))
	require.Equal(t, ChangeKept, changeFor(got, "file:/etc/demo/extra.conf").Status)
	require.Equal(t, ChangeKept, changeFor(got, "volume:cache").Status)

	// A file the new revision drops: removed only when untouched.
	cur2 := clone(old)
	cur2.Services[0].Files = nil
	got = classify(old, cur2, live(old, nil))
	require.Equal(t, ChangeApply, changeFor(got, "file:/etc/demo/app.conf").Status)
	got = classify(old, cur2, live(old, func(ls *liveService) { ls.Cfg.Files[0].Content = "edited" }))
	require.Equal(t, ChangeKept, changeFor(got, "file:/etc/demo/app.conf").Status)
}

func TestClassifyUnknownSecretInput(t *testing.T) {
	old := baseTemplate()
	cur := clone(old)
	cur.Revision = 2
	// The new revision builds a value from the generated admin password: its
	// value is not stored, so the update cannot write it.
	cur.Services[0].Env["ADMIN_URL"] = "https://admin:${input:ADMIN_PASSWORD}@host"
	got := classify(old, cur, live(old, nil))
	c := changeFor(got, "env.ADMIN_URL")
	require.Equal(t, ChangeManual, c.Status)
	require.Contains(t, c.Reason, "stored only as a secret")
}

func TestPatchConfig(t *testing.T) {
	old, cur := upgraded()
	vals := map[string]string{"TITLE": "Demo"}
	live := serviceConfig(&old.Services[0], vals).WithDefaults()
	live.MemoryMB = 777 // the user's own change, not in the patch
	live.StopGraceSeconds = 90
	next := serviceConfig(&cur.Services[0], vals).WithDefaults()

	got, err := patchConfig(live, next, []string{"image", "healthcheck.path"})
	require.NoError(t, err)
	require.Equal(t, next.Image, got.Image)
	require.Equal(t, "/healthz", got.Healthcheck.Path)
	require.Equal(t, 777, got.MemoryMB, "fields outside the patch keep the user's value")
	require.Equal(t, 90, got.StopGraceSeconds)
	require.Equal(t, live.Healthcheck.Type, got.Healthcheck.Type)

	// A field the new revision no longer sets goes back to its default.
	next2 := next
	next2.MemoryMB = 0
	got, err = patchConfig(live, next2, []string{"memoryMb"})
	require.NoError(t, err)
	require.Zero(t, got.MemoryMB)
}

func TestPatchFiles(t *testing.T) {
	cur := []services.FileMount{{Path: "/a", Mode: "0644", Content: "old"}, {Path: "/b", Mode: "0644", Content: "b"}, {Path: "/mine", Content: "m"}}
	next := []services.FileMount{{Path: "/a", Mode: "0644", Content: "new"}, {Path: "/c", Mode: "0600", Content: "c"}}
	changes := []TemplateUpdateChange{
		{Field: "file:/a", Status: ChangeApply},   // changed
		{Field: "file:/b", Status: ChangeApply},   // removed by the template
		{Field: "file:/c", Status: ChangeApply},   // added
		{Field: "file:/mine", Status: ChangeKept}, // never touched
		{Field: "image", Status: ChangeApply},
	}
	got := patchFiles(cur, changes, next)
	require.Equal(t, []services.FileMount{
		{Path: "/a", Mode: "0644", Content: "new"}, {Path: "/mine", Content: "m"}, {Path: "/c", Mode: "0600", Content: "c"},
	}, got)
	require.Equal(t, "old", cur[0].Content, "the input slice is not modified")
}

func TestKnownInputs(t *testing.T) {
	old := baseTemplate()
	cur := clone(old)
	cur.Inputs = append(cur.Inputs,
		TemplateInput{Key: "REGION", Label: "Region", Default: "eu"},
		TemplateInput{Key: "API_TOKEN", Label: "Token", Default: "${{secret(16)}}"})
	k := knownInputs(old, cur, map[string]string{"TITLE": "My title"})
	require.Equal(t, "My title", k.Vals["TITLE"])
	require.Equal(t, "eu", k.Vals["REGION"], "a new input starts at its default")
	require.NotContains(t, k.Vals, "API_TOKEN")
	require.True(t, k.Unknown["ADMIN_PASSWORD"], "an installed secret input has no stored value")
	require.False(t, k.Unknown["TITLE"])
	require.True(t, k.Secret["ADMIN_PASSWORD"])
	require.True(t, k.Secret["API_TOKEN"])
	require.False(t, k.Secret["REGION"])
}

func TestNewInputs(t *testing.T) {
	old := baseTemplate()
	cur := clone(old)
	cur.Inputs = append(cur.Inputs,
		TemplateInput{Key: "SMTP_HOST", Label: "SMTP host", Required: true},
		TemplateInput{Key: "API_TOKEN", Label: "Token", Default: "${{secret(16)}}"})
	got := newInputs(old, cur)
	require.Len(t, got, 2)
	require.Equal(t, "SMTP_HOST", got[0].Key)
	require.True(t, got[0].Required)
	require.True(t, got[1].Generated)
	require.Empty(t, got[1].Value, "a generated value is never shown")
}

func TestCountStatus(t *testing.T) {
	cs := []TemplateUpdateChange{{Status: ChangeApply}, {Status: ChangeApply}, {Status: ChangeKept}, {Status: ChangeManual}}
	require.Equal(t, 2, countStatus(cs, ChangeApply))
	require.Equal(t, 1, countStatus(cs, ChangeKept))
	require.Equal(t, 0, countStatus(cs, ChangeApplied))
}

// TestCatalogSnapshotsRoundTripAndSelfDiff: every catalog template survives the
// snapshot encoding unchanged, and diffing a template with itself reports
// nothing (so a stack installed from it never shows a phantom update).
func TestCatalogSnapshotsRoundTripAndSelfDiff(t *testing.T) {
	cat, errs := LoadFS(catalogfs.FS, "catalog")
	require.Empty(t, errs)
	for _, tpl := range cat.Search("", "") {
		raw, err := encodeSnapshot(tpl)
		require.NoError(t, err, tpl.ID)
		back, err := decodeSnapshot(raw)
		require.NoError(t, err, tpl.ID)
		// Nil and empty slices are the same thing to the diff; compare through JSON.
		again, err := encodeSnapshot(back)
		require.NoError(t, err, tpl.ID)
		require.JSONEq(t, string(raw), string(again), tpl.ID)
		require.Empty(t, diffTemplates(tpl, back, knownInputs(tpl, back, nil)), tpl.ID)
		require.NotEmpty(t, AppVersion(tpl), "%s pins no app version", tpl.ID)
	}
}
