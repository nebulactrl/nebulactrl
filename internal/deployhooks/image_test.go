package deployhooks

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseImageNormalisesDockerHub(t *testing.T) {
	for _, ref := range []string{"nginx", "library/nginx", "docker.io/library/nginx:1.27", "index.docker.io/library/nginx@sha256:" + hex64("a")} {
		img, err := parseImage(ref)
		require.NoError(t, err, ref)
		require.Equal(t, "docker.io/library/nginx", img.Repo(), ref)
	}
	img, err := parseImage("ghcr.io/Acme/API:v1")
	require.NoError(t, err)
	require.Equal(t, "ghcr.io/acme/api", img.Repo())
	require.Equal(t, "ghcr.io/Acme/API", img.Name, "the user's spelling is kept for the base of an override")
	require.Equal(t, "v1", img.Tag)

	img, err = parseImage("registry.example.com:5000/team/app")
	require.NoError(t, err)
	require.Equal(t, "registry.example.com:5000", img.Host)
	require.Empty(t, img.Tag, "a registry port is not a tag")
	require.Equal(t, "latest", img.effectiveTag())

	for _, bad := range []string{"", "a b", "nginx:", "nginx@sha256:short", "Nginx:bad tag", "-x/y"} {
		_, err := parseImage(bad)
		require.Error(t, err, bad)
	}
}

func hex64(c string) string {
	out := ""
	for len(out) < 64 {
		out += c
	}
	return out
}

func TestResolveOverrideStaysInRepository(t *testing.T) {
	d := "sha256:" + hex64("b")
	cases := []struct {
		name, image, tag, digest string
		want, wantCode           string
	}{
		{"none", "ghcr.io/acme/api:v1", "", "", "", ""},
		{"tag", "ghcr.io/acme/api:v1", "v2", "", "ghcr.io/acme/api:v2", ""},
		{"tag on untagged image", "nginx", "1.27", "", "nginx:1.27", ""},
		{"digest", "ghcr.io/acme/api:v1", "", d, "ghcr.io/acme/api@" + d, ""},
		{"full ref, same repo", "acme/api:v1", "docker.io/acme/api:v3", "", "docker.io/acme/api:v3", ""},
		{"full ref, other repo", "acme/api:v1", "acme/other:v3", "", "", "image_outside_repository"},
		{"full ref, other registry", "acme/api:v1", "ghcr.io/acme/api:v3", "", "", "image_outside_repository"},
		{"digest full ref, other repo", "acme/api:v1", "", "evil/api@" + d, "", "image_outside_repository"},
		{"tag and digest", "acme/api:v1", "v2", d, "", "validation_failed"},
		{"bad tag", "acme/api:v1", "bad tag", "", "", "validation_failed"},
		{"bad digest", "acme/api:v1", "", "sha256:xyz", "", "validation_failed"},
		{"full ref without tag", "acme/api:v1", "acme/api", "", "", "validation_failed"},
	}
	for _, c := range cases {
		got, err := resolveOverride(c.image, c.tag, c.digest)
		if c.wantCode != "" {
			require.NotNil(t, err, c.name)
			require.Equal(t, c.wantCode, err.code, c.name)
			continue
		}
		require.Nil(t, err, c.name)
		require.Equal(t, c.want, got, c.name)
	}
}

