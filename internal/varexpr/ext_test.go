package varexpr

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseExternalRefs(t *testing.T) {
	tpl, err := Parse(`postgres://app:${{ vault:bao/apps/shop/db#password }}@db/${{urlencode(vault:bao/apps/shop/db#user)}} ${{team.REGION}} ${{base64(vault:inf/prod/backend#TOKEN)}}`)
	require.NoError(t, err)
	require.Equal(t, []ExtRef{
		{Integration: "bao", Path: "apps/shop/db", Key: "password"},
		{Integration: "bao", Path: "apps/shop/db", Key: "user"},
		{Integration: "inf", Path: "prod/backend", Key: "TOKEN"},
	}, tpl.ExtRefs())
	// Ordinary references are untouched by external ones.
	require.Equal(t, []Ref{{NS: "team", Key: "REGION"}}, tpl.Refs())
	require.Equal(t, "vault:bao/apps/shop/db#password", tpl.ExtRefs()[0].String())
	_, err = Validate(tpl)
	require.NoError(t, err)
}

func TestParseExternalRefErrors(t *testing.T) {
	for in, want := range map[string]string{
		`${{vault:bao}}`:                      "expected vault:<integration>/<path>#<key>",
		`${{vault:bao/path}}`:                 "missing #<key>",
		`${{vault:bao/#key}}`:                 "secret path is empty",
		`${{vault:bao/a//b#k}}`:               "empty, . or .. segment",
		`${{vault:bao/../b#k}}`:               "empty, . or .. segment",
		`${{vault:bao/a b#k}}`:                "missing #<key>",
		`${{vault:Bao/a#k}}`:                  "expected vault:<integration>/<path>#<key>",
		`${{vault:bao/a#}}`:                   "key after # is empty",
		`${{vault:bao/a#k$}}`:                 "key contains",
		`${{vault:bao/a%20b#k}}`:              "secret path contains",
		`${{vault.KEY}}`:                      "not vault.KEY",
		`${{urlencode(vault:bao/a#k}}`:        "expected , or )",
		`${{vault:bao/a#k`:                    "expected }}",
		`${{vault:bao/` + longPath() + `#k}}`: "longer than",
	} {
		_, err := Parse(in)
		var se *SyntaxError
		require.True(t, errors.As(err, &se), in)
		require.Contains(t, se.Message, want, in)
	}
}

func longPath() string {
	b := make([]byte, MaxExtPathLen+1)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestExternalRefEscapedIsLiteral(t *testing.T) {
	tpl, err := Parse(`$${{vault:bao/a#k}}`)
	require.NoError(t, err)
	require.Empty(t, tpl.ExtRefs())
	require.Equal(t, "${{vault:bao/a#k}}", tpl.Parts[0].Text)
}

func TestExternalRefInGeneratorRejected(t *testing.T) {
	tpl, err := Parse(`${{secret(vault:bao/a#k)}}`)
	require.NoError(t, err)
	_, err = Validate(tpl)
	require.Error(t, err)
}

func TestParseDopplerRefs(t *testing.T) {
	tpl, err := Parse(`${{doppler:dp/backend/prd#DATABASE_URL}} ${{doppler:pinned#API_KEY}} ${{urlencode(doppler:dp/web/dev_local#PW)}}`)
	require.NoError(t, err)
	refs := tpl.ExtRefs()
	require.Equal(t, []ExtRef{
		{Scheme: DopplerScheme, Integration: "dp", Path: "backend/prd", Key: "DATABASE_URL"},
		{Scheme: DopplerScheme, Integration: "pinned", Path: "", Key: "API_KEY"},
		{Scheme: DopplerScheme, Integration: "dp", Path: "web/dev_local", Key: "PW"},
	}, refs)
	require.True(t, refs[0].Doppler())
	require.Equal(t, "doppler:dp/backend/prd#DATABASE_URL", refs[0].String())
	require.Equal(t, "doppler:pinned#API_KEY", refs[1].String())
	// What String renders parses back to the same reference.
	for _, r := range refs {
		back, err := ParseExtRef(r.String())
		require.NoError(t, err)
		require.Equal(t, r, back)
	}
	_, err = Validate(tpl)
	require.NoError(t, err)

	// Vault references keep an empty scheme, so their JSON form has no scheme field.
	v, err := ParseExtRef("vault:bao/a#k")
	require.NoError(t, err)
	require.Empty(t, v.Scheme)
	require.False(t, v.Doppler())
}

func TestParseDopplerRefErrors(t *testing.T) {
	for in, want := range map[string]string{
		`${{doppler:dp}}`:             "missing #<key>",
		`${{doppler:dp/backend#K}}`:   "the path is the Doppler project and config",
		`${{doppler:dp/a/b/c#K}}`:     "the path is the Doppler project and config",
		`${{doppler:dp//prd#K}}`:      "not empty",
		`${{doppler:dp/../prd#K}}`:    "not empty",
		`${{doppler:dp/a b/prd#K}}`:   "missing #<key>",
		`${{doppler:dp/a%b/prd#K}}`:   "contains",
		`${{doppler:Dp/a/b#K}}`:       "lowercase letters",
		`${{doppler:/a/b#K}}`:         "lowercase letters",
		`${{doppler:dp#}}`:            "key after # is empty",
		`${{doppler:dp#K$}}`:          "key contains",
		`${{doppler:dp/a/b#K`:         "expected }}",
		`${{urlencode(doppler:dp#K}}`: "expected , or )",
	} {
		_, err := Parse(in)
		var se *SyntaxError
		require.True(t, errors.As(err, &se), in)
		require.Contains(t, se.Message, want, in)
	}
}

func TestDopplerEscapedAndServiceNamedDoppler(t *testing.T) {
	tpl, err := Parse(`$${{doppler:dp#K}}`)
	require.NoError(t, err)
	require.Empty(t, tpl.ExtRefs())
	// `doppler.KEY` is an ordinary service reference: only the colon form is a Doppler reference.
	tpl, err = Parse(`${{doppler.KEY}}`)
	require.NoError(t, err)
	require.Empty(t, tpl.ExtRefs())
	require.Equal(t, []Ref{{NS: "doppler", Key: "KEY"}}, tpl.Refs())
}
