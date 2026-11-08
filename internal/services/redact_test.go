package services

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nebulactrl/nebulactrl/internal/databases/engines"
	"github.com/stretchr/testify/require"
)

func TestRedactorMasksSecretValues(t *testing.T) {
	red := MakeRedactor([]string{"sekret-canary-12345", "short", "abc-123-token", "sekret-canary"})
	require.Equal(t, "password="+Mask+" ok", red("password=sekret-canary-12345 ok"))
	require.Equal(t, Mask+" "+Mask, red("abc-123-token sekret-canary"))
	// Values shorter than 6 characters are left alone.
	require.Equal(t, "short stays", red("short stays"))
	require.NotContains(t, red("x sekret-canary-12345 y"), "canary")
}

func TestRedactorMasksTheJSONEscapedForm(t *testing.T) {
	secret := `pa&ss"w<o>rd\1`
	red := MakeRedactor([]string{secret})
	line, err := json.Marshal(map[string]string{"path": "/x?token=" + secret})
	require.NoError(t, err)
	require.Contains(t, string(line), `pa\u0026ss\"w\u003co\u003erd\\1`, "the line carries the escaped form")
	got := red(string(line))
	require.Equal(t, `{"path":"/x?token=`+Mask+`"}`, got)
	require.Equal(t, "p="+Mask, red("p="+secret), "the raw form is still masked")
}

func TestRedactorMasksTheJSONFormWithoutHTMLEscapes(t *testing.T) {
	secret := `p"a&ss1`
	red := MakeRedactor([]string{secret})
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	require.NoError(t, enc.Encode(map[string]string{"path": "/x?token=" + secret}))
	line := strings.TrimSuffix(buf.String(), "\n")
	require.Contains(t, line, `p\"a&ss1`, "the line carries the form an Encoder with SetEscapeHTML(false) writes")
	require.NotContains(t, line, `\u0026`)
	require.Equal(t, `{"path":"/x?token=`+Mask+`"}`, red(line))
}

func TestProposeLinkVariables(t *testing.T) {
	cfg := Config{Database: &engines.Spec{Engine: engines.Postgres, Version: "17"}}.WithDefaults()
	got := ProposeLinkVariables("postgres", KindDatabase, cfg)
	m := map[string]string{}
	for _, p := range got {
		m[p.Key] = p.Value
	}
	require.Equal(t, "${{postgres.NEBULA_PRIVATE_HOST}}", m["DATABASE_HOST"])
	require.Equal(t, "${{postgres.DATABASE_PORT}}", m["DATABASE_PORT"])
	require.Equal(t, "${{postgres.DATABASE_URL}}", m["DATABASE_URL"], "one reference to the derived export")

	api := ProposeLinkVariables("api", KindApp, Config{Port: 8080})
	require.Equal(t, "API_HOST", api[0].Key)
}

func TestDiffConfigListsChangedFields(t *testing.T) {
	a := Config{Image: "app:1", Port: 80}.WithDefaults()
	b := Config{Image: "app:2", Port: 80, Healthcheck: Healthcheck{Path: "/up"}}.WithDefaults()
	fields := map[string][2]string{}
	for _, c := range DiffConfig("config", a, b) {
		fields[c.Field] = [2]string{c.From, c.To}
	}
	require.Equal(t, [2]string{"app:1", "app:2"}, fields["config.image"])
	require.Equal(t, [2]string{"/", "/up"}, fields["config.healthcheck.path"])
	require.Len(t, fields, 2)
}
