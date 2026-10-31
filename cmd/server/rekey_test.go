package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/secretbox"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
)

func newTestKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, secretbox.KeySize)
	_, err := rand.Read(k)
	require.NoError(t, err)
	return k
}

func runRekey(t *testing.T, dsn string, env map[string]string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("NEBULACTRL_DATABASE_URL", dsn)
	for _, k := range []string{"NEBULACTRL_SECRET_KEY", "NEBULACTRL_SECRET_KEY_FILE", "NEBULACTRL_SECRET_KEY_PREVIOUS", "NEBULACTRL_SECRET_KEY_PREVIOUS_FILE"} {
		t.Setenv(k, env[k])
	}
	cmd := rekeyCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// TestRekeyCommandRotatesRealColumns walks the command through a key rotation
// on the real singleton tables: a legacy v1 value on the old key, then the new
// key plus NEBULACTRL_SECRET_KEY_PREVIOUS, a dry run, the rewrite, and a check
// that the new key alone opens everything.
func TestRekeyCommandRotatesRealColumns(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	oldKey, newKey := newTestKey(t), newTestKey(t)
	enc := base64.StdEncoding.EncodeToString

	block, err := aes.NewCipher(oldKey)
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	nonce := make([]byte, aead.NonceSize())
	_, err = rand.Read(nonce)
	require.NoError(t, err)
	v1 := aead.Seal(append([]byte{secretbox.FormatV1}, nonce...), nonce, []byte("smtp-secret"), []byte{secretbox.FormatV1})
	oldBox, err := secretbox.New(oldKey)
	require.NoError(t, err)
	v2, err := oldBox.SealString(secretbox.InstanceBackupSecretKey, secretbox.InstanceRow, "s3-secret")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE instance_settings SET smtp_password_enc = $1`, v1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE instance_backup_settings SET secret_key_enc = $1`, v2)
	require.NoError(t, err)

	dsn := pool.Config().ConnString()
	env := map[string]string{"NEBULACTRL_SECRET_KEY": enc(newKey), "NEBULACTRL_SECRET_KEY_PREVIOUS": enc(oldKey)}

	out, err := runRekey(t, dsn, env, "--dry-run")
	require.NoError(t, err, out)
	require.Contains(t, out, "dry run: 2 value(s) would be re-encrypted")
	var smtp []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT smtp_password_enc FROM instance_settings`).Scan(&smtp))
	require.Equal(t, v1, smtp, "dry run writes nothing")

	out, err = runRekey(t, dsn, env)
	require.NoError(t, err, out)
	require.Contains(t, out, "done: every sealed value opens with the current key alone")
	require.Contains(t, out, "instance_settings.smtp_password_enc")

	only, err := secretbox.New(newKey)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `SELECT smtp_password_enc FROM instance_settings`).Scan(&smtp))
	got, err := only.OpenString(secretbox.InstanceSettingsSMTPPassword, secretbox.InstanceRow, smtp)
	require.NoError(t, err)
	require.Equal(t, "smtp-secret", got)
	var s3 []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT secret_key_enc FROM instance_backup_settings`).Scan(&s3))
	got, err = only.OpenString(secretbox.InstanceBackupSecretKey, secretbox.InstanceRow, s3)
	require.NoError(t, err)
	require.Equal(t, "s3-secret", got)

	// Idempotent: the old key can go now.
	out, err = runRekey(t, dsn, map[string]string{"NEBULACTRL_SECRET_KEY": enc(newKey)})
	require.NoError(t, err, out)
	require.NotContains(t, out, "instance_settings.smtp_password_enc\t")
}

// TestRekeyCommandFailsWhenTheOldKeyIsMissing: values the ring cannot open are
// reported and the command exits with an error, leaving them unchanged.
func TestRekeyCommandFailsWhenTheOldKeyIsMissing(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	oldKey, newKey := newTestKey(t), newTestKey(t)
	oldBox, err := secretbox.New(oldKey)
	require.NoError(t, err)
	ct, err := oldBox.SealString(secretbox.InstanceSettingsSMTPPassword, secretbox.InstanceRow, "smtp-secret")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE instance_settings SET smtp_password_enc = $1`, ct)
	require.NoError(t, err)

	out, err := runRekey(t, pool.Config().ConnString(), map[string]string{"NEBULACTRL_SECRET_KEY": base64.StdEncoding.EncodeToString(newKey)})
	require.ErrorContains(t, err, "could not be decrypted")
	require.Contains(t, out, "cannot decrypt instance_settings.smtp_password_enc row instance")
	var after []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT smtp_password_enc FROM instance_settings`).Scan(&after))
	require.Equal(t, ct, after)
}
