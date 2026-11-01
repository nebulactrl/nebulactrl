package templates_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/templates"
	"github.com/nebulactrl/nebulactrl/internal/templates/testsample"
	catalogfs "github.com/nebulactrl/nebulactrl/templates"
)

func TestSampleInputValueFitsItsFormat(t *testing.T) {
	for _, f := range templates.InputFormats {
		require.Empty(t, templates.CheckInputFormat(f, testsample.SampleInputValue(f)), f)
	}
}

// TestCatalogInputsAreCollectable: every input of the catalog is something the
// install form can collect (text, password, select, or one of the typed
// fields), a required input without a default has a sample value that fits its
// format, and the sample passes the install's own check.
func TestCatalogInputsAreCollectable(t *testing.T) {
	cat, errs := templates.LoadFS(catalogfs.FS, "catalog")
	require.Empty(t, errs)
	typed := 0
	for _, tpl := range cat.Search("", "") {
		for _, in := range tpl.Inputs {
			if in.Format != "" {
				typed++
			}
			if in.Required && in.Default == "" && !in.Secret && len(in.Options) == 0 {
				v := testsample.SampleInputValue(in.Format)
				require.NotEmpty(t, v, "%s.%s", tpl.ID, in.Key)
				require.Empty(t, templates.CheckInputFormat(in.Format, v), "%s.%s", tpl.ID, in.Key)
			}
		}
	}
	require.Positive(t, typed, "no input declares a format")
}
