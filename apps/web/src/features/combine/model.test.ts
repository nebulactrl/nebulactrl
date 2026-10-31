import { describe, expect, test } from "bun:test";
import type { Service } from "#/api/models";
import { makeDeployment, makeService } from "#/features/canvas/lib/fixtures";
import {
	blockerText,
	buildRequest,
	type CombineChoices,
	canCombine,
	chooseFor,
	combineSteps,
	conflictOptionLabel,
	defaultTarget,
	isSettled,
	keptAsIs,
	MAX_COMBINE,
	normalizeGitUrl,
	type PlanLike,
	planBlockers,
	processNameProblems,
	progressSteps,
	reviewOf,
	suggestProcessName,
	suggestProcessNames,
	unresolvedConflicts,
} from "./model";

const REPO = "https://github.com/acme/shop.git";

function git(id: string, over: Partial<Service> = {}): Service {
	const base = makeService();
	return makeService({
		id,
		name: id,
		slug: id,
		sourceType: "git",
		config: { ...base.config, gitUrl: REPO, gitBranch: "main" },
		...over,
	});
}

/** A git service whose build config differs from `git()` in one field. */
function differing(config: Partial<Service["config"]>): Service {
	const base = git("worker");
	return { ...base, config: { ...base.config, ...config } };
}

function reason(services: readonly Service[]): string {
	const check = canCombine(services);
	if (check.ok) throw new Error("expected the pre-check to fail");
	return check.reason;
}

describe("canCombine", () => {
	test("services of one repository, branch and build settings can be combined", () => {
		expect(canCombine([git("web"), git("worker")])).toEqual({ ok: true });
		expect(canCombine([git("a"), git("b"), git("c")])).toEqual({ ok: true });
	});

	test("needs two services and at most ten", () => {
		expect(reason([git("web")])).toContain("at least two");
		expect(reason([])).toContain("at least two");
		const eleven = Array.from({ length: MAX_COMBINE + 1 }, (_, i) =>
			git(`s${i}`),
		);
		expect(reason(eleven)).toContain(`at most ${MAX_COMBINE}`);
		const ten = Array.from({ length: MAX_COMBINE }, (_, i) => git(`s${i}`));
		expect(canCombine(ten).ok).toBe(true);
	});

	test("services of different environments cannot be combined", () => {
		expect(
			reason([git("web"), git("worker", { environmentId: "env-2" })]),
		).toContain("different environments");
	});

	test("only apps and workers qualify", () => {
		expect(canCombine([git("web"), git("queue", { kind: "worker" })]).ok).toBe(
			true,
		);
		for (const kind of ["database", "stack", "route"] as const) {
			expect(reason([git("web"), git("other", { kind })])).toContain(
				"Only apps and workers",
			);
		}
	});

	test("an image service is named in the reason", () => {
		const text = reason([git("web"), git("api", { sourceType: "image" })]);
		expect(text).toContain("api deploys an image");
	});

	test("a service with processes is refused", () => {
		const process = {
			name: "web",
			default: true,
			port: 0,
			command: [],
			singleton: false,
			stopGraceSeconds: 30,
			scaling: makeService().scaling,
			observed: null,
			pending: false,
			removed: false,
		};
		expect(
			reason([git("web"), git("worker", { processes: [process] })]),
		).toContain("worker already has several processes");
	});

	test("every build setting must match, and the reason names the one that differs", () => {
		const cases: [Partial<Service["config"]>, string][] = [
			[{ gitUrl: "https://github.com/acme/other.git" }, "repository"],
			[{ gitBranch: "next" }, "branch"],
			[{ buildType: "dockerfile" }, "build type"],
			[{ dockerfilePath: "docker/Dockerfile" }, "Dockerfile path"],
			[{ buildContext: "apps/web" }, "build context"],
			[{ submodules: "on" }, "submodules setting"],
			[{ buildLocation: "build_server" }, "build location"],
		];
		for (const [config, label] of cases) {
			const text = reason([git("web"), differing(config)]);
			expect(text).toContain(`use a different ${label}`);
			expect(text).toContain("web and worker");
		}
	});

	test("the same repository written differently still matches", () => {
		const same = differing({ gitUrl: "HTTPS://GitHub.com/acme/shop/" });
		expect(canCombine([git("web"), same]).ok).toBe(true);
		const credentials = differing({
			gitUrl: "https://token@github.com/acme/shop.git",
		});
		expect(canCombine([git("web"), credentials]).ok).toBe(true);
	});

	test("defaults match each other: an unset setting equals its default value", () => {
		const explicit = differing({
			buildContext: ".",
			dockerfilePath: "Dockerfile",
			submodules: "auto",
			buildType: "auto",
		});
		expect(canCombine([git("web"), explicit]).ok).toBe(true);
	});

	test("a service that is deploying blocks it", () => {
		for (const status of [
			"queued",
			"building",
			"releasing",
			"verifying",
		] as const) {
			const busy = git("worker", { latestDeployment: makeDeployment(status) });
			expect(reason([git("web"), busy])).toContain("worker is deploying");
		}
		const settled = git("worker", {
			latestDeployment: makeDeployment("succeeded"),
		});
		expect(canCombine([git("web"), settled]).ok).toBe(true);
	});
});

