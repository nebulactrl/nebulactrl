package main

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Shared helpers on top of cfgFake (cmd_config_test.go), the scripted control
// plane the DB-free command tests use. Routes match "METHOD /exact/path"
// under /api/v1 (use cfgAPI + "/..."); anything unscripted answers 404 with a
// problem body and is visible through calls()/total().

const (
	fakeSvcID  = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	fakeSvc2ID = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
)

// runStdin is run with standard input (a secret read from `-`, a confirmation).
func (f *cfgFake) runStdin(stdin string, args ...string) result {
	f.t.Helper()
	var out, errOut bytes.Buffer
	c := &cli{in: strings.NewReader(stdin), out: &out, errOut: &errOut, pollEvery: time.Millisecond}
	require.NoError(f.t, c.dial(config{URL: f.srv.URL, Token: "nct_test"}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code := c.execute(ctx, args)
	return result{code: code, stdout: out.String(), err: errOut.String()}
}

// replySeq answers successive GET requests with the given bodies (all 200); the last body repeats once the list is used up.
func (f *cfgFake) replySeq(path string, bodies ...any) {
	var n atomic.Int64
	f.handle("GET", path, func(cfgReq) cfgReply {
		i := int(n.Add(1)) - 1
		return jsonReply(200, bodies[min(i, len(bodies)-1)])
	})
}

// problem scripts an RFC 9457 error answer.
func (f *cfgFake) problem(method, path string, status int, code, detail string) {
	f.replyRaw(method, path, problemReply(status, code, detail, nil))
}

// only returns the single recorded call to method+path, failing otherwise.
func (f *cfgFake) only(method, path string) cfgReq {
	f.t.Helper()
	calls := f.calls(method, path)
	require.Len(f.t, calls, 1, "%s %s", method, path)
	return calls[0]
}

// runWithConfig is run with a saved config (profiles, team) in place of the
// bare URL+token login of the fake.
func (f *cfgFake) runWithConfig(cfg config, args ...string) result {
	f.t.Helper()
	var out, errOut bytes.Buffer
	c := &cli{in: strings.NewReader(""), out: &out, errOut: &errOut, pollEvery: time.Millisecond}
	cfg.URL, cfg.Token = f.srv.URL, "nct_test"
	require.NoError(f.t, c.dial(cfg))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code := c.execute(ctx, args)
	return result{code: code, stdout: out.String(), err: errOut.String()}
}

// runLocal runs a command that only touches the config file (the CLI never
// connects), against whatever cfgEnv/cfgDir set up.
func runLocal(t *testing.T, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	c := &cli{in: strings.NewReader(""), out: &out, errOut: &errOut, pollEvery: time.Millisecond}
	code := c.execute(context.Background(), args)
	return result{code: code, stdout: out.String(), err: errOut.String()}
}
