---
title: Test mix in large web-plus-backend OSS projects
description: "How 13 large open-source projects split unit, component, integration and e2e tests, run e2e in CI, keep frontend mocks honest and set coverage, and what that means for NebulaCtrl's test mix."
section: Internals
order: 54
---

# Test strategy: what large web + backend projects actually do

Question: NebulaCtrl has ~13,000 web component tests (881 files, 10-minute suite), Go tests mostly against real Postgres, an 8-scenario Docker smoke tier, 3 Playwright specs outside CI, and an 85% Codecov target. Should it keep many fine-grained tests or have fewer, broader ones (integration, e2e)? This builds on [coverage-practices.md](coverage-practices.md), [oss-quality-tooling.md](oss-quality-tooling.md) and [web-ui-testing.md](web-ui-testing.md) and does not repeat them.

Method: blobless clones of each repo on 2026-11-01; **counts are files** matched by name from `git ls-tree` at the pinned commit (JS/TS `*.test|spec.*`, Go `_test.go`, Python `test_*.py`, Elixir `_test.exs`, Playwright/Cypress specs), not test cases, and not run. A file holds from 1 to 100+ cases, so ratios are rough. JS/TS counts include Node backend tests where the backend is TypeScript (n8n, cal.com, outline, immich, supabase). Claims come from workflows, configs and contributor docs at the pin; `[INFERENCE]` is my reading, **unverified** is what I could not read. Required-ness of a check is a repo setting the GitHub API hides from non-admins, so it is unverified everywhere unless a workflow aggregates it. Pins: [grafana](https://github.com/grafana/grafana/tree/01f74f769) `01f74f769`, [gitea](https://github.com/go-gitea/gitea/tree/1f51d2c31) `1f51d2c31`, [mattermost](https://github.com/mattermost/mattermost/tree/2b3fb21fe) `2b3fb21fe`, [coder](https://github.com/coder/coder/tree/bb72d3982) `bb72d3982`, [supabase](https://github.com/supabase/supabase/tree/a05e7ec1d) `a05e7ec1d`, [cal.com](https://github.com/calcom/cal.com/tree/f972feefa) `f972feefa`, [n8n](https://github.com/n8n-io/n8n/tree/c08ba2dd3) `c08ba2dd3`, [posthog](https://github.com/PostHog/posthog/tree/8d1791c18) `8d1791c18`, [sentry](https://github.com/getsentry/sentry/tree/063a6cb27) `063a6cb27`, [immich](https://github.com/immich-app/immich/tree/b7510e79a) `b7510e79a`, [plausible](https://github.com/plausible/analytics/tree/02cfac151) `02cfac151`, [outline](https://github.com/outline/outline/tree/5b10e046f) `5b10e046f`, [excalidraw](https://github.com/excalidraw/excalidraw/tree/8bcdca452) `8bcdca452`. GitLab is not on GitHub at a pinnable commit, so it is out; tldraw is covered in web-ui-testing.md.

## 1. Per project

### Grafana (Go + React, Jest + Playwright)
| Q | Finding |
|---|---|
| Mix | 2,894 JS/TS unit/component files, 2,557 Go (320 in `pkg/tests`, HTTP-level), **242** Playwright specs in `e2e-playwright/` (4% of files) |
| e2e in CI | Every PR, change-gated: [pr-e2e-tests.yml](https://github.com/grafana/grafana/blob/01f74f769/.github/workflows/pr-e2e-tests.yml) builds backend and frontend, then **8 shards** against a real `grafana-server` with provisioned devenv data |
| Drift | Generated RTK Query clients (`packages/grafana-api-clients`, `generate-apis` in [package.json](https://github.com/grafana/grafana/blob/01f74f769/package.json)); 21 shared MSW handler files in `packages/grafana-test-utils/src/handlers`; Go generates "golden files" that the frontend unit job downloads ([pr-frontend-unit-tests.yml](https://github.com/grafana/grafana/blob/01f74f769/.github/workflows/pr-frontend-unit-tests.yml)). A diff gate on the generated clients: **unverified** |
| Coverage | No Codecov config. [check-frontend-test-coverage.yml](https://github.com/grafana/grafana/blob/01f74f769/.github/workflows/check-frontend-test-coverage.yml): only 4 opted-in codeowner teams, runs coverage on main and on the PR, fails the job on a regression. e2e not counted |
| Stance | [e2e-playwright.md](https://github.com/grafana/grafana/blob/01f74f769/contribute/style-guides/e2e-playwright.md): "We generally do not use stubs or mocks as to fully simulate a real user." No prune policy found |

### Gitea (Go, server-rendered, little JS)
| Q | Finding |
|---|---|
| Mix | 1,073 Go files, **251** in `tests/integration` (real DB, sqlite/pg/mysql/mssql), 57 Vitest browser-mode files, **32** Playwright specs |
| e2e in CI | Every PR and merge queue, path-gated ([pull-e2e-tests.yml](https://github.com/go-gitea/gitea/blob/1f51d2c31/.github/workflows/pull-e2e-tests.yml)): builds the binary, `make test-e2e`, 10-minute timeout. Integration runs per database in [pull-db-tests.yml](https://github.com/go-gitea/gitea/blob/1f51d2c31/.github/workflows/pull-db-tests.yml) |
| Drift | Little to drift: pages are server-rendered. Migration tests replay recorded API responses from `_mock_data/` and re-record against the live API by deleting the directory ([docs/testing.md](https://github.com/go-gitea/gitea/blob/1f51d2c31/docs/testing.md)) |
| Coverage | No service config |
| Stance | [AGENTS.md](https://github.com/go-gitea/gitea/blob/1f51d2c31/AGENTS.md): "Write the fewest, fastest tests covering the behavior, extending an existing one where possible. Prefer unit tests where logic is testable in isolation"; "sub-2s per integration test and sub-4s per e2e test" |

### Mattermost (Go + React, Jest, Cypress and Playwright)
| Q | Finding |
|---|---|
| Mix | 1,411 webapp files, 971 Go (97 in `api4`/`web`), e2e **610 Cypress + 403 Playwright** specs: 30% of files, the outlier |
| e2e in CI | Not a PR trigger: [e2e-tests-ci.yml](https://github.com/mattermost/mattermost/blob/2b3fb21fe/.github/workflows/e2e-tests-ci.yml) is `workflow_dispatch`, fired by Argo Events after the image build, with a file-change check; merge/release runs in `e2e-tests-on-merge.yml`. Real server image, Docker services; Playwright moved to Testcontainers ([README](https://github.com/mattermost/mattermost/blob/2b3fb21fe/e2e-tests/README.md)) |
| Drift | Webapp mocks with `nock`; I found no generated-type gate (**unverified** beyond package.json). e2e is the net |
| Coverage | Codecov, all statuses informational ([coverage-practices.md](coverage-practices.md) §1) |
| Stance | None found |

### Coder (Go + React, Vitest + Storybook + Playwright)
| Q | Finding |
|---|---|
| Mix | 1,383 Go (688 in `coderd`), 341 Vitest files, **480 stories** (visual + interaction), **39** Playwright specs (2%) |
| e2e in CI | `test-e2e` in [ci.yaml](https://github.com/coder/coder/blob/bb72d3982/.github/workflows/ci.yaml) on PRs when go/site/ci paths change, not on forks, `--forbid-only --workers 1`, in the aggregated `required` job. `nightly-gauntlet.yaml` runs Go tests on macOS/Windows nightly, not e2e |
| Drift | `site/src/api/typesGenerated.ts` is generated from Go `codersdk` (`apitypings`); the `gen` job runs `make gen` then `scripts/check_unstaged.sh` and fails on a diff. Stories feed query data through typed `parameters.queries` |
| Coverage | No service config |
| Stance | [frontend.md](https://github.com/coder/coder/blob/bb72d3982/contributing/frontend.md): unit tests are for code that "does _not_ pertain to React"; if logic is "tightly coupled to a React component, a Storybook test or an E2E test is usually a better option". What to test: "Things that can block the user", "Reported bugs", "Regression issues" |

### Supabase Studio (TS, Vitest + MSW + Playwright)
| Q | Finding |
|---|---|
| Mix | Studio: 361 jsdom `.test.tsx` + 499 node `.test.ts`; **30** Playwright specs (`e2e/studio`, 3% of 1,069 JS files) |
| e2e in CI | [studio-e2e-test.yml](https://github.com/supabase/supabase/blob/a05e7ec1d/.github/workflows/studio-e2e-test.yml): push and PR, path-filtered, 2 shards, self-hosted Studio build against `supabase start` (real stack) |
| Drift | OpenAPI to `packages/api-types/types/*.d.ts` for 3 specs; the `api-types` skill names a CI job comparing committed types to the **production** specs as a required merge check (setting unverified). MSW `addAPIMock` is "typed from the OpenAPI `paths`" |
| Coverage | Coveralls from [studio-unit-tests.yml](https://github.com/supabase/supabase/blob/a05e7ec1d/.github/workflows/studio-unit-tests.yml); `coverage.include: ['lib/**/*.ts']` ([vitest.config.ts](https://github.com/supabase/supabase/blob/a05e7ec1d/apps/studio/vitest.config.ts)); no threshold seen |
| Stance | Best documented: [studio-testing](https://github.com/supabase/supabase/blob/a05e7ec1d/.agents/skills/studio-testing/SKILL.md) "push logic out of React components into pure utility functions, then test those functions exhaustively", component tests "only for complex UI interaction", e2e for features shared by self-hosted and platform; rule `testing-no-redundant-tests`: "delete tests that can't fail or only cover libraries, class names, or snapshots". "Studio has thousands of unit tests ... CI from ~14 minutes to ~9 before sharding". [studio-mock-api-tests](https://github.com/supabase/supabase/blob/a05e7ec1d/.agents/skills/studio-mock-api-tests/SKILL.md): mocking `@/data/...` "hides real bugs" and "drifts independently from the OpenAPI types" |

### Cal.com (Next.js, tRPC, Vitest + Playwright)
| Q | Finding |
|---|---|
| Mix | 434 unit files, 17 `*integration-test.ts`, **66** Playwright `.e2e.ts` + **42** API v2 `.e2e-spec.ts` (20% of files) |
| e2e in CI | [all-checks.yml](https://github.com/calcom/cal.com/blob/f972feefa/.github/workflows/all-checks.yml) runs on `merge_group`: e2e 8 shards, Postgres 18 service, built app; also embed, app-store, atoms. On a PR it needs the `ready-for-e2e` label ([pr.yml#L284](https://github.com/calcom/cal.com/blob/f972feefa/.github/workflows/pr.yml#L284)), and a skipped e2e "intentionally fails" the required check ([testing-playwright.md](https://github.com/calcom/cal.com/blob/f972feefa/agents/rules/testing-playwright.md)) |
| Drift | Types shared inside the monorepo via tRPC/zod (`packages/trpc`); no drift job found (**unverified**) |
| Coverage | [testing-coverage-requirements.md](https://github.com/calcom/cal.com/blob/f972feefa/agents/rules/testing-coverage-requirements.md): "80%+ for new code ... enforced automatically in our CI"; no config in the repo (**unverified**) |
| Stance | "Always ensure Playwright tests pass locally before pushing ... CI, which is too slow for development iteration" |

### n8n (TS, Vitest + Playwright, Docker)
| Q | Finding |
|---|---|
| Mix | 7,773 Vitest/Jest files (1,668 under `packages/frontend`, 389 in `cli/test/integration`), **264** e2e specs in `tests/e2e` (3%) plus ~40 infrastructure and workflow-suite specs |
| e2e in CI | [ci-pull-requests.yml](https://github.com/n8n-io/n8n/blob/c08ba2dd3/.github/workflows/ci-pull-requests.yml): path filter or `force-e2e` label, shard matrix from a test-impact tool, Docker image per SHA. [Nightly 02:00 UTC](https://github.com/n8n-io/n8n/blob/c08ba2dd3/.github/workflows/test-e2e-coverage-nightly.yml): coverage-instrumented image, full suite, shards weighted by historical duration |
| Drift | Shared `@n8n/api-types` package (208 test files); e2e creates data "through API helpers" ([AGENTS.md](https://github.com/n8n-io/n8n/blob/c08ba2dd3/packages/quality/testing/playwright/AGENTS.md)); no generated-client gate found |
| Coverage | [codecov.yml](https://github.com/n8n-io/n8n/blob/c08ba2dd3/codecov.yml): patch 80%, `informational` "during the trial", limited to PR flags (`backend-unit`, `backend-integration`, `nodes-unit`, `frontend`); project status only on flag `nightly-full`, `threshold: 0.5`; a `frontend-e2e` flag exists. **e2e coverage is counted, but nightly, not per PR** |
| Stance | [packages/quality](https://github.com/n8n-io/n8n/blob/c08ba2dd3/packages/quality/testing/playwright/README.md) "choose a suite" table routes each goal to one runner; no prune policy |

### PostHog (Python/Rust + React, Jest + Storybook + Playwright)
| Q | Finding |
|---|---|
| Mix | ~5.8k JS/TS files (1,172 in `products/desktop`), ~6.5k Python test files, **54** web Playwright specs (0.4%), **1,116 stories** with visual review |
| e2e in CI | [ci-e2e-playwright.yml](https://github.com/PostHog/posthog/blob/8d1791c18/.github/workflows/ci-e2e-playwright.yml): PR, path-gated, debounced; CI `retries: 3`, a quarantine reporter ([playwright.config.ts](https://github.com/PostHog/posthog/blob/8d1791c18/playwright/playwright.config.ts)); full stack incl. ClickHouse. [ci-frontend.yml](https://github.com/PostHog/posthog/blob/8d1791c18/.github/workflows/ci-frontend.yml): PRs run "a narrowed jest selection"; the full suite runs hourly on master and in the merge queue |
| Drift | 81 committed `api.schemas.ts`; `schema:build:json && git diff --exit-code` in ci-frontend.yml (L776, L796); gate on the API schemas themselves **unverified**. MSW in 2 package.json |
| Coverage | No coverage-service config; master's jest coverage comes from the scheduled run |
| Stance | [playwright/README.md](https://github.com/PostHog/posthog/blob/8d1791c18/playwright/README.md): "Prefer the cheapest layer that can catch the bug"; "'Page renders', 'button is present' ... are smoke checks, not e2e"; "You should treat adding a spec like adding a CI job. Justify it in the PR description"; "Write fewer, longer tests" |

### Sentry (Python + React, Jest + selenium acceptance)
| Q | Finding |
|---|---|
| Mix | 2,539 Jest files, 2,706 Python test files, **50** selenium `tests/acceptance` (1%) |
| e2e in CI | [acceptance.yml](https://github.com/getsentry/sentry/blob/063a6cb27/.github/workflows/acceptance.yml): push + PR, 5 or 10 shards, real backend, Snuba, headless Chrome. Backend PR runs pick tests from a coverage database (`compute-sentry-selected-tests.py`, [backend.yml](https://github.com/getsentry/sentry/blob/063a6cb27/.github/workflows/backend.yml)) |
| Drift | `MockApiClient.addMockResponse` plus 143 `tests/js/fixtures` files; `openapi-diff.yml` on PRs for the public API; no generated UI types found (**unverified**) |
| Coverage | Codecov token in workflows, no repo config; coverage is used to **select tests**, not to gate |
| Stance | [react-testing skill](https://github.com/getsentry/sentry/blob/063a6cb27/.agents/skills/react-testing/SKILL.md): "Avoid mocking hooks, functions, or components. Do not use `jest.mocked()`"; set the network response instead |

### Immich (NestJS + Svelte, Vitest + Playwright, Docker)
| Q | Finding |
|---|---|
| Mix | 246 TS unit files (70 "medium", 109 server small) + 132 Dart; **42** `e2e-spec` (25 API, 11 web, 6 maintenance) on a real stack + **8** mocked-network `ui` specs (17% of files) |
| e2e in CI | [test.yml](https://github.com/immich-app/immich/blob/b7510e79a/.github/workflows/test.yml): PR + push, path-filtered (`server`, `web`, `e2e`), x64 and arm64 matrix, real docker-compose stack |
| Drift | OpenAPI to `@immich/sdk`; job `generated-api-up-to-date` fails on a diff, plus `sql-schema-up-to-date`; the mocked `ui` project builds responses from SDK DTO types ([memory-network.ts](https://github.com/immich-app/immich/blob/b7510e79a/e2e/src/ui/mock-network/memory-network.ts)) |
| Coverage | v8 coverage `include` only `cores`, `services`, `utils`, `sql-tools`; no threshold, no service |
| Stance | [e2e/AGENTS.md](https://github.com/immich-app/immich/blob/b7510e79a/e2e/AGENTS.md): API specs through the SDK against the real server; browser specs split into real-backend and mocked-network projects |

### Small ones
| | Plausible (Elixir) | Outline (Node + React) | Excalidraw (client only) |
|---|---|---|---|
| Mix | 340 ExUnit files on real Postgres + ClickHouse, 62 JS, **14** Playwright (3%) | 232 server tests on real Postgres (CI: migrate, 6 shards), 34 `app`, 79 `shared`, **no e2e** | 149 Vitest files, jsdom, 23 `.snap`, **no e2e** |
| e2e in CI | `e2e` job in [elixir.yml](https://github.com/plausible/analytics/blob/02cfac151/.github/workflows/elixir.yml) on every PR | none | none |
| Coverage | none | none | [thresholds](https://github.com/excalidraw/excalidraw/blob/8bcdca452/vitest.config.mts) 60/70/63/60 lines/branches/functions/statements, PR coverage workflow |

## 2. Patterns

1. **e2e is a thin tier.** By file count e2e is 0.4 to 4% in 8 of 11 projects with one (PostHog 0.4, Sentry 1, Coder 2, Supabase 3, Gitea 3, n8n 3, Plausible 3, Grafana 4). The outliers are Immich 17, Cal.com 20 and Mattermost 30, the last with two runners and an e2e run that is not a PR trigger. No project in the sample has an e2e count above its unit count.
2. **Where e2e exists it runs on PRs, path-gated, against the real backend** (Grafana, Gitea, Coder, Supabase, n8n, PostHog, Sentry, Immich, Plausible). Cal.com gates it by label and merge queue. **None runs it nightly only**; nightly is used for a coverage build (n8n), other OSes (Coder, Go only) or the full suite beside PR selection (PostHog's hourly jest). Sharding is common (Grafana 8, Cal.com 8, Outline 6, Supabase 2, Sentry 5 to 10); Gitea and Coder run one job.
3. **Real-stack e2e, mocks lower down.** Grafana states no stubs; Supabase, Coder, Immich run the real stack. Only Immich keeps a *second*, mocked-network browser project (8 specs, typed from the SDK), for states a real stack cannot produce on demand.
4. **The backend is where the weight goes.** Outline and Plausible are mostly DB-backed server tests; Gitea has 251 real-DB HTTP/integration files against 32 e2e; Grafana and Coder keep HTTP-level Go tests (`pkg/tests`, `coderd`). Immich splits server tests into small and medium (real DB).
5. **Contract drift is solved by generation plus a diff gate, not by more tests.** Verified gates: Coder (`gen` + `check_unstaged.sh`), Supabase (types vs production specs), Immich (`generated-api-up-to-date`), PostHog (`git diff --exit-code`). Supabase goes further and types the mock helper from OpenAPI `paths`. Sentry and Supabase both forbid mocking at the hook/module boundary because it hides renamed query keys and changed payloads; Kent C. Dodds makes the same argument ("stop mocking so much stuff", [post](https://kentcdodds.com/blog/write-tests)).
6. **Coverage gates are rare and mostly new-code or regression based.** n8n (patch 80% informational, project gate on the nightly flag), Grafana (regression per opted-in team), Excalidraw (fixed thresholds), Mattermost (informational). Nobody gates PRs on e2e coverage; n8n collects it nightly. Supabase and Immich restrict coverage to logic directories, which is the same idea as measuring the layer where unit tests are the right tool.
7. **No project documents a mass cull of unit tests.** What is documented is *restraint on new tests*: Supabase (delete tests that cannot fail, merge near-duplicates into `test.each`, logic out of components), Gitea (fewest, fastest tests; time budgets), PostHog (each e2e spec justified; cheapest layer), Coder (test what blocks users, reported bugs, regressions). The largest frontend suites are 1.7k files (n8n editor), 2.5k (Sentry); none of them is a documented 13k-case component suite.
8. **Speed is bought with selection and environment, not deletion:** Supabase runs DOM-free tests in node and shards; Sentry picks tests from a coverage database; PostHog narrows jest on PRs; n8n picks e2e shards by test impact; Grafana and Cal.com shard 8 ways.
9. **The theory agrees, with a caveat.** Google's testing blog suggests "a 70/20/10 split: 70% unit tests, 20% integration tests, and 10% end-to-end tests", "the exact mix will be different for each team", and names the inverted pyramid and the hourglass as anti-patterns ([post](https://testing.googleblog.com/2015/04/just-say-no-to-more-end-to-end-tests.html)). Dodds argues "Write tests. Not too many. Mostly integration." for frontends. They disagree on where the bulk sits; the projects above sit with Dodds for backends and with Google for the browser tier.

## 3. What this means for NebulaCtrl

Facts at `81c571c`: 881 web test files (about 360 use `stubApi`, on the order of 1,900 route lines by rough grep), 782 Go test files (54 in `tests/integration`, behind the smoke and full tiers), 3 Playwright specs outside CI, and a `ci` workflow "run by hand" ([tests/README.md](../../tests/README.md)). `openapi/openapi.json` is the contract and `apps/web/src/api/gen` is generated from it and committed ([openapi-ts.config.ts](../../apps/web/openapi-ts.config.ts)).

**Answer: neither extreme.** Do not mass-delete the 13k cases and do not replace them with e2e. The evidence says the tier that should grow is real-Postgres HTTP/integration tests plus a *small* real-stack e2e tier, while the component tier stops growing per widget. **[INFERENCE]** The 10-minute suite and the cross-file leaks are an environment problem first ([web-ui-testing.md](web-ui-testing.md) §3), a count problem second.

**Effort mix for new and changed behaviour** (share of testing effort, **[INFERENCE]** from the pattern table, not measured):

| Layer | Share | What | Peer basis |
|---|---|---|---|
| Go real-Postgres service and HTTP tests | 35% | state machines, auth/IDOR, secrets, API validation, reconciler logic | Gitea, Outline, Plausible, Grafana `pkg/tests` |
| Pure unit (Go and DOM-free TS) | 20% | formatters, reducers, query keys, graph math, parsers | Supabase `.utils.ts` rule, Coder |
| Web component (happy-dom, `stubApi`) | 20% | one screen: forms, validation, permissions, error and empty states, the request a click emits | Supabase "component tests only for complex UI" |
| Docker smoke and full tiers (in-process control plane + agent) | 15% | deploy, rollback, network isolation, agent reconnect | n8n infrastructure tier, Immich real stack |
| Browser e2e | 10% | ~8 flows below | pattern 1: 0.4 to 4% of files in most peers |

The share of *files* will stay far below the share of effort for e2e (8 to 12 specs against 881). In the web tier: no new per-widget file if a screen-level test already drives that widget; delete or merge a test that "can't fail" (Supabase rule); treat a test that asserts only a class name or a snapshot as deletable.

**Flows for e2e (8), each one needing browser + control plane + agent + Docker.** Existing: 1 and 3 ([01-setup.spec.ts](../../apps/web/e2e/01-setup.spec.ts), [03-project-config.spec.ts](../../apps/web/e2e/03-project-config.spec.ts)).
1. Sign in, enrol a server, see it online; sign-out and an expired session redirect.
2. Project to Docker-image service to deploy to running, with the log stream (SSE) visible in the UI.
3. Export `nebula.toml`, edit, plan, stage; staged secret shown as "set by nebula.toml".
4. Failed health check: new release fails, UI shows "previous release is still serving"; rollback restores v1.
5. Canvas on a real layout: add two services, connect them, reload, positions and links persist (React Flow needs layout; happy-dom has none).
6. Terminal and live logs over WebSocket to a real agent container (xterm render).
7. Role-gated UI: a viewer sees no deploy/delete controls and the API refuses the same action (browser half of the Go IDOR test).
8. Database service: create, back up, restore, and the restored data is visible.
The dither/WebGL fallback spec stays as a layer-2 component test, not a flow. Add one axe scan per journey page, not a spec per page.

**Where they run.** Because CI is manual today, put flows 1 to 4 in the `ci` run when `apps/web/**`, `openapi/**` or `internal/api/**` changed (Gitea, Supabase, Immich, Grafana all path-gate), and all eight in `full`, the release gate and a nightly if a schedule exists. Reuse the smoke harness's built binaries; the peers' 8-way sharding is for suites of 30 to 260 specs. Keep `retries: 0` at first, with traces on failure (already configured); add a quarantine file only after a real flake (PostHog). Gitea aims for under 4 s per e2e test; deploy flows wait on Docker and cannot, so budget per flow. **[INFERENCE]** at 8 flows the tier should finish well under 10 minutes on one runner; unmeasured.

**Coverage.** Keep 85% as a *patch* target on `go` and `web` flags. Do not count e2e toward the PR gate (nobody does); if wanted, follow n8n and collect Go-binary coverage from the full tier nightly under its own flag with `carryforward` ([coverage-practices.md](coverage-practices.md) §3). Do not write component tests to raise the web number; Supabase and Immich scope coverage to logic directories instead.

**Keeping mocks honest** (cheapest first):
1. **Gate regeneration.** Add `make generate` then `git diff --exit-code` to CI (Coder, Immich, PostHog). I found no workflow step and no Go test that compares the committed spec or client with a fresh generation (searched `.github/workflows` for `generate`, `gen:api`, `git diff --exit-code`, and `*_test.go` for `openapi.json`), so an API change can leave them stale until someone runs `make generate`.
2. **Type the stub.** `stubApi` takes `Record<string, ApiReply>` with `body?: unknown` ([src/test/api.ts](../../apps/web/src/test/api.ts)): a route key is any string and a body any value. Type keys as `"METHOD /path"` from the OpenAPI `paths` and bodies from the response type, as Supabase's `addAPIMock` does. A removed endpoint then fails `tsc`, not production.
3. **Validate at runtime.** Hey API's `zod` plugin generates a schema for each response ([docs](https://heyapi.dev/docs/openapi/typescript/plugins/zod/v4)); parse every stubbed body against it inside `stubApi`, so a fixture with a wrong field fails the test that uses it. At least 213 of the 881 test files import `@/api/gen` today (grep of `api/gen`; relative imports not counted).
4. **Real responses for the top endpoints.** Have Go HTTP tests write the JSON for ~10 core endpoints (projects, services, deployments, servers) to `testdata`, and have the web fixtures import them, as Grafana's Go-generated golden files feed its frontend tests. Gitea's record-and-replay with a documented re-record step is the same idea.
5. **Mock the network, not the module.** Keep `stubApi` at `fetch` and keep `mock.module` for non-network concerns only (Supabase, Sentry).
6. **Stub-only endpoints are a smell.** A script that lists operations used in `stubApi` but absent from every Go HTTP test and e2e flow is a cheap review aid; **[INFERENCE]**, not built by any peer.

## Open items

- File counts are not case counts; a `bun test` and `go test -json` count would turn the effort-mix table into measured proportions.
- Whether Grafana and Mattermost gate generated clients, and whether Cal.com's 80% rule is enforced anywhere, could not be read from the repos.
- No peer publishes flake rates or e2e wall times per PR; the 10-minute figure above is a guess.
