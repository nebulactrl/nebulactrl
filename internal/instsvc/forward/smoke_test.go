//go:build instsmoke

package forward

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	installdeploy "github.com/nebulactrl/nebulactrl/deploy/install"
	"github.com/nebulactrl/nebulactrl/internal/agent/backup"
	"github.com/nebulactrl/nebulactrl/internal/agents"
	"github.com/nebulactrl/nebulactrl/internal/instsvc"
	"github.com/nebulactrl/nebulactrl/internal/instsvc/lokiadmin"
	"github.com/nebulactrl/nebulactrl/internal/instsvc/s3admin"
	"github.com/nebulactrl/nebulactrl/internal/registries"
	"github.com/nebulactrl/nebulactrl/internal/testutil"
	"github.com/nebulactrl/nebulactrl/pkg/agentprotocol"
)

// Smoke test of the instance services against the real, pinned images, started
// by the installer's own compose file and configuration, with the real agent
// store (internal/agent/backup) as the S3 client and the forwarder in between:
//
//	go test -tags instsmoke -run TestSmokeInstanceServices -v ./internal/instsvc/forward/
//
// It needs a Docker daemon, the compose plugin and the free loopback ports
// 18333, 18500 and 18100 (the ports an agent offers). It starts and removes the
// compose project "nctest-instsmoke" (container name nebulactrl-registry-1, so
// stop a real installation's registry first on the same machine).
//
// Verified here:
//
//	1. SeaweedFS bucket-scoped IAM and the native quota;
//	2. a SigV4 request signed for 127.0.0.1:18333 through the forwarder;
//	3. Distribution accepts exact-name tokens: push, pull and cross-repo mount,
//	   two teams; and Loki's tenant cannot be spoofed.

const smokeProject = "nctest-instsmoke"

type smokeStack struct {
	dir                  string
	s3URL, regURL, lokiU string
	adminKey, adminSec   string
	ms                   registries.ManagedSecrets
}

