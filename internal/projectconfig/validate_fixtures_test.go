package projectconfig

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// t2header returns the values of `# <key>: a, b` lines in the leading
// comment block of a fixture.
func t2header(data []byte, key string) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			break
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if rest, ok := strings.CutPrefix(body, key+":"); ok {
			for _, c := range strings.Split(rest, ",") {
				if c = strings.TrimSpace(c); c != "" {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

func t2invalidFixtures(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "invalid", "*.toml"))
	require.NoError(t, err)
	sort.Strings(paths)
	return paths
}

// warning-only codes: a fixture that expects nothing else is not an error.
var t2warningCodes = []string{CodeUnresolvedReference}

func TestInvalidFixturesAreRejectedWithTheExpectedCodes(t *testing.T) {
	paths := t2invalidFixtures(t)
	require.GreaterOrEqual(t, len(paths), 30, "one fixture per important code")
	covered := map[string]bool{}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(data), "# expect: "), "the first line is the `# expect:` header")
			expect := t2header(data, "expect")
			require.NotEmpty(t, expect)
			require.True(t, strings.HasPrefix(filepath.Base(path), expect[0]), "a fixture is named after its first expected code")
			for _, h := range t2header(data, "schema") {
				require.Equal(t, "reject", h, "the only schema annotation is `# schema: reject`")
			}

			_, _, ds := Parse(data, nil)
			warningOnly := true
			for _, code := range expect {
				covered[code] = true
				require.True(t, ds.HasCode(code), "want %s, got:\n%s", code, ds.RenderText(""))
				if !slices.Contains(t2warningCodes, code) {
					warningOnly = false
				}
			}
			if warningOnly {
				require.False(t, ds.HasErrors(), ds.RenderText(""))
			} else {
				require.True(t, ds.HasErrors(), ds.RenderText(""))
			}
			for _, d := range ds {
				require.NotEmpty(t, d.Message)
				if d.Code != CodeTOMLSyntax {
					require.NotZero(t, d.Line, "%s has no position", d.Render(""))
				}
			}
		})
	}
	t.Run("every static code has a fixture", func(t *testing.T) {
		for _, code := range []string{
			CodeTOMLSyntax, CodeLimitExceeded, CodeSchemaUnsupported, CodeMissingRequired, CodeUnknownKey, CodeWrongType,
			CodeInvalidValue, CodeInvalidSlug, CodeReservedSlug, CodeMissingSource, CodeConflictingSource,
			CodeNotAllowedForKind, CodeDerivedField, CodeUnsetInBase, CodeInvalidUnsetPath, CodeCredentialKey,
			CodeCredentialInURL, CodeLiteralSecret, CodeSecretInFile, CodeSecretHasValue, CodeInvalidReference,
			CodeUnresolvedReference, CodeReferenceCycle, CodeReservedVariable, CodeInvalidTemplate, CodePathEscape,
			CodeAttachmentMissing, CodeLicenseNotAccepted, CodeReplicasRequireStateles, CodeEgressBlocked,
			CodeInvalidUpstream, CodeHostnameInUse, CodeProcessCommandRequired, CodeProcessNameReserved,
			CodeProcessPortConflict, CodeProcessSingletonScale,
		} {
			require.True(t, covered[code], "no testdata/invalid fixture expects %s", code)
		}
	})
}

func TestValidFixturesHaveNoErrors(t *testing.T) {
	for _, path := range t2validFixtures(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.False(t, strings.HasPrefix(string(data), "# expect:"), "valid fixtures carry no expect header")
			f, idx, ds := Parse(data, nil)
			require.NotNil(t, f)
			require.False(t, ds.HasErrors(), "valid fixture has errors:\n%s", ds.RenderText(""))
			allowed := t2header(data, "warn")
			for _, d := range ds {
				require.Equal(t, SeverityWarning, d.Severity, d.Render(""))
				require.Contains(t, allowed, d.Code, "an undeclared warning (write `# warn: %s` first): %s", d.Code, d.Render(""))
			}
			for _, env := range f.Environments() {
				_, rds := f.Resolve(env, idx)
				require.Empty(t, rds)
			}
		})
	}
}

func TestFixtureHeaderHelper(t *testing.T) {
	data := []byte("# expect: a, b\n# schema: reject\n# warn: unresolved_reference\n# text\nschema = 1\n# expect: late\n")
	require.Equal(t, []string{"a", "b"}, t2header(data, "expect"))
	require.Equal(t, []string{"reject"}, t2header(data, "schema"))
	require.Equal(t, []string{"unresolved_reference"}, t2header(data, "warn"))
	require.Empty(t, t2header([]byte("schema = 1\n# expect: x\n"), "expect"))
}