describe("normalizeGitUrl", () => {
	test("drops credentials, the .git suffix and trailing slashes; lowercases the host", () => {
		expect(normalizeGitUrl("https://user:pw@GitHub.com/Acme/Shop.git/")).toBe(
			"https://github.com/Acme/Shop",
		);
	});

	test("compares an scp-style address as written, without .git", () => {
		expect(normalizeGitUrl("git@github.com:acme/shop.git")).toBe(
			"git@github.com:acme/shop",
		);
	});
});

describe("defaultTarget", () => {
	test("the service that serves a domain stays", () => {
		const domain = {
			id: "d1",
			hostname: "shop.acme.com",
		} as Service["domains"][number];
		const served = git("web", { domains: [domain] });
		expect(defaultTarget([git("a"), served, git("c")])).toBe("web");
	});

	test("else the one with a port, else the first", () => {
		const ported = git("api", {
			config: { ...git("x").config, port: 3000 },
		});
		const portless = (id: string) =>
			git(id, { config: { ...git("x").config, port: 0 } });
		expect(defaultTarget([portless("a"), ported])).toBe("api");
		expect(defaultTarget([portless("a"), portless("b")])).toBe("a");
	});

	test("an empty selection has no target", () => {
		expect(defaultTarget([])).toBe("");
	});
});