func sh(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s %s: %s", name, strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func startStack(t *testing.T) *smokeStack {
	t.Helper()
	for _, port := range []string{"18333", "18500", "18100"} {
		ln, err := net.Listen("tcp", "127.0.0.1:"+port)
		require.NoError(t, err, "the port %s an agent offers must be free", port)
		ln.Close()
	}
	st := &smokeStack{dir: t.TempDir(), adminKey: "SMOKEADMINKEY0123456", adminSec: "smoke-admin-secret-0123456789abcdef0123"}
	ms, err := registries.GenerateManagedSecrets()
	require.NoError(t, err)
	st.ms = ms
	jwks, err := ms.JWKS()
	require.NoError(t, err)

	auth := filepath.Join(st.dir, "registry-auth")
	require.NoError(t, os.MkdirAll(auth, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(auth, "jwks.json"), []byte(jwks), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(st.dir, "instance"), 0o755))
	for _, name := range installdeploy.InstanceConfigs() {
		require.NoError(t, os.WriteFile(filepath.Join(st.dir, name), []byte(testutil.RepoFile(t, "deploy/install/"+name)), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(st.dir, "docker-compose.yml"), []byte(testutil.RepoFile(t, "deploy/install/docker-compose.yml")), 0o644))
	// Published ports for the test (the real services publish nothing; Docker cannot publish from an internal network, which embed_test asserts the real file has).
	require.NoError(t, os.WriteFile(filepath.Join(st.dir, "docker-compose.override.yml"), []byte(`
services:
  seaweedfs: {ports: ["127.0.0.1::8333"]}
  registry: {ports: ["127.0.0.1::5000"]}
  loki: {ports: ["127.0.0.1::3100"]}
networks:
  services: {internal: false}
`), 0o644))
	env := fmt.Sprintf(`NEBULACTRL_S3_ADMIN_KEY=%s
NEBULACTRL_S3_ADMIN_SECRET=%s
NEBULACTRL_REGISTRY_HTTP_SECRET=0123456789abcdef0123456789abcdef
NEBULACTRL_REGISTRY_AUTH_DIR=%s
POSTGRES_PASSWORD=x
NEBULACTRL_IMAGE=x
NEBULACTRL_SECRET_KEY=x
NEBULACTRL_PUBLIC_URL=http://x
`, st.adminKey, st.adminSec, auth)
	require.NoError(t, os.WriteFile(filepath.Join(st.dir, ".env"), []byte(env), 0o600))

	compose := func(args ...string) string {
		return sh(t, st.dir, "docker", append([]string{"compose", "-p", smokeProject, "-f", "docker-compose.yml", "-f", "docker-compose.override.yml", "--env-file", ".env"}, args...)...)
	}
	_ = exec.Command("docker", "compose", "-p", smokeProject, "down", "-v", "--remove-orphans").Run()
	t.Cleanup(func() {
		if t.Failed() {
			for _, svc := range []string{"seaweedfs", "registry", "loki"} {
				out, _ := exec.Command("docker", "compose", "-p", smokeProject, "logs", "--tail", "25", svc).CombinedOutput()
				t.Logf("---- %s\n%s", svc, out)
			}
		}
		cmd := exec.Command("docker", "compose", "-p", smokeProject, "-f", "docker-compose.yml", "-f", "docker-compose.override.yml", "--env-file", ".env", "down", "-v", "--remove-orphans")
		cmd.Dir = st.dir
		_ = cmd.Run()
	})
	compose("up", "-d", "--wait", "--wait-timeout", "180", "seaweedfs", "registry", "loki")
	port := func(svc, inner string) string {
		out := compose("port", svc, inner)
		return "http://127.0.0.1:" + out[strings.LastIndex(out, ":")+1:]
	}
	st.s3URL, st.regURL, st.lokiU = port("seaweedfs", "8333"), port("registry", "5000"), port("loki", "3100")
	return st
}

// smokeSlices is fakeSlices with a secret per key.
type smokeSlices struct {
	*fakeSlices
	secrets map[string]string
}

func (s smokeSlices) Secret(_ context.Context, sl instsvc.Slice) (string, error) {
	if v, ok := s.secrets[sl.AccessKey]; ok {
		return v, nil
	}
	return "", instsvc.ErrNoSlice
}

// limitGate is a quota gate over a fixed limit per team.
type limitGate struct {
	mu    sync.Mutex
	limit map[uuid.UUID]int64
	used  map[uuid.UUID]int64
}

func (g *limitGate) Admit(_ context.Context, team uuid.UUID, n int64) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if lim, ok := g.limit[team]; ok && g.used[team]+n > lim {
		return nil, instsvc.ErrQuotaExceeded
	}
	g.used[team] += n
	return func() {}, nil
}

func sha(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }

// agentListener plays an agent's local listener: every accepted connection becomes a tunnel stream.
func agentListener(t *testing.T, ctx context.Context, f *Forwarder, addr string, server uuid.UUID, service string) net.Addr {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = f.AcceptTunnel(ctx, agents.TunnelPeer{ServerID: server, Service: service}, c)
			}()
		}
	}()
	return ln.Addr()
}

