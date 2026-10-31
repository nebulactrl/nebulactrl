package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/nebulactrl/nebulactrl/internal/agent/docker"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Execer is the part of docker.Engine the runner uses.
type Execer interface {
	ContainerExec(ctx context.Context, id string, argv []string, spec docker.ExecSpec, stdout, stderr io.Writer) (int, error)
}

// Runner executes backup, restore, delete and test commands.
type Runner struct {
	Docker Execer
	Stores StoreFactory
	Now    func() time.Time
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// stderrTail keeps the last bytes written to it.
type stderrTail struct {
	mu  sync.Mutex
	buf []byte
}

const stderrMax = 4096

func (s *stderrTail) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	if len(s.buf) > stderrMax {
		s.buf = s.buf[len(s.buf)-stderrMax:]
	}
	return len(p), nil
}

func (s *stderrTail) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(string(s.buf))
}

// scrub masks every secret value (length >= 4) in s.
func scrub(s string, secrets map[string]string) string {
	for _, v := range secrets {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "••••••")
		}
	}
	return s
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// Backup dumps the database in container into the destination and returns the
// stored object's size and checksum. Any failure leaves no object (and no
// manifest) behind.
func (r *Runner) Backup(ctx context.Context, container string, p agentprotocol.DBBackupPayload, secrets map[string]string, logf func(string)) (agentprotocol.DBBackupResult, error) {
	res := agentprotocol.DBBackupResult{Outcome: agentprotocol.OutcomeFailed, StartedAt: r.now()}
	fail := func(err error) (agentprotocol.DBBackupResult, error) {
		res.Message = scrub(err.Error(), secrets)
		res.FinishedAt = r.now()
		return res, errors.New(res.Message)
	}
	if err := ValidKey(p.ObjectKey); err != nil {
		return fail(err)
	}
	spec, err := dumpFor(p.Target, secrets)
	if err != nil {
		return fail(err)
	}
	store, err := r.Stores(p.Destination, secrets)
	if err != nil {
		return fail(err)
	}
	logf(fmt.Sprintf("dumping %s with %s", p.Target.Engine, spec.tool))

	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()
	pr, pw := io.Pipe()
	stderr := &stderrTail{}
	var dumpErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		var w io.Writer = pw
		var gz *gzip.Writer
		if spec.gzip {
			gz = gzip.NewWriter(pw)
			w = gz
		}
		code, err := r.Docker.ContainerExec(execCtx, container, spec.argv, docker.ExecSpec{Env: spec.env}, w, stderr)
		switch {
		case err != nil:
			err = fmt.Errorf("run %s: %w", spec.tool, err)
		case code != 0:
			// The exit status is checked BEFORE EOF is signalled, so a failed
			// dump is a read error for the uploader and never a complete object.
			msg := stderr.String()
			if msg == "" {
				msg = "no error output"
			}
			err = fmt.Errorf("%s exited with status %d: %s", spec.tool, code, msg)
		case gz != nil:
			err = gz.Close()
		}
		dumpErr = err
		_ = pw.CloseWithError(err) // nil closes with io.EOF
	}()

	h := sha256.New()
	cnt := &countingWriter{}
	body := io.TeeReader(pr, io.MultiWriter(h, cnt))
	putErr := store.Put(ctx, p.ObjectKey, body)
	if putErr != nil {
		cancelExec()
		_ = pr.CloseWithError(putErr)
		// Prefer the dump's own error: it is the root cause when the upload
		// aborted because the dump failed.
		<-done
		if dumpErr != nil && !errors.Is(dumpErr, context.Canceled) && !errors.Is(dumpErr, putErr) {
			putErr = dumpErr
		}
		return fail(putErr)
	}
	if cnt.n == 0 {
		_ = store.Delete(ctx, p.ObjectKey)
		return fail(errors.New("the dump produced no output"))
	}
	sum := hex.EncodeToString(h.Sum(nil))

	man := agentprotocol.BackupManifest{
		Version: 1, BackupID: p.BackupID, Engine: p.Target.Engine, EngineVersion: p.Target.Version,
		Database: p.Target.Database, Format: spec.format, ObjectKey: p.ObjectKey, SizeBytes: cnt.n,
		SHA256: sum, CreatedAt: res.StartedAt, Meta: p.Meta,
	}
	raw, _ := json.MarshalIndent(man, "", "  ")
	if err := store.Put(ctx, manifestKey(p.ObjectKey), bytes.NewReader(raw)); err != nil {
		_ = store.Delete(context.WithoutCancel(ctx), p.ObjectKey)
		return fail(fmt.Errorf("write manifest: %w", err))
	}
	stored, err := store.Size(ctx, p.ObjectKey)
	if err != nil || stored != cnt.n {
		_ = store.Delete(context.WithoutCancel(ctx), p.ObjectKey)
		_ = store.Delete(context.WithoutCancel(ctx), manifestKey(p.ObjectKey))
		if err == nil {
			err = fmt.Errorf("stored size %d differs from streamed size %d", stored, cnt.n)
		}
		return fail(fmt.Errorf("verify stored object: %w", err))
	}
	res.Outcome = agentprotocol.OutcomeSucceeded
	res.SizeBytes, res.SHA256, res.Format, res.ObjectKey = cnt.n, sum, spec.format, p.ObjectKey
	res.FinishedAt = r.now()
	res.Message = fmt.Sprintf("stored %d bytes", cnt.n)
	logf(res.Message)
	return res, nil
}