describe("process names", () => {
	test("a slug loses the target's slug as a prefix", () => {
		expect(suggestProcessName("shop-worker", "shop")).toBe("worker");
		expect(suggestProcessName("queue", "shop")).toBe("queue");
		expect(suggestProcessName("shop", "shop")).toBe("shop");
		expect(suggestProcessName("shop-", "shop")).toBe("shop");
	});

	test("a suggestion is a valid process name", () => {
		const long = suggestProcessName("a".repeat(60), "shop");
		expect(long.length).toBeLessThanOrEqual(31);
		expect(suggestProcessName("Shop_Worker.v2", "other")).toBe(
			"shop-worker-v2",
		);
	});

	test("every other service gets a unique name, the target none", () => {
		const names = suggestProcessNames(
			[
				{ id: "t", slug: "shop" },
				{ id: "a", slug: "shop-worker" },
				{ id: "b", slug: "worker" },
				{ id: "c", slug: "shop-web" },
			],
			"t",
		);
		expect(names).toEqual({ a: "worker", b: "worker-2", c: "web-2" });
	});

	test("the default process' own name is never suggested", () => {
		const names = suggestProcessNames(
			[
				{ id: "t", slug: "shop" },
				{ id: "a", slug: "shop-app" },
			],
			"t",
			"app",
		);
		expect(names).toEqual({ a: "app-2" });
	});

	test("typed names are checked: required, pattern, default name, duplicates", () => {
		expect(processNameProblems({ a: "worker", b: "queue" })).toEqual({});
		const problems = processNameProblems({
			a: "",
			b: "Worker",
			c: "-x",
			d: "web",
			e: "same",
			f: "same",
		});
		expect(problems.a).toContain("required");
		expect(problems.b).toContain("lowercase");
		expect(problems.c).toContain("lowercase");
		expect(problems.d).toContain("default process");
		expect(problems.e).toContain("both become same");
		expect(problems.f).toContain("both become same");
		expect(Object.keys(problems).sort()).toEqual([
			"a",
			"b",
			"c",
			"d",
			"e",
			"f",
		]);
	});

	test("the pattern allows 31 characters, not 32", () => {
		expect(processNameProblems({ a: "a".repeat(31) })).toEqual({});
		expect(processNameProblems({ a: "a".repeat(32) }).a).toBeDefined();
	});

	test("a renamed default process frees 'web'", () => {
		expect(processNameProblems({ a: "web" }, "app")).toEqual({});
		expect(processNameProblems({ a: "app" }, "app").a).toContain("app");
	});
});

// --- the plan -----------------------------------------------------------------

function member(
	serviceId: string,
	over: Partial<NonNullable<PlanLike["members"]>[number]> = {},
) {
	return {
		serviceId,
		slug: serviceId,
		name: serviceId,
		port: 0,
		command: null,
		domains: null,
		hasStagedChanges: false,
		suggestedProcessName: serviceId,
		isTarget: false,
		...over,
	};
}

function plan(over: Partial<PlanLike> = {}): PlanLike {
	return {
		fingerprint: "fp-1",
		targetServiceId: "web",
		members: [member("web", { isTarget: true }), member("worker")],
		blocking: [],
		warnings: [],
		variables: { sameCount: 0, onlyIn: [], conflicts: [] },
		settingsIgnored: [],
		domainMoves: [],
		taskMoves: [],
		...over,
	};
}

const conflicting = plan({
	variables: {
		sameCount: 12,
		onlyIn: [
			{ key: "QUEUE_NAME", serviceId: "worker", secret: false },
			{ key: "WEB_ONLY", serviceId: "web", secret: false },
			{ key: "QUEUE_TOKEN", serviceId: "worker", secret: true },
		],
		conflicts: [
			{
				key: "APP_ENV",
				secret: false,
				scope: "runtime",
				options: [
					{
						serviceId: "web",
						slug: "web",
						preview: "production",
						sameAs: null,
					},
					{
						serviceId: "worker",
						slug: "worker",
						preview: "staging",
						sameAs: ["cron"],
					},
				],
			},
			{
				key: "API_KEY",
				secret: true,
				scope: "runtime",
				options: [
					{ serviceId: "web", slug: "web", preview: "", sameAs: null },
					{ serviceId: "worker", slug: "worker", preview: "", sameAs: null },
				],
			},
			{
				key: "ONLY_WORKER_OPTION",
				secret: false,
				scope: "runtime",
				options: [
					{ serviceId: "worker", slug: "worker", preview: "x", sameAs: null },
				],
			},
		],
	},
});