func TestSmokeInstanceServices(t *testing.T) {
	st := startStack(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	teamA, teamB := uuid.New(), uuid.New()
	srvA, srvB := uuid.New(), uuid.New()
	bucketA, bucketB := "nc-"+teamA.String(), "nc-"+teamB.String()
	keyA, keyB := "SMOKEKEYAAAAAAAAAAAA", "SMOKEKEYBBBBBBBBBBBB"
	secA, secB := "secret-a-0123456789012345678901234567890", "secret-b-0123456789012345678901234567890"
	slice := func(team uuid.UUID, kind, ref, key string) *instsvc.Slice {
		return &instsvc.Slice{ID: uuid.New(), TeamID: team, Kind: kind, Ref: ref, AccessKey: key, State: "ready"}
	}
	fs := &fakeSlices{
		servers: map[uuid.UUID]uuid.UUID{srvA: teamA, srvB: teamB},
		teams: map[uuid.UUID]instsvc.TeamSlices{
			teamA: {S3: slice(teamA, "s3", bucketA, keyA), Registry: slice(teamA, "registry", teamA.String()+"/", ""), Loki: slice(teamA, "loki", teamA.String(), "")},
			teamB: {S3: slice(teamB, "s3", bucketB, keyB), Registry: slice(teamB, "registry", teamB.String()+"/", ""), Loki: slice(teamB, "loki", teamB.String(), "")},
		},
		keys: map[string]uuid.UUID{keyA: teamA, keyB: teamB},
	}
	slices := smokeSlices{fs, map[string]string{keyA: secA, keyB: secB}}
	gate := &limitGate{limit: map[uuid.UUID]int64{}, used: map[uuid.UUID]int64{}}
	reg := &fakeRegistry{ms: st.ms}

	admin, err := s3admin.New(s3admin.Config{Endpoint: st.s3URL, AdminKey: st.adminKey, AdminSecret: st.adminSec, Slices: slices})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return admin.Health(ctx) == nil }, 60*time.Second, time.Second, "the nebula-admin identity the S3 container made must work: %v", admin.Health(ctx))

	f, err := New(Deps{Slices: slices, Gate: gate, S3URL: st.s3URL, RegistryURL: st.regURL, LokiURL: st.lokiU, Registry: reg})
	require.NoError(t, err)
	f.cacheTTL = 0

	// ---- SeaweedFS bucket-scoped IAM and the native quota ----------------------------
	t.Run("bucket-scoped keys and native quota", func(t *testing.T) {
		for _, s := range []*instsvc.Slice{fs.teams[teamA].S3, fs.teams[teamB].S3} {
			require.NoError(t, admin.EnsureSlice(ctx, *s, 0))
			require.NoError(t, admin.EnsureSlice(ctx, *s, 0), "EnsureSlice is idempotent")
		}
		dest := func(bucket string) agentprotocol.BackupDestination {
			return agentprotocol.BackupDestination{Endpoint: st.s3URL, PathStyle: true, Region: "us-east-1", CompatMode: true, Bucket: bucket}
		}
		a, err := backup.NewS3Store(dest(bucketA), keyA, secA)
		require.NoError(t, err)
		aOnB, err := backup.NewS3Store(dest(bucketB), keyA, secA)
		require.NoError(t, err)
		b, err := backup.NewS3Store(dest(bucketB), keyB, secB)
		require.NoError(t, err)

		require.NoError(t, a.Put(ctx, "mine.txt", strings.NewReader("team a data")))
		require.NoError(t, b.Put(ctx, "theirs.txt", strings.NewReader("team b data")))
		r, err := a.Get(ctx, "mine.txt")
		require.NoError(t, err)
		got, _ := io.ReadAll(r)
		r.Close()
		require.Equal(t, "team a data", string(got))

		// Cross-bucket access must fail, for write, read, size and delete.
		require.ErrorContains(t, aOnB.Put(ctx, "stolen.txt", strings.NewReader("x")), "AccessDenied")
		_, err = aOnB.Get(ctx, "theirs.txt")
		require.Error(t, err)
		require.NotContains(t, fmt.Sprint(err), "team b data")
		_, err = aOnB.Size(ctx, "theirs.txt")
		require.Error(t, err)
		require.Error(t, aOnB.Delete(ctx, "theirs.txt"))
		size, err := b.Size(ctx, "theirs.txt")
		require.NoError(t, err)
		require.EqualValues(t, len("team b data"), size, "team b's object is untouched")

		// Usage is the sum of the sizes.
		require.NoError(t, a.Put(ctx, "more.bin", bytes.NewReader(make([]byte, 1<<20))))
		used, err := admin.Usage(ctx, *fs.teams[teamA].S3)
		require.NoError(t, err)
		require.EqualValues(t, len("team a data")+1<<20, used)

		// Native quota: units are bytes; once the enforcer finds the bucket over, writes fail and reads work.
		require.NoError(t, admin.SetQuota(ctx, bucketA, 512<<10))
		q, err := admin.Quota(ctx, bucketA)
		require.NoError(t, err)
		require.EqualValues(t, 512<<10, q, "?seaweedfs-quota takes and returns exact bytes")
		weedEnforce := func() {
			out := sh(t, "", "docker", "exec", smokeProject+"-seaweedfs-1", "sh", "-c",
				`printf 'lock\ns3.bucket.quota.enforce -apply\nunlock\nexit\n' | weed shell -master=127.0.0.1:9333 2>&1`)
			t.Logf("enforce: %s", strings.ReplaceAll(out, "\n", " | "))
		}
		// The enforcer sees the size when the volume server's next heartbeat reports it; keep running it.
		require.Eventually(t, func() bool {
			weedEnforce()
			return a.Put(ctx, "over.bin", strings.NewReader("x")) != nil
		}, 60*time.Second, 3*time.Second, "an over-quota bucket must refuse writes")
		_, err = a.Get(ctx, "mine.txt")
		require.NoError(t, err, "reads still work over quota")
		require.NoError(t, a.Delete(ctx, "more.bin"), "so does deleting")

		// Raising the quota lets writes through again at once (no wait for the next enforcer run).
		require.NoError(t, admin.SetQuota(ctx, bucketA, 50<<20))
		require.NoError(t, a.Put(ctx, "after.txt", strings.NewReader("again")), "raising the quota must clear the read-only flag")
		q, err = admin.Quota(ctx, bucketA)
		require.NoError(t, err)
		require.EqualValues(t, 50<<20, q)
		require.NoError(t, admin.SetQuota(ctx, bucketA, 0))

		// Re-keying: another key of the same user is revoked by EnsureSlice.
		_, err = admin.Quota(ctx, bucketB)
		require.NoError(t, err)

		// Purge removes bucket, user and key, objects included.
		pb := &instsvc.Slice{ID: uuid.New(), TeamID: uuid.New(), Kind: "s3", Ref: "nc-purged", AccessKey: "SMOKEKEYPURGE000000", State: "ready"}
		slices.secrets[pb.AccessKey] = "secret-purge-0123456789012345678901234"
		require.NoError(t, admin.EnsureSlice(ctx, *pb, 0))
		pstore, err := backup.NewS3Store(dest("nc-purged"), pb.AccessKey, slices.secrets[pb.AccessKey])
		require.NoError(t, err)
		for i := range 5 {
			require.NoError(t, pstore.Put(ctx, fmt.Sprintf("k/%d", i), strings.NewReader("x")))
		}
		require.NoError(t, admin.Purge(ctx, *pb))
		require.NoError(t, admin.Purge(ctx, *pb), "purge is idempotent")
		require.Error(t, pstore.Put(ctx, "after", strings.NewReader("x")), "the purged team's key is gone")
		_, err = admin.Usage(ctx, *pb)
		require.Error(t, err, "and so is its bucket")
	})

	// ---- a request signed for 127.0.0.1:18333 through the forwarder ---------------------
	t.Run("SigV4 signed for 127.0.0.1:18333 through the forwarder", func(t *testing.T) {
		agentListener(t, ctx, f, "127.0.0.1:18333", srvA, agentprotocol.TunnelS3)
		dest := func(bucket string) agentprotocol.BackupDestination {
			// The destination the control plane hands every team's backups.
			return agentprotocol.BackupDestination{Endpoint: "http://127.0.0.1:18333", PathStyle: true, Region: "us-east-1", CompatMode: true, Bucket: bucket}
		}
		a, err := backup.NewS3Store(dest(bucketA), keyA, secA)
		require.NoError(t, err)
		aOnB, err := backup.NewS3Store(dest(bucketB), keyA, secA)
		require.NoError(t, err)
		bKey, err := backup.NewS3Store(dest(bucketA), keyB, secB)
		require.NoError(t, err)

		// A small object, then a multipart one (the agent's transfer manager uses parts of 16 MiB).
		require.NoError(t, a.Put(ctx, "dumps/small.sql", strings.NewReader("select 1;")))
		big := make([]byte, 40<<20)
		for i := range big {
			big[i] = byte(i * 7)
		}
		gate.mu.Lock()
		gate.used[teamA] = 0
		gate.mu.Unlock()
		require.NoError(t, a.Put(ctx, "dumps/big.bin", bytes.NewReader(big)), "multipart upload through the forwarder")
		size, err := a.Size(ctx, "dumps/big.bin")
		require.NoError(t, err)
		require.EqualValues(t, len(big), size)
		r, err := a.Get(ctx, "dumps/big.bin")
		require.NoError(t, err)
		back, err := io.ReadAll(r)
		r.Close()
		require.NoError(t, err)
		require.Equal(t, sha(big), sha(back), "the object comes back byte for byte")
		gate.mu.Lock()
		require.GreaterOrEqual(t, gate.used[teamA], int64(len(big)), "the gate admitted what was uploaded")
		gate.mu.Unlock()

		// Others' buckets and keys are refused by the forwarder before SeaweedFS is asked.
		require.ErrorContains(t, aOnB.Put(ctx, "x", strings.NewReader("x")), "AccessDenied")
		_, err = aOnB.Get(ctx, "theirs.txt")
		require.Error(t, err)
		require.ErrorContains(t, bKey.Put(ctx, "x", strings.NewReader("x")), "AccessDenied", "another team's key on this team's stream")

		// A full quota is a plain, early error and leaves everything in place.
		gate.mu.Lock()
		gate.limit[teamA] = gate.used[teamA] + 1<<20
		gate.mu.Unlock()
		err = a.Put(ctx, "dumps/too-big.bin", bytes.NewReader(make([]byte, 8<<20)))
		require.ErrorContains(t, err, "QuotaExceeded")
		_, err = a.Size(ctx, "dumps/too-big.bin")
		require.Error(t, err, "nothing was stored")
		r, err = a.Get(ctx, "dumps/small.sql")
		require.NoError(t, err, "existing backups are kept and readable")
		r.Close()
		require.NoError(t, a.Delete(ctx, "dumps/big.bin"))

		// Raw requests the SDK never makes: the service root, IAM, sub-resources.
		raw := func(method, path, body string) (int, string) {
			req, _ := http.NewRequest(method, "http://127.0.0.1:18333"+path, strings.NewReader(body))
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
		for _, p := range []string{"/", "/" + bucketA + "?seaweedfs-quota", "/" + bucketA + "?policy"} {
			code, body := raw(http.MethodGet, p, "")
			require.Equal(t, http.StatusForbidden, code, p)
			require.Contains(t, body, "AccessDenied", p)
		}
		code, _ := raw(http.MethodPost, "/", "Action=ListUsers&Version=2010-05-08")
		require.Equal(t, http.StatusForbidden, code, "the IAM API is not reachable through the tunnel")
	})

	// ---- Distribution accepts exact-name tokens -------------------------------------------
	t.Run("registry exact-name tokens, push pull and mount with two teams", func(t *testing.T) {
		agentListener(t, ctx, f, "127.0.0.1:18500", srvA, agentprotocol.TunnelRegistry)
		bAddr := agentListener(t, ctx, f, "127.0.0.1:0", srvB, agentprotocol.TunnelRegistry)
		ca, cb := &http.Client{Timeout: time.Minute}, &http.Client{Timeout: time.Minute}
		baseA, baseB := "http://127.0.0.1:18500", "http://"+bAddr.String()
		do := func(c *http.Client, base, method, path string, body []byte, hdr map[string]string) (*http.Response, []byte) {
			t.Helper()
			req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
			require.NoError(t, err)
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			resp, err := c.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp, b
		}
		pushBlob := func(c *http.Client, base, repo string, data []byte) string {
			t.Helper()
			resp, body := do(c, base, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", nil, nil)
			require.Equal(t, http.StatusAccepted, resp.StatusCode, string(body))
			loc := resp.Header.Get("Location")
			require.True(t, strings.HasPrefix(loc, "/v2/"), "http.relativeurls keeps Location on the client's address: %q", loc)
			sep := "?"
			if strings.Contains(loc, "?") {
				sep = "&"
			}
			d := sha(data)
			resp, body = do(c, base, http.MethodPut, loc+sep+"digest="+d, data, map[string]string{"Content-Type": "application/octet-stream"})
			require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
			return d
		}
		pushImage := func(c *http.Client, base, repo, tag string) (layer string, manifestDigest string) {
			t.Helper()
			layerData := []byte("layer of " + repo + uuid.NewString())
			cfgData := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]},"nonce":"` + uuid.NewString() + `"}`)
			layer = pushBlob(c, base, repo, layerData)
			cfg := pushBlob(c, base, repo, cfgData)
			m, _ := json.Marshal(map[string]any{
				"schemaVersion": 2, "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
				"config": map[string]any{"mediaType": "application/vnd.docker.container.image.v1+json", "size": len(cfgData), "digest": cfg},
				"layers": []map[string]any{{"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip", "size": len(layerData), "digest": layer}},
			})
			resp, body := do(c, base, http.MethodPut, "/v2/"+repo+"/manifests/"+tag, m, map[string]string{"Content-Type": "application/vnd.docker.distribution.manifest.v2+json"})
			require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
			return layer, resp.Header.Get("Docker-Content-Digest")
		}

		repoA, repoA2, repoB := teamA.String()+"/shop-web", teamA.String()+"/shop-base", teamB.String()+"/shop-web"
		resp, _ := do(ca, baseA, http.MethodGet, "/v2/", nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, "the ping works with a token that has no access")

		// Push and pull in the team's own repositories.
		layerBase, _ := pushImage(ca, baseA, repoA2, "v1")
		layerWeb, digestWeb := pushImage(ca, baseA, repoA, "v1")
		_, digestB := pushImage(cb, baseB, repoB, "v1")
		require.NotEmpty(t, digestWeb)
		resp, body := do(ca, baseA, http.MethodGet, "/v2/"+repoA+"/manifests/v1", nil, map[string]string{"Accept": "application/vnd.docker.distribution.manifest.v2+json"})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, digestWeb, resp.Header.Get("Docker-Content-Digest"))
		require.Contains(t, string(body), layerWeb)
		resp, _ = do(ca, baseA, http.MethodGet, "/v2/"+repoA+"/blobs/"+layerWeb, nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp, body = do(ca, baseA, http.MethodGet, "/v2/"+repoA+"/tags/list", nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, string(body), `"v1"`)

		// Cross-repo mount inside the team: 201 means the registry accepted a token naming both repositories.
		resp, body = do(ca, baseA, http.MethodPost, "/v2/"+repoA+"/blobs/uploads/?mount="+layerBase+"&from="+repoA2, nil, nil)
		require.Equal(t, http.StatusCreated, resp.StatusCode, "mount within the team: %s", body)

		// Team B cannot reach team A's repositories, nor push into them, nor mount from them; the catalog is closed.
		for name, tc := range map[string]struct{ method, path string }{
			"pull A from B":    {http.MethodGet, "/v2/" + repoA + "/manifests/v1"},
			"blob A from B":    {http.MethodGet, "/v2/" + repoA + "/blobs/" + layerWeb},
			"push A from B":    {http.MethodPost, "/v2/" + repoA + "/blobs/uploads/"},
			"delete A from B":  {http.MethodDelete, "/v2/" + repoA + "/manifests/" + digestWeb},
			"mount from A":     {http.MethodPost, "/v2/" + repoB + "/blobs/uploads/?mount=" + layerWeb + "&from=" + repoA},
			"catalog":          {http.MethodGet, "/v2/_catalog"},
			"tags of A from B": {http.MethodGet, "/v2/" + repoA + "/tags/list"},
		} {
			resp, _ := do(cb, baseB, tc.method, tc.path, nil, nil)
			require.Equal(t, http.StatusForbidden, resp.StatusCode, name)
		}
		// …and A's data is intact.
		resp, _ = do(ca, baseA, http.MethodGet, "/v2/"+repoA+"/manifests/v1", nil, map[string]string{"Accept": "application/vnd.docker.distribution.manifest.v2+json"})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp, _ = do(cb, baseB, http.MethodGet, "/v2/"+repoB+"/manifests/v1", nil, map[string]string{"Accept": "application/vnd.docker.distribution.manifest.v2+json"})
		require.Equal(t, digestB, resp.Header.Get("Docker-Content-Digest"))

		// Behind the forwarder: Distribution itself matches names exactly. A token naming repoA grants nothing
		// on a sibling, a child, a prefix, the catalog or a mount source it does not name.
		direct := func(method, path string, access ...registries.RepoAccess) int {
			tok, err := registries.SignRepoToken(st.ms, time.Now(), time.Minute, access...)
			require.NoError(t, err)
			req, _ := http.NewRequest(method, st.regURL+path, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()
			return resp.StatusCode
		}
		pull := registries.RepoAccess{Name: repoA, Actions: []string{"pull"}}
		require.Equal(t, http.StatusOK, direct(http.MethodGet, "/v2/"+repoA+"/tags/list", pull), "the exact name works")
		require.Equal(t, http.StatusUnauthorized, direct(http.MethodGet, "/v2/"+repoA2+"/tags/list", pull), "a sibling does not")
		require.Equal(t, http.StatusUnauthorized, direct(http.MethodGet, "/v2/"+repoA+"/child/tags/list", pull), "a child does not")
		require.Equal(t, http.StatusUnauthorized, direct(http.MethodGet, "/v2/"+teamA.String()+"/shop/tags/list", pull), "a shorter name does not")
		require.Equal(t, http.StatusUnauthorized, direct(http.MethodGet, "/v2/"+repoB+"/tags/list", pull), "another team's does not")
		require.Equal(t, http.StatusUnauthorized, direct(http.MethodGet, "/v2/_catalog", pull), "the catalog does not")
		require.Equal(t, http.StatusUnauthorized, direct(http.MethodPost, "/v2/"+repoA+"/blobs/uploads/", pull), "a pull token does not push")
		wildcard := registries.RepoAccess{Name: teamA.String() + "/*", Actions: []string{"pull"}}
		require.Equal(t, http.StatusUnauthorized, direct(http.MethodGet, "/v2/"+repoA+"/tags/list", wildcard), "a pattern is not a name")
		// A mount whose source the token does not name is refused outright; it never links B's blob.
		pushOnly := registries.RepoAccess{Name: repoA, Actions: []string{"pull", "push"}}
		require.Equal(t, http.StatusUnauthorized, direct(http.MethodPost, "/v2/"+repoA+"/blobs/uploads/?mount="+layerWeb+"&from="+repoB, pushOnly), "no pull grant on the source: no mount")
		bothNamed := []registries.RepoAccess{pushOnly, {Name: repoA2, Actions: []string{"pull"}}}
		require.Equal(t, http.StatusAccepted, direct(http.MethodPost, "/v2/"+repoA+"/blobs/uploads/?mount="+sha([]byte("absent"))+"&from="+repoA2, bothNamed...), "with the source named the mount is understood (blob absent: a normal upload)")
		// B's blob is not readable through A's repository.
		require.Equal(t, http.StatusNotFound, direct(http.MethodHead, "/v2/"+repoA+"/blobs/"+sha([]byte("not here")), pull))

		// Delete and read-only: a delete through the forwarder, then the maintenance window refuses writes.
		resp, _ = do(ca, baseA, http.MethodDelete, "/v2/"+repoA2+"/manifests/v1", nil, nil)
		require.Contains(t, []int{http.StatusAccepted, http.StatusOK}, resp.StatusCode, "delete is enabled")
		reg.readOnly.Store(true)
		resp, _ = do(ca, baseA, http.MethodPut, "/v2/"+repoA+"/manifests/v2", []byte("{}"), nil)
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		resp, _ = do(ca, baseA, http.MethodGet, "/v2/"+repoA+"/manifests/v1", nil, map[string]string{"Accept": "application/vnd.docker.distribution.manifest.v2+json"})
		require.Equal(t, http.StatusOK, resp.StatusCode, "pulls work in the window")
		reg.readOnly.Store(false)

		// The garbage collector the agent's registry_gc operation runs (same container, same argv; see
		// internal/agent/engine/instanceop.go) works on a registry that holds data and reclaims what the delete freed.
		out := sh(t, "", "docker", "exec", agentprotocol.RegistryContainerName, "/bin/registry", "garbage-collect", "/etc/distribution/config.yml")
		require.Contains(t, out, "eligible for deletion", "the layer of the deleted manifest is unreferenced now")
	})

	// ---- Loki: the tenant cannot be spoofed ---------------------------------------------------------
	t.Run("loki tenant spoofing is denied", func(t *testing.T) {
		loki, err := lokiadmin.New(lokiadmin.Config{URL: st.lokiU})
		require.NoError(t, err)
		require.Eventually(t, func() bool { return loki.Health(ctx) == nil }, 120*time.Second, time.Second, "loki never became ready")
		agentListener(t, ctx, f, "127.0.0.1:18100", srvA, agentprotocol.TunnelLoki)
		bAddr := agentListener(t, ctx, f, "127.0.0.1:0", srvB, agentprotocol.TunnelLoki)

		push := func(base, tenantHeader, marker string) int {
			t.Helper()
			body, _ := json.Marshal(map[string]any{"streams": []map[string]any{{
				"stream": map[string]string{"server": "srv", "service_id": "svc", "service": "web"},
				"values": [][2]string{{fmt.Sprintf("%d", time.Now().UnixNano()), marker}},
			}}})
			req, _ := http.NewRequest(http.MethodPost, base+"/loki/api/v1/push", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tenantHeader != "" {
				req.Header.Set("X-Scope-OrgID", tenantHeader)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()
			return resp.StatusCode
		}
		// Team A's agent claims to be team B, three ways.
		require.Equal(t, http.StatusNoContent, push("http://127.0.0.1:18100", teamB.String(), "spoofed-a-as-b"))
		require.Equal(t, http.StatusNoContent, push("http://127.0.0.1:18100", "", "honest-a"))
		require.Equal(t, http.StatusNoContent, push("http://"+bAddr.String(), teamA.String(), "spoofed-b-as-a"))
		require.Equal(t, http.StatusNoContent, push("http://"+bAddr.String(), "", "honest-b"))

		lines := func(tenant string) []string {
			t.Helper()
			var all []string
			require.Eventually(t, func() bool {
				streams, err := loki.QueryRange(ctx, tenant, `{server="srv"}`, time.Now().Add(-time.Hour), time.Now().Add(time.Minute), 100)
				if err != nil {
					return false
				}
				all = nil
				for _, s := range streams {
					for _, e := range s.Entries {
						all = append(all, e.Line)
					}
				}
				return len(all) >= 2
			}, 60*time.Second, time.Second, "tenant %s never showed its lines", tenant)
			return all
		}
		a, b := lines(teamA.String()), lines(teamB.String())
		require.ElementsMatch(t, []string{"honest-a", "spoofed-a-as-b"}, a, "a client-supplied tenant is ignored: the line lands in the stream's own team")
		require.ElementsMatch(t, []string{"honest-b", "spoofed-b-as-a"}, b)

		// Loki itself refuses a request without a tenant; the control plane's purge removes a team's logs.
		resp, err := http.Get(st.lokiU + "/loki/api/v1/labels")
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "auth_enabled: no tenant, no data")
		require.NoError(t, loki.Purge(ctx, *fs.teams[teamB].Loki), "the delete API accepts the tenant's purge")
	})
}
