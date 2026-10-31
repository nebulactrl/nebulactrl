package services

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func imageCfg() Config { return Config{Image: "busybox:1.37", Port: 8080}.WithDefaults() }

func TestFileMountValidation(t *testing.T) {
	ok := imageCfg()
	ok.Files = []FileMount{{Path: "/etc/app/a.conf", Mode: "0640", Content: "x"}}
	require.NoError(t, ok.Validate(SourceImage))

	for name, f := range map[string]FileMount{
		"relative":  {Path: "etc/a"},
		"traversal": {Path: "/etc/../a"},
		"unclean":   {Path: "/etc//a"},
		"root":      {Path: "/"},
		"bad mode":  {Path: "/a", Mode: "9"},
		"too big":   {Path: "/a", Content: strings.Repeat("x", MaxFileBytes+1)},
	} {
		c := imageCfg()
		c.Files = []FileMount{f}
		require.Error(t, c.Validate(SourceImage), name)
	}

	dup := imageCfg()
	dup.Files = []FileMount{{Path: "/a"}, {Path: "/a"}}
	require.Error(t, dup.Validate(SourceImage))
}

func TestExecHealthcheckNeedsCommand(t *testing.T) {
	c := imageCfg()
	c.Healthcheck = Healthcheck{Type: HealthExec, IntervalSeconds: 1, TimeoutSeconds: 1, DeadlineSeconds: 1, SuccessThreshold: 1}
	require.Error(t, c.Validate(SourceImage))
	c.Healthcheck.Command = []string{"CMD", "true"}
	require.NoError(t, c.Validate(SourceImage))
	c.Healthcheck.Command = []string{"true"}
	require.Error(t, c.Validate(SourceImage))
}

func TestOneShotHasNoPortAndFilesBlockScaling(t *testing.T) {
	c := Config{Image: "busybox:1.37", OneShot: true}.WithDefaults()
	require.NoError(t, c.Validate(SourceImage))
	c.Port = 80
	require.Error(t, c.Validate(SourceImage))

	withFiles := imageCfg()
	withFiles.Files = []FileMount{{Path: "/a"}}
	ok, why := Eligibility(KindApp, withFiles, 0)
	require.False(t, ok)
	require.Contains(t, why, "config files")
}

func TestFileChangesShowInTheConfigDiff(t *testing.T) {
	a, b := imageCfg(), imageCfg()
	b.Files = []FileMount{{Path: "/a", Content: "x"}}
	b.OneShot = false
	changes := DiffConfig("config", a, b)
	require.NotEmpty(t, changes)
	require.Contains(t, changes[0].Field, "files")
}
