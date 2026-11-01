package mcp

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The authoring rules of the project file are published twice, in the docs
// page and in the author_config prompt, and must read the same.
func TestAuthoringGuideIsTheDocsGuide(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "operations", "project-config.md"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?s)<!-- ai-authoring-guide -->\n(.*?)\n<!-- /ai-authoring-guide -->`).FindSubmatch(page)
	require.NotNil(t, m, "the docs page marks its guide with <!-- ai-authoring-guide --> comments")
	require.Equal(t, strings.TrimSpace(string(m[1])), strings.TrimSpace(configAuthoringGuide))
}

func TestAuthoringGuideHasElevenRules(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(configAuthoringGuide), "\n")
	require.Len(t, lines, 11)
	for i, l := range lines {
		require.True(t, strings.HasPrefix(l, strconv.Itoa(i+1)+". "), "rule %d: %q", i+1, l)
	}
	// What an agent must never do is spelled out.
	for _, must := range []string{"Never write secret values", "never deploys", "do not retry around it", "agents cannot enable it", "Unknown keys are errors"} {
		require.Contains(t, configAuthoringGuide, must)
	}
}