// Restore streams the stored dump into the database in container.
func (r *Runner) Restore(ctx context.Context, container string, p agentprotocol.DBRestorePayload, secrets map[string]string, logf func(string)) (agentprotocol.DBRestoreResult, error) {
	res := agentprotocol.DBRestoreResult{Outcome: agentprotocol.OutcomeFailed}
	fail := func(err error) (agentprotocol.DBRestoreResult, error) {
		res.Message = scrub(err.Error(), secrets)
		res.FinishedAt = r.now()
		return res, errors.New(res.Message)
	}
	if err := ValidKey(p.ObjectKey); err != nil {
		return fail(err)
	}
	spec, err := restoreFor(p, secrets)
	if err != nil {
		return fail(err)
	}
	store, err := r.Stores(p.Destination, secrets)
	if err != nil {
		return fail(err)
	}
	obj, err := store.Get(ctx, p.ObjectKey)
	if err != nil {
		return fail(fmt.Errorf("open backup: %w", err))
	}
	defer obj.Close()

	h := sha256.New()
	var in io.Reader = io.TeeReader(obj, h)
	if spec.gunzip {
		gz, err := gzip.NewReader(in)
		if err != nil {
			return fail(fmt.Errorf("backup is not a gzip stream: %w", err))
		}
		defer gz.Close()
		in = gz
	}
	failRestore := fail
	var sc *pgScratch
	if spec.scratch {
		sc = newPGScratch(r.Docker, container, p.Target.User, secrets[agentprotocol.SecretDBPassword], p.Target.Database, p.RestoreID, secrets)
		logf("preparing a scratch database " + sc.names.scratch + " (" + p.Target.Database + " is not touched until the restore succeeded)")
		if err := sc.prepare(ctx); err != nil {
			return fail(fmt.Errorf("could not prepare the restore: %w", err))
		}
		// A failed restore must not leave the scratch database behind.
		fail = func(err error) (agentprotocol.DBRestoreResult, error) {
			sc.discard(ctx)
			return failRestore(err)
		}
	}
	logf(fmt.Sprintf("restoring into %s with %s", p.Target.Engine, spec.tool))
	stderr := &stderrTail{}
	code, err := r.Docker.ContainerExec(ctx, container, spec.argv, docker.ExecSpec{Env: spec.env, Stdin: in}, io.Discard, stderr)
	if err != nil {
		return fail(fmt.Errorf("run %s: %w", spec.tool, err))
	}
	if code != 0 {
		msg := stderr.String()
		if msg == "" {
			msg = "no error output"
		}
		return fail(fmt.Errorf("%s exited with status %d: %s", spec.tool, code, msg))
	}
	// Drain whatever the tool left unread so the checksum covers the whole object.
	_, _ = io.Copy(io.Discard, obj)
	if p.SHA256 != "" {
		if got := hex.EncodeToString(h.Sum(nil)); got != p.SHA256 {
			return fail(fmt.Errorf("checksum mismatch: the stored object is %s, the backup recorded %s; the restored data may be incomplete", got, p.SHA256))
		}
	}
	if sc != nil {
		// Only a fully restored and verified dump replaces the database.
		if err := sc.analyze(ctx); err != nil {
			logf("ANALYZE failed (the restore is kept): " + err.Error())
		}
		logf("switching " + p.Target.Database + " over to the restored data")
		if err := sc.switchOver(ctx, logf); err != nil {
			return failRestore(err) // switchOver has already dropped the scratch database
		}
	}
	res.Outcome = agentprotocol.OutcomeSucceeded
	res.Message = "restored"
	res.FinishedAt = r.now()
	logf(res.Message)
	return res, nil
}

