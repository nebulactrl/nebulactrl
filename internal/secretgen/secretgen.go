// Package secretgen produces random values for generated variables. It is the
// only source of randomness for secrets: crypto/rand exclusively (math/rand is
// banned in internal/ by a test, see TestNoMathRand). Everything here is pure
// apart from reading the OS entropy source.
package secretgen

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
)

// Limits shared with the template validator.
const (
	MaxSecretLen   = 1024
	MaxBytes       = 1024
	MinAlphabetLen = 2
	MaxAlphabetLen = 128
)

// Alphabet presets addressable by name.
const (
	AlphaAlnum     = "alnum"
	AlphaHex       = "hex"
	AlphaBase64    = "base64"
	AlphaBase64URL = "base64url"
	AlphaDigits    = "digits"
	AlphaLower     = "lower"
)

var presets = map[string]string{
	AlphaAlnum:     "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789",
	AlphaHex:       "0123456789abcdef",
	AlphaBase64:    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/",
	AlphaBase64URL: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_",
	AlphaDigits:    "0123456789",
	AlphaLower:     "abcdefghijklmnopqrstuvwxyz",
}

// Preset returns the characters of a named alphabet.
func Preset(name string) (string, bool) {
	a, ok := presets[name]
	return a, ok
}

// PresetNames lists the alphabet presets.
func PresetNames() []string {
	return []string{AlphaAlnum, AlphaHex, AlphaBase64, AlphaBase64URL, AlphaDigits, AlphaLower}
}

// ValidateAlphabet checks a literal alphabet: 2..128 distinct printable ASCII
// characters, no whitespace, and none of `$ { }` (which would let a generated
// value look like a template expression).
func ValidateAlphabet(a string) error {
	if len(a) < MinAlphabetLen || len(a) > MaxAlphabetLen {
		return fmt.Errorf("alphabet must have %d..%d characters", MinAlphabetLen, MaxAlphabetLen)
	}
	seen := map[byte]bool{}
	for i := range len(a) {
		c := a[i]
		if c <= ' ' || c >= 0x7f {
			return errors.New("alphabet must be printable ASCII without spaces")
		}
		if c == '$' || c == '{' || c == '}' {
			return errors.New("alphabet must not contain $ { }")
		}
		if seen[c] {
			return errors.New("alphabet characters must be distinct")
		}
		seen[c] = true
	}
	return nil
}

// Secret returns n characters drawn uniformly from alphabet (a preset name or a
// literal alphabet). Selection uses rejection sampling, so there is no modulo
// bias.
func Secret(n int, alphabet string) (string, error) {
	if n < 1 || n > MaxSecretLen {
		return "", fmt.Errorf("length must be 1..%d", MaxSecretLen)
	}
	if alphabet == "" {
		alphabet = AlphaAlnum
	}
	chars, ok := presets[alphabet]
	if !ok {
		chars = alphabet
		if err := ValidateAlphabet(chars); err != nil {
			return "", err
		}
	}
	m := len(chars)
	limit := 256 - 256%m // bytes >= limit are rejected
	out := make([]byte, 0, n)
	buf := make([]byte, n+n/2+8)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, chars[int(b)%m])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

// Int returns a uniform integer in [min, max].
func Int(min, max int64) (int64, error) {
	if min > max {
		return 0, errors.New("min must not exceed max")
	}
	span := new(big.Int).Sub(big.NewInt(max), big.NewInt(min))
	span.Add(span, big.NewInt(1))
	if span.BitLen() > 62 {
		return 0, errors.New("range too large")
	}
	v, err := rand.Int(rand.Reader, span)
	if err != nil {
		return 0, err
	}
	return min + v.Int64(), nil
}

// UUID returns a random (v4) UUID.
func UUID() (string, error) {
	u, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// Encodings of Bytes.
const (
	EncHex       = "hex"
	EncBase64    = "base64"
	EncBase64URL = "base64url"
)

// Bytes returns n random bytes encoded as hex, base64 or base64url.
func Bytes(n int, enc string) (string, error) {
	if n < 1 || n > MaxBytes {
		return "", fmt.Errorf("byte count must be 1..%d", MaxBytes)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	switch strings.ToLower(enc) {
	case "", EncBase64:
		return base64.StdEncoding.EncodeToString(b), nil
	case EncHex:
		return hex.EncodeToString(b), nil
	case EncBase64URL:
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	return "", fmt.Errorf("unknown encoding %q (hex, base64, base64url)", enc)
}
