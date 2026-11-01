---
title: Third-party licences
description: "Dependency licence scan for the AGPL-3.0-only release, with the rules and the images NebulaCtrl deploys."
section: Internals
order: 8
---

# Third-party licences

NebulaCtrl is licensed under **AGPL-3.0-only** ([ADR 0005](../decisions/0005-license.md)). This page records the dependency licence scan and the rule new dependencies must meet.

**Result: no incompatible licence was found** in the Go modules that are compiled into the three binaries, nor in the web packages of `apps/web`. Nothing needs a manual licence decision.

## Rule

A dependency is acceptable when its licence can be combined with the AGPL-3.0:

| Compatible | Not acceptable |
|---|---|
| MIT, MIT-0, ISC, BSD-2-Clause, BSD-3-Clause, 0BSD, Unlicense, CC0-1.0, Apache-2.0 (one-way compatible with GPLv3/AGPLv3), MPL-2.0 (its §3.3 names the AGPL as a Secondary License), LGPL-3.0, GPL-3.0 and AGPL-3.0, OFL-1.1 for fonts, CC-BY-4.0 for data | Proprietary or "source-available" licences (SSPL, BUSL, Elastic, FSL, Bitwarden SDK, non-commercial or no-derivatives terms), GPL-2.0-only, and anything with no licence |

Run the scan again before approving a new dependency and before each release.

## Go (compiled into `nebulactrl-server`, `nebulactrl-agent`, `nebulactrl`)

Command (the tool must be built once, because `go run` cannot cross-compile it; the agent and server ship for Linux):

```sh
GOBIN=/tmp/gl go install github.com/google/go-licenses/v2@latest
GOOS=linux GOARCH=amd64 /tmp/gl/go-licenses csv ./cmd/... ./internal/... ./pkg/...
```

go-licenses v2.0.1 reported 134 licence entries across 112 third-party modules. Summary by licence, counted per module:

| Licence | Modules |
|---|---|
| MIT | 37 |
| Apache-2.0 | 36 |
| BSD-3-Clause | 17 |
| Apache-2.0 plus BSD-3-Clause files | 8 |
| MPL-2.0 | 3 |
| ISC | 2 |
| BSD-2-Clause | 2 |
| MIT plus MPL-2.0 files | 2 |
| BSD-3-Clause plus MPL-2.0 files | 1 |
| BSD-2-Clause plus ISC files | 1 |
| BSD-2-Clause, BSD-3-Clause and ISC files | 1 |
| Apache-2.0 plus MIT files | 1 |
| MIT-0 | 1 |

No GPL, LGPL, AGPL, SSPL, BUSL, proprietary or non-commercial module is in the build.

Notes:

