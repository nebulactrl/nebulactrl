// Package releasesign verifies the signatures of NebulaCtrl releases.
//
// Releases are signed in CI with minisign (https://jedisct1.github.io/minisign/,
// secret NEBULACTRL_MINISIGN_KEY, .github/workflows/release.yml). The public key
// is compiled into the control plane and the agent (PublicKey) and written into
// deploy/install/install.sh, which verifies the same signatures in shell. Anything
// that runs as root because a release feed or a mirror said so is verified here
// first: the installer the one-click update runs, and (in the installer and in
// install-agent.sh) the agent binary, the release manifest and the library files.
//
// Only prehashed signatures ("ED": Ed25519 over the BLAKE2b-512 of the file, what
// minisign -S writes by default) are accepted. The legacy format signs the file
// itself, which needs the whole file in memory when verified with OpenSSL, so the
// shell verifier cannot check it and nothing is signed that way.
package releasesign

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/blake2b"
)

// PublicKey is the release signing key (minisign public key, base64). Keep it in
// step with NC_RELEASE_PUBKEY in deploy/install/install.sh (a test checks it).
const PublicKey = "RWSfYAoo0hKcaKeFYcOsofTSgfZdb2GVo/jjE4pfxaDYSi4SSsC6inQP"

// SignatureSuffix is appended to the URL of a signed file to get its signature.
const SignatureSuffix = ".minisig"

// ErrInvalid means the signature does not verify (or is not a signature this
// package accepts). The message never says more than why in one line.
var ErrInvalid = errors.New("release signature is not valid")

const (
	algPrehashed = "ED"
	keyIDLen     = 8
	sigLen       = ed25519.SignatureSize
)

// key is a decoded minisign public key.
type key struct {
	id  [keyIDLen]byte
	pub ed25519.PublicKey
}

func parsePublicKey(s string) (key, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != 2+keyIDLen+ed25519.PublicKeySize || string(raw[:2]) != "Ed" {
		return key{}, errors.New("not a minisign public key")
	}
	var k key
	copy(k.id[:], raw[2:2+keyIDLen])
	k.pub = ed25519.PublicKey(raw[2+keyIDLen:])
	return k, nil
}

// Verify checks that signature (the contents of a .minisig file) is a valid
// minisign signature of data under publicKey (a base64 minisign public key).
// It checks the file signature and the signature over the trusted comment.
func Verify(publicKey string, data, signature []byte) error {
	k, err := parsePublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	lines := strings.Split(strings.ReplaceAll(string(signature), "\r\n", "\n"), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "untrusted comment:") || !strings.HasPrefix(lines[2], "trusted comment:") {
		return fmt.Errorf("%w: not a minisign signature file", ErrInvalid)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(raw) != 2+keyIDLen+sigLen {
		return fmt.Errorf("%w: malformed signature", ErrInvalid)
	}
	if string(raw[:2]) != algPrehashed {
		return fmt.Errorf("%w: only prehashed signatures are accepted", ErrInvalid)
	}
	if !bytes.Equal(raw[2:2+keyIDLen], k.id[:]) {
		return fmt.Errorf("%w: signed with another key", ErrInvalid)
	}
	sig := raw[2+keyIDLen:]
	sum := blake2b.Sum512(data)
	if !ed25519.Verify(k.pub, sum[:], sig) {
		return fmt.Errorf("%w: the file does not match its signature", ErrInvalid)
	}
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(global) != sigLen {
		return fmt.Errorf("%w: malformed trusted comment signature", ErrInvalid)
	}
	trusted := strings.TrimPrefix(lines[2], "trusted comment:")
	trusted = strings.TrimPrefix(trusted, " ")
	if !ed25519.Verify(k.pub, append(append([]byte{}, sig...), trusted...), global) {
		return fmt.Errorf("%w: the trusted comment was altered", ErrInvalid)
	}
	return nil
}

// ValidateSourceURL rejects what a signed download may not come from: anything
// but https, and a URL with credentials (they would reach a process list and the
// logs). Verification protects the content; this keeps the transport honest.
func ValidateSourceURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("the URL is not valid")
	}
	if u.Scheme != "https" {
		return errors.New("the URL must be https://")
	}
	if u.User != nil {
		return errors.New("the URL must not carry credentials")
	}
	return nil
}

// FetchVerified downloads rawURL and rawURL+SignatureSuffix over https, verifies
// the signature under publicKey and returns the file. hc may be nil. maxBytes
// bounds the file; the signature is bounded at 4 KiB.
func FetchVerified(ctx context.Context, hc *http.Client, rawURL, publicKey string, maxBytes int64) ([]byte, error) {
	if err := ValidateSourceURL(rawURL); err != nil {
		return nil, err
	}
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	c := *hc
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("refusing a redirect to a URL that is not https")
		}
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}
	data, err := get(ctx, &c, rawURL, maxBytes)
	if err != nil {
		return nil, err
	}
	sig, err := get(ctx, &c, rawURL+SignatureSuffix, 4<<10)
	if err != nil {
		return nil, fmt.Errorf("no signature: %w", err)
	}
	if err := Verify(publicKey, data, sig); err != nil {
		return nil, err
	}
	return data, nil
}

func get(ctx context.Context, hc *http.Client, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("the URL is not valid")
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("the download is larger than allowed")
	}
	return body, nil
}
