package engines

import "testing"

func TestRepoTag(t *testing.T) {
	for ref, want := range map[string][2]string{
		"postgres:17":                            {"postgres", "17"},
		"docker.io/library/Postgres:17-alpine":   {"postgres", "17-alpine"},
		"postgres@sha256:abc":                    {"postgres", ""},
		"registry.example.com:5000/team/app:1.2": {"registry.example.com:5000/team/app", "1.2"},
		"nginx":                                  {"nginx", ""},
	} {
		repo, tag := RepoTag(ref)
		if repo != want[0] || tag != want[1] {
			t.Errorf("RepoTag(%q) = %q, %q; want %q, %q", ref, repo, tag, want[0], want[1])
		}
	}
}

func TestFromImageAndExactVersion(t *testing.T) {
	engine, flavor, tag, ok := FromImage("docker.io/postgis/postgis:17-3.5")
	if !ok || engine != Postgres || flavor != FlavorPostGIS || tag != "17-3.5" {
		t.Fatalf("FromImage = %q %q %q %v", engine, flavor, tag, ok)
	}
	if v, ok := ExactVersion(Lookup(engine), flavor, tag); !ok || v != "17" {
		t.Errorf("ExactVersion postgis = %q %v", v, ok)
	}
	if v, ok := ExactVersion(Lookup(Postgres), "", "17"); !ok || v != "17" {
		t.Errorf("ExactVersion postgres:17 = %q %v", v, ok)
	}
	for _, tag := range []string{"", "17-alpine", "9.6", "latest"} {
		if v, ok := ExactVersion(Lookup(Postgres), "", tag); ok {
			t.Errorf("ExactVersion(%q) = %q, want no match", tag, v)
		}
	}
	if _, ok := ExactVersion(Lookup(Postgres), "nope", "17"); ok {
		t.Error("an unknown flavor must not match")
	}
	if _, _, _, ok := FromImage("nginx:1.27"); ok {
		t.Error("nginx is not a database image")
	}
}
