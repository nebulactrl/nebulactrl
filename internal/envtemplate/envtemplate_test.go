package envtemplate

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func TestParse(t *testing.T) {
	src := `# Database
# Postgres connection string
DATABASE_URL=

export SECRET_KEY=changeme
APP_NAME="My App" # the display name
QUOTED='a # b'
TRAILING=value # note
# orphan comment, then a blank line

PORT=3000
DATABASE_URL=ignored-duplicate
not a variable
1BAD=x
=novalue
`
	got := Parse(strings.NewReader(src), 100)
	require.Equal(t, []agentprotocol.EnvVarHint{
		{Key: "DATABASE_URL", Value: "", Comment: "Database Postgres connection string"},
		{Key: "SECRET_KEY", Value: "changeme"},
		{Key: "APP_NAME", Value: "My App"},
		{Key: "QUOTED", Value: "a # b"},
		{Key: "TRAILING", Value: "value"},
		{Key: "PORT", Value: "3000"},
	}, got)
}

func TestParseLimitsAndTruncation(t *testing.T) {
	require.Len(t, Parse(strings.NewReader("A=1\nB=2\nC=3\n"), 2), 2)
	long := Parse(strings.NewReader("K="+strings.Repeat("x", 600)+"\n"), 10)
	require.Len(t, long[0].Value, 512)
	c := Parse(strings.NewReader("# "+strings.Repeat("y", 300)+"\nK=1\n"), 10)
	require.Len(t, c[0].Comment, 200)
}

func TestValidKey(t *testing.T) {
	for _, ok := range []string{"A", "_A", "a1", "DATABASE_URL"} {
		require.True(t, ValidKey(ok), ok)
	}
	for _, bad := range []string{"", "1A", "A-B", "A B", "A.B", strings.Repeat("A", 256)} {
		require.False(t, ValidKey(bad), bad)
	}
}

func TestCleanContext(t *testing.T) {
	for in, want := range map[string]string{"": "", ".": "", "./": "", "app": "app", "./app/": "app", "a//b": "a/b", `app\sub`: "app/sub"} {
		got, err := CleanContext(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"/etc", "..", "../x", "a/../../x", "a/..", "x\x00"} {
		_, err := CleanContext(bad)
		require.ErrorIs(t, err, ErrBadContext, bad)
	}
}

func readFrom(files map[string]string, asked *[]string) ReadFunc {
	return func(p string) ([]byte, bool, error) {
		*asked = append(*asked, p)
		s, ok := files[p]
		return []byte(s), ok, nil
	}
}

func TestScanReadsOnlyTemplateNamesInRootAndContext(t *testing.T) {
	files := map[string]string{
		".env.example":          "A=1\n",
		"app/.env.sample":       "B=2\n",
		"app/.env.dist":         "# nothing but a comment\n",
		".env":                  "REAL_SECRET=hunter2\n",
		".env.local":            "REAL_SECRET=hunter2\n",
		"app/.env.production":   "REAL_SECRET=hunter2\n",
		"other/.env.example":    "C=3\n",
		"app/.env.template":     "D=4\n",
		"app/sub/.env.example":  "E=5\n",
		"unrelated/.env.sample": "F=6\n",
	}
	var asked []string
	got, err := Scan("./app", readFrom(files, &asked))
	require.NoError(t, err)
	require.Equal(t, []string{
		".env.example", ".env.sample", ".env.template", ".env.dist",
		"app/.env.example", "app/.env.sample", "app/.env.template", "app/.env.dist",
	}, asked)
	var paths []string
	for _, f := range got {
		paths = append(paths, f.Path)
	}
	require.Equal(t, []string{".env.example", "app/.env.sample", "app/.env.template"}, paths, "a file without a variable is left out")
}

func TestScanRootOnlyWhenContextIsRoot(t *testing.T) {
	var asked []string
	_, err := Scan(".", readFrom(nil, &asked))
	require.NoError(t, err)
	require.Len(t, asked, len(Files))
}

func TestScanSkipsOversizedAndStopsAtBudget(t *testing.T) {
	var asked []string
	files := map[string]string{
		".env.example": strings.Repeat("A=1\n", agentprotocol.ScanMaxFileBytes/4+10), // over 64 KiB: skipped
		".env.sample":  "B=2\n",
	}
	got, err := Scan("", readFrom(files, &asked))
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, ".env.sample", got[0].Path)

	var b strings.Builder
	for i := range agentprotocol.ScanMaxVars {
		b.WriteString("K" + strconv.Itoa(i) + "=1\n")
	}
	asked = nil
	got, err = Scan("", readFrom(map[string]string{".env.example": b.String(), ".env.sample": "LATE=1\n"}, &asked))
	require.NoError(t, err)
	require.Len(t, got, 1, "the variable budget is spent by the first file")
	require.Len(t, got[0].Vars, agentprotocol.ScanMaxVars)
	require.Equal(t, []string{".env.example"}, asked, "nothing is read once the budget is gone")
}

func TestScanStopsOnReadError(t *testing.T) {
	boom := errors.New("provider down")
	_, err := Scan("", func(string) ([]byte, bool, error) { return nil, false, boom })
	require.ErrorIs(t, err, boom)
	_, err = Scan("../x", func(string) ([]byte, bool, error) { return nil, false, nil })
	require.ErrorIs(t, err, ErrBadContext)
}
