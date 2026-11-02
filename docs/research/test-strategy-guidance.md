---
title: Test strategy guidance: granularity, value and pruning
description: "What the pyramid, trophy, honeycomb and Google's test sizes say, what evidence exists on many small tests versus fewer broad ones and on coverage targets, when to delete a test, and a mix and pruning rule for NebulaCtrl."
section: Internals
order: 55
---

# Test strategy: many fine-grained tests, or fewer broad ones?

Question: `apps/web` has ~13,000 component tests (881 files, a 10-minute run, cross-file leaks), the Go side tests mostly through real services and HTTP handlers against Postgres, a Docker smoke tier covers the system end to end, three Playwright specs are not in CI, and Codecov asks 85% on new code. Does that many fine-grained tests make sense, or should there be fewer, broader tests?

Sources are primary only, read 2026-11-01: the articles and posts linked inline (Fowler and Vocke, Dodds, Spotify, the Google Testing Blog, *Software Engineering at Google* chapters [11](https://abseil.io/resources/swe-book/html/ch11.html) and [12](https://abseil.io/resources/swe-book/html/ch12.html), [DORA](https://dora.dev/capabilities/test-automation/), [Beck](https://testdesiderata.com/)) and the papers. Web pages carry no commit; GitHub tools are pinned. NebulaCtrl is read at `81c571c` with `grep`/`find`/`wc` counts (nothing was run; counts are of source text, so `.each` rows and helpers are not expanded). **Unverified** marks what I could not read at the source; **[INFERENCE]** marks my reasoning. It builds on [coverage-practices.md](coverage-practices.md), [oss-quality-tooling.md](oss-quality-tooling.md) and [web-ui-testing.md](web-ui-testing.md) and does not repeat them. Rauch's tweet and Beck's original Medium post were not fetchable (Medium returned 403); both are read through [Dodds's embed](https://kentcdodds.com/blog/write-tests) and Beck's [site](https://testdesiderata.com/).

## Short answer

1. Count is not the defect. Every source treats a test as a cost (write, read, run, fix when it breaks) that must buy confidence; none gives a count that is "too many". Brittleness, flakiness, speed and redundancy are the defects, and all can be measured.
2. "Fine-grained" is mostly a naming problem here. 70% of the web tests (8,650 of 12,271 counted) sit in files that render a screen with a stubbed API; that is Dodds's "integration" and Fowler's "sociable unit" test, not a solitary mock-heavy unit test. Swapping them for e2e tests would move tests the wrong way: Google measured 0.5% flaky small tests against 14% flaky large ones.
3. The honest evidence is thin. Nothing read here is a controlled comparison of many small against few broad suites; the schools rest on practitioner experience, and two observational studies say coverage is a weak proxy.
4. Recommended: keep the shape (many fast tests at the component and handler level, few Docker and browser flows), cut redundancy and change-detectors by a measured rule, bring the web suite under 10 minutes (DORA's bar), and use coverage only to find untested code. Details in section 5.

## 1. What each school recommends

| Source | Shape and numbers | What it calls a "unit" | For a web app with a Go backend |
|---|---|---|---|
| [Vocke, *Practical Test Pyramid* (Fowler site, 2018)](https://martinfowler.com/articles/practical-test-pyramid.html), [Fowler's bliki](https://martinfowler.com/bliki/TestPyramid.html) | Pyramid: "lots of small and fast unit tests", "some more coarse-grained tests", "very few high-level tests". No percentages. | Sociable or solitary, both fine | Go: handler plus real database as the middle layer. Web: "a rich javascript UI should have most of its UI behavior tested with javascript unit tests". Bliki footnote: "If my high level tests are fast, reliable, and cheap to modify - then lower-level tests aren't needed." |
| [Google, *Just Say No to More End-to-End Tests* (2015)](https://testing.googleblog.com/2015/04/just-say-no-to-more-end-to-end-tests.html) and [SWE@Google ch. 11](https://abseil.io/resources/swe-book/html/ch11.html) | 70/20/10 in the blog ("a good first guess"); 80/15/5 in the book, "by test case count, and every team's mix will be a little different". Anti-patterns: ice-cream cone, hourglass. | Narrow scope; **size** (small = one process, no network or disk; medium = one machine, localhost; large = anything) is the second axis, defined in [Test Sizes](https://testing.googleblog.com/2010/12/test-sizes.html) | A Go handler test over a real Postgres is *medium*; a happy-dom component test with a stubbed API is *small*. The book: "a broad-scoped test of a server endpoint ... which is nevertheless small because it uses doubles to stand in for all out-of-process dependencies". |
| [Dodds, *Write tests. Not too many. Mostly integration.*](https://kentcdodds.com/blog/write-tests) (from Rauch's 2016 tweet), [Trophy](https://kentcdodds.com/blog/the-testing-trophy-and-testing-classifications) | Trophy: static analysis, then a few unit, **mostly integration**, a few e2e. No percentages; his 70% coverage figure is "(I made that number up... no science there)". | Unit = no collaborators or mocked ones; integration = several units together, mocking only the network (MSW) and animations | Written for frontends: "I never considered whether it applied to microservices or even backend services at all". Fits `apps/web` well; for Go read it as "stop mocking so much stuff". |
| [Spotify, *Testing of Microservices* (2018)](https://engineering.atspotify.com/2018/01/testing-of-microservices) | Honeycomb: mostly integration tests, "a few Implementation Detail Tests", "even fewer Integrated Tests (ideally none)" (tests that pass or fail on another system's correctness). | Implementation-detail tests only for "parts of the code that are naturally isolated and have an internal complexity of their own" (a log parser) | Their simple service: start a database, populate, start the service, query the real API; "no Implementation Detail tests at all". Closest to NebulaCtrl's Go tests. Cost admitted: failures point less precisely. |
| [Beck, Test Desiderata](https://testdesiderata.com/) | No shape; twelve properties to trade off: isolated, composable, deterministic, fast, writable, readable, behavioral, structure-insensitive, automated, specific, predictive, inspiring. | n/a | "Making tests more predictive of production behavior makes them slower", but "properties only seem to interfere" (composability). Use as the per-test scorecard in section 4. |
| [Testing Library, Guiding Principles](https://testing-library.com/docs/guiding-principles) | "The more your tests resemble the way your software is used, the more confidence they can give you"; query DOM nodes, not component instances. | n/a | Already NebulaCtrl's style (9,947 `*ByRole` calls, 246 `*ByTestId`). |

Where they differ is mostly vocabulary. [Fowler (2021)](https://martinfowler.com/articles/2021-test-shapes.html): honeycomb and trophy authors "criticize the excessive use of mocks", so their "unit test" is what he calls a *solitary* unit test and their "integration test" what he calls a *sociable* one: "This makes the pyramid versus honeycomb discussion moot". The quote he endorses (Searls): "People love debating what percentage of which type of tests to write, but it's a distraction. Nearly zero teams write expressive tests that establish clear boundaries, run quickly & reliably, and only fail for useful reasons. Focus on that instead."

Where they agree, and where the advice is actionable:
- **Test the public behavior, not the implementation.** Fowler/Vocke ("Test for observable behaviour"), Google ([Test Behavior, Not Implementation](https://testing.googleblog.com/2013/08/testing-on-toilet-test-behavior-not.html), [Prefer Testing Public APIs](https://testing.googleblog.com/2015/01/testing-on-toilet-prefer-testing-public.html)), Dodds ([Testing Implementation Details](https://kentcdodds.com/blog/testing-implementation-details)), Spotify.
- **Use real collaborators when they are fast and deterministic.** SWE@Google ch. 11: "we prefer to keep the real dependencies in place when it is feasible to do so"; Google's [Don't Overuse Mocks](https://testing.googleblog.com/2013/05/testing-on-toilet-dont-overuse-mocks.html) lists harder to understand, harder to maintain, less assurance.
- **Few tests through the whole stack**, and push a bug found there down: Vocke's rule "If a higher-level test spots an error and there's no lower-level test failing, you need to write a lower-level test"; DORA says the same.
- **Unit-sized tests are still right for isolated, many-case logic** (parsers, planners, state machines, schemas): Spotify's log-parser example and Dodds's `fizzbuzz` table are the same idea.

## 2. Evidence: many fine-grained tests against fewer broad ones

**Broad tests cost more per test (best evidence).**
- Google, one week of runs: "0.5% of our small tests were flaky, 1.6% of our medium tests were flaky, and 14% of our large tests were flaky"; flakiness rises with RAM and binary size (r² 0.76 and 0.82), and size predicts it better than tool choice ([Where do our flaky tests come from?](https://testing.googleblog.com/2017/04/where-do-our-flaky-tests-come-from.html)). In an earlier [post](https://testing.googleblog.com/2016/05/flaky-tests-at-google-and-how-we.html): about 1.5% of all runs flaky and "about 84% of the transitions we observe from pass to fail involve a flaky test".
- The 2015 post's argument is the feedback loop: fast, reliable, "isolates failures"; end-to-end loses on all three, and "a failing test does not directly benefit the user", only the fix does.
- DORA: "A test suite of ten tests that is reliable, fast, and trustworthy is much better than a test suite of hundreds of tests that is hard to maintain and that nobody trusts"; feedback "in less than ten minutes both on local workstations and from the continuous integration system".

**Small tests are not immune.**
- Microsoft studied five projects' *unit* tests over a month: 4.6% of tests flaky, up to ~52% of one project's failing builds hit a flaky failure, and 58 developers ranked flaky tests second of ten reasons slowing deployments ([Lam et al., ISSTA 2019](https://patricegodefroid.github.io/public_psfiles/issta2019.pdf)). Flakiness tracks nondeterminism (time, randomness, async waits, concurrency), not level. NebulaCtrl's equivalent is the `waitFor` and fake-timer class in [web-ui-testing.md](web-ui-testing.md).

**Fine-grained tests cost in change friction, not in a test count.**
- [SWE@Google ch. 12](https://abseil.io/resources/swe-book/html/ch12.html): "the ideal test is unchanging"; a test that must change in a pure refactoring means "the tests were not written at an appropriate level of abstraction". Brittle tests are those "that over-specify expected outcomes or rely on extensive and complicated boilerplate".
- Spotify: "Having too many unit tests in Microservices, which are small by definition, also restricts how we can change the code without also having to change the tests." Dodds: tests of internals give false negatives on refactor and false positives when wiring breaks.
- Google calls [change-detector tests](https://testing.googleblog.com/2015/01/testing-on-toilet-change-detector-tests.html) (a mocked call-order check that restates the code) "negative value ... These tests should be re-written or deleted."
- DORA's smell test: "If every time you change your code you must also change multiple unit tests, you're probably over-relying on mocking, or failing to prune your unit test suite."

**Defect detection per test: weak evidence either way.**
- More tests do detect more (mutants): across 31,000 sampled suites of five large Java projects, effectiveness correlates moderately to very highly with suite size (r from 0.51 to 0.98), but coverage adds only a "low to moderate" correlation once size is fixed ([Inozemtseva and Holmes, ICSE 2014](https://www.cs.ubc.ca/~rtholmes/papers/icse_2014_inozemtseva.pdf)). Limits stated there: Java, five open-source projects, mutants as the stand-in for faults, suites that are random subsets of the developers' own suite.
- Observational industry data: higher coverage went with fewer field defects at Avaya and Microsoft (Vista), but "test effort increases exponentially with test coverage, but the reduction in field problems increases linearly" ([Mockus, Nagappan, Dinh-Trong, ESEM 2009](https://mockus.org/papers/coverage.pdf)); in Avaya's data the biggest drop was still from the 60-80% band to 80-100% (probability 0.040 to 0.018 at three prior change requests), so "stop at 70%" is not what that table says.
- **I found no controlled study comparing a many-small suite against a few-broad one on escaped defects** (unverified that none exists; none surfaced). Dodds says of his own numbers "no science there", and cites Tim Bray: "let's not kid ourselves that our software-testing tenets constitute scientific knowledge".
- Google's confidence case for tests is policy-level, not per-test: after Google Web Server required tests with every change, "the number of emergency pushes dropped by half" (SWE ch. 11). That supports *having* a suite, which nobody here disputes.

**Reading for NebulaCtrl.** Broad tests are the expensive and flaky kind, so the answer to a slow, leaky suite is not "replace with e2e". Fine-grained tests are risky when they pin internals: 585 `toHaveBeenCalled*` matchers and 22 `mock.module` calls across 12,353 tests, and no snapshots, is a low-mock suite by those counts, so brittleness is an open question to measure (section 5), not a presumption.

## 3. Coverage targets: when they help and when they hurt

Help:
- **Finding untested code.** Inozemtseva: coverage is "useful for identifying under-tested parts of a program", "should not be used as a quality target". Google applies it per changeset in review ([Code Coverage at Google, FSE 2019](https://research.google/pubs/code-coverage-at-google/): "a key aspect of making coverage information actionable is to apply it at the level of changesets and code review"). NebulaCtrl's `codecov/patch` at 85% does this.
- **Kick-starting a testing habit** in code with none (SWE ch. 12: unit tests "promote high levels of test coverage because they are quick and easy to write"; Mockus lists "by introducing Coverage ... developers who did not write unit tests have started doing so" as practitioner folklore, not as a finding).

Hurt:
- **It rewards execution, not verification.** SWE ch. 11: coverage "only measures that a line was invoked, not what happened as a result", and "instead of treating 80% like a floor, engineers treat it like a ceiling". [Google, Code Coverage Best Practices](https://testing.googleblog.com/2020/08/code-coverage-best-practices.html) (quoted in [coverage-practices.md](coverage-practices.md)): pressure to hit the metric "almost never yields the desired outcome".
- **It pushes toward testing what has no logic and toward implementation details.** Dodds: "spending time testing things that really don't need to be tested ... Maintaining tests like this actually really slow you and your team down"; Vocke: "you **don't test trivial code**".
- **It is incidental under broad tests.** SWE ch. 11 recommends "only measuring coverage from small tests to avoid coverage inflation". NebulaCtrl already counts `-coverpkg=./...` and smoke coverage ([codecov.yml](../../codecov.yml)); a high number there says little about whether any one package is *asserted*.
- **Diminishing returns.** Mockus (effort exponential, benefit linear) and Google's own "60% acceptable, 75% commendable, 90% exemplary".

The alternative, **mutation testing**, measures whether tests notice a change, and Google runs it on code review rather than as a global score:
- Bugs coupled with a mutant "in around 70% of cases"; projects using it "get more tests over time" and see fewer live mutants ([Petrovic, Mutation Testing, 2021](https://testing.googleblog.com/2021/04/mutation-testing.html); the [ICSE 2021 study](https://arxiv.org/abs/2103.07189) is cited there; I read only its abstract).
- The catch is noise: the "Not useful" rate began "around 80%" and took heuristics (one mutant per line, ignore logging and "arid" nodes) to reach about 15%; the post says tests for unproductive mutants "would make the test suite worse, not better". Do not chase every survivor.
- Tools: [Gremlins](https://github.com/go-gremlins/gremlins/blob/b48a4aad17eff33dc7262e0d1487b1a4d6eec322/README.md) for Go ("doesn't work very well on very big Go modules ... a run can take hours"; 0.x, flags may change); [StrykerJS](https://stryker-mutator.io/docs/stryker-js/incremental/) with `--incremental` reruns only changed code and tests. Its runner table lists Jest, Mocha, Vitest and a generic Command runner; **Bun's test runner is not listed (unverified that it works)**, and the Command runner reports no test locations, so incremental mode would only track mutant changes.

## 4. Is this test worth keeping? Deletion criteria

Apply to a test (or a file) in this order; the first "yes" in the delete list is enough.

**Delete (or rewrite at the public interface) when:**
1. **Change detector.** It asserts which mocks were called, in what order, or restates the implementation. Google: "A correct or incorrect program is equally likely to pass a test that is a derivative of the code under test."
2. **It cannot fail for the bug it names.** No assertion (Biome's `useExpect` flags the shape), or an assertion that holds for any render. Check by hand: break the code under test (delete a line, flip a condition) and see whether it fails. Mutation does this at scale.
3. **It breaks on a behavior-preserving change.** SWE ch. 12's test: tests that change in a pure refactoring were written at the wrong level. Evidence is in your history: test-only edits in refactor commits.
4. **Another test fails for the same break.** Vocke: "I delete high-level tests that are already covered on a lower level ... Beware of the sunk cost fallacy and hit the delete key." Test it with the mutation check: if breaking the code fails five tests, four are redundant unless they pin different behaviors.
5. **It is flaky and nobody will fix it.** Google quarantines automatically; DORA: "don't tolerate flaky tests". A quarantined test that stays quarantined is dead weight.
6. **Nobody can say what it protects.** SWE ch. 12: with an unclear test "removing the test will have no effect other than (potentially) introducing a subtle hole in test coverage"; if you cannot name the behavior, the hole is not a hole.
7. **It tests the library, generator or trivial code.** Generated clients, framework behavior, getters (Vocke), a prop passed through.

**Keep, even if it looks redundant, when:**
- It guards a bug that already happened or a security boundary (SWE's Beyonce Rule: "test everything that you don't want to break": the IDOR, secrets-never-leak and network-isolation smoke tests).
- It is a shared helper's own test: SWE ch. 12 keeps the redundancy for "support libraries" because users' tests can disappear.
- It is isolated, many-case logic where a table of cases replaces dozens of broad tests (Spotify's log parser).
- It is the one broad test of a path no narrower test can reach (Vocke: the higher-level test should cover "the part that the lower-level tests couldn't cover").

**Scorecard.** For a doubtful test grade Beck's properties that matter here, in the order *behavioral, structure-insensitive, deterministic, specific, fast*; two failures and no unique protection means delete. Beck's *predictive* and *inspiring* are what the retained tests exist for.

## 5. Recommendation for NebulaCtrl

**Measured baseline** (source counts at `81c571c`, see the method note above):

| | Web (`apps/web/src`) | Go |
|---|---|---|
| Files | 881 test files (262 `.test.ts`, 619 `.test.tsx`) | 775 `_test.go` |
| Tests | 12,353 `it/test(` calls (~13k with `.each`); 14 per file; 2.7 `expect(` per test | 5,860 `func Test`, 609 `t.Run` |
| Level | 8,650 (70%) in files that render; 3,621 (29%) in files that never render | 1,103 (19%) in files using `internal/testutil` (a fresh Postgres schema per test; skipped without `NEBULACTRL_TEST_DATABASE_URL`); 1,203 use `httptest`; 65 in `tests/integration` (smoke and full tiers) |
| Mocks | 22 `mock.module`, 585 `toHaveBeenCalled*`, 0 snapshots | 0 uses of gomock, mockery, testify/mock or sqlmock |
| Size | tests 229,876 lines against 188,218 of source (1.22x; generated API and route tree excluded) | tests 225,438 lines against 466,426 non-test (ratio at most 0.48; the denominator includes generated code) |
| Broad | 3 Playwright specs, 7 tests, not in CI | smoke: 8 Docker scenarios; CI timeouts: go 14 min, web 6 min, smoke 12 min ([ci.yml](../../.github/workflows/ci.yml)) |

**Verdict.** Keep the layered shape; do not swap fine-grained for broad. The web tests are mostly Dodds-integration already, the Go tests already avoid mock frameworks and use a real database at the handler boundary (Spotify's model), and the broad tier is correctly tiny. What the sources say is wrong is a 10-minute run, leaks that make passes order-dependent (neither DORA's bar nor Google's isolation property), and volume nobody has checked for redundancy. So: prune by a rule, harden the run, add a few real flows, change what coverage is used for.

**Target mix.** Counts are *shares of tests*; the binding constraints are time and the pruning rule, not the shares. All numbers are **[INFERENCE]** calibrated on Google's 80/15/5 and Spotify's "few implementation tests", except the DORA bar.

| Tier | Web target | Go target | Budget |
|---|---|---|---|
| Isolated logic (small, narrow): formatters, reducers, schemas, parsers, planners, crypto, state machines | ~30% (today 29%); table-driven | ~65% (today ~80% of tests do not use the database helper) | each test far below 0.1 s (Google: "one tenth of a second is considered slow for unit tests") |
| Screen, feature or handler tests over real collaborators (small or medium, wide): rendered screen with stubbed API; `httptest` handler plus service plus real Postgres | ~70% (today 70%), but one test per user-visible behavior and per distinct error, empty or permission state, not per prop or branch | ~30% (today ~19% use the DB helper) | web job under **4 minutes** in CI (the maintainer reports ~10 today; the CI job limit is 6), Go shard under 6 minutes |
| Real browser (layer 2 in [web-ui-testing.md](web-ui-testing.md)) and Playwright flows | 10 to 15 component tests, 5 to 10 flows (sign-in, deploy, one cross-service path), **in CI** | n/a | all under 5 minutes |
| Docker smoke and full tier | n/a | keep 8 smoke scenarios and the full tier as the release gate; add one only when a bug escaped below it and cannot be pushed down | smoke at most 12 minutes (today's timeout) |

Read the Go row as direction for **new** tests, not a rewrite: 4,776 functions are outside the DB helper, and I have not checked how many assert behavior. Pushing handler-level DB tests up from 19% only makes sense where a package's logic is mostly glue; where it is parsing or planning, the isolated test is the right one (Spotify).

**Pruning rule** (solo-maintainer sized; reuses the checks in section 4):
1. **Sample first.** Take 30 web test files stratified by directory and apply the section 4 list. Record the share p of tests that fail criteria 1 to 4 and 7. If p < 10%, the suite is not bloated: stop pruning and spend the effort on time and leaks ([web-ui-testing.md](web-ui-testing.md), phase 0 and 1). If 10% <= p < 30%, prune the worst directories only. If p >= 30%, sweep every directory in turn. The expectation of 30 to 50% fewer web tests is a guess; the sample replaces it.
2. **Mutation check, bounded.** On the sampled sources run Stryker (web) or Gremlins (three or four small Go packages, not the whole module) and read survivors only in changed or critical code (auth, secrets, permissions, deploy planner). A test that kills no mutant its siblings do not also kill is a deletion candidate. Cap at one survivor per line and skip logging, display strings and constants, as Google does.
3. **Always, per PR.** A behavior-preserving change that forces edits in more than a handful of test files means those tests are coupled to internals: rewrite them to the public interface or delete them, and say so in the PR description. Count "test-only edits per behavior-preserving commit" from PR descriptions; git history is too short to measure today (7 commits at `81c571c`).
4. **Flaky means fix in a week or delete.** No `retry` (Google quarantine reduces noise and "could easily mask a real race condition"). Consistent with [web-ui-testing.md](web-ui-testing.md) section 3.5.
5. **Never prune toward a coverage number, and never write toward one.** Deleting redundant tests lowers the project figure; that is fine because the project status is informational. The patch status stays the prompt it already is: when it fails, read the uncovered lines, then either add a test for a behavior or justify the gap in the PR. Do not raise the target to 90% as a goal ([coverage-practices.md](coverage-practices.md) discusses the gate); a high total from `-coverpkg` and smoke runs is incidental credit.

**Order of work.** (a) Sample and measure (half a day); (b) the setup fixes already listed in web-ui-testing phase 0 and 1, since a leaky run makes every pruning decision untrustworthy; (c) prune per the sample result; (d) move Playwright into CI with the flows above and put the unit tests that those flows should have had below them (Vocke's rule 1). Weekly or quarterly mutation runs on a rotating slice are optional and cheap only because they are scoped.

## Limits and open items

- No controlled evidence for any ratio; the 70/20/10 and 80/15/5 figures are rules of thumb with different bases (Google by test count, with team variation stated). My table is built on them and marked as inference.
- Mutation score and coverage studies are Java and mutants-as-faults. Stryker with `bun:test` is untested here; Gremlins on this module is unmeasured (466k non-test lines, so scope it per package).
- Counts are textual: `it/test(` misses `.each` expansion and helper-driven cases; the Go split is by import of `internal/testutil`, not by what each test asserts.
- Whether Playwright against the Docker smoke images serves the web UI (needed for flows in CI) is open (see [web-ui-testing.md](web-ui-testing.md)).
- Fowler's and Dodds's 2021 posts and Google's blog posts are single-author opinions; DORA's page is a capability summary, not a data table, and I did not read the underlying survey data.