describe("blockers", () => {
	test("a known code adds what to do after the server's message", () => {
		const text = blockerText({
			code: "source_has_volumes",
			message: "worker has a volume.",
		});
		expect(text.startsWith("worker has a volume. ")).toBe(true);
		expect(text).toContain("volume");
		expect(text.length).toBeGreaterThan("worker has a volume.".length);
	});

	test("the fix is not repeated when the message already says it", () => {
		const once = blockerText({ code: "plan_stale", message: "Out of date." });
		expect(blockerText({ code: "plan_stale", message: once })).toBe(once);
	});

	test("an unknown code is the server's message alone", () => {
		expect(blockerText({ code: "something_new", message: "Not now." })).toBe(
			"Not now.",
		);
	});

	test("the plan's blockers read in order; none is an empty list", () => {
		expect(planBlockers(plan())).toEqual([]);
		expect(planBlockers(plan({ blocking: null }))).toEqual([]);
		const texts = planBlockers(
			plan({
				blocking: [
					{
						code: "port_conflict",
						serviceId: "worker",
						message: "Port 3000 twice.",
					},
					{ code: "other", serviceId: "web", message: "Second." },
				],
			}),
		);
		expect(texts).toHaveLength(2);
		expect(texts[0]).toContain("Port 3000 twice.");
		expect(texts[1]).toBe("Second.");
	});
});

describe("variable choices", () => {
	test("every conflict without a valid choice is unresolved", () => {
		expect(unresolvedConflicts(conflicting, {})).toEqual([
			"APP_ENV",
			"API_KEY",
			"ONLY_WORKER_OPTION",
		]);
		expect(
			unresolvedConflicts(conflicting, {
				APP_ENV: "web",
				API_KEY: "worker",
				ONLY_WORKER_OPTION: "worker",
			}),
		).toEqual([]);
	});

	test("a choice naming a service the conflict does not offer does not count", () => {
		expect(
			unresolvedConflicts(conflicting, {
				APP_ENV: "web",
				API_KEY: "gone",
				ONLY_WORKER_OPTION: "web",
			}),
		).toEqual(["API_KEY", "ONLY_WORKER_OPTION"]);
	});

	test("a plan without conflicts has nothing to resolve", () => {
		expect(unresolvedConflicts(plan(), {})).toEqual([]);
	});

	test("'use <service> for every key' fills the conflicts it has an option in and leaves others", () => {
		const next = chooseFor(conflicting, "web", {
			ONLY_WORKER_OPTION: "worker",
		});
		expect(next).toEqual({
			APP_ENV: "web",
			API_KEY: "web",
			ONLY_WORKER_OPTION: "worker",
		});
		expect(unresolvedConflicts(conflicting, next)).toEqual([]);
	});

	test("it overrides earlier choices for the keys it covers and does not mutate its input", () => {
		const before = { APP_ENV: "worker" };
		const next = chooseFor(conflicting, "web", before);
		expect(next.APP_ENV).toBe("web");
		expect(before).toEqual({ APP_ENV: "worker" });
	});

	test("an option reads as who has the value and the value; a secret only 'differs'", () => {
		const [env, key] = conflicting.variables.conflicts ?? [];
		const options = env?.options ?? [];
		expect(conflictOptionLabel(env as never, options[0] as never)).toBe(
			"web · production",
		);
		expect(conflictOptionLabel(env as never, options[1] as never)).toBe(
			"worker, cron · staging",
		);
		const secrets = key?.options ?? [];
		expect(conflictOptionLabel(key as never, secrets[0] as never)).toBe(
			"web · differs",
		);
	});

	test("an option names the other services through the caller's lookup", () => {
		const env = conflicting.variables.conflicts?.[0];
		const option = env?.options?.[1];
		const label = conflictOptionLabel(env as never, option as never, (id) =>
			id.toUpperCase(),
		);
		expect(label).toBe("worker, CRON · staging");
	});

	test("an empty non-secret value shows only who has it", () => {
		expect(
			conflictOptionLabel(
				{ secret: false },
				{ slug: "web", preview: "", sameAs: null },
			),
		).toBe("web");
	});

	test("keys in one service only are kept as they are, grouped by service", () => {
		const kept = keptAsIs(conflicting);
		expect([...kept.keys()]).toEqual(["worker", "web"]);
		expect(kept.get("worker")).toEqual(["QUEUE_NAME", "QUEUE_TOKEN"]);
		expect(kept.get("web")).toEqual(["WEB_ONLY"]);
		expect(keptAsIs(plan()).size).toBe(0);
	});
});

