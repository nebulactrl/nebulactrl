package projectconfig

import (
	"regexp"
	"strings"
)

// credentialNameRe flags variable names that look like credentials. It is the
// same pattern as templates.secretKeyRe so the file and the template catalog
// agree on what a credential-looking name is.
var credentialNameRe = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|SECRET|TOKEN|PRIVATE_KEY|API_KEY|ADMIN_KEY|MASTER_KEY|ENCRYPTION_KEY|SALT)`)

// credentialName reports whether a variable name looks like a credential
// (literal_secret applies to literal values of such variables).
func credentialName(name string) bool {
	return credentialNameRe.MatchString(name)
}

// credentialKey reports whether a TOML key name can only ever hold a
// credential, which makes it a credential_key error. Matching is
// case-insensitive and treats "-" as "_".
func credentialKey(key string) bool {
	switch strings.ReplaceAll(strings.ToLower(key), "-", "_") {
	case "token", "password", "passwd", "secret", "secrets_value",
		"registry_auth", "registry_password", "registry_token",
		"git_token", "deploy_key", "ssh_key", "private_key", "api_key",
		"access_token", "auth_token", "basic_auth_users", "webhook_url",
		"cloudflare_token", "credentials":
		return true
	}
	return false
}

// maskLiteral shows at most the first two characters of a rejected literal and
// never the whole value. An empty value stays empty.
func maskLiteral(v string) string {
	if v == "" {
		return ""
	}
	head := make([]rune, 0, 3)
	for _, r := range v {
		head = append(head, r)
		if len(head) == 3 {
			break
		}
	}
	return string(head[:len(head)-1]) + "••••••"
}

// ScanSecret reports whether s contains something shaped like a credential and
// names the kind of credential. It never returns, logs or otherwise exposes
// the matched text.
//
// Detected: PEM private keys, AWS access key ids, JWTs, bearer tokens, the
// token prefixes of common services (GitHub, GitLab, Slack, Stripe secret
// keys, npm, SendGrid, Google API keys), Slack webhook URLs and `sk-` API
// keys. URLs, UUIDs, hex digests, image digests, ${{…}} references and prose do
// not match.
//
// Every check is a literal search followed by a bounded look at the bytes
// after it, so the scan is linear in len(s) and cannot be driven into
// catastrophic behaviour by its input.
func ScanSecret(s string) (kind string, found bool) {
	for _, c := range secretChecks {
		if c.scan(s) {
			return c.kind, true
		}
	}
	return "", false
}

type secretCheck struct {
	kind string
	scan func(string) bool
}

// prefixRule is a token that starts with a fixed prefix followed by at least
// min bytes of class. The byte before the prefix must not be alphanumeric.
type prefixRule struct {
	kind   string
	prefix string
	class  func(byte) bool
	min    int
	// rightBoundary: the byte after the tail must not be alphanumeric.
	rightBoundary bool
	// accept is an optional extra test on the first min bytes of the tail.
	accept func(string) bool
}

var prefixRules = []prefixRule{
	{kind: "aws_access_key_id", prefix: "AKIA", class: isBase32Upper, min: 16, rightBoundary: true},
	{kind: "aws_access_key_id", prefix: "ASIA", class: isBase32Upper, min: 16, rightBoundary: true},
	{kind: "aws_access_key_id", prefix: "AGPA", class: isBase32Upper, min: 16, rightBoundary: true},
	{kind: "aws_access_key_id", prefix: "AIDA", class: isBase32Upper, min: 16, rightBoundary: true},
	{kind: "aws_access_key_id", prefix: "AROA", class: isBase32Upper, min: 16, rightBoundary: true},
	{kind: "aws_access_key_id", prefix: "ANPA", class: isBase32Upper, min: 16, rightBoundary: true},
	{kind: "aws_access_key_id", prefix: "ANVA", class: isBase32Upper, min: 16, rightBoundary: true},
	{kind: "aws_access_key_id", prefix: "AIPA", class: isBase32Upper, min: 16, rightBoundary: true},
	{kind: "github_token", prefix: "ghp_", class: isAlnum, min: 20},
	{kind: "github_token", prefix: "gho_", class: isAlnum, min: 20},
	{kind: "github_token", prefix: "ghu_", class: isAlnum, min: 20},
	{kind: "github_token", prefix: "ghs_", class: isAlnum, min: 20},
	{kind: "github_token", prefix: "ghr_", class: isAlnum, min: 20},
	{kind: "github_token", prefix: "github_pat_", class: isWordByte, min: 20},
	{kind: "gitlab_token", prefix: "glpat-", class: isURLSafe, min: 20},
	{kind: "slack_token", prefix: "xoxb-", class: isDashAlnum, min: 10},
	{kind: "slack_token", prefix: "xoxa-", class: isDashAlnum, min: 10},
	{kind: "slack_token", prefix: "xoxp-", class: isDashAlnum, min: 10},
	{kind: "slack_token", prefix: "xoxr-", class: isDashAlnum, min: 10},
	{kind: "slack_token", prefix: "xoxs-", class: isDashAlnum, min: 10},
	// Stripe publishable keys (pk_live_) are public by design and not listed.
	{kind: "stripe_secret_key", prefix: "sk_live_", class: isAlnum, min: 10},
	{kind: "stripe_secret_key", prefix: "rk_live_", class: isAlnum, min: 10},
	{kind: "google_api_key", prefix: "AIza", class: isURLSafe, min: 35},
	{kind: "npm_token", prefix: "npm_", class: isAlnum, min: 36},
	// `sk-` API keys. A run of plain lower-case words and hyphens is a slug,
	// not a key: the first 32 bytes need a digit, an upper-case letter or "_".
	{kind: "api_key", prefix: "sk-", class: isURLSafe, min: 32, accept: looksRandom},
}

var secretChecks = buildSecretChecks()

func buildSecretChecks() []secretCheck {
	checks := []secretCheck{
		{"pem_private_key", scanPEM},
		{"jwt", scanJWT},
		{"slack_webhook", scanSlackWebhook},
		{"sendgrid_api_key", scanSendGrid},
		{"bearer_token", scanBearer},
	}
	for _, r := range prefixRules {
		checks = append(checks, secretCheck{r.kind, func(s string) bool { return scanPrefix(s, r) }})
	}
	return checks
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }
func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isAlnum(c byte) bool { return isDigit(c) || isUpper(c) || isLower(c) }

func isWordByte(c byte) bool  { return isAlnum(c) || c == '_' }
func isURLSafe(c byte) bool   { return isAlnum(c) || c == '-' || c == '_' }
func isDashAlnum(c byte) bool { return isAlnum(c) || c == '-' }

func isBase32Upper(c byte) bool { return isUpper(c) || (c >= '2' && c <= '7') }

func isBearerByte(c byte) bool { return isAlnum(c) || strings.IndexByte("-._~+/=", c) >= 0 }

func isUpperDigit(c byte) bool { return isUpper(c) || isDigit(c) }

// looksRandom reports whether t has a digit, an upper-case letter or "_".
func looksRandom(t string) bool {
	for i := range len(t) {
		if c := t[i]; isDigit(c) || isUpper(c) || c == '_' {
			return true
		}
	}
	return false
}

func allBytes(s string, class func(byte) bool) bool {
	for i := range len(s) {
		if !class(s[i]) {
			return false
		}
	}
	return true
}

// runEnd returns the index of the first byte at or after from that is not in class.
func runEnd(s string, from int, class func(byte) bool) int {
	for from < len(s) && class(s[from]) {
		from++
	}
	return from
}

func scanPrefix(s string, r prefixRule) bool {
	for pos := 0; pos < len(s); {
		i := strings.Index(s[pos:], r.prefix)
		if i < 0 {
			return false
		}
		i += pos
		pos = i + 1
		if i > 0 && isAlnum(s[i-1]) {
			continue
		}
		start := i + len(r.prefix)
		end := start
		for end < len(s) && end-start < r.min && r.class(s[end]) {
			end++
		}
		if end-start < r.min {
			continue
		}
		if r.rightBoundary && end < len(s) && isAlnum(s[end]) {
			continue
		}
		if r.accept != nil && !r.accept(s[start:end]) {
			continue
		}
		return true
	}
	return false
}

// scanPEM finds "-----BEGIN [LABEL ]PRIVATE KEY-----" (also "... KEY BLOCK-----").
func scanPEM(s string) bool {
	const begin = "-----BEGIN "
	const window = 96
	for pos := 0; pos < len(s); {
		i := strings.Index(s[pos:], begin)
		if i < 0 {
			return false
		}
		start := pos + i + len(begin)
		pos += i + 1
		end := start + window
		if end > len(s) {
			end = len(s)
		}
		w := s[start:end]
		k := strings.Index(w, "PRIVATE KEY")
		if k < 0 {
			continue
		}
		label := w[:k]
		if label != "" && (label[len(label)-1] != ' ' || !allBytes(label, func(c byte) bool { return isUpperDigit(c) || c == ' ' })) {
			continue
		}
		rest := w[k+len("PRIVATE KEY"):]
		if strings.HasPrefix(rest, "-----") || strings.HasPrefix(rest, " BLOCK-----") {
			return true
		}
	}
	return false
}

// scanJWT finds eyJ… three base64url segments of at least 8 bytes each.
func scanJWT(s string) bool {
	for pos := 0; pos < len(s); {
		i := strings.Index(s[pos:], "eyJ")
		if i < 0 {
			return false
		}
		i += pos
		if i > 0 && isAlnum(s[i-1]) {
			pos = i + 1
			continue
		}
		e1 := runEnd(s, i, isURLSafe)
		if e1-i >= 8 && e1 < len(s) && s[e1] == '.' {
			e2 := runEnd(s, e1+1, isURLSafe)
			if e2-(e1+1) >= 8 && e2 < len(s) && s[e2] == '.' {
				e3 := runEnd(s, e2+1, isURLSafe)
				if e3-(e2+1) >= 8 {
					return true
				}
			}
		}
		// Any later "eyJ" inside this first segment shares its tail and
		// fails the same way, so resume after it (keeps the scan linear).
		pos = e1
	}
	return false
}

// scanSlackWebhook finds https://hooks.slack.com/services/T…/B…/….
func scanSlackWebhook(s string) bool {
	const prefix = "https://hooks.slack.com/services/"
	const window = 128
	for pos := 0; pos < len(s); {
		i := strings.Index(s[pos:], prefix)
		if i < 0 {
			return false
		}
		start := pos + i + len(prefix)
		pos += i + 1
		end := start + window
		if end > len(s) {
			end = len(s)
		}
		parts := strings.SplitN(s[start:end], "/", 3)
		if len(parts) < 3 {
			continue
		}
		if len(parts[0]) < 2 || parts[0][0] != 'T' || !allBytes(parts[0][1:], isUpperDigit) {
			continue
		}
		if len(parts[1]) < 2 || parts[1][0] != 'B' || !allBytes(parts[1][1:], isUpperDigit) {
			continue
		}
		if runEnd(parts[2], 0, isAlnum) >= 8 {
			return true
		}
	}
	return false
}

// scanSendGrid finds SG.<22 base64url>.<43 base64url>.
func scanSendGrid(s string) bool {
	const prefix = "SG."
	for pos := 0; pos < len(s); {
		i := strings.Index(s[pos:], prefix)
		if i < 0 {
			return false
		}
		i += pos
		pos = i + 1
		if i > 0 && isAlnum(s[i-1]) {
			continue
		}
		a := i + len(prefix)
		b := a + 22
		c := b + 1
		d := c + 43
		if d > len(s) || s[b] != '.' {
			continue
		}
		if allBytes(s[a:b], isURLSafe) && allBytes(s[c:d], isURLSafe) {
			return true
		}
	}
	return false
}

// scanBearer finds "Bearer " followed by at least 20 token bytes that are not
// just a lower-case word (prose such as "bearer authentication-scheme").
func scanBearer(s string) bool {
	const need = 20
	for pos := 0; pos < len(s); {
		i := strings.Index(s[pos:], "earer")
		if i < 0 {
			return false
		}
		i += pos
		pos = i + 1
		if i == 0 || (s[i-1] != 'B' && s[i-1] != 'b') {
			continue
		}
		if i >= 2 && isAlnum(s[i-2]) {
			continue
		}
		j := i + len("earer")
		k := j
		for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
			k++
		}
		if k == j {
			continue
		}
		end := runEnd(s, k, isBearerByte)
		if end-k < need {
			continue
		}
		if allBytes(s[k:end], func(c byte) bool { return isLower(c) || c == '-' }) {
			continue
		}
		return true
	}
	return false
}
