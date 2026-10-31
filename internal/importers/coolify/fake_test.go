package coolify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Canary values: they must never appear in a plan, an audit entry or an error.
const (
	fakeToken       = "1|coolify-token-canary-9f3a"
	pgPassword      = "pg-canary-Pa55w0rdAAAA"
	pgPassword2     = "tracker-pw-canary-77"
	valkeyPassword  = "valkey-canary-Zk82mq1234"
	appKeyValue     = "base64:app-key-canary-QQQQQQ"
	mailPassword    = "mail-canary-pw-123"
	lockedHidden    = "locked-canary-never-returned"
	s3Secret        = "s3-secret-canary-XYZ"
	explicitCanary  = "explicit-value-canary"
	ghcrCanaryToken = "ghcr-token-canary-abc"
)

// UUIDs of the seeded resources.
const (
	uuidDash    = "dashapp0000000000000000a"
	uuidWeb     = "webapp00000000000000000b"
	uuidSSR     = "ssrapp00000000000000000c"
	uuidWorker  = "workerapp00000000000000d"
	uuidPGDash  = "pgdash0000000000000000ee"
	uuidPGExp   = "pgexp00000000000000000ff"
	uuidValkey  = "valkey0000000000000000gg"
	uuidProjDsh = "proj-dash"
	uuidProjExp = "proj-shop"
)

type fakeCoolify struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	requests []string
	// sensitive controls read:sensitive: without it values and options are hidden.
	sensitive bool
	extraApps []map[string]any
}

func newFakeCoolify(t *testing.T) *fakeCoolify {
	t.Helper()
	f := &fakeCoolify{t: t, sensitive: true}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCoolify) url() string { return f.srv.URL }

func env(key, value string, flags ...string) map[string]any {
	e := map[string]any{"key": key, "value": value, "real_value": value, "is_runtime": true, "is_buildtime": false, "is_preview": false, "is_shown_once": false, "is_literal": false, "is_multiline": false}
	for _, fl := range flags {
		switch fl {
		case "build":
			e["is_buildtime"] = true
		case "buildonly":
			e["is_buildtime"], e["is_runtime"] = true, false
		case "locked":
			e["is_shown_once"] = true
			delete(e, "value")
			delete(e, "real_value")
		case "preview":
			e["is_preview"] = true
		}
	}
	return e
}

func (f *fakeCoolify) apps() []map[string]any {
	base := func(uuid, name string, envID int) map[string]any {
		return map[string]any{
			"id": len(uuid), "uuid": uuid, "name": name, "environment_id": envID, "fqdn": "", "build_pack": "dockerimage", "ports_exposes": "8080",
			"health_check_enabled": false, "health_check_path": "/", "health_check_port": nil, "health_check_return_code": 200, "health_check_interval": 5,
			"health_check_timeout": 5, "health_check_retries": 10, "health_check_start_period": 5, "health_check_type": "http",
			"limits_memory": "0", "limits_memory_swap": "0", "limits_memory_reservation": "0", "limits_cpus": "0", "limits_cpuset": nil, "limits_cpu_shares": 1024,
			"base_directory": "/", "dockerfile_location": "/Dockerfile", "custom_docker_run_options": nil, "pre_deployment_command": nil, "post_deployment_command": nil,
			"is_http_basic_auth_enabled": false, "custom_labels": nil,
		}
	}
	dash := base(uuidDash, "Dashboard", 1)
	dash["build_pack"] = "dockerfile"
	dash["git_repository"] = "acme/tracker"
	dash["git_full_url"] = "https://github.com/acme/tracker"
	dash["git_branch"] = "main"
	dash["git_commit_sha"] = "HEAD"
	dash["source_id"] = 1
	dash["fqdn"] = "https://home.example.com,https://share.example.com"
	dash["noindex_domains"] = []string{"https://share.example.com"}
	dash["custom_labels"] = "dHJhZWZpay5odHRwLm1pZGRsZXdhcmVzLmd6aXAuY29tcHJlc3M9dHJ1ZQ=="

	web := base(uuidWeb, "shop-web", 2)
	web["docker_registry_image_name"] = "ghcr.io/acme/shop"
	web["docker_registry_image_tag"] = "v0.6.0"
	web["fqdn"] = "https://shop.example.com"
	web["health_check_enabled"] = true
	web["health_check_path"] = "/up"
	web["pre_deployment_command"] = "php artisan migrate --force"
	web["ports_mappings"] = ""

	ssr := base(uuidSSR, "shop-ssr", 2)
	ssr["docker_registry_image_name"] = "ghcr.io/acme/shop"
	ssr["docker_registry_image_tag"] = "v0.6.0"
	ssr["ports_exposes"] = "13714"
	ssr["health_check_enabled"] = true
	ssr["health_check_path"] = "/health"
	ssr["custom_docker_run_options"] = `--entrypoint "docker-php-serversideup-entrypoint bun /var/www/html/bootstrap/ssr/ssr.js"`

	worker := base(uuidWorker, "shop-worker", 2)
	worker["docker_registry_image_name"] = "ghcr.io/acme/shop"
	worker["docker_registry_image_tag"] = "v0.6.0"
	worker["ports_exposes"] = "80"
	worker["custom_docker_run_options"] = `--entrypoint "docker-php-serversideup-entrypoint php /var/www/html/artisan queue:work --tries=3 --timeout=60"`

	out := []map[string]any{dash, web, ssr, worker}
	out = append(out, f.extraApps...)
	if !f.sensitive {
		for _, a := range out {
			for _, k := range []string{"custom_docker_run_options", "pre_deployment_command", "post_deployment_command", "start_command", "install_command", "build_command", "custom_labels"} {
				delete(a, k)
			}
		}
	}
	return out
}