// Delete removes the given objects and their manifests. The manifest goes
// first: it is the commit marker restores list, so a backup is never listed
// once its dump may already be gone.
func (r *Runner) Delete(ctx context.Context, p agentprotocol.BackupDeletePayload, secrets map[string]string) (agentprotocol.BackupSimpleResult, error) {
	store, err := r.Stores(p.Destination, secrets)
	if err != nil {
		return agentprotocol.BackupSimpleResult{Outcome: agentprotocol.OutcomeFailed, Message: scrub(err.Error(), secrets)}, err
	}
	var errs []string
	for _, k := range p.ObjectKeys {
		for _, key := range []string{manifestKey(k), k} {
			if err := store.Delete(ctx, key); err != nil {
				errs = append(errs, scrub(err.Error(), secrets))
			}
		}
	}
	if len(errs) > 0 {
		msg := strings.Join(errs, "; ")
		return agentprotocol.BackupSimpleResult{Outcome: agentprotocol.OutcomeFailed, Message: msg}, errors.New(msg)
	}
	return agentprotocol.BackupSimpleResult{Outcome: agentprotocol.OutcomeSucceeded, Message: fmt.Sprintf("deleted %d backup(s)", len(p.ObjectKeys))}, nil
}

// Test writes, reads back and deletes a probe object.
func (r *Runner) Test(ctx context.Context, p agentprotocol.BackupTestPayload, secrets map[string]string) (agentprotocol.BackupSimpleResult, error) {
	fail := func(err error) (agentprotocol.BackupSimpleResult, error) {
		msg := scrub(err.Error(), secrets)
		return agentprotocol.BackupSimpleResult{Outcome: agentprotocol.OutcomeFailed, Message: msg}, errors.New(msg)
	}
	store, err := r.Stores(p.Destination, secrets)
	if err != nil {
		return fail(err)
	}
	var idb [6]byte
	_, _ = rand.Read(idb[:])
	key := probeKey(p.Prefix, hex.EncodeToString(idb[:]))
	payload := []byte("nebulactrl backup destination test " + hex.EncodeToString(idb[:]))
	if err := store.Put(ctx, key, bytes.NewReader(payload)); err != nil {
		return fail(fmt.Errorf("write: %w", err))
	}
	defer func() { _ = store.Delete(context.WithoutCancel(ctx), key) }()
	size, err := store.Size(ctx, key)
	if err != nil {
		return fail(fmt.Errorf("stat: %w", err))
	}
	if size != int64(len(payload)) {
		return fail(fmt.Errorf("stat: stored %d bytes, wrote %d", size, len(payload)))
	}
	rc, err := store.Get(ctx, key)
	if err != nil {
		return fail(fmt.Errorf("read: %w", err))
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(got, payload) {
		return fail(errors.New("read back a different object than was written"))
	}
	if err := store.Delete(ctx, key); err != nil {
		return fail(fmt.Errorf("delete: %w", err))
	}
	return agentprotocol.BackupSimpleResult{Outcome: agentprotocol.OutcomeSucceeded, Message: "destination is writable, readable and deletable"}, nil
}
