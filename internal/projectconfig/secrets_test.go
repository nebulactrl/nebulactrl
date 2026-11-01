package projectconfig

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const (
	testJWT = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4ifQ." +
		"SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	testHex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// secretKinds is every kind ScanSecret can return.
var secretKinds = map[string]bool{
	"pem_private_key":   true,
	"aws_access_key_id": true,
	"jwt":               true,
	"bearer_token":      true,
	"github_token":      true,
	"gitlab_token":      true,
	"slack_token":       true,
	"slack_webhook":     true,
	"stripe_secret_key": true,
	"api_key":           true,
	"google_api_key":    true,
	"npm_token":         true,
	"sendgrid_api_key":  true,
}

func TestScanSecretPositive(t *testing.T) {
	awsTail := "ABCDEFGHIJKLMNOP"
	tests := []struct {
		name string
		in   string
		kind string
	}{
		{"pem rsa", "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----\n", "pem_private_key"},
		{"pem pkcs8", "-----BEGIN PRIVATE KEY-----\nMIIE\n", "pem_private_key"},
		{"pem ec", "-----BEGIN EC PRIVATE KEY-----", "pem_private_key"},
		{"pem openssh", "-----BEGIN OPENSSH PRIVATE KEY-----", "pem_private_key"},
		{"pem encrypted", "-----BEGIN ENCRYPTED PRIVATE KEY-----", "pem_private_key"},
		{"pem pgp block", "-----BEGIN PGP PRIVATE KEY BLOCK-----", "pem_private_key"},
		{"pem inside yaml", "key: |\n  -----BEGIN RSA PRIVATE KEY-----\n  MIIE\n", "pem_private_key"},
		{"pem after decoy header", "-----BEGIN CERTIFICATE-----\nMIIC\n-----BEGIN PRIVATE KEY-----", "pem_private_key"},

		{"aws akia", "AKIAIOSFODNN7EXAMPLE", "aws_access_key_id"},
		{"aws in assignment", "aws_access_key_id = AKIAIOSFODNN7EXAMPLE\n", "aws_access_key_id"},
		{"aws in quotes", `"AKIAIOSFODNN7EXAMPLE"`, "aws_access_key_id"},
		{"aws asia", "ASIA" + awsTail, "aws_access_key_id"},
		{"aws agpa", "AGPA" + awsTail, "aws_access_key_id"},
		{"aws aida", "AIDA" + awsTail, "aws_access_key_id"},
		{"aws aroa", "AROA" + awsTail, "aws_access_key_id"},
		{"aws anpa", "ANPA" + awsTail, "aws_access_key_id"},
		{"aws anva", "ANVA" + awsTail, "aws_access_key_id"},
		{"aws aipa", "AIPA" + awsTail, "aws_access_key_id"},
		{"aws digits 2-7", "AKIA234567234567ABCD", "aws_access_key_id"},

		{"jwt", testJWT, "jwt"},
		{"jwt in header text", "token=" + testJWT + " # expires", "jwt"},
		{"jwt under bearer", "Bearer " + testJWT, "jwt"},
		{"jwt after dash", "x-" + testJWT, "jwt"},

		{"bearer", "Authorization: Bearer Abc123Def456Ghi789Jkl012", "bearer_token"},
		{"bearer lowercase scheme", "authorization: bearer Abc123Def456Ghi789Jkl012", "bearer_token"},
		{"bearer opaque token chars", "Bearer ab.cd_ef~gh+ij/kl=mn-op0", "bearer_token"},
		{"bearer lower token with digits", "Bearer abcdefghijklmnopqrstuvwxyz0123456789", "bearer_token"},
		{"bearer tab", "Bearer\tAbc123Def456Ghi789Jkl012", "bearer_token"},

		{"github ghp", "ghp_" + strings.Repeat("aB3", 12), "github_token"},
		{"github gho", "gho_" + strings.Repeat("aB3", 12), "github_token"},
		{"github ghu", "ghu_" + strings.Repeat("aB3", 12), "github_token"},
		{"github ghs", "ghs_" + strings.Repeat("aB3", 12), "github_token"},
		{"github ghr", "ghr_" + strings.Repeat("aB3", 12), "github_token"},
		{"github pat", "github_pat_11ABCDEFG0abcdefghij_klmnopqrstuvwxyz0123456789", "github_token"},
		{"github in env line", "GITHUB_TOKEN=ghp_" + strings.Repeat("aB3", 12), "github_token"},

		{"gitlab", "glpat-abcdefghij0123456789", "gitlab_token"},
		{"gitlab with dash", "glpat-abcde-fghij_0123456789", "gitlab_token"},

		{"slack bot", "xoxb-123456789012-abcdefghijkl", "slack_token"},
		{"slack app", "xoxa-123456789012-abcdefghijkl", "slack_token"},
		{"slack user", "xoxp-123456789012-abcdefghijkl", "slack_token"},

		{"stripe secret", "sk_live_" + "abcdefghijklmnopqrstuvwx", "stripe_secret_key"},
		{"stripe restricted", "rk_live_" + "abcdefghijklmnopqrstuvwx", "stripe_secret_key"},

		{"sk key", "sk-" + "abcdefghijklmnopqrstuvwxyz0123456789ABCD", "api_key"},
		{"sk project key", "sk-proj-" + "aB3dE5gH7jK9mN1pQ3sT5vW7yZ9bC1d", "api_key"},
		{"sk key in quotes", `OPENAI = "sk-` + "abcdefghijklmnopqrstuvwxyzABCDEF" + `"`, "api_key"},

		{"google", "AIza" + strings.Repeat("aB1", 11) + "xy", "google_api_key"},
		{"npm", "npm_" + strings.Repeat("aB3", 12), "npm_token"},

		{"slack webhook", "https://hooks.slack.com/services/T01234567/B01234567/abcdefghijklmnopqrstuvwx", "slack_webhook"},
		{"slack webhook in json", `{"url": "https://hooks.slack.com/services/T0AAAA/B0BBBB/Xyz0123456789"}`, "slack_webhook"},

		{"sendgrid", "SG." + strings.Repeat("aB1", 7) + "x" + "." + strings.Repeat("aB1", 14) + "x", "sendgrid_api_key"},

		{"secret late in long text", strings.Repeat("lorem ipsum dolor sit amet ", 500) + "ghp_" + strings.Repeat("aB3", 12), "github_token"},
		{"second occurrence qualifies", "ghp_short ghp_" + strings.Repeat("aB3", 12), "github_token"},
		{"second jwt candidate qualifies", "eyJabc.def.ghi " + testJWT, "jwt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, found := ScanSecret(tt.in)
			if !found || kind != tt.kind {
				t.Fatalf("ScanSecret = (%q, %v), want (%q, true)", kind, found, tt.kind)
			}
		})
	}
}