func (f *fakeCoolify) envsOf(uuid string) []map[string]any {
	pgURL := "postgres://shop:" + pgPassword + "@" + uuidPGExp + ":5432/shop"
	laravel := []map[string]any{
		env("APP_NAME", "Shop"),
		env("APP_KEY", appKeyValue),
		env("APP_URL", "https://shop.example.com"),
		env("DB_CONNECTION", "pgsql"),
		env("DB_HOST", uuidPGExp),
		env("DB_PORT", "5432"),
		env("DB_DATABASE", "shop"),
		env("DB_USERNAME", "shop"),
		env("DB_PASSWORD", pgPassword),
		env("DATABASE_URL", pgURL),
		env("REDIS_HOST", uuidValkey),
		env("REDIS_PORT", "6379"),
		env("REDIS_PASSWORD", valkeyPassword),
		env("REDIS_URL", "redis://default:"+valkeyPassword+"@"+uuidValkey+":6379/0"),
		env("REDIS_CACHE_URL", "redis://default:"+valkeyPassword+"@"+uuidValkey+":6379/1"),
		env("INERTIA_SSR_URL", "http://"+uuidSSR+":13714"),
		env("AUTORUN_LARAVEL_MIGRATION", "true"),
		env("MAIL_PASSWORD", mailPassword),
		env("MAIL_USERNAME", "mailer"),
		env("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE"),
		env("AWS_SECRET_ACCESS_KEY", s3Secret),
		env("VITE_APP_NAME", "Shop", "build"),
		env("NODE_ENV", "production", "buildonly"),
		env("PORT", "8080"),
		env("COOLIFY_FQDN", "shop.example.com"),
		env("SERVICE_FQDN_WEB", "shop.example.com"),
		env("PREVIEW_ONLY", "x", "preview"),
		env("LOCKED_SECRET", lockedHidden, "locked"),
		env("LITERAL_BRACES", "price ${{ 5 }} usd"),
	}
	switch uuid {
	case uuidWeb:
		return laravel
	case uuidWorker:
		return laravel
	case uuidSSR:
		return []map[string]any{env("NODE_ENV", "production"), env("SSR_PORT", "13714"), env("APP_KEY", appKeyValue)}
	case uuidDash:
		return []map[string]any{
			env("DATABASE_URL", "postgres://tracker:"+pgPassword2+"@"+uuidPGDash+":5432/tracker", "build"),
			env("TRACKER_MASTER_KEY", "mk-canary-1"),
			env("TRACKER_PUBLIC_URL", "https://home.example.com"),
			env("OIDC_CLIENT_SECRET", "oidc-canary"),
			env("S3_ENDPOINT", "https://s3.example.com"),
			env("VAPID_PUBLIC_KEY", "pub-key-not-secret"),
			env("VAPID_PRIVATE_KEY", "priv-key-canary"),
		}
	}
	return nil
}

