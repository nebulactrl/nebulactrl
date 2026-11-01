package coolify

import (
	"net/url"
	"regexp"
	"strings"
)

// Reasons a variable is a secret (PlanEnv.SecretReason).
const (
	SecretLocked    = "locked"
	SecretName      = "name"
	SecretValue     = "value"
	SecretReference = "reference"
	SecretChoice    = "choice"
)

// secretKeyRe matches variable names that usually hold credentials. Over-marking
// is cheap (the value is masked in the UI but works the same); under-marking
// would leave a credential readable, so the list is generous.
var secretKeyRe = regexp.MustCompile(`(?i)(SECRET|PASSWORD|PASSWD|PASSPHRASE|TOKEN|PRIVATE|CREDENTIAL|API_?KEY|APIKEY|ACCESS_?KEY|_KEY$|^KEY$|_KEY_|_KEK|^KEK|SALT|SIGNING|ENCRYPTION|COOKIE_?KEY|_DSN$|^DSN$|_PASS$|^PASS$|AUTH_?KEY|WEBHOOK_?URL|BEARER|CERT_?KEY)`)

// notSecretKeyRe are names that match the secret pattern but are identifiers or public values.
var notSecretKeyRe = regexp.MustCompile(`(?i)(PUBLIC_?KEY|_KEY_ID$|KEY_ID$|KEY_NAME$|KEYBOARD|_KEY_PATH$|KEY_FILE$|VAPID_PUBLIC|^APP_KEY_ID)`)

// isSecretKey reports whether a variable name looks like it holds a credential.
func isSecretKey(key string) bool {
	if notSecretKeyRe.MatchString(key) {
		return false
	}
	return secretKeyRe.MatchString(key)
}

// valueHasCredentials reports whether v is a URL with a password in its user info.
func valueHasCredentials(v string) bool {
	if !strings.Contains(v, "://") {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || u.User == nil {
		return false
	}
	_, has := u.User.Password()
	return has
}

// coolifyInjected are variables Coolify adds to containers itself; they are not
// part of the application's configuration.
func coolifyInjected(key string) bool {
	switch {
	case strings.HasPrefix(key, "COOLIFY_"):
		return true
	case strings.HasPrefix(key, "SERVICE_FQDN_"), strings.HasPrefix(key, "SERVICE_URL_"), strings.HasPrefix(key, "SERVICE_NAME_"):
		return true
	case key == "SOURCE_COMMIT":
		return true
	}
	return false
}
