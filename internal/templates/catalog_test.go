package templates_test

import (
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	catalogfs "github.com/nebulactrl/nebulactrl/templates"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/nebulactrl/nebulactrl/internal/templates"
)

// TestCatalogStatic validates every template in the embedded catalog: strict
// schema, pinned tag@digest, a licence, sensible health checks, references.
func TestCatalogStatic(t *testing.T) {
	cat, errs := templates.LoadFS(catalogfs.FS, "catalog")
	for _, err := range errs {
		t.Error(err)
	}
	require.GreaterOrEqual(t, cat.Len(), 1)

	covered := map[string]int{}
	for _, tpl := range cat.Search("", "") {
		covered[tpl.Category]++
		for _, s := range tpl.Services {
			if s.Image != "" {
				require.Regexp(t, `^sha256:[0-9a-f]{64}$`, s.Digest, "%s/%s", tpl.ID, s.Key)
			}
		}
		require.NotEmpty(t, tpl.Upstream.License, tpl.ID)
		if tpl.Upstream.Class == templates.LicenseSourceAvailable {
			require.NotEmpty(t, tpl.Upstream.LicenseURL, tpl.ID)
		}
	}
	t.Logf("%d templates: %v", cat.Len(), covered)
}

// TestCatalogRegistry resolves every pinned digest against its registry
// (manifest check, no pull). It needs network and docker buildx, so it only
// runs with NEBULACTRL_TEMPLATE_REGISTRY_CHECK=1.
func TestCatalogRegistry(t *testing.T) {
	if os.Getenv("NEBULACTRL_TEMPLATE_REGISTRY_CHECK") != "1" {
		t.Skip("set NEBULACTRL_TEMPLATE_REGISTRY_CHECK=1 to verify digests against registries")
	}
	out, err := exec.Command("go", "run", "../../tools/template-pin", "../../templates/catalog").CombinedOutput()
	require.NoError(t, err, strings.TrimSpace(string(out)))
}

// minCatalog is the smallest size the catalog may have; it only goes up.
const minCatalog = 300

func loadCatalog(t *testing.T) *templates.Catalog {
	t.Helper()
	cat, errs := templates.LoadFS(catalogfs.FS, "catalog")
	for _, err := range errs {
		t.Error(err)
	}
	return cat
}

// TestCatalogSize: the catalog has at least 300 templates and every gallery
// category is used.
func TestCatalogSize(t *testing.T) {
	cat := loadCatalog(t)
	require.GreaterOrEqual(t, cat.Len(), minCatalog)
	perCategory := map[string]int{}
	for _, tpl := range cat.Search("", "") {
		perCategory[tpl.Category]++
	}
	for _, c := range templates.Categories {
		require.Positive(t, perCategory[c], "category %s has no template", c)
	}
	require.Len(t, perCategory, len(templates.Categories), "a template uses a category that is not in templates.Categories")
}

var (
	shortCommitRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	// reviewMarkerRe finds what an unfinished translator draft carries.
	reviewMarkerRe = regexp.MustCompile(`REVIEW|DRAFT generated|sha256:0{64}`)
)

// TestCatalogLicensesAndAttribution: every template names its upstream licence
// and class; translated ones carry an origin that points into the upstream
// catalog at a commit and are covered by templates/NOTICE; none is an
// unfinished draft.
func TestCatalogLicensesAndAttribution(t *testing.T) {
	cat := loadCatalog(t)
	notice, err := os.ReadFile("../../templates/NOTICE")
	require.NoError(t, err)
	require.Contains(t, string(notice), "coollabsio/coolify")
	require.Contains(t, string(notice), "Dokploy/templates")
	require.Contains(t, string(notice), "Apache License")
	require.Contains(t, string(notice), "MIT License")

	origins := map[string]int{}
	for _, tpl := range cat.Search("", "") {
		require.NotEmpty(t, tpl.Upstream.License, tpl.ID)
		switch tpl.Upstream.Class {
		case templates.LicensePermissive, templates.LicenseCopyleft, templates.LicenseSourceAvailable, templates.LicenseProprietary:
		default:
			t.Errorf("%s: licence class %q", tpl.ID, tpl.Upstream.Class)
		}
		if tpl.Upstream.Class == templates.LicenseSourceAvailable || tpl.Upstream.Class == templates.LicenseProprietary {
			require.True(t, strings.HasPrefix(tpl.Upstream.LicenseURL, "http"), "%s needs a licenseUrl", tpl.ID)
		}
		if tpl.Origin == nil {
			t.Errorf("%s: origin is required (original, coolify or dokploy)", tpl.ID)
			continue
		}
		origins[tpl.Origin.Type]++
		switch tpl.Origin.Type {
		case templates.OriginCoolify:
			require.True(t, strings.HasPrefix(tpl.Origin.Path, "templates/compose/"), "%s origin.path %q", tpl.ID, tpl.Origin.Path)
			require.Equal(t, "Apache-2.0", tpl.Origin.License, tpl.ID)
			require.Regexp(t, shortCommitRe, tpl.Origin.Commit, tpl.ID)
		case templates.OriginDokploy:
			require.True(t, strings.HasPrefix(tpl.Origin.Path, "blueprints/"), "%s origin.path %q", tpl.ID, tpl.Origin.Path)
			require.Equal(t, "MIT", tpl.Origin.License, tpl.ID)
			require.Regexp(t, shortCommitRe, tpl.Origin.Commit, tpl.ID)
		case templates.OriginOriginal:
		default:
			t.Errorf("%s: origin type %q", tpl.ID, tpl.Origin.Type)
		}
	}
	// The growth came from translated drafts: both catalogs are represented.
	require.Positive(t, origins[templates.OriginCoolify])
	require.Positive(t, origins[templates.OriginDokploy])
	t.Logf("origins: %v", origins)

	// No unfinished draft in the embedded files.
	entries, err := fs.ReadDir(catalogfs.FS, "catalog")
	require.NoError(t, err)
	for _, e := range entries {
		raw, err := fs.ReadFile(catalogfs.FS, "catalog/"+e.Name())
		require.NoError(t, err)
		if m := reviewMarkerRe.Find(raw); m != nil {
			t.Errorf("%s still holds %q (unfinished draft)", e.Name(), m)
		}
	}
}