describe("steps", () => {
	test("Variables appears only when the plan has conflicts", () => {
		expect(combineSteps(undefined).map((s) => s.id)).toEqual([
			"services",
			"review",
			"progress",
		]);
		expect(combineSteps(plan()).map((s) => s.id)).toEqual([
			"services",
			"review",
			"progress",
		]);
		expect(combineSteps(conflicting).map((s) => s.id)).toEqual([
			"services",
			"variables",
			"review",
			"progress",
		]);
		expect(combineSteps(conflicting).map((s) => s.label)).toEqual([
			"Services",
			"Variables",
			"Review",
			"Progress",
		]);
	});
});

describe("reviewOf", () => {
	const full = plan({
		warnings: ["Pre-deploy of worker is dropped."],
		blocking: [{ code: "other", serviceId: "web", message: "Held." }],
		settingsIgnored: [
			{
				serviceId: "worker",
				field: "config.memoryMb",
				note: "the target's value is kept",
			},
		],
		domainMoves: [
			{
				domainId: "d1",
				hostname: "queue.acme.com",
				fromServiceId: "worker",
				process: "worker",
			},
		],
		taskMoves: [{ taskId: "t1", name: "digest", fromServiceId: "worker" }],
	});

	test("the default process comes first, then one per other service in plan order", () => {
		const review = reviewOf(full, {
			targetServiceId: "web",
			processNames: { worker: "jobs" },
		});
		expect(review.processes).toEqual([
			{ name: "web", fromSlug: "web", isDefault: true },
			{ name: "jobs", fromSlug: "worker", isDefault: false },
		]);
	});

	test("a missing name falls back to the plan's suggestion; the default name can be renamed", () => {
		const review = reviewOf(
			full,
			{ targetServiceId: "web", processNames: {} },
			"app",
		);
		expect(review.processes.map((p) => p.name)).toEqual(["app", "worker"]);
	});

	test("the target can be another member: it takes the default process", () => {
		const review = reviewOf(full, {
			targetServiceId: "worker",
			processNames: { web: "frontend" },
		});
		expect(review.processes).toEqual([
			{ name: "web", fromSlug: "worker", isDefault: true },
			{ name: "frontend", fromSlug: "web", isDefault: false },
		]);
	});

	test("domains move to the process their service becomes; tasks, ignored settings, warnings and blockers are listed", () => {
		const review = reviewOf(full, {
			targetServiceId: "web",
			processNames: { worker: "jobs" },
		});
		expect(review.domains).toEqual([
			{ hostname: "queue.acme.com", process: "jobs", fromSlug: "worker" },
		]);
		expect(review.tasks).toEqual([{ name: "digest", fromSlug: "worker" }]);
		expect(review.ignored).toEqual([
			{
				fromSlug: "worker",
				field: "config.memoryMb",
				note: "the target's value is kept",
			},
		]);
		expect(review.warnings).toEqual(["Pre-deploy of worker is dropped."]);
		expect(review.blockers).toEqual(["Held."]);
	});

	test("an empty plan reviews as just the processes", () => {
		const review = reviewOf(
			plan({
				blocking: null,
				warnings: null,
				settingsIgnored: null,
				domainMoves: null,
				taskMoves: null,
			}),
			{ targetServiceId: "web", processNames: { worker: "worker" } },
		);
		expect(review.domains).toEqual([]);
		expect(review.tasks).toEqual([]);
		expect(review.ignored).toEqual([]);
		expect(review.warnings).toEqual([]);
		expect(review.blockers).toEqual([]);
	});
});

