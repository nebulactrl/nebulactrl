package varexpr

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseForms(t *testing.T) {
	tpl, err := Parse(`a ${{ db.PW }} b ${{KEY}} c ${{ secret(32, "ab}}c") }} d $${{x}} ${{urlencode(base64(db.PW))}}`)
	require.NoError(t, err)
	var kinds []string
	for _, p := range tpl.Parts {
		switch {
		case p.Expr == nil:
			kinds = append(kinds, "text")
		case p.Expr.Ref != nil:
			kinds = append(kinds, "ref:"+p.Expr.Ref.String())
		default:
			kinds = append(kinds, "call:"+p.Expr.Call.Fn)
		}
	}
	require.Equal(t, []string{"text", "ref:db.PW", "text", "ref:KEY", "text", "call:secret", "text", "call:urlencode"}, kinds)
	// The escape decodes to the literal characters.
	require.Equal(t, " d ${{x}} ", tpl.Parts[6].Text)
	require.Equal(t, []Ref{{NS: "db", Key: "PW"}, {Key: "KEY"}, {NS: "db", Key: "PW"}}, tpl.Refs())
}

func TestParseErrors(t *testing.T) {
	for in, want := range map[string]string{
		`${{`:                                 "expected a variable name",
		`${{ a.b `:                            "expected }}",
		`${{ a.B.C }}`:                        "expected }}",
		`${{ 1x }}`:                           "invalid variable name",
		`${{ Bad.KEY }}`:                      "invalid namespace",
		`${{ secret(1 }}`:                     "expected , or )",
		`${{ secret("a }}`:                    "unterminated string",
		`${{ f(g(h(i(j())))) }}`:              "nest at most",
		`${{ a-b }}`:                          "invalid variable name",
		`${{ secret(99999999999999999999) }}`: "number out of range",
	} {
		_, err := Parse(in)
		var se *SyntaxError
		require.True(t, errors.As(err, &se), in)
		require.Contains(t, se.Message, want, in)
	}
	_, err := Parse(strings.Repeat("${{A}}", MaxExprs+1))
	require.Error(t, err)
	_, err = Parse(strings.Repeat("x", MaxValueLen+1))
	require.Error(t, err)
}

func TestValidate(t *testing.T) {
	ok := []string{`${{secret(32)}}`, `${{secret()}}`, `${{secret(16,hex)}}`, `${{secret(8,"ab")}}`, `${{randomInt(1,6)}}`, `${{randomInt()}}`,
		`${{uuid()}}`, `${{randomBytes(32,hex)}}`, `${{randomBytes()}}`, `${{urlencode(db.PW)}}`, `${{base64(urlencode(PW))}}`, `${{base64("x")}}`}
	for _, s := range ok {
		tpl, err := Parse(s)
		require.NoError(t, err, s)
		_, err = Validate(tpl)
		require.NoError(t, err, s)
	}
	bad := []string{`${{secret(0)}}`, `${{secret(2000)}}`, `${{secret(8,"a")}}`, `${{secret(8,nope)}}`, `${{secret(8,"a b")}}`, `${{secret(8,"a$")}}`,
		`${{secret(1,2,3)}}`, `${{randomInt(5,1)}}`, `${{randomInt("a")}}`, `${{uuid(1)}}`, `${{randomBytes(0)}}`, `${{randomBytes(8,rot13)}}`,
		`${{urlencode(secret(8))}}`, `${{urlencode()}}`, `${{urlencode(1)}}`, `${{nope()}}`, `${{base64(uuid())}}`, `${{randomInt(-9223372036854775808, 9223372036854775807)}}`}
	for _, s := range bad {
		tpl, err := Parse(s)
		if err == nil {
			_, err = Validate(tpl)
		}
		require.Error(t, err, s)
	}
}

func TestMaterializeOnce(t *testing.T) {
	m, err := Materialize(`pre_${{secret(32)}}_${{uuid()}}_${{randomInt(5,5)}}_${{db.PW}}_$${{lit}}_${{urlencode(db.PW)}}`)
	require.NoError(t, err)
	require.True(t, m.Secret)
	require.Len(t, m.Calls, 3)
	require.Equal(t, "secret(32)", m.Calls[0].String())
	require.Equal(t, "randomInt(5, 5)", m.Calls[2].String())
	require.NotContains(t, m.Stored, "secret(")
	require.Contains(t, m.Stored, "_5_${{db.PW}}_$${{lit}}_${{urlencode(db.PW)}}")
	require.True(t, strings.HasPrefix(m.Stored, "pre_"))
	// The stored text re-parses and has no generators left.
	tpl, err := Parse(m.Stored)
	require.NoError(t, err)
	require.False(t, tpl.HasGenerators())

	// Two materialisations differ (randomness) but the shape is the same.
	m2, err := Materialize(`${{secret(32)}}`)
	require.NoError(t, err)
	m3, err := Materialize(`${{secret(32)}}`)
	require.NoError(t, err)
	require.NotEqual(t, m2.Stored, m3.Stored)
	require.Len(t, m2.Stored, 32)
}

func TestWarningsAndDescribeHidesLiterals(t *testing.T) {
	m, err := Materialize(`${{secret(8,"ab")}}`)
	require.NoError(t, err)
	require.Len(t, m.Warnings, 1)
	require.Equal(t, "weak_secret", m.Warnings[0].Code)
	require.Equal(t, "secret(8, 2-char alphabet)", m.Calls[0].String(), "literal alphabets are not echoed")

	m, err = Materialize(`${{secret(4)}}`)
	require.NoError(t, err)
	require.Equal(t, "weak_secret", m.Warnings[0].Code)
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{`${{A}}`, `$${{x}}`, `${{ db.X }}`, `${{secret(32,"a}}b")}}`, `${{urlencode(base64(a.B))}}`, `${{`, `}}`, `${{secret(`, "$$$${{"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		tpl, err := Parse(s)
		if err != nil {
			return
		}
		// Raw parts always reassemble the input.
		var b strings.Builder
		for _, p := range tpl.Parts {
			b.WriteString(p.Raw)
		}
		if b.String() != s {
			t.Fatalf("parts do not reassemble: %q != %q", b.String(), s)
		}
		_, _ = Validate(tpl)
	})
}