func TestScanSecretNegative(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"blank", "  \n\t "},
		{"https url", "https://example.com/path/to/resource?id=123&ref=main"},
		{"git https url", "https://github.com/acme/shop.git"},
		{"git scp url", "git@github.com:acme/shop.git"},
		{"uuid v4", "123e4567-e89b-42d3-a456-426614174000"},
		{"sha256 hex", testHex64},
		{"sha256 digest", "sha256:" + testHex64},
		{"image with digest", "ghcr.io/acme/web@sha256:" + testHex64},
		{"git sha", "9fceb02d0ae598e95dc970b74767f19372d61af8"},
		{"short base64", "aGVsbG8gd29ybGQ="},
		{"base64 sentence", "VGhlIHF1aWNrIGJyb3duIGZveCBqdW1wcyBvdmVyIHRoZSBsYXp5IGRvZw=="},
		{"reference", "${{secret(32)}}"},
		{"cross reference", "${{db.DATABASE_URL}}"},
		{"reference in url", "postgres://${{db.USER}}:${{db.PASSWORD}}@db:5432/app"},
		{"lorem ipsum", "Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua."},
		{"bearer in prose", "The bearer of this letter is known to us."},
		{"bearer scheme prose", "Send a bearer authentication header with each request."},
		{"bearer kebab word", "Bearer internationalization-of-everything"},
		{"bearer reference", "Bearer ${{secret(32)}}"},
		{"bearer short token", "Bearer abc123"},
		{"bearer no space", "Bearerabcdefghijklmnopqrstuvwxyz0123456789"},
		{"embedded bearer", "unbearer Abc123Def456Ghi789Jkl012"},
		{"jwt too short", "eyJabc.def.ghi"},
		{"jwt two segments", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIx"},
		{"jwt embedded in word", "xeyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0.abcdefghijkl"},
		{"eyJ prose", "base64 of a json object starts with eyJ"},
		{"aws prefix prose", "AKIA is the prefix of access key ids"},
		{"aws short", "AKIAIOSFODNN7EXAMPL"},
		{"aws too long", "AKIAIOSFODNN7EXAMPLE1"},
		{"aws embedded", "XAKIAIOSFODNN7EXAMPLE"},
		{"aws lowercase", "akiaiosfodnn7example"},
		{"aws non base32", "AKIA0000000000000000"},
		{"pem certificate", "-----BEGIN CERTIFICATE-----\nMIIC\n-----END CERTIFICATE-----"},
		{"pem public key", "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----"},
		{"pem private keys word", "-----BEGIN PRIVATE KEYS-----"},
		{"pem prose", "a PRIVATE KEY must never be committed"},
		{"github prefix prose", "tokens start with ghp_ and gho_ and so on"},
		{"github short", "ghp_short"},
		{"github embedded", "xghp_" + strings.Repeat("aB3", 12)},
		{"gitlab short", "glpat-abc"},
		{"slack short", "xoxb-short"},
		{"slack unknown family", "xoxc-123456789012-abcdefghijkl"},
		{"stripe publishable", "pk_live_abcdefghijklmnopqrstuvwx"},
		{"stripe test key", "sk_test_abcdefghijklmnopqrstuvwx"},
		{"stripe short", "sk_live_abc"},
		{"sk short", "sk-" + strings.Repeat("aB1", 10) + "a"},
		{"sk inside word", "task-" + "abcdefghijklmnopqrstuvwxyz0123456789ABCD"},
		{"sk kebab slug", "sk-this-is-a-long-kebab-case-slug-name-xx"},
		{"sk prose", "Use sk-learn for machine learning"},
		{"kebab slug containing sk", "disk-usage-monitor-for-all-the-nodes-in-the-cluster-1"},
		{"google short", "AIza" + strings.Repeat("aB1", 11) + "x"},
		{"npm short", "npm_" + strings.Repeat("aB3", 11) + "xy"},
		{"npm env prefix", "npm_config_cache=/tmp/npm"},
		{"slack docs url", "https://hooks.slack.com/docs/guide"},
		{"slack webhook prefix only", "https://hooks.slack.com/services/"},
		{"slack webhook short", "https://hooks.slack.com/services/T012/B012/ab"},
		{"sendgrid short", "SG.short.short"},
		{"sendgrid wrong lengths", "SG." + strings.Repeat("a", 21) + "." + strings.Repeat("b", 43)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if kind, found := ScanSecret(tt.in); found {
				t.Fatalf("ScanSecret(%q) = (%q, true), want not found", tt.in, kind)
			}
		})
	}
}