describe("buildRequest", () => {
	const choices: CombineChoices = {
		targetServiceId: "web",
		processNames: { worker: " jobs " },
		variableChoices: { APP_ENV: "web", API_KEY: "worker", GONE: "web" },
		stopSourcesFirst: false,
	};

	test("carries the fingerprint, every member, trimmed names and only the plan's conflicts", () => {
		const request = buildRequest(conflicting, choices, "web");
		expect(request).toEqual({
			fingerprint: "fp-1",
			targetServiceId: "web",
			serviceIds: ["web", "worker"],
			processNames: { worker: "jobs" },
			variableChoices: { APP_ENV: "web", API_KEY: "worker" },
			stopSourcesFirst: false,
		});
	});

	test("the target gets no process name even when one was typed for it", () => {
		const request = buildRequest(
			plan(),
			{ ...choices, processNames: { web: "x", worker: "jobs" } },
			"web",
		);
		expect(request.processNames).toEqual({ worker: "jobs" });
	});

	test("a default process name other than 'web' is sent; 'web' is left to the server", () => {
		expect(
			buildRequest(plan(), choices, "web").defaultProcessName,
		).toBeUndefined();
		expect(buildRequest(plan(), choices, "app").defaultProcessName).toBe("app");
	});

	test("a note is sent trimmed, and only when there is one", () => {
		expect(buildRequest(plan(), choices, "web").message).toBeUndefined();
		expect(buildRequest(plan(), choices, "web", "   ").message).toBeUndefined();
		expect(buildRequest(plan(), choices, "web", " cutover ").message).toBe(
			"cutover",
		);
	});

	test("'stop the others first' goes through", () => {
		const request = buildRequest(
			plan(),
			{ ...choices, stopSourcesFirst: true },
			"web",
		);
		expect(request.stopSourcesFirst).toBe(true);
	});

	test("a member without a typed name is sent as empty so the server refuses it by name", () => {
		const request = buildRequest(
			plan(),
			{ ...choices, processNames: {} },
			"web",
		);
		expect(request.processNames).toEqual({ worker: "" });
	});
});

describe("progressSteps", () => {
	const states = (view: Parameters<typeof progressSteps>[0]) =>
		progressSteps(view).map((step) => step.state);

	test("the four stages read Staged, Deploying, Cutover, Done", () => {
		expect(progressSteps({ state: "staged" }).map((s) => s.label)).toEqual([
			"Staged",
			"Deploying",
			"Cutover",
			"Done",
		]);
	});

	test("each state marks the stages before it done and itself current", () => {
		expect(states({ state: "staged" })).toEqual([
			"current",
			"pending",
			"pending",
			"pending",
		]);
		expect(states({ state: "deploying" })).toEqual([
			"done",
			"current",
			"pending",
			"pending",
		]);
		expect(states({ state: "cutover" })).toEqual([
			"done",
			"done",
			"current",
			"pending",
		]);
	});

	test("done marks every stage done", () => {
		expect(states({ state: "done" })).toEqual(["done", "done", "done", "done"]);
	});

	test("a failure stops at the stage it reached: deploying when a deployment exists, staged otherwise", () => {
		expect(states({ state: "failed", deploymentId: "dep-1" })).toEqual([
			"done",
			"failed",
			"pending",
			"pending",
		]);
		expect(states({ state: "failed", deploymentId: null })).toEqual([
			"failed",
			"pending",
			"pending",
			"pending",
		]);
		expect(states({ state: "failed" })).toEqual([
			"failed",
			"pending",
			"pending",
			"pending",
		]);
	});

	test("polling ends on done, failed and aborted only", () => {
		expect(isSettled({ state: "done" })).toBe(true);
		expect(isSettled({ state: "failed" })).toBe(true);
		expect(isSettled({ state: "aborted" })).toBe(true);
		expect(isSettled({ state: "staged" })).toBe(false);
		expect(isSettled({ state: "deploying" })).toBe(false);
		expect(isSettled({ state: "cutover" })).toBe(false);
	});
});
