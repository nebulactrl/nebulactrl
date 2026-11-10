package services

import (
	"net/url"
	"regexp"
	"strings"
)

// secretKeyRe matches variable names that usually hold credentials. Over-marking
// is cheap (the value is masked in the UI but works the same); under-marking
// would leave a credential readable, so the list is generous.
var secretKeyRe = regexp.MustCompile(`(?i)(SECRET|PASSWORD|PASSWD|PASSPHRASE|TOKEN|PRIVATE|CREDENTIAL|API_?KEY|APIKEY|ACCESS_?KEY|_KEY$|^KEY$|_KEY_|_KEK|^KEK|SALT|SIGNING|ENCRYPTION|COOKIE_?KEY|_DSN$|^DSN$|_PASS$|^PASS$|AUTH_?KEY|WEBHOOK_?URL|BEARER|CERT_?KEY)`)

// notSecretKeyRe are names that match the secret pattern but are identifiers or public values.
var notSecretKeyRe = regexp.MustCompile(`(?i)(PUBLIC_?KEY|_KEY_ID$|KEY_ID$|KEY_NAME$|KEYBOARD|_KEY_PATH$|KEY_FILE$|VAPID_PUBLIC|^APP_KEY_ID)`)

// LooksSecretKey reports whether a variable name looks like it holds a
// credential. Importers and adoption use it to preselect the secret flag.
func LooksSecretKey(key string) bool {
	if notSecretKeyRe.MatchString(key) {
		return false
	}
	return secretKeyRe.MatchString(key)
}

// ValueHasCredentials reports whether v is a URL with a password in its user info.
func ValueHasCredentials(v string) bool {
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
