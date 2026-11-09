package projectconfig

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const t5Volumes = "[services.web.volumes]\n" +
	"plain = \"/plain\"\n" +
	"dist = { path = \"/dist\", owner = \"10001:10001\" }\n" +
	"uidonly = { path = \"/uid\", owner = \"10001\" }\n" +
	"noowner = { path = \"/noowner\" }\n"

// A volume is a mount path string or a { path, owner } table; both resolve to
// the same field value type, and the string form means no owner.
func TestVolumeStringAndTableForms(t *testing.T) {
	text := t2doc(t2web + t5Volumes)
	f, _, ds := t2parse(t, text)
	require.False(t, ds.HasErrors(), ds.RenderText(""))

	web := f.Services["web"]
	require.Equal(t, map[string]VolumeDef{
		"plain":   {Path: "/plain"},
		"dist":    {Path: "/dist", Owner: "10001:10001"},
		"uidonly": {Path: "/uid", Owner: "10001"},
		"noowner": {Path: "/noowner"},
	}, web.Volumes)

	res, _ := f.view("")
	rs := res.Services["web"]
	require.Equal(t, Volume{Name: "plain", MountPath: "/plain"}, t1Field(t, rs, "volumes.plain").Value)
	require.Equal(t, Volume{Name: "dist", MountPath: "/dist", Owner: "10001:10001"}, t1Field(t, rs, "volumes.dist").Value)
	require.Equal(t, Volume{Name: "noowner", MountPath: "/noowner"}, t1Field(t, rs, "volumes.noowner").Value,
		"a table without an owner is the same value as the string form")
	require.Equal(t, AppliesStaged, t1Field(t, rs, "volumes.dist").Applies)
}

// Setting or changing the owner changes the field's fingerprint, so a plan
// sees it as a change of the volume.
func TestVolumeOwnerIsPartOfTheValue(t *testing.T) {
	a := Fingerprint(Volume{Name: "dist", MountPath: "/dist"})
	b := Fingerprint(Volume{Name: "dist", MountPath: "/dist", Owner: "10001:10001"})
	c := Fingerprint(Volume{Name: "dist", MountPath: "/dist", Owner: "10001:10002"})
	require.NotEqual(t, a, b)
	require.NotEqual(t, b, c)
}

// An environment override replaces a volume entry as a unit, whichever form
// the base and the override use.
func TestVolumeEntryIsAMergeLeaf(t *testing.T) {
	text := t2doc(t2web + "[services.web.volumes]\ndist = \"/dist\"\n\n" +
		"[environments.staging.services.web.volumes]\ndist = { path = \"/dist\", owner = \"10001:10001\" }\n")
	f, _, ds := t2parse(t, text)
	require.False(t, ds.HasErrors(), ds.RenderText(""))

	base, _ := f.view("")
	require.Equal(t, Volume{Name: "dist", MountPath: "/dist"}, t1Field(t, base.Services["web"], "volumes.dist").Value)
	staging, _ := f.view("staging")
	require.Equal(t, Volume{Name: "dist", MountPath: "/dist", Owner: "10001:10001"}, t1Field(t, staging.Services["web"], "volumes.dist").Value)
}

// Render writes the table form only for a volume with an owner, and what it
// writes parses back to the same volumes.
func TestRenderVolumeForms(t *testing.T) {
	want := map[string]VolumeDef{
		"plain": {Path: "/plain"},
		"dist":  {Path: "/dist", Owner: "10001:10001"},
	}
	text := t2doc("[services.web]\nimage = \"nginx:1\"\n\n[services.web.volumes]\nplain = \"/plain\"\ndist = { path = \"/dist\", owner = \"10001:10001\" }\n")
	f, _, ds := t2parse(t, text)
	require.False(t, ds.HasErrors(), ds.RenderText(""))
	require.Equal(t, want, f.Services["web"].Volumes)

	res, _ := f.view("")
	out := string(Render(res, RenderOptions{}))
	require.Contains(t, out, `dist = { path = "/dist", owner = "10001:10001" }`)
	require.Contains(t, out, `plain = "/plain"`)
	require.False(t, strings.Contains(out, `path = "/plain"`), "no table without an owner:\n%s", out)

	again, _, ds := t2parse(t, out)
	require.False(t, ds.HasErrors(), ds.RenderText(""))
	require.Equal(t, want, again.Services["web"].Volumes)
}
