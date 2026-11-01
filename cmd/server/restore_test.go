package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/backup"
	"github.com/nebulactrl/nebulactrl/internal/backups"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

func runRestore(t *testing.T, env map[string]string, args ...string) (string, error) {
	t.Helper()
	for _, k := range []string{"NEBULACTRL_RESTORE_ACCESS_KEY", "NEBULACTRL_RESTORE_SECRET_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "NEBULACTRL_DATABASE_URL"} {
		t.Setenv(k, env[k])
	}
	cmd := restoreCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestRestoreCmdRefusesBadInput(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"no source", nil, nil, "--from is required"},
		{"s3 without credentials", nil, []string{"--from", "s3://bucket/p"}, "S3 credentials are missing"},
		{"s3 flags on a directory", nil, []string{"--from", t.TempDir(), "--endpoint", "https://s3.example.com"}, "only apply to s3://"},
		{"bad --at", nil, []string{"--from", t.TempDir(), "--at", "yesterday"}, "invalid timestamp"},
		{"no target database", nil, []string{"--from", t.TempDir()}, "target database is required"},
	}
	for _, c := range cases {
		_, err := runRestore(t, c.env, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.want)
		}
	}
}

func TestRestoreCmdListShowsBackups(t *testing.T) {
	dir := t.TempDir()
	st, err := backup.NewLocalStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	m := backups.InstanceManifest{
		BackupManifest: agentprotocol.BackupManifest{
			Version: 1, BackupID: "0123456789abcdef", Engine: "postgres", EngineVersion: "17.2", Format: agentprotocol.FormatPGCustom,
			ObjectKey: "nebulactrl-instance/2026/10/x.dump", SizeBytes: 2048, SHA256: strings.Repeat("a", 64),
			CreatedAt: time.Date(2026, 10, 27, 3, 30, 0, 0, time.UTC),
		},
		Kind: backups.InstanceManifestKind, KeyFingerprint: "abc123abc123",
	}
	raw, _ := json.Marshal(m)
	if err := st.Put(context.Background(), backup.ManifestKey(m.ObjectKey), bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	out, err := runRestore(t, nil, "--from", dir, "--list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"01234567", "2026-10-27T03:30:00Z", "2.0 KiB", "17.2", "abc123abc123"} {
		if !strings.Contains(out, want) {
			t.Errorf("--list output lacks %q:\n%s", want, out)
		}
	}

	if _, err := runRestore(t, nil, "--from", t.TempDir(), "--list"); err == nil || !strings.Contains(err.Error(), "no control-plane backups") {
		t.Errorf("an empty source is reported: %v", err)
	}
}
