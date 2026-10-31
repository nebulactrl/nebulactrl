//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nebulactrl/nebulactrl/internal/templates"
	"github.com/nebulactrl/nebulactrl/internal/templates/testsample"
	catalogfs "github.com/nebulactrl/nebulactrl/templates"
)

// envTemplates selects the templates TestTemplatesDeploySample deploys:
//
//	(unset), "sample"  the default, bounded sample (see testsample.Select): a
//	                   daily rotation of testsample.RotationSize templates plus
//	                   at most testsample.MaxChanged of the templates changed
//	                   against origin/main (rotating by day, so the rest follow
//	                   on later days). Heavy templates are never in it. Every
//	                   template is still validated statically; the skipped ones
//	                   are logged.
//	all                the whole catalog, heavy templates included (hours)
//	id[,id...]         exactly these, heavy ones too
const envTemplates = "NEBULACTRL_TEST_TEMPLATES"

// changedTemplates lists the catalog templates changed relative to origin/main
// (HEAD~1 when there is no origin/main; nothing when git or the history is not
// available): committed changes since the merge base, plus uncommitted and
// untracked files, so a template being edited is tested before it is committed.
// Deleted templates are not returned.
func changedTemplates(root string, known map[string]bool) []string {
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	git := func(args ...string) (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := sh(ctx, root, "git", args...)
		return out, err == nil
	}
	base := ""
	for _, ref := range []string{"origin/main", "HEAD~1"} {
		if _, ok := git("rev-parse", "--verify", "--quiet", ref+"^{commit}"); ok {
			base = ref
			break
		}
	}
	if base == "" {
		return nil
	}
	const dir = "templates/catalog"
	var files []string
	for _, args := range [][]string{
		{"diff", "--name-only", base + "...HEAD", "--", dir},
		{"diff", "--name-only", "HEAD", "--", dir},
		{"ls-files", "--others", "--exclude-standard", "--", dir},
	} {
		if out, ok := git(args...); ok {
			files = append(files, strings.Fields(out)...)
		}
	}
	var ids []string
	for _, f := range files {
		if path.Dir(f) != dir || path.Ext(f) != ".yaml" {
			continue
		}
		if id := strings.TrimSuffix(path.Base(f), ".yaml"); known[id] && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// selectTemplates applies envTemplates to the catalog.
func selectTemplates(t testing.TB, cat *templates.Catalog, root string, now time.Time) []string {
	all := cat.Search("", "")
	known := map[string]bool{}
	var ids []string
	for _, tpl := range all {
		known[tpl.ID] = true
		ids = append(ids, tpl.ID)
	}
	slices.Sort(ids)
	switch v := tplGetenv(envTemplates); v {
	case "", "sample":
		changed := changedTemplates(root, known)
		sel := testsample.Select(all, changed, now.UTC().Unix()/86400)
		t.Logf("deploying %d of %d templates: rotation of the day %v, changed %v", len(sel.IDs), len(ids), sel.Rotation, sel.Changed)
		if len(sel.SkippedChanged) > 0 {
			t.Logf("changed templates not deployed this run (cap %d, they rotate in on other days): %v", testsample.MaxChanged, sel.SkippedChanged)
		}
		if len(sel.SkippedHeavy) > 0 {
			t.Logf("heavy templates not deployed (need a manual check, or name them in %s): %v", envTemplates, sel.SkippedHeavy)
		}
		t.Logf("not deployed: the other %d templates (static validation only); set %s=all or a list of ids for a wider sweep", len(ids)-len(sel.IDs), envTemplates)
		return sel.IDs
	case "all":
		return ids
	default:
		want := tplSplit(v)
		for _, id := range want {
			require.True(t, known[id], "%s=%q: no template %q in the catalog", envTemplates, v, id)
		}
		return want
	}
}

// probeOf returns what to check through the proxy after a template deployed: the
// HTTP health path of the service that gets a generated domain ("" = no domain,
// so nothing to probe), and how much longer than usual to wait (databases and
// heavy apps).
func probeOf(tpl *templates.CatalogTemplate) (probe string, slow time.Duration) {
	for _, s := range tpl.Services {
		if s.Kind == "database" || s.Database != nil {
			slow = 2 * time.Minute
		}
		if len(s.Domains) > 0 && probe == "" {
			probe = "/"
			if h := s.Healthcheck; h != nil && h.Type == "http" && h.Path != "" {
				probe = h.Path
			}
		}
	}
	if tpl.Requires.MemoryMB >= 1024 {
		slow = 2 * time.Minute
	}
	return probe, slow
}

// TestTemplatesDeploySample installs catalog templates through the API with
// deploy:true, waits for every deployment, probes the generated domain through
// Traefik, and removes stack, volumes and images again. Which templates run is
// decided by envTemplates: by default a daily rotating sample plus the
// templates changed against origin/main; the whole catalog is deployed only
// with NEBULACTRL_TEST_TEMPLATES=all. Exclusive: it sets the server's base
// domain.
func TestTemplatesDeploySample(t *testing.T) {
	requireTier(t, tierFull)
	if testing.Short() {
		t.Skip("pulls images")
	}
	cat, errs := templates.LoadFS(catalogfs.FS, "catalog")
	require.Empty(t, errs)
	root, err := repoRoot()
	require.NoError(t, err)
	ids := selectTemplates(t, cat, root, time.Now())
	if len(ids) == 0 {
		t.Skip("no templates selected")
	}

	s := ExclusiveStack(t)
	_, err = s.Pool.Exec(context.Background(), `UPDATE servers SET base_domain='localhost' WHERE id=$1`, s.ServerID)
	require.NoError(t, err)

	for _, id := range ids {
		tpl, _ := cat.Get(id)
		probe, slow := probeOf(tpl)
		inputs := testsample.InputValues(tpl)
		t.Run(id, func(t *testing.T) {
			deployTemplate(t, s, id, inputs, probe, slow)
		})
	}
}

func deployTemplate(t *testing.T, s *Stack, id string, inputs map[string]string, probe string, slow time.Duration) {
	timeout := 6*time.Minute + slow
	r := s.Do("POST", "/environments/"+s.EnvID+"/templates/"+id+"/plan", map[string]any{"serverId": s.ServerID, "inputs": inputs})
	require.Equal(t, http.StatusOK, r.Status, string(r.Raw))
	// A template that does not support the host's architecture (amd64-only
	// images on an arm64 machine) cannot be deployed here: the plan says so, and
	// that is the right answer, not a failure of the template.
	for _, c := range r.JSON()["checks"].([]any) {
		if m := J(c.(map[string]any)); m.Str("id") == "arch" && m.Str("status") == "fail" {
			t.Skipf("not deployable on this host: %s", m.Str("message"))
		}
	}
	require.Equal(t, true, r.JSON()["installable"], string(r.Raw))

	r = s.Do("POST", "/environments/"+s.EnvID+"/templates/"+id+"/instances",
		map[string]any{"serverId": s.ServerID, "inputs": inputs, "deploy": true}, "Idempotency-Key", "tpl-"+id)
	require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
	res := r.JSON()
	instID := res.Obj("instance").Str("id")

	var images, startedDeployments []string
	type svcInfo struct{ id, name, kind, host string }
	var svcs []svcInfo
	for _, v := range res["services"].([]any) {
		m := J(v.(map[string]any))
		s.servicesMu.Lock()
		s.serviceIDs = append(s.serviceIDs, m.Str("id"))
		s.servicesMu.Unlock()
		info := svcInfo{id: m.Str("id"), name: m.Str("name"), kind: m.Str("kind")}
		if doms, _ := m["domains"].([]any); len(doms) > 0 {
			info.host = J(doms[0].(map[string]any)).Str("hostname")
		}
		if img := m.Obj("config").Str("image"); img != "" {
			images = append(images, img)
		}
		svcs = append(svcs, info)
	}
	// Only images this test pulled are removed: postgres & co. may be in use by
	// other containers on the host (and are rate-limited to pull again).
	before := localImageIDs(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		// A stack with unfinished deployments cannot be deleted: cancel and drain them first.
		for _, d := range startedDeployments {
			s.Do("POST", "/deployments/"+d+"/cancel", nil)
			eventually(60*time.Second, time.Second, func() bool {
				r := s.Do("GET", "/deployments/"+d, nil)
				return r.Status != http.StatusOK || terminal(r.JSON().Str("status"))
			})
		}
		rr := s.Do("DELETE", "/template-instances/"+instID+"?confirm="+id+"&deleteVolumes=true", nil)
		if rr.Status != http.StatusNoContent {
			t.Logf("delete template instance: %d %s", rr.Status, rr.Raw)
		}
		// The agent removes containers and volumes asynchronously.
		eventually(90*time.Second, time.Second, func() bool {
			for _, sv := range svcs {
				if len(s.Containers(sv.id)) > 0 {
					return false
				}
			}
			return true
		})
		for _, img := range images {
			id, err := sh(ctx, "", "docker", "image", "inspect", "-f", "{{.Id}}", img)
			if err != nil || before[id] {
				continue
			}
			_, _ = sh(ctx, "", "docker", "image", "rm", "-f", img)
		}
		_, _ = sh(ctx, "", "docker", "image", "prune", "-f")
	})

	deps, _ := res["deployments"].([]any)
	for _, d := range deps {
		startedDeployments = append(startedDeployments, J(d.(map[string]any)).Str("id"))
	}
	require.NotEmpty(t, deps, "deploy:true must start deployments: %s", r.Raw)
	for _, d := range deps {
		depID := J(d.(map[string]any)).Str("id")
		dep := s.WaitDeployment(depID, timeout)
		require.Equal(t, "succeeded", dep.Str("status"), "deployment %s: %v\nlogs:\n%s", depID, dep, s.DeploymentLogs(depID))
	}

	for _, sv := range svcs {
		view := s.Service(sv.id)
		switch {
		case view.Obj("config").Bool("oneshot"):
			cs := s.Containers(sv.id)
			require.NotEmpty(t, cs, "one-shot %s left no container", sv.name)
			require.False(t, cs[0].Running, "one-shot %s must have exited", sv.name)
			require.Zero(t, cs[0].ExitCode, "one-shot %s exit code", sv.name)
		case sv.kind == "route":
		default:
			require.NotEmpty(t, s.RunningContainers(sv.id), "service %s is not running", sv.name)
		}
		// Generated secrets are never readable.
		vr := s.Do("GET", "/services/"+sv.id+"/variables", nil)
		require.Equal(t, http.StatusOK, vr.Status, string(vr.Raw))
		for _, it := range vr.JSON().Items() {
			if it["isSecret"] == true {
				_, has := it["value"]
				require.False(t, has && it["value"] != nil, "secret %s of %s returned a value", it.Str("key"), sv.name)
			}
		}
	}

	if probe == "" {
		return
	}
	var host string
	for _, sv := range svcs {
		if sv.host != "" {
			host = sv.host
			break
		}
	}
	require.NotEmpty(t, host, "expected a generated domain")
	pc := NewProxyClient(host)
	var code int
	var err error
	ok := eventually(90*time.Second, time.Second, func() bool {
		code, _, err = pc.Get(probe)
		return err == nil && code > 0 && code < 500
	})
	require.True(t, ok, "GET https://%s%s through the proxy: last %d err=%v", host, probe, code, err)
}

// TestFilesExecHealthAndOneShot covers the template prerequisites on the agent
// with a tiny image: config files are in place at first start, an exec health
// check gates the deploy, a one-shot succeeds on exit 0 and fails otherwise.
func TestFilesExecHealthAndOneShot(t *testing.T) {
	requireTier(t, tierFull)
	t.Parallel()
	s := SharedStack(t)
	const image = "quay.io/prometheus/busybox:latest"
	had := localImageIDs(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if id, err := sh(ctx, "", "docker", "image", "inspect", "-f", "{{.Id}}", image); err == nil && !had[id] {
			_, _ = sh(ctx, "", "docker", "image", "rm", "-f", image)
		}
	})

	create := func(name string, cfg map[string]any) string {
		base := map[string]any{"image": image, "stopGraceSeconds": 2}
		for k, v := range cfg {
			base[k] = v
		}
		r := s.Do("POST", "/environments/"+s.EnvID+"/services", map[string]any{
			"name": name, "sourceType": "image", "serverId": s.ServerID, "config": base,
		})
		require.Equal(t, http.StatusCreated, r.Status, string(r.Raw))
		id := r.JSON().Str("id")
		s.servicesMu.Lock()
		s.serviceIDs = append(s.serviceIDs, id)
		s.servicesMu.Unlock()
		return id
	}

	// A long-running service: file present, exec health check passes only because of the file.
	web := create("files", map[string]any{
		"startCommand": []string{"sh", "-c", "sleep 3600"},
		"files":        []map[string]any{{"path": "/etc/nebula/hello.txt", "mode": "0640", "content": "hello from a template\n"}},
		"healthcheck": map[string]any{
			"type": "exec", "command": []string{"CMD", "test", "-f", "/etc/nebula/hello.txt"},
			"intervalSeconds": 1, "deadlineSeconds": 60, "successThreshold": 1,
		},
	})
	s.DeployOK(web)
	cs := s.RunningContainers(web)
	require.Len(t, cs, 1)
	out, err := sh(context.Background(), "", "docker", "exec", cs[0].ID, "sh", "-c", "cat /etc/nebula/hello.txt && stat -c %a /etc/nebula/hello.txt")
	require.NoError(t, err)
	require.Equal(t, "hello from a template\n640", strings.TrimSpace(out))
	require.Equal(t, "healthy", cs[0].Health, "the exec check is the container HEALTHCHECK")

	// An exec check that never passes fails the deploy and leaves nothing running.
	bad := create("badhealth", map[string]any{
		"startCommand": []string{"sh", "-c", "sleep 3600"},
		"healthcheck": map[string]any{
			"type": "exec", "command": []string{"CMD", "test", "-f", "/nope"},
			"intervalSeconds": 1, "deadlineSeconds": 8, "successThreshold": 1,
		},
	})
	d := s.WaitDeployment(s.Deploy(bad), 2*time.Minute)
	require.Equal(t, "failed", d.Str("status"), fmt.Sprint(d))
	require.Empty(t, s.RunningContainers(bad))

	// One-shot: exit 0 succeeds and the container stays exited; exit 3 fails.
	job := create("job", map[string]any{
		"oneshot":      true,
		"startCommand": []string{"sh", "-c", "echo migrating; exit 0"},
		"healthcheck":  map[string]any{"type": "none", "deadlineSeconds": 60},
	})
	s.DeployOK(job)
	jc := s.Containers(job)
	require.Len(t, jc, 1)
	require.False(t, jc[0].Running)
	require.Zero(t, jc[0].ExitCode)
	require.Equal(t, "no", jc[0].RestartMode, "one-shots are never restarted")
	// The agent reports a cleanly exited one-shot as healthy (observed is reported periodically).
	var obs J
	require.True(t, eventually(2*time.Minute, 2*time.Second, func() bool {
		obs = s.Service(job).Obj("observed")
		return obs.Str("health") == "healthy"
	}), "observed one-shot: %v", obs)

	failing := create("failjob", map[string]any{
		"oneshot":      true,
		"startCommand": []string{"sh", "-c", "echo boom; exit 3"},
		"healthcheck":  map[string]any{"type": "none", "deadlineSeconds": 60},
	})
	d = s.WaitDeployment(s.Deploy(failing), 2*time.Minute)
	require.Equal(t, "failed", d.Str("status"), fmt.Sprint(d))
	require.Contains(t, d.Str("statusMessage")+s.DeploymentLogs(d.Str("id")), "3")
}

// localImageIDs lists the IDs of the images present on the Docker host.
func localImageIDs(ctx context.Context) map[string]bool {
	out, _ := sh(ctx, "", "docker", "images", "-q", "--no-trunc")
	ids := map[string]bool{}
	for _, id := range strings.Fields(out) {
		ids[id] = true
	}
	return ids
}

func tplGetenv(k string) string { return strings.TrimSpace(os.Getenv(k)) }

func tplSplit(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
