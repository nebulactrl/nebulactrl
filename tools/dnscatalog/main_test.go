package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A trimmed copy of lego v5.5.2 providers/dns/linode/linode.toml and
// gcloud.toml quirks: multi-line strings that contain [brackets] and key-like
// lines, an alias with a trailing comment, quoted keys and file variables.
const sample = `Name = "Linode (v4)"
Description = ''''''
URL = "https://www.linode.com/"
Code = "linode"
Aliases = ["linodev4"] # "linodev4" is for compatibility with v3, must be dropped in the future.
Since = "v1.1.0"

Example = '''
LINODE_TOKEN=xxxxx \
lego run --dns linode -d '*.example.com' -d example.com
'''

Additional = '''
## Description

[Configuration]
FAKE_KEY = "inside a multi-line string"
'''

[Configuration]
  [Configuration.Credentials]
    LINODE_TOKEN = "API token"
    'Application Default Credentials' = "[Documentation](https://cloud.google.com/docs)"
    GCE_SERVICE_ACCOUNT_FILE = "Account file path"
    GCE_SERVICE_ACCOUNT = "Account [docs](https://example.com/x)"
  [Configuration.Additional]
    LINODE_TTL = "The TTL of the TXT record used for the DNS challenge in seconds (Default: 300)"
    AZURE_CLIENT_CERTIFICATE_PATH = "Client certificate path"

[Links]
  API = "https://developers.linode.com/api/v4"
`

func TestParseTOML(t *testing.T) {
	p, err := parseTOML([]byte(sample))
	require.NoError(t, err)
	require.Equal(t, "linode", p.Code)
	require.Equal(t, "Linode (v4)", p.Name)
	require.Equal(t, "https://www.linode.com/", p.URL)
	require.Equal(t, []string{"linodev4"}, p.Aliases)
	require.Equal(t, []env{
		{Name: "LINODE_TOKEN", Desc: "API token"},
		{Name: "GCE_SERVICE_ACCOUNT", Desc: "Account docs"},
		{Name: "LINODE_TTL", Desc: "The TTL of the TXT record used for the DNS challenge in seconds (Default: 300)", Optional: true},
	}, p.Env, "file variables, prose keys and text inside multi-line strings are left out; links become their text")
}

func TestParseTOMLNeedsCodeAndName(t *testing.T) {
	_, err := parseTOML([]byte("URL = \"https://x\"\n"))
	require.Error(t, err)
}

func TestDescribeTrims(t *testing.T) {
	long := ""
	for range 40 {
		long += "word "
	}
	require.LessOrEqual(t, len(describe(long)), 120)
	require.Equal(t, "see docs", describe("see [docs](https://a.b/c)"))
	require.Equal(t, "no ticks", describe("no `ticks`"))
}
