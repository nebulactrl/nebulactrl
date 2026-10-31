package varexpr

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRewriteRefSlug(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		changed        bool
	}{
		{"plain", `${{pg.DATABASE_URL}}`, `${{pg2.DATABASE_URL}}`, true},
		{"embedded text", `postgres://u:${{pg.PW}}@${{pg.NEBULA_PRIVATE_HOST}}:${{pg.PORT}}/x`, `postgres://u:${{pg2.PW}}@${{pg2.NEBULA_PRIVATE_HOST}}:${{pg2.PORT}}/x`, true},
		{"spaces normalised only where changed", `a ${{ pg.PW }} b ${{ other.PW }}`, `a ${{pg2.PW}} b ${{ other.PW }}`, true},
		{"transform", `${{urlencode(pg.PW)}}`, `${{urlencode(pg2.PW)}}`, true},
		{"nested transform", `${{base64(urlencode(pg.PW))}}`, `${{base64(urlencode(pg2.PW))}}`, true},
		{"other namespace untouched", `${{pgx.PW}} ${{other.PW}}`, `${{pgx.PW}} ${{other.PW}}`, false},
		{"escape untouched", `$${{pg.PW}}`, `$${{pg.PW}}`, false},
		{"escape beside ref", `$${{pg.PW}} ${{pg.PW}}`, `$${{pg.PW}} ${{pg2.PW}}`, true},
		{"same-service ident untouched", `${{urlencode(PW)}}`, `${{urlencode(PW)}}`, false},
		{"generator keeps args", `${{secret(32, "a\"b")}} ${{pg.X}}`, `${{secret(32, "a\"b")}} ${{pg2.X}}`, true},
		{"unparsable unchanged", `${{pg.`, `${{pg.`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := RewriteRefSlug(tc.in, "pg", "pg2")
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.changed, changed)
		})
	}
	got, changed := RewriteRefSlug(`${{pg.PW}}`, "pg", "pg")
	require.False(t, changed)
	require.Equal(t, `${{pg.PW}}`, got)
}

func TestRewriteRefSlugResultParses(t *testing.T) {
	in := `${{ urlencode( pg.PW ) }}:${{pg.PORT}}`
	got, changed := RewriteRefSlug(in, "pg", "replica-1")
	require.True(t, changed)
	tpl, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []Ref{{NS: "replica-1", Key: "PW"}, {NS: "replica-1", Key: "PORT"}}, tpl.Refs())
}
