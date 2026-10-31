package projectconfig

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const docsRoot = "../../docs"

func readDoc(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(docsRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var exampleRE = regexp.MustCompile("(?s)<!-- example: ([a-z0-9-]+) -->\\s*\n```toml\n(.*?)\n```")

// Every complete example in the docs is a fixture the validator accepts, and
// the page shows it byte for byte.
func TestDocsExamplesAreFixtures(t *testing.T) {
	seen := 0
	for _, page := range []string{"operations/project-config.md", "reference/nebula-toml.md", "operations/processes.md"} {
		for _, m := range exampleRE.FindAllStringSubmatch(readDoc(t, page), -1) {
			name, block := m[1], m[2]
			fixture, err := os.ReadFile(filepath.Join("testdata", "valid", name+".toml"))
			if err != nil {
				t.Fatalf("%s: example %q has no fixture: %v", page, name, err)
			}
			if strings.TrimRight(string(fixture), "\n") != block {
				t.Errorf("%s: example %q differs from testdata/valid/%s.toml", page, name, name)
			}
			if _, _, diags := Parse(fixture, nil); diags.HasErrors() {
				t.Errorf("fixture %s has errors: %v", name, diags)
			}
			seen++
		}
	}
	if seen != 9 {
		t.Fatalf("found %d examples in the docs, want 9 (one in the guide, four in the reference, four in the processes guide)", seen)
	}
}

var guideRE = regexp.MustCompile(`(?s)<!-- ai-authoring-guide -->\n(.*?)\n<!-- /ai-authoring-guide -->`)

func TestDocsAuthoringGuide(t *testing.T) {
	m := guideRE.FindStringSubmatch(readDoc(t, "operations/project-config.md"))
	if m == nil {
		t.Fatal("the guide has no authoring rules block")
	}
	rules := strings.Split(m[1], "\n")
	if len(rules) != 11 {
		t.Fatalf("authoring rules: got %d, want 11", len(rules))
	}
	for i, rule := range rules {
		if !strings.HasPrefix(rule, strconv.Itoa(i+1)+". ") {
			t.Errorf("rule %d is not numbered in order: %.30q", i+1, rule)
		}
	}
}

var codeRE = regexp.MustCompile(`Code\w+\s*=\s*"([a-z_]+)"`)

// Every code the format core raises is explained in the troubleshooting table.
func TestDocsExplainEveryCode(t *testing.T) {
	src, err := os.ReadFile("diagnostics.go")
	if err != nil {
		t.Fatal(err)
	}
	guide := readDoc(t, "operations/project-config.md")
	codes := codeRE.FindAllStringSubmatch(string(src), -1)
	if len(codes) < 30 {
		t.Fatalf("found only %d codes in diagnostics.go", len(codes))
	}
	for _, m := range codes {
		if !strings.Contains(guide, "`"+m[1]+"`") {
			t.Errorf("the guide does not explain %s", m[1])
		}
	}
}