func (f *fakeCoolify) databases() []map[string]any {
	pgDash := map[string]any{
		"uuid": uuidPGDash, "name": "tracker-postgres", "database_type": "standalone-postgresql", "image": "postgres:18-alpine", "environment_id": 1,
		"is_public": false, "postgres_user": "tracker", "postgres_password": pgPassword2, "postgres_db": "tracker",
		"internal_db_url": "postgres://tracker:" + pgPassword2 + "@" + uuidPGDash + ":5432/tracker",
	}
	pgExp := map[string]any{
		"uuid": uuidPGExp, "name": "shop-postgres", "database_type": "standalone-postgresql", "image": "postgis/postgis:18-3.6", "environment_id": 2,
		"is_public": true, "public_port": 54321, "postgres_user": "shop", "postgres_password": pgPassword, "postgres_db": "shop",
		"internal_db_url": "postgres://shop:" + pgPassword + "@" + uuidPGExp + ":5432/shop", "postgres_conf": "shared_buffers=256MB",
	}
	valkey := map[string]any{
		"uuid": uuidValkey, "name": "shop-valkey", "database_type": "standalone-redis", "image": "valkey/valkey:8-alpine", "environment_id": 2,
		"redis_password": valkeyPassword, "internal_db_url": "redis://default:" + valkeyPassword + "@" + uuidValkey + ":6379/0",
	}
	out := []map[string]any{pgDash, pgExp, valkey}
	if !f.sensitive {
		for _, d := range out {
			for k := range d {
				if strings.HasSuffix(k, "_password") || k == "internal_db_url" || k == "postgres_conf" {
					delete(d, k)
				}
			}
		}
	}
	return out
}

func (f *fakeCoolify) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.Path)
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		http.Error(w, `{"message":"Unauthenticated."}`, http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case path == "/version":
		_, _ = w.Write([]byte("4.5.0"))
	case path == "/projects":
		reply([]map[string]any{
			{"id": 1, "uuid": uuidProjDsh, "name": "Dashboard", "description": "d"},
			{"id": 2, "uuid": uuidProjExp, "name": "shop", "description": nil},
		})
	case path == "/projects/"+uuidProjDsh+"/environments":
		reply([]map[string]any{{"id": 1, "name": "production", "project_id": 1}})
	case path == "/projects/"+uuidProjExp+"/environments":
		reply([]map[string]any{{"id": 2, "name": "production", "project_id": 2}})
	case path == "/applications":
		reply(f.apps())
	case strings.HasPrefix(path, "/applications/"):
		rest := strings.TrimPrefix(path, "/applications/")
		uuid, sub, _ := strings.Cut(rest, "/")
		switch sub {
		case "":
			for _, a := range f.apps() {
				if a["uuid"] == uuid {
					reply(a)
					return
				}
			}
			http.NotFound(w, r)
		case "envs":
			envs := f.envsOf(uuid)
			if !f.sensitive {
				for _, e := range envs {
					delete(e, "value")
					delete(e, "real_value")
				}
			}
			reply(envs)
		case "storages":
			st := map[string]any{"persistent_storages": []any{}, "file_storages": []any{}}
			if uuid == uuidWeb {
				st["persistent_storages"] = []any{map[string]any{"name": uuidWeb + "-storage", "mount_path": "/var/www/html/storage/app"}}
			}
			reply(st)
		case "scheduled-tasks":
			if uuid == uuidWeb {
				reply([]any{map[string]any{"name": "schedule", "frequency": "* * * * *", "enabled": true, "command": "php artisan schedule:run"}})
				return
			}
			reply([]any{})
		default:
			http.NotFound(w, r)
		}
	case path == "/databases":
		reply(f.databases())
	case strings.HasPrefix(path, "/databases/"):
		rest := strings.TrimPrefix(path, "/databases/")
		uuid, sub, _ := strings.Cut(rest, "/")
		if sub == "storages" {
			reply(map[string]any{"persistent_storages": []any{}})
			return
		}
		for _, d := range f.databases() {
			if d["uuid"] == uuid {
				reply(d)
				return
			}
		}
		http.NotFound(w, r)
	case path == "/services":
		reply([]map[string]any{{"uuid": "svc1", "name": "plausible", "service_type": "plausible-analytics", "environment_id": 2}})
	case path == "/servers":
		reply([]map[string]any{{"uuid": "srv1", "name": "homelab-1", "ip": "203.0.113.10"}})
	case path == "/servers/srv1/registries":
		reply(map[string]any{"registries": []any{map[string]any{"registry": "ghcr.io", "logged_in": true, "username": "acme-bot", "used_by": []any{map[string]any{"type": "Application", "name": "shop-web"}}}}})
	default:
		http.NotFound(w, r)
	}
}

// newRedirector serves a 302 to the same path on target.
func newRedirector(t *testing.T, target string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
