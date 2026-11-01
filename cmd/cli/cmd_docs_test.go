package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// docs/reference/cli.md is generated and committed; it is compiled into the
// in-app docs. A command or flag change without regenerating it fails here.
func TestCLIReferenceIsCurrent(t *testing.T) {
	var got bytes.Buffer
	if err := writeCLIReference(&got, newRoot(&cli{})); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../docs/reference/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Fatal("docs/reference/cli.md is stale: run `go run ./cmd/cli docs --out docs/reference/cli.md` (or `make generate`)")
	}
}

func TestCLIReferenceCoversEveryCommandOnce(t *testing.T) {
	var out bytes.Buffer
	if err := writeCLIReference(&out, newRoot(&cli{})); err != nil {
		t.Fatal(err)
	}
	doc := out.String()
	for _, want := range []string{"## nebulactrl services", "### nebulactrl services deploy", "--wait"} {
		if !strings.Contains(doc, want) {
			t.Errorf("reference lacks %q", want)
		}
	}
	if strings.Contains(doc, "nebulactrl docs") {
		t.Error("the hidden docs command must not appear in the reference")
	}
	if strings.Count(doc, "\n### nebulactrl services deploy\n") != 1 {
		t.Error("services deploy is not documented exactly once")
	}
}
