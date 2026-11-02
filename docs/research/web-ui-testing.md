---
title: Testing the web UI properly, and linting the tests
description: "A test pyramid for the React dashboard, whether to keep happy-dom under bun test, how to harden the current setup against hangs and leaks, and which Biome rules to add for tests."
section: Internals
order: 53
---

# Web UI testing: pyramid, environment choice, hardening and test linting

Question: `apps/web` runs 879 test files under `bun test` with happy-dom and Testing Library. Role queries are slow on big DOMs, a failing jest-dom assertion inside `waitFor` can hang or run out of memory, React Flow, charts, canvas and WebGL cannot be tested, and some tests hang only in the full run. How should the UI be tested, and how should the tests be linted?

Sources are primary only: the docs of [Bun](https://bun.sh/docs/test/dom), [happy-dom](https://github.com/capricorn86/happy-dom), [jsdom](https://github.com/jsdom/jsdom/blob/main/README.md), [Testing Library](https://testing-library.com/docs/queries/about), [Vitest](https://vitest.dev/guide/browser/why), [Playwright](https://playwright.dev/docs/test-components), [Storybook](https://storybook.js.org/docs/writing-tests/integrations/vitest-addon), [Biome](https://biomejs.dev/linter/domains/), [oxlint](https://oxc.rs/docs/guide/usage/linter/js-plugins.html) and [Codecov](https://docs.codecov.com/docs/merging-reports); library source at the tag or commit named; the Bun and Testing Library issue trackers. NebulaCtrl is read at `81c571c`; nothing was run (this machine is off limits for test runs), so every claim about our own behaviour is **[INFERENCE]** until the remote runner confirms it. Counts of our own files come from `grep` over `apps/web/src`. **Unverified** marks anything not read from a primary source.

## 1. The pyramid for this app

What exists today: 879 test files in `bun test src` (CI job `web`, 6-minute limit, Bun 1.4.4, [ci.yml#L176-L222](../../.github/workflows/ci.yml)); 3 Playwright specs ([apps/web/e2e](../../apps/web/e2e)) that need a running stack (`make e2e`, [Makefile#L138](../../Makefile)) and are **not** in any workflow; no real-browser component tests.

| Layer | Runs in | Belongs here | Fixes which pain point |
|---|---|---|---|
| 0. Pure logic | `bun test`, **no DOM** | formatters, reducers, query-key builders, zod schemas, graph math, URL helpers | DOM start-up cost for files that never touch `document` (see section 2) |
| 1. DOM component | `bun test` + happy-dom + Testing Library | one screen or widget: forms, validation, permissions, empty/error states, the request a click emits (`stubApi`), router and Query wiring, roles and accessible names | the current ~880 files; keep and harden (section 3) |
| 2. Real-browser component | Vitest browser mode (Chromium via Playwright) or Playwright against a story page | anything that needs layout, paint or a GPU: React Flow camera, fit-view and drag; Recharts sizing; xterm render and resize; dither canvas/WebGL states; Base UI popup positioning and focus | "no layout/canvas/WebGL"; the "Base UI dialog re-open hangs" class of bug is **[INFERENCE]** a happy-dom animation or timer artefact that a browser does not have |
| 3. E2E | Playwright against the full stack | sign-in, deploy flow, cross-service paths; axe scans; aria snapshots; a few screenshots | what no mocked layer can prove; keep it small |

Why each simulator layer cannot cover layer 2, from the sources:
- happy-dom: `Element.getBoundingClientRect()` is `// TODO: Not full implementation` and returns `new DOMRect()` ([Element.ts#L999-L1002](https://github.com/capricorn86/happy-dom/blob/master/packages/happy-dom/src/nodes/element/Element.ts), master read 2026-11-01); `offsetWidth`/`offsetHeight` start at 0 ([HTMLElement.ts#L55-L56](https://github.com/capricorn86/happy-dom/blob/master/packages/happy-dom/src/nodes/html-element/HTMLElement.ts)); `getContext` goes through a canvas adapter and returns `null` without one ([HTMLCanvasElement.ts#L161-L183](https://github.com/capricorn86/happy-dom/blob/master/packages/happy-dom/src/nodes/html-canvas-element/HTMLCanvasElement.ts)).
- jsdom is the same class: "does not do any layout or rendering", layout getters return zeros, and `<canvas>` behaves like a `<div>` unless the `canvas` package is installed ([README](https://github.com/jsdom/jsdom/blob/main/README.md#unimplemented-parts-of-the-web-platform), [canvas support](https://github.com/jsdom/jsdom/blob/main/README.md#canvas-support)).
- React Flow's own docs recommend Cypress or Playwright, because it "needs to measure nodes in order to render edges"; for Jest it ships a `ResizeObserver` and `DOMMatrixReadOnly` mock that only makes nodes appear measured ([reactflow.dev testing](https://reactflow.dev/learn/advanced-use/testing)). That mock lets edges render; it cannot test the camera or drag.
- Vitest says it plainly: simulators "only simulate a browser environment and not an actual browser, which may result in some discrepancies... false positives or negatives" ([Why Browser Mode](https://vitest.dev/guide/browser/why)).

What comparable projects do, at the pinned commit (config read directly):

| Project | DOM tests | Real browser | Notes |
|---|---|---|---|
| [coder](https://github.com/coder/coder/blob/67e216b3c/site/vite.config.mts#L192-L260) | Vitest project `unit`, jsdom | Vitest project `storybook`, browser mode, Playwright Chromium, `bail: 5`, `maxWorkers: 4` | the closest analogue: two Vitest projects in one config |
| [Base UI](https://github.com/mui/base-ui/blob/4e89b3adf/vitest.shared.mts#L12-L69) (our component library) | jsdom (`VITEST_ENV=jsdom`, used for coverage), `pretendToBeVisual` | `pnpm test` is `test:chromium`; also firefox, webkit, all-browsers ([package.json#L36-L60](https://github.com/mui/base-ui/blob/4e89b3adf/package.json#L36-L60)); screen-reader and Argos visual jobs | same tests, switchable environment; `retry: 1` in CI |
| [supabase studio](https://github.com/supabase/supabase/blob/a05e7ec1d/apps/studio/vitest.config.ts#L20-L60) | projects `node` (`*.test.ts`) and `jsdom` (`*.test.tsx`) | none | comment: "jsdom costs ~1s of setup per file, so pure-logic `.test.ts` files run in node"; `retry: IS_CI ? 2 : 0` |
| [excalidraw](https://github.com/excalidraw/excalidraw/blob/8bcdca452/vitest.config.mts#L76-L100) | Vitest + jsdom + `vitest-canvas-mock` | none | overrides `getElementError` (section 3); coverage thresholds 60/70/63/60 |
| [tldraw](https://github.com/tldraw/tldraw/blob/9e700c63f/internal/config/vitest/node-preset.ts#L18-L45) | Vitest + jsdom, `pool: 'threads'`, `vitest-canvas-mock` | an `apps/examples/e2e` folder with `baselines` and a global setup (Playwright assumed, **unverified**) | canvas editor, so the closest to our React Flow and dither code |
| [grafana](https://github.com/grafana/grafana/blob/01f74f769/jest.config.js#L50-L103) | Jest + jsdom + `jest-canvas-mock`, `testTimeout: 30000` | Playwright e2e (**unverified**, not read) | [`configure({ asyncUtilTimeout: 2000 })`](https://github.com/grafana/grafana/blob/01f74f769/public/test/setupTests.ts) |
| [mattermost](https://github.com/mattermost/mattermost/blob/2b3fb21fe/webapp/channels/jest.config.js#L44-L50) | Jest + jsdom + `jest-canvas-mock`, `testTimeout: 60000` | not read | `resize-observer-polyfill` |
| [xterm.js](https://github.com/xtermjs/xterm.js/blob/08829c7eb/package.json) (our terminal) | `test-unit` runs in Node | `test-integration` = Playwright on Chromium, Firefox, WebKit | unit and browser split by what needs a real terminal element |

Pattern: every canvas or editor project keeps a simulated DOM for logic and pushes rendering to a real browser (coder, tldraw, xterm.js) or to browser-less mocks (excalidraw, grafana, mattermost); of the eight, only Base UI, the component library itself, runs its default `test` script in a real browser.

**Recommendation:** keep layers 0 and 1 as they are; add layer 2 for a short list of components (below); grow layer 3 only for flows that cross services. The pyramid stays bottom-heavy: layer 2 is a dozen files, not a second suite.

## 2. Keep happy-dom under bun test, switch to jsdom, or go to a browser?

**Keep happy-dom and `bun test` for layer 1.** Reasons:
- jsdom does not solve any listed pain point: it has no layout or canvas either (section 1), and Vitest's own numbers put it slower to create: "`jsdom` costs roughly 200-500ms per import and `happy-dom` roughly 90-200ms" ([Improving Performance](https://vitest.dev/guide/improving-performance#test-environments)). The Bun 1.4 hang in [oven-sh/bun#39876](https://github.com/oven-sh/bun/issues/39876) reproduces "on both jsdom and happy-dom".
- It is a one-line experiment, so measure instead of guessing: Bun switches with `bun test --environment=jsdom` or `[test] environment = "jsdom"` ([Bun DOM docs](https://bun.sh/docs/test/dom#test-environments)). Run the three slowest files under both on the sprite before deciding.
- Moving all 879 files to a browser means Vitest, not `bun:test`: Vitest browser mode "does not completely replace standalone end-to-end test runners" and has "longer initialization" ([Why Browser Mode](https://vitest.dev/guide/browser/why#drawbacks)); the browser project cannot `vi.spyOn` an imported module (sealed ESM namespace, [Limitations](https://vitest.dev/guide/browser#spying-on-module-exports)), and 31 `mock.module` calls in 13 files would need rewriting. Base UI can afford both worlds; a solo maintainer should not.

**Two cheap changes to the Bun setup itself:**
- Today `src/test/setup.ts` registers happy-dom for every file, including pure-logic files. Bun supports a per-file opt-out: set `environment = "happy-dom"` in `bunfig.toml` and add `// @vitest-environment node` to DOM-free files ([docs](https://bun.sh/docs/test/dom#test-environments)); files with the same environment share one window, and "Bun restores the globals after each file". Preloads "run in the project's environment", so the existing preload keeps working. Supabase does the same split with project globs. Gain is unmeasured here (**[INFERENCE]**: Bun's own docs call happy-dom creation cheap, so it may be small).
- Treat `--isolate` as the answer to cross-file leaks (section 3), not jsdom.

**Real-browser component tests (layer 2): which tool.**

| | Vitest browser mode | Playwright component testing |
|---|---|---|
| Status | documented as a feature of Vitest's API; I saw no experimental label in the guide text read ([guide](https://vitest.dev/guide/browser/)) | the `experimental-ct-*` packages are removed; the replacement is a plain `@playwright/test` project with `mount()` and a story gallery page you write ([docs](https://playwright.dev/docs/test-components)) |
| Fit here | reuses the Vite 8 config and React plugin; `vitest-browser-react` has `render`, `page.getByRole`, and `expect.element` that retries ([guide](https://vitest.dev/guide/browser/#examples)) | we already depend on `@playwright/test`; no new runner, but a gallery plus `*.story.tsx` per scenario, and callbacks must be recorded in the story |
| Coverage | built in: V8 through the Chrome DevTools Protocol ([coverage](https://vitest.dev/guide/coverage#v8-provider)); Chromium only | DIY: `page.coverage` is "only supported on Chromium-based browsers" and returns V8 data you convert with `v8-to-istanbul` ([API](https://playwright.dev/docs/api/class-coverage)) |
| Screenshots | `toMatchScreenshot` ([docs](https://vitest.dev/guide/browser/visual-regression-testing)) | `toHaveScreenshot` ([docs](https://playwright.dev/docs/test-snapshots)) |
| Runtime | needs Node for the Vite server; the Bun-runtime report above is one more reason to run it with `bunx vitest` (no `--bun`), where `bunx` runs Node-shebang tools with Node ([bunx docs](https://bun.sh/docs/pm/bunx)); that vitest's bin has a Node shebang is **unverified** | Node |

**Pick Vitest browser mode** for tests that should count toward the 85% target (coverage is first class), **Playwright e2e with `page.route` stubs** for behaviour that needs no coverage number. Both follow [Playwright's own guidance](https://playwright.dev/docs/test-components#handling-network-requests) of stubbing the network at the page boundary.

**Coverage from both runners into Codecov.** Codecov "does not override report data for multiple uploads. We always merge the data" ([Merging Reports](https://docs.codecov.com/docs/merging-reports)), and flags group reports by test type ([Flags](https://docs.codecov.com/docs/flags)). The CI job already uploads five Bun lcov files under flag `web`; add `apps/web/coverage-browser/lcov.info` from Vitest (`coverage.provider: 'v8'`, `reporter: ['lcovonly']`, `coverage.include` limited to `src/**`) under flag `web-browser`, with the same `sed` path rewrite so both runners name files `apps/web/src/...`. Vitest's V8 provider applies AST-aware remapping "which produces identical coverage reports to Istanbul" since 3.2 ([docs](https://vitest.dev/guide/coverage#v8-provider)), so line counts line up with Bun's. Per-line union semantics across flags is **[INFERENCE]**: check one file in the Codecov UI after the first upload. Do not use Istanbul instrumentation for this ("slower than V8", higher memory, same page).

Visual regression is the weakest layer: Vitest and Playwright both warn that rendering depends on GPU, OS, fonts and headless mode and recommend one controlled environment ([Vitest](https://vitest.dev/guide/browser/visual-regression-testing#environmental-stability), [Playwright](https://playwright.dev/docs/test-snapshots)); Vitest adds that `toMatchScreenshot` "is not a substitute for proper assertions". Use it for the dither shader only, in CI on Linux, baselines committed per platform. Prefer Playwright aria snapshots (`toMatchAriaSnapshot`, YAML of roles, names and states, partial matching by default, [docs](https://playwright.dev/docs/aria-snapshots)) for structure and `@axe-core/playwright` for accessibility ([docs](https://playwright.dev/docs/accessibility-testing)); axe needs a real engine, and Playwright warns automated checks find only some issues.

Storybook's test runner is "superseded by the Vitest addon" ([docs](https://storybook.js.org/docs/writing-tests/integrations/test-runner)). Coder's setup shows it works, but we have no stories; adopt it only if a component catalogue is wanted for other reasons.

## 3. Hardening the current setup

Evidence first, then the fix. Each line says whether the cause was verified.

**3.1 DOM dumps and hangs inside `waitFor`.**
- `getElementError` is Testing Library's hook for the message of every failed `get*`/`find*`; the default builds `prettyDOM(container)` ([config.ts](https://github.com/testing-library/dom-testing-library/blob/v10.4.0/src/config.ts)). `prettyDOM` formats the **whole** document with `prettyFormat.format(...)` and only then slices to 7000 characters ([pretty-dom.js](https://github.com/testing-library/dom-testing-library/blob/v10.4.0/src/pretty-dom.js)), so the cost scales with the DOM, not the limit. `DEBUG_PRINT_LIMIT=0` does not skip it: the env value is a string and the early return tests `maxLength === 0` (**[INFERENCE]** from reading the code; not run).
- Inside `waitFor` this repeats on every poll that throws a `TestingLibraryElementError`, and again once on timeout through `onTimeout` ([wait-for.js](https://github.com/testing-library/dom-testing-library/blob/v10.4.0/src/wait-for.js)). `waitFor` only disables the *role listing* (`runWithExpensiveErrorDiagnosticsDisabled`), not `prettyDOM`. Read at v10.4.0; the repo has 10.4.2 (differences **unverified**).
- jest-dom failure messages embed elements: `toBeInTheDocument` uses `stringify(element.cloneNode(true))`, `toBeDisabled` prints `element.cloneNode(false)` ([v6.6.3 source](https://github.com/testing-library/jest-dom/blob/v6.6.3/src/to-be-in-the-document.js); we use 7.0.1, differences **unverified**). Under Bun the printed element has been the whole happy-dom object graph with `Symbol(...)` fields, an "infinite error log" on `toBeDisabled`/`toBeEnabled` ([oven-sh/bun#10886](https://github.com/oven-sh/bun/issues/10886), now closed, last updated 2026-10-20; a 2025-10 comment still reproduces it on happy-dom 20.0.8). Whether Bun 1.4.4 contains the fix is **unverified**. This matches the hang we see, so assume it is live.
- The DOM-dump hook is an official extension point (`configure({ getElementError })`, [docs](https://testing-library.com/docs/dom-testing-library/api-configuration#getelementerror)), and excalidraw uses it for exactly this reason: its setup comment says Testing Library "dumps the entire serialized DOM into the error message whenever a `waitFor`/`getBy*` fails", and strips it unless `VITE_DEBUG_DOM` is set ([setupTests.ts#L18-L33](https://github.com/excalidraw/excalidraw/blob/8bcdca452/setupTests.ts#L18-L33)).

Concrete additions to `src/test/setup.ts`:

```ts
const { configure } = await import("@testing-library/react");
const debugDom = process.env.DEBUG_DOM === "1";
configure({
	// No prettyDOM per failed poll; keep the message and the query that failed.
	...(debugDom ? {} : {
		getElementError: (message) =>
			Object.assign(new Error(message ?? ""), { name: "TestingLibraryElementError" }),
	}),
	// Must stay below Bun's 5000 ms per-test default (bun.sh/docs/test/writing-tests), or
	// Bun reports a bare timeout instead of Testing Library's message. Grafana uses 2000.
	asyncUtilTimeout: process.env.CI ? 3000 : 1000,
});
```

Also: write polls as cheap predicates that throw a plain `Error` (what the brief already does), prefer `findBy*` over `waitFor(() => getBy*)`, and never put a jest-dom matcher in a poll. Testing Library's own docs say `waitFor` re-runs on every DOM mutation and every 50 ms ([async API](https://testing-library.com/docs/dom-testing-library/api-async#waitfor)). A test helper `waitUntil(() => boolean, message)` is 10 lines and keeps the 1,705 current `waitFor` calls honest.

**3.2 Slow role queries.** Maintainers confirm `getByRole` is "fairly slow" and "syncronous, so `waitFor` keeps rerunning it" ([dom-testing-library#820](https://github.com/testing-library/dom-testing-library/issues/820)); the role query calls `window.getComputedStyle(element)` per candidate to check visibility ([role-helpers.js](https://github.com/testing-library/dom-testing-library/blob/v10.4.0/src/role-helpers.js)), and the cost of that call in happy-dom is **[INFERENCE]**, not measured. We make 9,909 `*ByRole` calls. Ordered options from that thread (one of the maintainers, eps1lon), cheapest-to-confidence first:
1. Scope: `within(dialog).getByRole(...)` instead of `screen`; one reporter went from `getByRole` taking "upwards of 40 seconds" to a "drastic" reduction by giving the query a container.
2. `getByRole(role, { hidden: true })` skips the visibility check; MUI sets `defaultHidden` true locally and false in CI because "in 1015 `ByRole` calls we only ever need to explicitly specify the `hidden` state in 25 cases" ([comment](https://github.com/testing-library/dom-testing-library/issues/820)). Local and CI behaving differently is a trade-off; take it only after profiling.
3. Do not filter by `name` when `getAllByRole` order is stable; fall back to `getByLabelText`/`getByText`, which are higher in the [priority list](https://testing-library.com/docs/queries/about#priority) than test ids anyway.
4. Profile first: `bun test --timings=.bun-test-timings.json --update-timings` writes per-file wall time, "slowest first, so it doubles as a 'what's slow' report" ([Bun](https://bun.sh/docs/test/parallel#balancing-with---timings)).
Smaller DOMs help more than any option: render one screen, not the app shell, when the test is about one widget.

**3.3 Cross-file leaks (fake timers, module mocks, module-level stores).** What Bun guarantees, from its docs:
- Default: "all files share one global and one module registry" ([parallel](https://bun.sh/docs/test/parallel#--isolate)). Module mocks "are removed when the test file ends... Other modules stay cached between files", and `mock.restore()` "doesn't remove module mocks" ([mocks](https://bun.sh/docs/test/mocks#module-mocks-are-removed-after-each-test-file)). So **module-level stores are never reset between files**; that is the leak class we cannot see in a single-file run.
- "Bun restores the real timers and the real time at the end of each test file" ([dates-times](https://bun.sh/docs/test/dates-times#fake-timers)), not per test: a test that throws before its `useRealTimers()` leaks into the next test of the same file. 20 of our files use `useFakeTimers`; 21 mention `useRealTimers`. Testing Library's `waitFor` has two branches, one for fake and one for real timers, and it rejects when the mode changes mid-wait ("Changed from using real timers to fake timers while using waitFor", [wait-for.js](https://github.com/testing-library/dom-testing-library/blob/v10.4.0/src/wait-for.js)). Bun made `setTimeout.clock` appear and disappear to match ([bun#25915](https://github.com/oven-sh/bun/pull/25915), [bun#26285](https://github.com/oven-sh/bun/pull/26285)); with `user-event` under fake timers you must pass `advanceTimers` ([docs](https://testing-library.com/docs/user-event/options#advancetimers)).
- `--isolate` is the supported fix: fresh global, cleared module registry, "cancels its timers, and restores fake timers", re-runs preloads, and "makes 'passes alone, fails in the full suite' bugs go away at the cost of re-evaluating imports per file" ([parallel](https://bun.sh/docs/test/parallel#--isolate)). `--isolate=modules` keeps JIT code and is cheaper. Bun's own benchmark (2,000 small files, M4 Max, synthetic) puts shared global at 2.4 s, `--parallel` isolated at 6.8 s, and `--parallel --no-isolate` at 0.75 s ([same page](https://bun.sh/docs/test/parallel#how-it-compares)); our cost is unmeasured, so run `bun test --isolate` and `--isolate=modules` once on the sprite.
- Bug-finding without paying for isolation every run: `randomize = true` with a printed `seed` ([config](https://bun.sh/docs/test/configuration#randomize)), forwarded to workers per the parallel docs. A nightly `--randomize` run exposes order dependence.

Hardening for the global state we own:

```ts
// import { afterEach, jest, mock } from "bun:test";
afterEach(() => {
	jest.useRealTimers();          // per test, not per file; Bun only does it per file
	mock.restore();                // spies; module mocks need --isolate (bun.sh/docs/test/mocks)
	resetModuleStores();           // one exported function per module-level store (toast already done)
});
globalThis.BASE_UI_ANIMATIONS_DISABLED = true;
```

Base UI documents the same two problems and solves them the same way: its test setup sets `BASE_UI_ANIMATIONS_DISABLED`, and its `useAnimationFrame` scheduler "is process-global, so a callback scheduled in one test but never run (e.g. requested under fake timers that were torn down before the frame fired) would otherwise survive into a later test", with a `reset` called after every test ([setupVitest.ts](https://github.com/mui/base-ui/blob/4e89b3adf/test/setupVitest.ts), [useAnimationFrame.ts#L86-L107](https://github.com/mui/base-ui/blob/4e89b3adf/packages/utils/src/useAnimationFrame.ts)). The flag is in the shipped 1.9.0 we use (`global.d.ts`: "disables animation-related code, even if supported by the runtime"; `useAnimationsFinished.js` skips waiting on `getAnimations()` when it is set); our setup does not set it (grep). A happy-dom `Animation.finished` that never settles is a plausible cause of the dialog re-open hang (**[INFERENCE]**, not reproduced); our setup already wraps `Element.prototype.animate` for the unhandled-rejection case. `MotionConfig` also has a `skipAnimations` option ([changelog](https://github.com/motiondivision/motion/blob/7415b6cd1/CHANGELOG.md)) beyond the `reducedMotion="always"` we use.

**3.4 Hang that appears only in the full run.** Bun 1.4.x has an open regression with this exact signature: Testing Library `waitFor` never sees a state update, passes alone, passes on 1.3.14 and on Node, "fails on both jsdom and happy-dom", and only under CPU pressure (`taskset -c 0`) ([bun#39876](https://github.com/oven-sh/bun/issues/39876), standalone repro on 1.4.2). We pin Bun 1.4.4, and this machine was overloaded during the runs. It may be unrelated to our hang (**[INFERENCE]**), but it is cheap to rule out: on the sprite, run the hanging file full-suite under `taskset -c 0` with Bun 1.3.14, then 1.4.4.

**3.5 What not to do.** Do not raise `testTimeout` to 30 to 60 s as grafana and mattermost do (their configs above) to hide hangs; a hang then costs a minute instead of five seconds. Do not add `retry` to hide leaks (supabase retries twice in CI, Base UI once; Base UI's note "Avoid committing tests that influence their own retry"). `--bail` is for CI only.

## 4. Linting the tests

`bun lint` runs the `lint` script (`biome check .`, [package.json](../../apps/web/package.json)); Bun itself ships no linter (I found none in its docs; absence **unverified**).

**Biome today.** `apps/web/biome.json` extends the recommended preset plus three nursery promise rules, with no `domains` key. Biome rules are grouped into domains, and "Biome's linter will automatically enable the rules that belong to a domain when it detects certain dependencies in the nearest package.json" ([domains](https://biomejs.dev/linter/domains/)). The `test` domain keys on `jest`, `mocha`, `ava` or `vitest` ([Test domain](https://biomejs.dev/linter/domains/#test)); `apps/web` declares none (it uses `bun:test`), so **[INFERENCE] `noFocusedTests`, `noExportsInTest`, `noDuplicateTestHooks` and `noIdenticalTestTitle` (all "recommended" in that domain) are not running.** The Playwright domain *is* on (`@playwright/test` is a dependency), which is why the config already switches `usePlaywrightValidDescribeCallback` off for `src/**`. Confirm by adding a throwaway `test.only` and running `biome lint` on the sprite. The repo has no `.only`/`.skip`/`.todo` today (grep), so turning the rules on should be quiet.

Proposed `linter` block (the rule names and defaults are from each rule page: [noFocusedTests](https://biomejs.dev/linter/rules/no-focused-tests/javascript/), [noSkippedTests](https://biomejs.dev/linter/rules/no-skipped-tests/javascript/), [useExpect](https://biomejs.dev/linter/rules/use-expect/javascript/), [noConditionalExpect](https://biomejs.dev/linter/rules/no-conditional-expect/javascript/), [noExportsInTest](https://biomejs.dev/linter/rules/no-exports-in-test/javascript/), [useTestHooksOnTop](https://biomejs.dev/linter/rules/use-test-hooks-on-top/javascript/)):

```jsonc
"linter": {
	"domains": { "test": "recommended" },        // explicit; bun:test is not auto-detected
	"rules": { "preset": "recommended", /* nursery promise rules unchanged */ }
},
"overrides": [
	{ "includes": ["src/**/*.test.ts", "src/**/*.test.tsx", "src/test/**"],
	  "linter": { "rules": {
		"suspicious": { "noFocusedTests": "error", "noSkippedTests": "error",
		                "noConditionalExpect": "error", "useExpect": "error" },
		"style": { "useTestHooksOnTop": "error" } } } }
]
```

Notes:
- `noFocusedTests` ships as a warning with an unsafe fix; make it `error`, or run CI with `biome check --error-on-warnings` ([CLI](https://biomejs.dev/reference/cli/)).
- `useExpect` accepts only `expect`, `assert`, `expectTypeOf`, `assertType`; tests that assert through our own helper (`expectToast(...)`) will be flagged, which is a useful prompt: either the helper is a hidden assertion or the test asserts nothing. Start it at `warn`, count, then decide. It is the lint rule closest to our "no render-without-assert" bar; it cannot judge assertion quality.
- `noConditionalExpect` flags an `expect` inside `if`, `catch` or a loop; the plain-`Error` polls in 3.1 contain none, so they pass.
- Skip `useLowercaseTestTitle`, `useValidTestTitle` (nursery) and `noExcessiveNestedTestSuites`: style, not defects.
- e2e: of the [Playwright domain](https://biomejs.dev/linter/domains/#playwright) rules only `noPlaywrightMissingAwait` and `usePlaywrightValidDescribeCallback` are recommended; enable `noPlaywrightWaitForTimeout`, `noPlaywrightForceOption` and `noPlaywrightNetworkidle` for `e2e/**` as errors, since fixed waits and forced clicks are the usual sources of e2e flakiness (**[INFERENCE]**, judgement).

**Testing Library and jest-dom rules.** Biome has none: its [rules-sources](https://biomejs.dev/linter/rules-sources/) page lists no `eslint-plugin-testing-library` or `jest-dom` origin (searched the page text for both names: zero hits). The rules that target our failure modes exist only in [eslint-plugin-testing-library](https://github.com/testing-library/eslint-plugin-testing-library#supported-rules): `await-async-queries`, `await-async-utils`, `no-wait-for-multiple-assertions`, `no-wait-for-side-effects`, `prefer-find-by`, `no-unnecessary-act`, `no-debugging-utils`, `no-node-access`.

Is a second linter worth it? Options:
1. **Do nothing.** Biome's `noFloatingPromises` is already on; it is type-aware, so it should catch an un-awaited `findBy*` or `waitFor` (**[INFERENCE]**). Cost zero. It misses `waitFor` with several assertions or side effects, the shapes behind the hangs above.
2. **oxlint with the Testing Library plugin only, scoped to test files.** Oxlint runs ESLint plugins as "JS plugins", status **alpha**, and lists `eslint-plugin-testing-library` among tested plugins: 29 of 29 rules pass, 17,016 upstream tests pass and 1 is skipped, at plugin 7.15.4 ([JS plugins](https://oxc.rs/docs/guide/usage/linter/js-plugins.html), [conformance](https://github.com/oxc-project/oxc/blob/main/apps/oxlint/conformance/snapshots/testing_library.md)). `eslint-plugin-jest-dom` is not in oxlint's conformance list (**unverified** whether it runs). Precedent: xterm.js lints with oxlint (`typeAware`, `maxWarnings: 0`, [.oxlintrc.json](https://github.com/xtermjs/xterm.js/blob/08829c7eb/.oxlintrc.json)) next to ESLint. Risks: alpha status; the plugin detects wrapped helpers through "aggressive reporting" and a `testing-library/utils-module` setting ([README](https://github.com/testing-library/eslint-plugin-testing-library#aggressive-reporting)), so our `renderWithProviders` may need that setting (**unverified** for a Bun repo); whether the plugin's ESLint peer dependency must be installed is **unverified**; a second config and CI step.
3. **Full ESLint** with the plugin: the heaviest option for one rule family; not recommended for a Biome repo.

**Recommendation:** do option 1 plus the Biome block now. Try option 2 behind a throwaway branch with exactly these rules: `await-async-queries`, `await-async-utils`, `no-wait-for-multiple-assertions`, `no-wait-for-side-effects`, `prefer-find-by`, `no-unnecessary-act`, `no-debugging-utils`. Keep it only if it finds a real defect in the 879 files that Biome missed; if it reports only style, drop it. Leave `no-node-access`, `no-container`, `prefer-screen-queries` off: they will fire thousands of times on helpers and add no defect catching.

## 5. Phased plan for a solo maintainer

| Phase | Work | Effort | Verify with |
|---|---|---|---|
| 0 | Edit `src/test/setup.ts` (3.1, 3.3: `getElementError`, `asyncUtilTimeout`, per-test `useRealTimers`, `BASE_UI_ANIMATIONS_DISABLED`, store resets); add the Biome `test` domain and overrides (4); add `waitUntil` helper | an afternoon | remote run of the 5 slowest and the 3 hanging files; `biome lint` with a throwaway `.only` |
| 1 | Measure: `--timings`, then `--isolate`, `--isolate=modules`, `--environment=jsdom`, `--randomize`, and the Bun 1.3.14 vs 1.4.4 `taskset -c 0` run (3.4). Choose CI flags from numbers; fix every leak the isolated run exposes | 1 to 2 days | remote runner only |
| 2 | Put Playwright into CI: it needs a stack to run against (reuse the smoke job's Docker images; whether they serve the web UI is **unverified**); add `@axe-core/playwright` and `toMatchAriaSnapshot` to 3 to 5 key pages; dither-state screenshots on Linux only | 2 to 3 days | CI run |
| 3 | Layer 2 for the list that layers 1 cannot test: React Flow canvas (camera, fit view, drag), Recharts, xterm, dither/WebGL. Vitest browser project named `browser`, files `*.browser.test.tsx`, `coverage.include` limited to those directories, lcov under flag `web-browser`; script `bun run test:browser` | 3 to 5 days for ~10 files | CI run; compare Codecov per-file before and after |
| 4 | Option 2 oxlint trial (4); jsdom-vs-happy-dom decision revisited only if phase 1 shows a number worth the move | half a day | the trial branch |

Order matters: phase 0 and 1 remove the hangs that cost the most time and need no new dependency; phase 3 is the only phase that adds a runner. If the 85% target is out of reach without the canvas and React Flow code, the honest alternative to phase 3 is to **exclude that code from the gate with a written reason**. That is a judgement call against [coverage-practices.md](coverage-practices.md), which ends "hold there rather than excluding code" for testable code; Google's guidance is not to chase the last points ([blog](https://testing.googleblog.com/2020/08/code-coverage-best-practices.html)).

## Open items

- Is the whole-document `prettyDOM` cost or Bun's element printing the larger share of our hangs? Needs one profiled failing `waitFor` on the sprite (**[INFERENCE]** until then).
- Does Bun 1.4.4 contain a fix for bun#10886? The issue is closed; the fix commit was not read.
- Does `--isolate` fit the CI job's 6-minute limit at 879 files? Unmeasured.
- Vitest browser mode with MSW or a `page.route` equivalent for `stubApi`: the Vitest docs read here do not say; **unverified**.