- **MPL-2.0** applies to `github.com/riverqueue/river` (the job queue and its pgx driver), `github.com/cyphar/filepath-securejoin`. MPL-2.0 is file-level copyleft: NebulaCtrl links them unmodified, so only changes to those files would have to be published, and the AGPL is a permitted Secondary License.
- **`github.com/segmentio/asm`** is reported "no license found" by go-licenses because the licence sits in a file the classifier does not recognise. The file is MIT No Attribution (MIT-0), read directly from the module.
- **Apache-2.0** modules that ship a `NOTICE` file are carried in the top-level [`NOTICE`](https://github.com/nebulactrl/nebulactrl/blob/main/NOTICE): `aws-sdk-go-v2`, `smithy-go`, `go-oidc`, `skeema/knownhosts` and `go.yaml.in/yaml/v3`. Distribution of the binaries must keep that file.
- The scan was also run over `./tools/...` and `./tests/...` (including the `integration` build tag): the only additional module is `github.com/stretchr/testify` (MIT, with BSD-3-Clause and ISC internals). These are not shipped.
- **`github.com/tailscale/hujson`** (BSD-3-Clause, compatible) is used for the optional managed Tailscale ACL push of the [server mesh](../operations/mesh.md#the-acl-mode), which edits the user's policy without destroying its comments. It is not part of the counts above; its licence is the upstream project's published BSD-3-Clause and was not scanned with go-licenses.
- `templates/` is embedded in the server binary. It carries its own attribution file (`templates/NOTICE`, Apache-2.0 text for Coolify-derived templates and MIT for Dokploy-derived ones); go-licenses therefore reports that package as Apache-2.0. The licences of the **applications** the templates deploy are recorded per template in its `upstream` block.

### Modules in the build

| Module | Version | Licence |
|---|---|---|
| `dario.cat/mergo` | v1.0.0 | BSD-3-Clause |
| `github.com/ProtonMail/go-crypto` | v1.1.6 | BSD-3-Clause |
| `github.com/alexedwards/argon2id` | v1.0.0 | MIT |
| `github.com/alexedwards/scs/pgxstore` | v0.0.0-20251002162104-209de6e426de | MIT |
| `github.com/alexedwards/scs/v2` | v2.9.0 | MIT |
| `github.com/apapsch/go-jsonmerge/v2` | v2.0.0 | MIT |
| `github.com/aws/aws-sdk-go-v2` | v1.47.4 | Apache-2.0 + BSD-3-Clause |
| `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream` | v1.7.24 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/credentials` | v1.20.10 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager` | v0.4.19 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/internal/configsources` | v1.5.7 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/internal/endpoints/v2` | v2.8.7 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/internal/v4a` | v1.5.7 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding` | v1.13.22 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/service/internal/checksum` | v1.11.8 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/service/internal/presigned-url` | v1.14.7 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/service/internal/s3shared` | v1.20.7 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2/service/s3` | v1.114.5 | Apache-2.0 |
| `github.com/aws/smithy-go` | v1.28.6 | Apache-2.0 + BSD-3-Clause |
| `github.com/axiomhq/hyperloglog` | v0.3.0 | MIT |
| `github.com/boyter/go-string` | v1.0.5 | MIT |
| `github.com/cespare/xxhash/v2` | v2.3.0 | MIT |
| `github.com/cloudflare/circl` | v1.6.3 | BSD-3-Clause |
| `github.com/coder/websocket` | v1.8.15 | ISC |
| `github.com/containerd/errdefs` | v1.0.0 | Apache-2.0 |
| `github.com/containerd/errdefs/pkg` | v0.3.0 | Apache-2.0 |
| `github.com/coreos/go-oidc/v3` | v3.21.0 | Apache-2.0 |
| `github.com/cyphar/filepath-securejoin` | v0.6.1 | BSD-3-Clause + MPL-2.0 |
| `github.com/danielgtaylor/huma/v2` | v2.39.1 | MIT |
| `github.com/dgryski/go-metro` | v0.0.0-20250106013310-edb8663e5e33 | MIT |
| `github.com/distribution/reference` | v0.6.0 | Apache-2.0 |
| `github.com/docker/go-connections` | v0.8.2 | Apache-2.0 |
| `github.com/docker/go-units` | v0.5.0 | Apache-2.0 |
| `github.com/emirpasic/gods` | v1.18.1 | BSD-2-Clause + ISC |
| `github.com/felixge/httpsnoop` | v1.1.0 | MIT |
| `github.com/fxamacker/cbor/v2` | v2.9.4 | MIT |
| `github.com/go-chi/chi/v5` | v5.5.0 | MIT |
| `github.com/go-git/gcfg` | v1.5.1-0.20230307220236-3a3c6141e376 | BSD-3-Clause |
| `github.com/go-git/go-billy/v5` | v5.9.2 | Apache-2.0 |
| `github.com/go-git/go-git/v5` | v5.19.3 | Apache-2.0 |
| `github.com/go-jose/go-jose/v4` | v4.1.4 | Apache-2.0 + BSD-3-Clause |
| `github.com/go-logr/logr` | v1.4.4 | Apache-2.0 |
| `github.com/go-logr/stdr` | v1.2.2 | Apache-2.0 |
| `github.com/go-viper/mapstructure/v2` | v2.5.0 | MIT |
| `github.com/go-webauthn/webauthn` | v0.18.2 | BSD-3-Clause |
| `github.com/go-webauthn/x` | v0.3.1 | BSD-2-Clause + BSD-3-Clause + ISC |
| `github.com/golang-jwt/jwt/v5` | v5.3.1 | MIT |
| `github.com/golang/groupcache` | v0.0.0-20241129210726-2c02b8208cf8 | Apache-2.0 |
| `github.com/google/go-tpm` | v0.9.8 | Apache-2.0 |
| `github.com/google/jsonschema-go` | v0.4.3 | MIT |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause |
| `github.com/jackc/pgpassfile` | v1.0.0 | MIT |
| `github.com/jackc/pgservicefile` | v0.0.0-20240606120523-5a60cdf6a761 | MIT |
| `github.com/jackc/pgx/v5` | v5.11.0 | MIT |
| `github.com/jackc/puddle/v2` | v2.2.3 | MIT |
| `github.com/jbenet/go-context` | v0.0.0-20150711004518-d14ea06fba99 | MIT |
| `github.com/kamstrup/intmap` | v0.5.2 | BSD-2-Clause |
| `github.com/kevinburke/ssh_config` | v1.2.0 | MIT |
| `github.com/medama-io/go-useragent` | v1.2.5 | MIT |
| `github.com/mfridman/interpolate` | v0.0.2 | MIT |
| `github.com/moby/docker-image-spec` | v1.3.1 | Apache-2.0 |
| `github.com/moby/moby/api` | v1.56.1 | Apache-2.0 |
| `github.com/moby/moby/client` | v0.6.2 | Apache-2.0 |
| `github.com/modelcontextprotocol/go-sdk` | v1.8.0 | Apache-2.0 + MIT |
| `github.com/monperrus/crawler-user-agents` | v1.65.0 | MIT |
| `github.com/oapi-codegen/runtime` | v1.8.0 | Apache-2.0 |
| `github.com/opencontainers/go-digest` | v1.0.0 | Apache-2.0 |
| `github.com/opencontainers/image-spec` | v1.1.1 | Apache-2.0 |
| `github.com/oschwald/maxminddb-golang/v2` | v2.8.0 | ISC |
| `github.com/philhofer/fwd` | v1.2.0 | MIT |
| `github.com/pjbgf/sha1cd` | v0.7.0 | Apache-2.0 |
| `github.com/pressly/goose/v3` | v3.29.0 | MIT |
| `github.com/riverqueue/river` | v0.49.0 | MIT + MPL-2.0 |
| `github.com/riverqueue/river/riverdriver` | v0.49.0 | MPL-2.0 |
| `github.com/riverqueue/river/riverdriver/riverpgxv5` | v0.49.0 | MPL-2.0 |
| `github.com/riverqueue/river/rivershared` | v0.49.0 | MIT + MPL-2.0 |
| `github.com/riverqueue/river/rivertype` | v0.49.0 | MPL-2.0 |
| `github.com/segmentio/asm` | v1.2.1 | MIT-0 |
| `github.com/segmentio/encoding` | v0.5.4 | MIT |
| `github.com/sergi/go-diff` | v1.3.2-0.20230802210424-5b0b94c5c0d3 | MIT |
| `github.com/sethvargo/go-retry` | v0.5.0 | Apache-2.0 |
| `github.com/shirou/gopsutil/v4` | v4.26.9 | BSD-3-Clause |
| `github.com/skeema/knownhosts` | v1.3.1 | Apache-2.0 |
| `github.com/spf13/cobra` | v1.10.2 | Apache-2.0 |
| `github.com/spf13/pflag` | v1.0.10 | BSD-3-Clause |
| `github.com/tidwall/gjson` | v1.19.0 | MIT |
| `github.com/tidwall/match` | v1.2.0 | MIT |
| `github.com/tidwall/pretty` | v1.2.1 | MIT |
| `github.com/tidwall/sjson` | v1.2.5 | MIT |
| `github.com/tinylib/msgp` | v1.6.4 | MIT |
| `github.com/tklauser/go-sysconf` | v0.4.0 | BSD-3-Clause |
| `github.com/tklauser/numcpus` | v0.12.0 | Apache-2.0 |
| `github.com/x448/float16` | v0.8.4 | MIT |
| `github.com/xanzy/ssh-agent` | v0.3.3 | Apache-2.0 |
| `github.com/yosida95/uritemplate/v3` | v3.0.2 | BSD-3-Clause |
| `go.etcd.io/bbolt` | v1.5.0 | MIT |
| `go.opentelemetry.io/auto/sdk` | v1.2.1 | Apache-2.0 |
| `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | v0.72.0 | Apache-2.0 + BSD-3-Clause |
| `go.opentelemetry.io/otel` | v1.47.0 | Apache-2.0 + BSD-3-Clause |
| `go.opentelemetry.io/otel/log` | v1.47.0 | Apache-2.0 + BSD-3-Clause |
| `go.opentelemetry.io/otel/metric` | v1.47.0 | Apache-2.0 + BSD-3-Clause |
| `go.opentelemetry.io/otel/trace` | v1.47.0 | Apache-2.0 + BSD-3-Clause |
| `go.uber.org/multierr` | v1.11.0 | MIT |
| `go.yaml.in/yaml/v3` | v3.0.5 | MIT |
| `golang.org/x/crypto` | v0.58.0 | BSD-3-Clause |
| `golang.org/x/net` | v0.61.0 | BSD-3-Clause |
| `golang.org/x/oauth2` | v0.37.0 | BSD-3-Clause |
| `golang.org/x/sync` | v0.24.0 | BSD-3-Clause |
| `golang.org/x/sys` | v0.49.0 | BSD-3-Clause |
| `golang.org/x/text` | v0.43.0 | BSD-3-Clause |
| `golang.org/x/time` | v0.16.0 | BSD-3-Clause |
| `gopkg.in/warnings.v0` | v0.1.2 | BSD-2-Clause |

## Web (`apps/web`)

Method: the `license` field of every `package.json` in the resolved `node_modules` tree, starting from the `dependencies` and `devDependencies` of `apps/web/package.json` and following nested dependencies. 256 packages: 90 reachable from `dependencies` (bundled into the shipped app) and 166 only from `devDependencies` (build and test tools, not shipped).

| Licence | Shipped (90) | Dev only (166) |
|---|---|---|
| MIT | 66 | 130 |
| ISC | 17 | 15 |
| OFL-1.1 (the two IBM Plex font packages) | 2 | – |
| BSD-3-Clause | 1 | 3 |
| BSD-2-Clause | – | 2 |
| Apache-2.0 | 1 (`class-variance-authority`) | 6 (TypeScript, Playwright, `detect-libc`, `baseline-browser-mapping`) |
| 0BSD, Unlicense, MIT AND ISC | 1 each (`tslib`, `isbot`, `victory-vendor`) | – |
| MIT OR Apache-2.0 | – | 2 (Biome) |
| MPL-2.0 | – | 4 (`lightningcss`, build-time CSS transform only) |
| CC-BY-4.0 | – | 1 (`caniuse-lite` browser data, build time) |
| CC0-1.0 | – | 1 (`simple-icons`, source of the brand glyph sprite) |
| Python-2.0, PSF-2.0 | – | 2 (`argparse`, dev tooling) |
| Unknown or missing | 0 | 0 |

Direct dependencies that ship in the app:

| Package | Version | Licence |
|---|---|---|
| `@fontsource-variable/ibm-plex-sans` | 5.3.0 | OFL-1.1 |
| `@fontsource/ibm-plex-mono` | 5.3.0 | OFL-1.1 |
| `@tanstack/react-query` | 5.103.1 | MIT |
| `@tanstack/react-router` | 1.170.38 | MIT |
| `@tanstack/react-virtual` | 3.14.13 | MIT |
| `@xterm/addon-fit` | 0.11.0 | MIT |
| `@xterm/xterm` | 6.0.0 | MIT |
| `@xyflow/react` | 12.12.0 | MIT |
| `class-variance-authority` | 0.7.1 | Apache-2.0 |
| `cn` | 0.3.2 | MIT |
| `lucide-react` | 1.61.0 | ISC |
| `motion` | 13.5.1 | MIT |
| `react` | 19.3.0 | MIT |
| `react-dom` | 19.3.0 | MIT |
| `react-is` | 19.3.0 | MIT |
| `recharts` | 3.10.1 | MIT |
| `zod` | 4.6.5 | MIT |

Notes:

- No GPL, LGPL, AGPL, SSPL, BUSL, proprietary or non-commercial package is in the tree. Fonts are under the SIL OFL-1.1, which permits bundling.
- Code adapted into the repository keeps its own notice in the source files: `lucide-animated` icons (MIT, `apps/web/src/components/icons/animated/LICENSE`) and the dithercn image recipes (MIT, Copyright (c) 2026 Leo and Shadcn Labs (startercn); header comments in `packages/dither/src/components/images/look.ts` and `algorithms.ts`, full MIT text in `NOTICE`). Both are in the top-level `NOTICE`. dithercn's other catalogue entries are rebuilt in-house from its public documentation and no dithercn package is installed. The `nebulactrl/nebula` design-system files are the project author's own Apache-2.0 work and are relicensed under this repository's licence.
- The brand glyphs in `apps/web/src/assets/brand-sprite.svg` come from `simple-icons` (CC0). The logos remain trademarks of their owners and are used to identify the products.
- The marketing site `apps/site` is not covered by this scan. Run the same scan on it, and every manifest in the repository must carry `"license": "AGPL-3.0-only"` (with `"private": true` for unpublished packages).

## Images NebulaCtrl deploys are separate works

NebulaCtrl pulls and runs these container images unmodified, as separate processes that talk to NebulaCtrl over the network or the Docker API. They are not linked into, bundled with or derived from NebulaCtrl, so their licences neither extend to NebulaCtrl nor does the AGPL of NebulaCtrl extend to them. Each keeps its own licence and its own source-offer obligations (which NebulaCtrl does not take on by pulling the image on the user's server).

| Image | Used for | Licence (upstream) |
|---|---|---|
| Traefik v3 | per-server proxy | MIT |
| SeaweedFS 4.51 | built-in S3 backup storage, an instance service on the control-plane host | Apache-2.0 |
| CNCF Distribution (`registry:3.1.2`) | built-in image registry, an instance service on the control-plane host | Apache-2.0 |
| Loki 3.7.9 | built-in central logs, an instance service on the control-plane host | **AGPL-3.0** |
| Headscale | tailnet control server | BSD-3-Clause |
| Tailscale | per-service tailnet device, the proxy's managed node, and the [server mesh](../operations/mesh.md) server and link nodes | BSD-3-Clause |
| `cloudflared` | Cloudflare Tunnel connector | Apache-2.0 |
| nginx (alpine-slim) | maintenance and error pages | BSD-2-Clause |
| Postgres, MySQL, MariaDB, Valkey, Redis, MongoDB, ClickHouse | managed databases | per engine; the catalog labels Redis (RSALv2 / SSPLv1 / AGPLv3) and MongoDB (SSPL) as source-available and requires `acceptLicense` where relevant |

The three instance service images (SeaweedFS, the registry, Loki) are pinned by tag and digest in `deploy/install/docker-compose.yml`; the Headscale image and its licence are recorded in `internal/platform/catalog.go`; the database engines' licences are recorded in `internal/databases/engines/catalog.go`; the others are the upstream projects' published licences and were not re-verified against each image. NebulaCtrl does not run Grafana itself: the Grafana entry in the template catalog is an ordinary user template, installed only when a team chooses it. The licence class of every one-click template is handled separately by the template catalog (`permissive`, `copyleft`, `source-available`, see [templates](../operations/templates.md)).