func TestParsePayloadShapes(t *testing.T) {
	// Docker Hub: https://docs.docker.com/docker-hub/repos/manage/webhooks/
	hub := parsePayload(http.Header{}, []byte(`{"push_data":{"pushed_at":1417566161,"pusher":"trustedbuilder","tag":"latest"},"repository":{"repo_name":"svendowideit/testhook","namespace":"svendowideit","name":"testhook"}}`))
	require.NotNil(t, hub)
	require.Equal(t, SourceDockerHub, hub.Source)
	require.Equal(t, "docker.io/svendowideit/testhook", hub.Image.Repo())
	require.Equal(t, "latest", hub.Tag)
	require.Empty(t, hub.Ignore)

	// GitHub `package` event, container package with a tag.
	gh := http.Header{"X-Github-Event": {"package"}}
	pkg := parsePayload(gh, []byte(`{"action":"published","package":{"name":"api","namespace":"Acme","package_version":{"container_metadata":{"tag":{"name":"v2","digest":"sha256:x"}}}}}`))
	require.NotNil(t, pkg)
	require.Equal(t, SourceGHCR, pkg.Source)
	require.Equal(t, "ghcr.io/acme/api", pkg.Image.Repo())
	require.Equal(t, "v2", pkg.Tag)

	// The older `registry_package` event keeps the object under its own key.
	old := parsePayload(http.Header{"X-Github-Event": {"registry_package"}}, []byte(`{"action":"updated","registry_package":{"name":"api","namespace":"acme","package_version":{"container_metadata":{"tag":{"name":"v3"}}}}}`))
	require.NotNil(t, old)
	require.Equal(t, "v3", old.Tag)
	require.Equal(t, "ghcr.io/acme/api", old.Image.Repo())

	// Untagged manifests, other actions, non-container packages and pings never deploy.
	for name, p := range map[string]*registryPush{
		"untagged":  parsePayload(gh, []byte(`{"action":"published","package":{"name":"api","namespace":"acme","package_version":{"container_metadata":{"tag":{"name":""}}}}}`)),
		"deleted":   parsePayload(gh, []byte(`{"action":"deleted","package":{"name":"api","namespace":"acme"}}`)),
		"npm":       parsePayload(gh, []byte(`{"action":"published","package":{"name":"lib","namespace":"acme","package_version":{}}}`)),
		"ping":      parsePayload(http.Header{"X-Github-Event": {"ping"}}, []byte(`{"zen":"x"}`)),
		"other evt": parsePayload(http.Header{"X-Github-Event": {"push"}}, []byte(`{"ref":"refs/heads/main"}`)),
	} {
		require.NotNil(t, p, name)
		require.NotEmpty(t, p.Ignore, name)
	}

	// Anything else is a plain call.
	require.Nil(t, parsePayload(http.Header{}, nil))
	require.Nil(t, parsePayload(http.Header{}, []byte(`{"ref":"main"}`)))
	require.Nil(t, parsePayload(http.Header{}, []byte(`not json`)))
}

func TestParsePayloadRefusesMalformedNamesAndTagless(t *testing.T) {
	hub := func(repo, tag string) *registryPush {
		return parsePayload(http.Header{}, []byte(`{"push_data":{"tag":"`+tag+`"},"repository":{"repo_name":"`+repo+`"}}`))
	}
	bad := hub("Not A Repo!!", "v1")
	require.NotNil(t, bad)
	require.Equal(t, SourceDockerHub, bad.Source)
	require.Contains(t, bad.Ignore, "repository name is not valid")

	tagless := hub("acme/api", "")
	require.NotNil(t, tagless)
	require.Equal(t, "acme/api", tagless.Image.Path)
	require.Contains(t, tagless.Ignore, "no tag")

	// A Docker Hub payload without a repository name is not a registry delivery.
	require.Nil(t, parsePayload(http.Header{}, []byte(`{"push_data":{"tag":"v1"},"repository":{"repo_name":""}}`)))

	// GitHub: a package name that cannot be a repository is ignored, not deployed.
	gh := http.Header{"X-Github-Event": {"package"}}
	pkg := parsePayload(gh, []byte(`{"action":"published","package":{"name":"Bad Name!","namespace":"acme","package_version":{"container_metadata":{"tag":{"name":"v1"}}}}}`))
	require.NotNil(t, pkg)
	require.Contains(t, pkg.Ignore, "package name is not valid")
}

func TestEffectiveTag(t *testing.T) {
	for ref, want := range map[string]string{
		"acme/api":                         "latest",
		"acme/api:v1":                      "v1",
		"acme/api:v1@sha256:" + hex64("b"): "",
		"acme/api@sha256:" + hex64("b"):    "",
	} {
		img, err := parseImage(ref)
		require.NoError(t, err, ref)
		require.Equal(t, want, img.effectiveTag(), ref)
	}
}

func TestResolveOverrideNeedsAPlainServiceImage(t *testing.T) {
	got, err := resolveOverride("not a ref", "v2", "")
	require.NotNil(t, err)
	require.Empty(t, got)
	require.Equal(t, "validation_failed", err.code)
	require.Contains(t, err.msg, "not a plain image reference")

	// With neither parameter there is nothing to apply, whatever the image is.
	got, err = resolveOverride("not a ref", "", "")
	require.Nil(t, err)
	require.Empty(t, got)
}
