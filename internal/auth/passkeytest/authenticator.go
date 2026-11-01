// Package passkeytest is a software WebAuthn authenticator for tests: it answers
// the JSON options the server sends (the shape PublicKeyCredential.parse*FromJSON
// reads) with the JSON a browser's PublicKeyCredential.toJSON() would produce. It
// makes ES256 credentials with "none" attestation, which is what NebulaCtrl asks
// for. It is not for production use.
package passkeytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

var b64 = base64.RawURLEncoding

type credential struct {
	id         []byte
	key        *ecdsa.PrivateKey
	userHandle []byte
	rpID       string
	counter    uint32
}

// Authenticator holds credentials and answers WebAuthn ceremonies.
type Authenticator struct {
	// Origin is written into clientDataJSON, as the browser would.
	Origin string
	// UserVerified sets the UV flag on what the authenticator signs.
	UserVerified bool
	// NoCounter keeps the signature counter at zero, like synced passkeys do.
	NoCounter bool

	creds []*credential
}

// New returns an authenticator that acts for the given page origin.
func New(origin string) *Authenticator {
	return &Authenticator{Origin: origin, UserVerified: true}
}

// SetCounter forces the signature counter of the i-th credential (to test clone
// detection).
func (a *Authenticator) SetCounter(i int, n uint32) { a.creds[i].counter = n }

type creationOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RP        struct {
			ID string `json:"id"`
		} `json:"rp"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		ExcludeCredentials []struct {
			ID string `json:"id"`
		} `json:"excludeCredentials"`
		AuthenticatorSelection struct {
			ResidentKey      string `json:"residentKey"`
			UserVerification string `json:"userVerification"`
		} `json:"authenticatorSelection"`
	} `json:"publicKey"`
}

type requestOptions struct {
	PublicKey struct {
		Challenge        string `json:"challenge"`
		RPID             string `json:"rpId"`
		UserVerification string `json:"userVerification"`
		AllowCredentials []struct {
			ID string `json:"id"`
		} `json:"allowCredentials"`
	} `json:"publicKey"`
}

func clientData(typ, challenge, origin string) ([]byte, error) {
	return json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
}

// Create answers creation options (the JSON of {"publicKey": {...}}) and returns
// the RegistrationResponseJSON.
func (a *Authenticator) Create(options []byte) ([]byte, error) {
	var o creationOptions
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, fmt.Errorf("parse creation options: %w", err)
	}
	pk := o.PublicKey
	if pk.RP.ID == "" || pk.Challenge == "" {
		return nil, errors.New("creation options lack rp.id or challenge")
	}
	userHandle, err := b64.DecodeString(pk.User.ID)
	if err != nil {
		return nil, fmt.Errorf("user.id: %w", err)
	}
	for _, ex := range pk.ExcludeCredentials {
		raw, _ := b64.DecodeString(ex.ID)
		for _, c := range a.creds {
			if string(c.id) == string(raw) {
				return nil, errors.New("InvalidStateError: a credential in excludeCredentials is already on this authenticator")
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	c := &credential{id: make([]byte, 32), key: key, userHandle: userHandle, rpID: pk.RP.ID}
	if _, err := rand.Read(c.id); err != nil {
		return nil, err
	}
	cose, err := cbor.Marshal(map[int]any{
		1: 2, 3: -7, -1: 1,
		-2: padded(key.X.Bytes()),
		-3: padded(key.Y.Bytes()),
	})
	if err != nil {
		return nil, err
	}
	flags := byte(0x01 | 0x40) // user present, attested credential data
	if a.UserVerified {
		flags |= 0x04
	}
	authData := authenticatorData(pk.RP.ID, flags, 0)
	authData = append(authData, make([]byte, 16)...) // AAGUID: all zero
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(c.id)))
	authData = append(authData, c.id...)
	authData = append(authData, cose...)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	if err != nil {
		return nil, err
	}
	cd, err := clientData("webauthn.create", pk.Challenge, a.Origin)
	if err != nil {
		return nil, err
	}
	a.creds = append(a.creds, c)
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(c.id), "rawId": b64.EncodeToString(c.id), "type": "public-key",
		"authenticatorAttachment": "platform",
		"clientExtensionResults":  map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"attestationObject": b64.EncodeToString(att),
			"transports":        []string{"internal"},
		},
	})
}

// Get answers request options and returns the AuthenticationResponseJSON. With an
// allowCredentials list it signs with the first matching credential; without one
// (a discoverable sign-in) it uses the first credential for the relying party.
func (a *Authenticator) Get(options []byte) ([]byte, error) {
	var o requestOptions
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, fmt.Errorf("parse request options: %w", err)
	}
	pk := o.PublicKey
	var c *credential
	switch {
	case len(pk.AllowCredentials) > 0:
	search:
		for _, allowed := range pk.AllowCredentials {
			raw, _ := b64.DecodeString(allowed.ID)
			for _, cand := range a.creds {
				if string(cand.id) == string(raw) && cand.rpID == pk.RPID {
					c = cand
					break search
				}
			}
		}
	default:
		for _, cand := range a.creds {
			if cand.rpID == pk.RPID {
				c = cand
				break
			}
		}
	}
	if c == nil {
		return nil, errors.New("NotAllowedError: no matching credential on this authenticator")
	}
	return a.sign(c, pk.Challenge)
}

func (a *Authenticator) sign(c *credential, challenge string) ([]byte, error) {
	flags := byte(0x01)
	if a.UserVerified {
		flags |= 0x04
	}
	if !a.NoCounter {
		c.counter++
	}
	authData := authenticatorData(c.rpID, flags, c.counter)
	cd, err := clientData("webauthn.get", challenge, a.Origin)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, authData...), hash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, c.key, digest[:])
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(c.id), "rawId": b64.EncodeToString(c.id), "type": "public-key",
		"authenticatorAttachment": "platform",
		"clientExtensionResults":  map[string]any{},
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(authData),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(c.userHandle),
		},
	})
}

func authenticatorData(rpID string, flags byte, counter uint32) []byte {
	h := sha256.Sum256([]byte(rpID))
	out := append([]byte{}, h[:]...)
	out = append(out, flags)
	return binary.BigEndian.AppendUint32(out, counter)
}

func padded(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	return append(make([]byte, 32-len(b)), b...)
}