// TestScanSecretLinear feeds inputs that make a naive scanner quadratic or
// backtrack catastrophically and checks that the scan stays fast.
func TestScanSecretLinear(t *testing.T) {
	const size = 64 << 10
	inputs := map[string]string{
		"eyJ dashes":       strings.Repeat("eyJ-", size/4),
		"eyJ dots":         strings.Repeat("eyJaaaaaaa.", size/11),
		"bearer repeats":   strings.Repeat("Bearer ", size/7),
		"bearer spaces":    "Bearer" + strings.Repeat(" ", size),
		"bearer long word": "Bearer " + strings.Repeat("a", size),
		"aws prefix":       strings.Repeat("AKIA", size/4),
		"sk dashes":        strings.Repeat("sk-", size/3),
		"sk long":          "sk-" + strings.Repeat("a", size),
		"pem headers":      strings.Repeat("-----BEGIN ", size/11),
		"slack prefix":     strings.Repeat("https://hooks.slack.com/services/", size/34),
		"sendgrid prefix":  strings.Repeat("SG.", size/3),
		"github prefix":    strings.Repeat("ghp_", size/4),
		"github tail":      "ghp_" + strings.Repeat("a", size),
		"non utf8":         strings.Repeat("\xff\xfe", size/2),
		"nul bytes":        strings.Repeat("\x00", size),
	}
	start := time.Now()
	for name, in := range inputs {
		kind, found := ScanSecret(in)
		if found && !secretKinds[kind] {
			t.Errorf("%s: unknown kind %q", name, kind)
		}
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("scanning %d adversarial inputs took %v", len(inputs), elapsed)
	}
}

func TestScanSecretDoesNotExposeMatch(t *testing.T) {
	in := "ghp_" + strings.Repeat("aB3", 12)
	kind, found := ScanSecret(in)
	if !found {
		t.Fatal("expected a match")
	}
	if strings.Contains(kind, "aB3") || strings.Contains(kind, "ghp_") {
		t.Fatalf("kind %q leaks the match", kind)
	}
}

