package query

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// The fixture is generated from the TypeScript implementation
// (apps/web/src/features/logs/search) and asserted there by parity.test.ts.
type parityFixture struct {
	Lines []struct {
		ID           string `json:"id"`
		Line         string `json:"line"`
		Stream       string `json:"stream"`
		ServiceName  string `json:"serviceName"`
		Replica      string `json:"replica"`
		DeploymentID string `json:"deploymentId"`
	} `json:"lines"`
	Cases []struct {
		Query   string   `json:"query"`
		Matches []string `json:"matches"`
		Errors  []string `json:"errors"`
	} `json:"cases"`
}

func TestParity(t *testing.T) {
	data, err := os.ReadFile("../../../../tests/fixtures/logquery/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx parityFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Lines) < 60 || len(fx.Cases) < 150 {
		t.Fatalf("fixture too small: %d lines, %d cases", len(fx.Lines), len(fx.Cases))
	}
	for _, c := range fx.Cases {
		t.Run(c.Query, func(t *testing.T) {
			q := Parse(c.Query)
			m := q.Matcher()
			got := []string{}
			for _, l := range fx.Lines {
				line := NewLine(Input{Line: l.Line, Stream: l.Stream, ServiceName: l.ServiceName, Replica: l.Replica, DeploymentID: l.DeploymentID})
				if m(line) {
					got = append(got, l.ID)
				}
			}
			if !slices.Equal(got, nonNil(c.Matches)) {
				t.Errorf("matches\n got  %v\n want %v", got, c.Matches)
			}
			msgs := []string{}
			for _, e := range q.Errors() {
				msgs = append(msgs, e.Message)
			}
			if !slices.Equal(msgs, nonNil(c.Errors)) {
				t.Errorf("errors\n got  %q\n want %q", msgs, c.Errors)
			}
		})
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
