package registries

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5"

	"github.com/nebulactrl/nebulactrl/internal/db"
	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

// Constants of the built-in registry's token authentication. The registry
// container is configured with the same values (deploy/install/instance/registry.yaml).
const (
	// ManagedIssuer is the "iss" of every token and auth.token.issuer.
	ManagedIssuer = "nebulactrl"
	// BuiltinService is the token "service": auth.token.service.
	BuiltinService = "nebulactrl-registry"
	// SigningAlgorithm is the one entry of auth.token.signingalgorithms.
	SigningAlgorithm = "ES256"
	// instanceKind is the instance_services row of the registry.
	instanceKind = "registry"
)

// ManagedSecrets are the registry's signing secrets, sealed in
// instance_services.secrets_enc (kind registry). The private key never leaves
// the control plane; the registry gets only the public JWKS.
type ManagedSecrets struct {
	// RegistryKeyPEM is the PKCS#8 PEM of the ES256 signing key.
	RegistryKeyPEM string `json:"registryKeyPem,omitempty"`
	// RegistryKeyID is the JWK thumbprint of the public key (the "kid" of tokens).
	RegistryKeyID string `json:"registryKeyId,omitempty"`
}

// GenerateManagedSecrets creates an ES256 signing key.
func GenerateManagedSecrets() (ManagedSecrets, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return ManagedSecrets{}, fmt.Errorf("generate registry signing key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return ManagedSecrets{}, err
	}
	kid, err := keyID(&k.PublicKey)
	if err != nil {
		return ManagedSecrets{}, err
	}
	return ManagedSecrets{
		RegistryKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		RegistryKeyID:  kid,
	}, nil
}

func keyID(pub *ecdsa.PublicKey) (string, error) {
	tp, err := (&jose.JSONWebKey{Key: pub}).Thumbprint(crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("key thumbprint: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(tp), nil
}

// signingKey parses the private key.
func (m ManagedSecrets) signingKey() (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(m.RegistryKeyPEM))
	if blk == nil {
		return nil, errors.New("registry signing key is missing")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse registry signing key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("registry signing key is not an ECDSA P-256 key")
	}
	return ec, nil
}

// JWKS renders the public key as the JSON Web Key Set file the registry trusts
// (auth.token.jwks).
func (m ManagedSecrets) JWKS() (string, error) {
	k, err := m.signingKey()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: &k.PublicKey, KeyID: m.RegistryKeyID, Algorithm: SigningAlgorithm, Use: "sig",
	}}})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// SealManagedSecrets seals the signing secrets for instance_services.secrets_enc.
func SealManagedSecrets(box *secretbox.Box, ms ManagedSecrets) ([]byte, error) {
	raw, err := json.Marshal(ms)
	if err != nil {
		return nil, err
	}
	defer secretbox.Wipe(raw)
	return box.Seal(secretbox.InstanceServicesSecrets, instanceKind, raw)
}

// LoadManagedSecrets opens the registry's signing secrets. ok is false when the
// registry has none yet.
func LoadManagedSecrets(ctx context.Context, q *db.Queries, box *secretbox.Box) (ms ManagedSecrets, ok bool, err error) {
	row, err := q.GetInstanceService(ctx, instanceKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return ms, false, nil
	}
	if err != nil {
		return ms, false, fmt.Errorf("get registry service: %w", err)
	}
	if len(row.SecretsEnc) == 0 {
		return ms, false, nil
	}
	raw, err := box.Open(secretbox.InstanceServicesSecrets, instanceKind, row.SecretsEnc)
	if err != nil {
		return ms, false, fmt.Errorf("decrypt registry secrets: %w", err)
	}
	defer secretbox.Wipe(raw)
	if err := json.Unmarshal(raw, &ms); err != nil {
		return ms, false, fmt.Errorf("decode registry secrets: %w", err)
	}
	return ms, ms.RegistryKeyPEM != "", nil
}

// ManagedState is the maintenance state of the built-in registry
// (instance_services.options of kind registry). The garbage-collection job of
// internal/instsvc/registryadmin drives it; the deployment flow reads it
// through Resolve (Info.ReadOnly) and the forwarder refuses writes while ReadOnly.
type ManagedState struct {
	// Phase is "" while idle, else the step of the GC window: drain, collect.
	Phase string `json:"phase,omitempty"`
	// ReadOnly is true from the start of the window until writes are restored.
	// New builds for this registry queue while it is set.
	ReadOnly bool       `json:"readOnly,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Since    *time.Time `json:"since,omitempty"`
	// CommandID is the registry_gc command the collect phase waits for.
	CommandID string `json:"commandId,omitempty"`
	// LastGCAt is when the last window finished (success or not), with its outcome.
	LastGCAt      *time.Time `json:"lastGcAt,omitempty"`
	LastGCMessage string     `json:"lastGcMessage,omitempty"`
	// LastSweepAt and LastSweepDeleted describe the last retention sweep.
	LastSweepAt      *time.Time `json:"lastSweepAt,omitempty"`
	LastSweepDeleted int        `json:"lastSweepDeleted,omitempty"`
	// Error is the failure of the current window (kept until it ends).
	Error string `json:"error,omitempty"`
}

// DecodeManagedState reads the state out of instance_services.options.
func DecodeManagedState(options []byte) ManagedState {
	var st ManagedState
	_ = json.Unmarshal(options, &st)
	return st
}

// builtinInfo is the built-in registry's service row and state.
type builtinInfo struct {
	state   ManagedState
	ready   bool
	message string
}

func (s *Service) builtin(ctx context.Context) (builtinInfo, error) {
	var m builtinInfo
	row, err := s.q.GetInstanceService(ctx, instanceKind)
	if errors.Is(err, pgx.ErrNoRows) {
		m.message = "The built-in registry is not set up."
		return m, nil
	}
	if err != nil {
		return m, fmt.Errorf("get registry service: %w", err)
	}
	m.state = DecodeManagedState(row.Options)
	m.ready = row.Status == "ready"
	if !m.ready {
		m.message = fmt.Sprintf("The built-in registry is %s.", row.Status)
		if row.Message != "" {
			m.message += " " + row.Message
		}
	}
	return m, nil
}

// ManagedState returns the maintenance state of the built-in registry.
func (s *Service) ManagedState(ctx context.Context) (ManagedState, error) {
	m, err := s.builtin(ctx)
	return m.state, err
}

// PatchManagedState merges patch into the maintenance state (the GC job's
// checkpoint). A nil value clears the field.
func (s *Service) PatchManagedState(ctx context.Context, patch map[string]any) error {
	raw, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	if _, err := s.q.MergeInstanceServiceOptions(ctx, db.MergeInstanceServiceOptionsParams{Kind: instanceKind, Patch: raw}); err != nil {
		return fmt.Errorf("record registry state: %w", err)
	}
	return nil
}