func FuzzSecretScan(f *testing.F) {
	seeds := []string{
		"", "plain text", "${{secret(32)}}", testJWT, "Bearer " + testJWT,
		"-----BEGIN RSA PRIVATE KEY-----", "-----BEGIN ", "AKIAIOSFODNN7EXAMPLE",
		"ghp_" + strings.Repeat("aB3", 12), "glpat-abcdefghij0123456789",
		"xoxb-123456789012-abcdefghijkl", "sk_live_abcdefghijklmnopqrstuvwx",
		"sk-abcdefghijklmnopqrstuvwxyz0123456789ABCD", "AIza" + strings.Repeat("aB1", 11) + "xy",
		"npm_" + strings.Repeat("aB3", 12),
		"https://hooks.slack.com/services/T01234567/B01234567/abcdefghijklmnopqrstuvwx",
		"SG." + strings.Repeat("aB1", 7) + "x." + strings.Repeat("aB1", 14) + "x",
		"Bearer Abc123Def456Ghi789Jkl012", "sha256:" + testHex64, "\xff\xfe\x00",
		"eyJ-eyJ-eyJ-", "Bearer ", "SG.", "https://hooks.slack.com/services/",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		kind, found := ScanSecret(s)
		kind2, found2 := ScanSecret(s)
		if kind != kind2 || found != found2 {
			t.Fatalf("not deterministic: (%q,%v) vs (%q,%v)", kind, found, kind2, found2)
		}
		if found != (kind != "") {
			t.Fatalf("found=%v but kind=%q", found, kind)
		}
		if found && !secretKinds[kind] {
			t.Fatalf("unknown kind %q", kind)
		}
	})
}

func TestCredentialName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"DB_PASSWORD", true},
		{"password", true},
		{"PASSWD", true},
		{"STRIPE_SECRET_KEY", true},
		{"API_TOKEN", true},
		{"GITHUB_TOKEN", true},
		{"SSH_PRIVATE_KEY", true},
		{"OPENAI_API_KEY", true},
		{"ADMIN_KEY", true},
		{"MASTER_KEY", true},
		{"ENCRYPTION_KEY", true},
		{"HASH_SALT", true},
		{"MySecret", true},
		{"TOKENIZER_MODEL", true},
		{"DATABASE_URL", false},
		{"PORT", false},
		{"NODE_ENV", false},
		{"APP_NAME", false},
		{"PUBLIC_KEY", false},
		{"KEY", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := credentialName(tt.name); got != tt.want {
				t.Fatalf("credentialName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestCredentialKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"token", true},
		{"password", true},
		{"passwd", true},
		{"secret", true},
		{"secrets_value", true},
		{"registry_auth", true},
		{"registry_password", true},
		{"registry_token", true},
		{"git_token", true},
		{"deploy_key", true},
		{"ssh_key", true},
		{"private_key", true},
		{"api_key", true},
		{"access_token", true},
		{"auth_token", true},
		{"basic_auth_users", true},
		{"webhook_url", true},
		{"cloudflare_token", true},
		{"credentials", true},
		{"Token", true},
		{"GIT-TOKEN", true},
		{"deploy-key", true},
		{"Basic_Auth_Users", true},
		{"username", false},
		{"user", false},
		{"secrets", false},
		{"keys", false},
		{"tokens", false},
		{"registry", false},
		{"url", false},
		{"name", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := credentialKey(tt.key); got != tt.want {
				t.Fatalf("credentialKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestMaskLiteral(t *testing.T) {
	const dots = "••••••"
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"a", dots},
		{"ab", "a" + dots},
		{"abc", "ab" + dots},
		{"supersecret", "su" + dots},
		{"日本語テキスト", "日本" + dots},
		{"é", dots},
		{"éa", "é" + dots},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := maskLiteral(tt.in); got != tt.want {
				t.Fatalf("maskLiteral(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	t.Run("never the whole value", func(t *testing.T) {
		for _, v := range []string{"x", "xy", "xyz", "hunter2", strings.Repeat("k", 1000)} {
			got := maskLiteral(v)
			if strings.Contains(got, v) {
				t.Errorf("maskLiteral(%q) contains the whole value", v)
			}
			if len(v) > 3 && strings.Contains(got, v[:3]) {
				t.Errorf("maskLiteral(%q) = %q shows more than two characters", v, got)
			}
		}
	})
	t.Run("invalid utf8 stays valid", func(t *testing.T) {
		if got := maskLiteral("\xff\xfe\xfdabc"); !utf8.ValidString(got) {
			t.Fatalf("maskLiteral produced invalid UTF-8: %q", got)
		}
	})
}