// TestCatalogPinning: every image service is pinned by tag and by an index
// digest (checked online by TestCatalogRegistry) that is not a placeholder, and
// every service is an image or a managed database.
func TestCatalogPinning(t *testing.T) {
	cat := loadCatalog(t)
	for _, tpl := range cat.Search("", "") {
		for _, s := range tpl.Services {
			if s.Image == "" {
				require.NotNil(t, s.Database, "%s/%s: an image or a database block", tpl.ID, s.Key)
				continue
			}
			require.Regexp(t, `^sha256:[0-9a-f]{64}$`, s.Digest, "%s/%s", tpl.ID, s.Key)
			require.NotEqual(t, "sha256:"+strings.Repeat("0", 64), s.Digest, "%s/%s", tpl.ID, s.Key)
			_, tag := templates.SplitImage(s.Image)
			require.NotEmpty(t, tag, "%s/%s", tpl.ID, s.Key)
		}
	}
}

// TestCatalogSearchFindsEveryTemplateByName: a template is reachable by its
// own name and by its id, and by each tag.
func TestCatalogSearchFindsEveryTemplateByName(t *testing.T) {
	cat := loadCatalog(t)
	for _, tpl := range cat.Search("", "") {
		found := false
		for _, hit := range cat.Find(templates.Filter{Query: tpl.Name}) {
			found = found || hit.ID == tpl.ID
		}
		require.True(t, found, "%s is not found by its name %q", tpl.ID, tpl.Name)
		found = false
		for _, hit := range cat.Find(templates.Filter{Query: tpl.ID, Category: tpl.Category, LicenseClass: tpl.Upstream.Class}) {
			found = found || hit.ID == tpl.ID
		}
		require.True(t, found, "%s is not found by its id", tpl.ID)
	}
}

// TestCatalogDatabaseVersionsAreOffered: a managed database may only ask for an
// engine version the engine catalog offers; otherwise the install fails with
// invalid_database after the plan said it was fine.
func TestCatalogDatabaseVersionsAreOffered(t *testing.T) {
	cat := loadCatalog(t)
	databases := 0
	for _, tpl := range cat.Search("", "") {
		for _, s := range tpl.Services {
			if s.Database == nil {
				continue
			}
			databases++
			e := engines.Lookup(s.Database.Engine)
			require.NotNil(t, e, "%s/%s: unknown engine %q", tpl.ID, s.Key, s.Database.Engine)
			require.NotNil(t, e.Version(s.Database.Version), "%s/%s: %s version %q is not offered (one of %v)",
				tpl.ID, s.Key, s.Database.Engine, s.Database.Version, e.SortedVersionIDs())
		}
	}
	require.Positive(t, databases)
}

// TestCatalogHeavyTemplates: a template that asks for 4 GB of RAM or more is
// marked heavy, so the integration sample leaves it to a manual check, and the
// ones known to download several GB are marked.
func TestCatalogHeavyTemplates(t *testing.T) {
	cat := loadCatalog(t)
	heavy := 0
	for _, tpl := range cat.Search("", "") {
		if tpl.Heavy {
			heavy++
		}
		if tpl.Requires.MemoryMB >= 4096 {
			require.True(t, tpl.Heavy, "%s needs %d MB: mark it `heavy: true`", tpl.ID, tpl.Requires.MemoryMB)
		}
	}
	require.Positive(t, heavy)
	for _, id := range []string{"azuracast", "gitlab-ce", "ollama-with-open-webui", "palworld"} {
		tpl, ok := cat.Get(id)
		require.True(t, ok, id)
		require.True(t, tpl.Heavy, id)
	}
}
