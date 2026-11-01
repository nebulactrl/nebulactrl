package templates

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckInputFormat(t *testing.T) {
	ok := map[string][]string{
		FormatEmail:    {"ops@nebulactrl.test", "a.b+c@sub.example.org"},
		FormatURL:      {"https://example.com", "http://10.0.0.1:8080/path?q=1"},
		FormatHostname: {"example.com", "a-b.c.example.org", "10.1.2.3", "::1", "localhost"},
		FormatNumber:   {"0", "587", "65535"},
		FormatText:     {"anything at all: ✓", ""},
		"":             {"anything"},
	}
	for f, vals := range ok {
		for _, v := range vals {
			require.Empty(t, CheckInputFormat(f, v), "%s %q", f, v)
		}
	}
	bad := map[string][]string{
		FormatEmail:    {"admin", "admin@localhost", "Ops <ops@example.com>", "a@b@c.com", "@example.com"},
		FormatURL:      {"example.com", "ftp://example.com", "https://", "javascript:alert(1)"},
		FormatHostname: {"https://example.com", "example.com/path", "-bad.example.com", "has space.com", strings.Repeat("a", 254)},
		FormatNumber:   {"-1", "1.5", "12ab", "1234567890"},
	}
	for f, vals := range bad {
		for _, v := range vals {
			require.NotEmpty(t, CheckInputFormat(f, v), "%s %q must be refused", f, v)
		}
	}
	// An empty value always fits: `required` is the input's own flag.
	for _, f := range InputFormats {
		require.Empty(t, CheckInputFormat(f, ""))
	}
}

func inputsTemplate(inputLine string) string {
	return strings.Replace(goodTemplate, "  - {key: TITLE, label: Title, default: Hello}", "  - "+inputLine, 1)
}

func TestValidateInputFormat(t *testing.T) {
	cases := map[string]struct{ line, want string }{
		"unknown format":           {"{key: TITLE, label: Title, format: phone}", "format"},
		"default does not fit":     {"{key: TITLE, label: Title, format: email, default: nope}", "default"},
		"secret with a format":     {"{key: TITLE, label: Title, format: email, secret: true}", "plain text"},
		"select with a format":     {"{key: TITLE, label: Title, format: url, options: [a, b]}", "plain text"},
		"email key needs a format": {"{key: ADMIN_EMAIL, label: Admin email, required: true}", "declare format: email"},
		"url key needs a format":   {"{key: OIDC_AUTH_URL, label: Endpoint, required: true}", "declare format: url"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			y := inputsTemplate(tc.line)
			// the template uses ${input:TITLE}: keep it declared
			if !strings.Contains(tc.line, "key: TITLE") {
				y = strings.Replace(y, "TITLE: ${input:TITLE}", "TITLE: ${input:"+keyOf(tc.line)+"}", 1)
			}
			err := Validate(parse(t, y))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}

	good := []string{
		"{key: TITLE, label: Title, format: email, default: ops@example.com}",
		"{key: ADMIN_EMAIL, label: Admin email, required: true, format: email}",
		"{key: ADMIN_EMAIL, label: Admin email, default: admin@example.com}", // has a default: nothing to collect
		"{key: TITLE, label: Title, format: number, default: \"587\"}",
	}
	for _, line := range good {
		y := inputsTemplate(line)
		if !strings.Contains(line, "key: TITLE") {
			y = strings.Replace(y, "TITLE: ${input:TITLE}", "TITLE: ${input:"+keyOf(line)+"}", 1)
		}
		require.NoError(t, Validate(parse(t, y)), line)
	}
}

func keyOf(line string) string {
	_, rest, _ := strings.Cut(line, "key: ")
	k, _, _ := strings.Cut(rest, ",")
	return k
}

func TestResolveInputsChecksFormat(t *testing.T) {
	tpl := &CatalogTemplate{Inputs: []TemplateInput{
		{Key: "ADMIN_EMAIL", Label: "Email", Required: true, Format: FormatEmail},
		{Key: "SITE", Label: "Site", Format: FormatURL, Default: "https://example.com"},
		{Key: "WORKERS", Label: "Workers", Format: FormatNumber},
	}}
	got, err := ResolveInputs(tpl, map[string]string{"ADMIN_EMAIL": "ops@nebulactrl.test"}, nil)
	require.NoError(t, err)
	require.Equal(t, "https://example.com", got.Values["SITE"])

	_, err = ResolveInputs(tpl, map[string]string{"ADMIN_EMAIL": "ops"}, nil)
	require.ErrorContains(t, err, "ADMIN_EMAIL")
	_, err = ResolveInputs(tpl, map[string]string{"ADMIN_EMAIL": "ops@nebulactrl.test", "SITE": "example.com"}, nil)
	require.ErrorContains(t, err, "http")
	_, err = ResolveInputs(tpl, map[string]string{"ADMIN_EMAIL": "ops@nebulactrl.test", "WORKERS": "many"}, nil)
	require.ErrorContains(t, err, "whole number")
	// An empty optional value stays allowed; an empty required one is missing_input.
	_, err = ResolveInputs(tpl, map[string]string{"ADMIN_EMAIL": "", "WORKERS": ""}, nil)
	require.ErrorContains(t, err, "required input")
}
