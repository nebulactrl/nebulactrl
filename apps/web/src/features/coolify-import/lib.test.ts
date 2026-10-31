import { describe, expect, test } from "bun:test";
import type {
	CoolifyApplyResult,
	CoolifyPlan,
	CoolifyPlanProject,
	CoolifyPlanResource,
} from "#/api/gen/types.gen";
import {
	baselineOf,
	buildApplyRequest,
	buildChoices,
	buildPlanRequest,
	collisionHint,
	coolifyHostAt,
	draftSecrets,
	EMPTY_DRAFT,
	envRowState,
	firstTarget,
	followupSegments,
	groupRewrites,
	type ImportDraft,
	importBlocker,
	includedText,
	isIncluded,
	notesBySeverity,
	patchDomain,
	patchEnv,
	patchProject,
	patchRegistry,
	patchResource,
	redact,
	requestFingerprint,
	resultText,
	rewritesFor,
	sortNotes,
	sourceLine,
	summarizeChecks,
	summarizePlan,
	totalsText,
	unrestrictedNeeds,
	usernameHints,
} from "./lib";

function resource(
	over: Partial<CoolifyPlanResource> & { uuid: string },
): CoolifyPlanResource {
	return {
		kind: "app",
		name: over.uuid,
		slug: over.uuid,
		include: true,
		source: { type: "image", image: "nginx:1.27" },
		domains: [],
		volumes: [],
		needsUnrestricted: [],
		env: [],
		dependsOn: [],
		unsupported: [],
		notes: [],
		...over,
	};
}

function project(
	over: Partial<CoolifyPlanProject> & { uuid: string },
	resources: CoolifyPlanResource[] = [],
): CoolifyPlanProject {
	return {
		name: over.uuid,
		slug: over.uuid,
		action: "create",
		collision: false,
		environments: [
			{
				name: "production",
				slug: "production",
				action: "create",
				production: true,
				resources,
			},
		],
		...over,
	};
}

function plan(projects: CoolifyPlanProject[], over: Partial<CoolifyPlan> = {}) {
	return {
		projects,
		unsupported: [],
		registries: [],
		rewrites: [],
		checks: [],
		warnings: [],
		blockers: [],
		followups: [],
		installable: true,
		totals: {
			projects: projects.length,
			services: 0,
			databases: 0,
			variables: 0,
			secrets: 0,
			domains: 0,
		},
		...over,
	} satisfies CoolifyPlan;
}

describe("buildChoices", () => {
	test("an untouched draft is no choices at all", () => {
		expect(buildChoices(EMPTY_DRAFT)).toEqual({});
	});

	test("skipping a project sends only the skip", () => {
		const draft = patchProject(
			patchProject(EMPTY_DRAFT, "p1", { name: "Shop" }),
			"p1",
			{ skip: true },
		);
		expect(buildChoices(draft)).toEqual({ projects: { p1: { skip: true } } });
	});

	test("a renamed project is trimmed; a blank name is no choice", () => {
		expect(
			buildChoices(patchProject(EMPTY_DRAFT, "p1", { name: "  Shop  " })),
		).toEqual({ projects: { p1: { name: "Shop" } } });
		expect(
			buildChoices(patchProject(EMPTY_DRAFT, "p1", { name: "   " })),
		).toEqual({});
	});

	test("merging into an existing project wins over a new name", () => {
		const draft = patchProject(EMPTY_DRAFT, "p1", {
			name: "Shop",
			existingProjectId: "11111111-1111-1111-1111-111111111111",
		});
		expect(buildChoices(draft)).toEqual({
			projects: {
				p1: { existingProjectId: "11111111-1111-1111-1111-111111111111" },
			},
		});
	});

	test("clearing a field removes its entry", () => {
		const draft = patchProject(
			patchProject(EMPTY_DRAFT, "p1", { skip: true }),
			"p1",
			{ skip: undefined },
		);
		expect(draft.projects).toEqual({});
		expect(buildChoices(draft)).toEqual({});
	});

	test("include false and an explicit include true both travel", () => {
		let draft = patchResource(EMPTY_DRAFT, "r1", { include: false });
		draft = patchResource(draft, "r2", { include: true });
		expect(buildChoices(draft)).toEqual({
			resources: { r1: { include: false }, r2: { include: true } },
		});
	});

	test("resource name, port, kind and version are sent when set", () => {
		let draft = patchResource(EMPTY_DRAFT, "r1", {
			name: " api ",
			port: 0,
			kind: "worker",
			engineVersion: " 16 ",
		});
		draft = patchResource(draft, "r2", { name: "", engineVersion: " " });
		expect(buildChoices(draft)).toEqual({
			resources: {
				r1: { name: "api", port: 0, kind: "worker", engineVersion: "16" },
			},
		});
	});

	test("variable flags, and a value only when it is not empty", () => {
		let draft = patchEnv(EMPTY_DRAFT, "r1", "SECRET_KEY", {
			secret: true,
			value: "s3cret",
		});
		draft = patchEnv(draft, "r1", "DEBUG", { skip: true });
		draft = patchEnv(draft, "r1", "DATABASE_URL", { keepCoolify: true });
		draft = patchEnv(draft, "r1", "EMPTY", { value: "" });
		draft = patchEnv(draft, "r1", "OFF", { secret: false });
		expect(buildChoices(draft)).toEqual({
			resources: {
				r1: {
					env: {
						SECRET_KEY: { secret: true, value: "s3cret" },
						DEBUG: { skip: true },
						DATABASE_URL: { rewrite: false },
						OFF: { secret: false },
					},
				},
			},
		});
	});

	test("keeping Coolify off again sends no rewrite flag", () => {
		let draft = patchEnv(EMPTY_DRAFT, "r1", "X", { keepCoolify: true });
		draft = patchEnv(draft, "r1", "X", { keepCoolify: false });
		expect(buildChoices(draft)).toEqual({});
	});

	test("a value with spaces is sent as typed", () => {
		const draft = patchEnv(EMPTY_DRAFT, "r1", "K", { value: " a b " });
		expect(buildChoices(draft).resources?.r1?.env?.K?.value).toBe(" a b ");
	});

	test("domains: enabled and a replacement host, keyed by the Coolify host", () => {
		let draft = patchDomain(EMPTY_DRAFT, "r1", "app.example.com", {
			enabled: false,
		});
		draft = patchDomain(draft, "r1", "www.example.com", {
			host: " new.example.com ",
		});
		draft = patchDomain(draft, "r1", "blank.example.com", { host: "  " });
		expect(buildChoices(draft)).toEqual({
			resources: {
				r1: {
					domains: {
						"app.example.com": { enabled: false },
						"www.example.com": { host: "new.example.com" },
					},
				},
			},
		});
	});

	test("a registry travels only with both a username and a password", () => {
		let draft = patchRegistry(EMPTY_DRAFT, "ghcr.io", {
			username: "bot",
			password: "pw",
		});
		draft = patchRegistry(draft, "quay.io", { username: "bot" });
		draft = patchRegistry(draft, "reg.example.com", { password: "pw" });
		draft = patchRegistry(draft, "empty.example.com", {
			username: " ",
			password: "pw",
		});
		expect(buildChoices(draft)).toEqual({
			registries: { "ghcr.io": { username: "bot", password: "pw" } },
		});
	});

	test("an untouched username is the plan's hint", () => {
		const draft = patchRegistry(EMPTY_DRAFT, "ghcr.io", { password: "pw" });
		expect(buildChoices(draft, { "ghcr.io": "alice" })).toEqual({
			registries: { "ghcr.io": { username: "alice", password: "pw" } },
		});
		expect(buildChoices(draft)).toEqual({});
	});

	test("the git connection is sent when chosen", () => {
		expect(buildChoices({ ...EMPTY_DRAFT, gitConnectionId: "g1" })).toEqual({
			gitConnectionId: "g1",
		});
	});

	test("the draft is not mutated", () => {
		const before: ImportDraft = patchResource(EMPTY_DRAFT, "r1", {
			include: false,
		});
		const snapshot = JSON.stringify(before);
		patchEnv(before, "r1", "K", { skip: true });
		expect(JSON.stringify(before)).toBe(snapshot);
		expect(JSON.stringify(EMPTY_DRAFT)).toBe(
			JSON.stringify({
				projects: {},
				resources: {},
				registries: {},
				gitConnectionId: "",
			}),
		);
	});
});

describe("requests", () => {
	const source = { url: " https://coolify.example.com ", token: " tok-123 " };

	test("the token appears once, in source, trimmed", () => {
		const draft = patchEnv(EMPTY_DRAFT, "r1", "K", { value: "v" });
		const request = buildPlanRequest(source, buildChoices(draft), "srv");
		expect(request.source).toEqual({
			url: "https://coolify.example.com",
			token: "tok-123",
		});
		expect(JSON.stringify(request).split("tok-123").length - 1).toBe(1);
		expect(JSON.stringify(request.choices)).not.toContain("tok-123");
		expect(request.serverId).toBe("srv");
	});

	test("a plan request without a server has no serverId", () => {
		expect("serverId" in buildPlanRequest(source, {}, "")).toBe(false);
	});

	test("the apply request carries the server, and deploy only when asked", () => {
		const off = buildApplyRequest(source, {}, "srv", false);
		expect(off.serverId).toBe("srv");
		expect("deploy" in off).toBe(false);
		const on = buildApplyRequest(source, {}, "srv", true);
		expect(on.deploy).toBe(true);
		expect(JSON.stringify(on.choices)).not.toContain("tok-123");
		expect(JSON.stringify(on).split("tok-123").length - 1).toBe(1);
	});

	test("the fingerprint follows the choices and the server, never the token", () => {
		const a = buildPlanRequest(source, {}, "srv");
		const b = buildPlanRequest({ ...source, token: "other-token" }, {}, "srv");
		expect(requestFingerprint(a)).toBe(requestFingerprint(b));
		expect(requestFingerprint(a)).not.toContain("tok-123");
		const changed = buildPlanRequest(
			source,
			buildChoices(patchResource(EMPTY_DRAFT, "r1", { include: false })),
			"srv",
		);
		expect(requestFingerprint(changed)).not.toBe(requestFingerprint(a));
		expect(requestFingerprint(buildPlanRequest(source, {}, "srv2"))).not.toBe(
			requestFingerprint(a),
		);
	});

	test("every typed secret can be scrubbed from a message", () => {
		let draft = patchEnv(EMPTY_DRAFT, "r1", "K", { value: "env-value" });
		draft = patchRegistry(draft, "ghcr.io", {
			username: "bot",
			password: "reg-pw",
		});
		draft = patchEnv(draft, "r1", "BLANK", { value: "" });
		const secrets = draftSecrets(source, draft);
		expect([...secrets].sort()).toEqual([" tok-123 ", "env-value", "reg-pw"]);
		expect(redact("bad env-value, reg-pw, tok-123", secrets)).toBe(
			"bad •••, •••, •••",
		);
	});

	test("redact takes a secret out of an error message", () => {
		expect(redact("401 for token tok-123 on host", ["tok-123"])).toBe(
			"401 for token ••• on host",
		);
		expect(redact("nothing here", ["", "  "])).toBe("nothing here");
	});
});

describe("summaries", () => {
	const db = resource({
		uuid: "db",
		kind: "database",
		env: [
			{ key: "POSTGRES_PASSWORD", scope: "runtime", secret: true },
			{ key: "LOCKED", scope: "runtime", secret: true, needsValue: true },
		],
	});
	const web = resource({
		uuid: "web",
		env: [
			{ key: "A", scope: "both", secret: false },
			{ key: "B", scope: "runtime", secret: true },
			{ key: "C", scope: "runtime", secret: false, skipped: true },
		],
		domains: [
			{ host: "a.example.com", enabled: true, forceHttps: true },
			{ host: "b.example.com", enabled: false, forceHttps: true },
		],
	});
	const worker = resource({ uuid: "wk", kind: "worker" });
	const off = resource({ uuid: "off", include: false });
	const p = plan([project({ uuid: "p1" }, [db, web, worker, off])]);

	test("counts what is included, from the plan", () => {
		const summary = summarizePlan(p);
		expect(summary).toMatchObject({
			projects: 1,
			apps: 1,
			workers: 1,
			databases: 1,
			excluded: 1,
			variables: 4,
			secrets: 3,
			domains: 1,
			needValue: 1,
		});
	});

	test("the reader's include, skip and value choices are laid over the plan", () => {
		let draft = patchResource(EMPTY_DRAFT, "wk", { include: false });
		draft = patchResource(draft, "off", { include: true });
		draft = patchEnv(draft, "web", "A", { skip: true });
		draft = patchEnv(draft, "db", "LOCKED", { value: "x" });
		draft = patchDomain(draft, "web", "b.example.com", { enabled: true });
		const summary = summarizePlan(p, draft);
		expect(summary).toMatchObject({
			apps: 2,
			workers: 0,
			excluded: 1,
			variables: 3,
			needValue: 0,
			domains: 2,
		});
	});

	test("a skipped project adds nothing", () => {
		const draft = patchProject(EMPTY_DRAFT, "p1", { skip: true });
		expect(summarizePlan(p, draft)).toMatchObject({
			projects: 0,
			apps: 0,
			databases: 0,
			variables: 0,
		});
		const planned = plan([project({ uuid: "p2", action: "skip" }, [web])]);
		expect(summarizePlan(planned).projects).toBe(0);
		expect(
			summarizePlan(planned, patchProject(EMPTY_DRAFT, "p2", { skip: false }))
				.projects,
		).toBe(1);
	});

	test("an environment the plan skips adds nothing", () => {
		const skipped = project({ uuid: "p3" }, [web]);
		const [environment] = skipped.environments ?? [];
		if (environment) environment.action = "skip";
		expect(summarizePlan(plan([skipped])).apps).toBe(0);
	});

	test("text lines", () => {
		expect(
			totalsText({
				projects: 1,
				services: 2,
				databases: 1,
				variables: 14,
				secrets: 3,
				domains: 0,
			}),
		).toBe(
			"1 project · 2 services · 1 database · 14 variables (3 secret) · 0 domains",
		);
		expect(includedText(summarizePlan(p))).toBe(
			"2 services, 1 database included, 1 left out",
		);
	});

	test("isIncluded: the draft wins over the plan", () => {
		expect(isIncluded(off, EMPTY_DRAFT)).toBe(false);
		expect(
			isIncluded(off, patchResource(EMPTY_DRAFT, "off", { include: true })),
		).toBe(true);
		expect(
			isIncluded(web, patchResource(EMPTY_DRAFT, "web", { include: false })),
		).toBe(false);
	});

	test("envRowState lays the draft over the row", () => {
		const row = {
			key: "K",
			scope: "runtime",
			secret: false,
			needsValue: true,
		} as const;
		expect(envRowState(row, undefined)).toEqual({
			secret: false,
			skipped: false,
			keepCoolify: false,
			supplied: false,
			needsValue: true,
		});
		expect(
			envRowState(row, {
				secret: true,
				skip: true,
				value: "v",
				keepCoolify: true,
			}),
		).toEqual({
			secret: true,
			skipped: true,
			keepCoolify: true,
			supplied: true,
			needsValue: false,
		});
	});
});

describe("severity order", () => {
	test("checks: failures, warnings, passes, each in plan order", () => {
		const summary = summarizeChecks([
			{ id: "a", status: "ok", message: "a" },
			{ id: "b", status: "warn", message: "b" },
			{ id: "c", status: "fail", message: "c" },
			{ id: "d", status: "warn", message: "d" },
		]);
		expect(summary.rows.map((c) => c.id)).toEqual(["c", "b", "d", "a"]);
		expect(summary).toMatchObject({ fail: 1, warn: 2, ok: 1 });
		expect(summarizeChecks(null).rows).toEqual([]);
	});

	test("notes: danger, warning, info", () => {
		const notes = [
			{ key: "i", severity: "info", message: "" },
			{ key: "w1", severity: "warning", message: "" },
			{ key: "d", severity: "danger", message: "" },
			{ key: "w2", severity: "warning", message: "" },
		] as const;
		expect(sortNotes(notes).map((n) => n.key)).toEqual(["d", "w1", "w2", "i"]);
		const by = notesBySeverity(notes);
		expect(by.danger).toHaveLength(1);
		expect(by.warning).toHaveLength(2);
		expect(by.info).toHaveLength(1);
		expect(sortNotes(undefined)).toEqual([]);
	});
});

describe("plan text", () => {
	test("source lines", () => {
		expect(
			sourceLine({
				type: "image",
				image: "docker.io/library/nginx:1.27@sha256:ab",
			}),
		).toBe("library/nginx:1.27");
		expect(
			sourceLine({
				type: "git",
				git: {
					url: "https://github.com/acme/shop",
					branch: "main",
					buildType: "dockerfile",
				},
			}),
		).toBe("https://github.com/acme/shop@main (dockerfile)");
		expect(
			sourceLine({
				type: "managed",
				managed: {
					engine: "postgres",
					version: "16",
					flavor: "postgis",
					from: "postgis/postgis:16",
					notes: [],
				},
			}),
		).toBe("Managed PostgreSQL 16 (postgis)");
		expect(
			sourceLine({
				type: "managed",
				managed: { engine: "valkey", version: "8", from: "", notes: null },
			}),
		).toBe("Managed Valkey 8");
	});

	test("rewrites by resource", () => {
		const rewrites = [
			{
				resource: "web",
				key: "DATABASE_URL",
				kind: "url",
				to: `\${{db.DATABASE_URL}}`,
			},
			{
				resource: "web",
				key: "DB_HOST",
				kind: "host",
				to: `\${{db.DATABASE_HOST}}`,
			},
			{ resource: "api", key: "REDIS", kind: "host", to: `\${{cache.HOST}}` },
		] as const;
		expect(
			groupRewrites(rewrites).map((g) => [g.resource, g.items.length]),
		).toEqual([
			["web", 2],
			["api", 1],
		]);
		expect(rewritesFor(rewrites, "web").get("DB_HOST")?.to).toBe(
			`\${{db.DATABASE_HOST}}`,
		);
		expect(rewritesFor(rewrites, "nobody").size).toBe(0);
	});

	test("registry username hints", () => {
		expect(
			usernameHints(
				plan([], {
					registries: [
						{ host: "ghcr.io", username: "bot", provided: false, services: [] },
						{ host: "quay.io", provided: false, services: [] },
					],
				}),
			),
		).toEqual({ "ghcr.io": "bot" });
		expect(usernameHints(undefined)).toEqual({});
	});

	test("collision hint", () => {
		const taken = project({
			uuid: "p",
			name: "Shop (Coolify)",
			collision: true,
		});
		expect(collisionHint(taken, undefined)).toBe(
			"Name taken → will be created as Shop (Coolify).",
		);
		expect(collisionHint(taken, { name: "Shop" })).toContain(
			"That name is taken",
		);
		expect(collisionHint(project({ uuid: "p" }), undefined)).toBeNull();
		expect(
			collisionHint({ ...taken, action: "existing" }, undefined),
		).toBeNull();
	});
});

describe("baseline", () => {
	test("keeps the Coolify names and domain hosts of the first plan", () => {
		const first = plan([
			project({ uuid: "p1", name: "Shop (Coolify)", collision: true }, [
				resource({
					uuid: "web",
					domains: [
						{ host: "a.example.com", enabled: true, forceHttps: true },
						{ host: "b.example.com", enabled: true, forceHttps: true },
					],
				}),
			]),
		]);
		const baseline = baselineOf(first);
		expect(baseline.projectNames.p1).toBe("Shop");
		expect(coolifyHostAt(baseline, "web", 1, 2)).toBe("b.example.com");
		// the list no longer lines up: do not guess
		expect(coolifyHostAt(baseline, "web", 1, 3)).toBeUndefined();
		expect(coolifyHostAt(undefined, "web", 0, 1)).toBeUndefined();
		expect(coolifyHostAt(baseline, "nope", 0, 1)).toBeUndefined();
	});
});

describe("importBlocker", () => {
	const ok = plan([], {
		totals: {
			projects: 1,
			services: 1,
			databases: 0,
			variables: 0,
			secrets: 0,
			domains: 0,
		},
	});
	const base = { plan: ok, serverId: "srv", fresh: true, failed: false };

	test("nothing in the way", () => {
		expect(importBlocker(base)).toBeNull();
	});
	test("in the order a reader can fix them", () => {
		expect(importBlocker({ ...base, plan: undefined })).toBe(
			"Read Coolify first.",
		);
		expect(importBlocker({ ...base, serverId: "" })).toContain("server");
		expect(importBlocker({ ...base, failed: true })).toContain(
			"could not be updated",
		);
		expect(importBlocker({ ...base, fresh: false })).toBe("Checking the plan…");
	});
	test("a plan that is not installable names its first blocker", () => {
		expect(
			importBlocker({
				...base,
				plan: {
					...ok,
					installable: false,
					blockers: ["Port 80 is taken.", "x"],
				},
			}),
		).toBe("Port 80 is taken.");
		expect(
			importBlocker({
				...base,
				plan: { ...ok, installable: false, blockers: [] },
			}),
		).toContain("problems");
	});
	test("an empty plan has nothing to import", () => {
		expect(importBlocker({ ...base, plan: plan([]) })).toBe(
			"Nothing is included.",
		);
	});
});

describe("result", () => {
	const result: CoolifyApplyResult = {
		projects: [
			{
				id: "p1",
				name: "Shop",
				slug: "shop",
				created: true,
				environments: [
					{ id: "e1", name: "production", slug: "production", created: true },
				],
			},
			{
				id: "p2",
				name: "Blog",
				slug: "blog",
				created: false,
				environments: [
					{ id: "e2", name: "staging", slug: "staging", created: true },
				],
			},
		],
		services: [
			{
				id: "s1",
				name: "web",
				slug: "web",
				kind: "git",
				projectId: "p2",
				projectSlug: "blog",
				environmentId: "e2",
				envSlug: "staging",
				coolifyUuid: "u",
			},
		],
		deployments: [],
		rewrites: [],
		warnings: [],
		followups: [],
	};

	test("opens the project of the first service, in its environment", () => {
		expect(firstTarget(result)).toEqual({
			projectId: "p2",
			envSlug: "staging",
			name: "Blog",
		});
	});

	test("with no service it opens the first project", () => {
		expect(firstTarget({ ...result, services: [] })).toEqual({
			projectId: "p1",
			envSlug: "production",
			name: "Shop",
		});
		expect(
			firstTarget({ ...result, services: null, projects: null }),
		).toBeNull();
	});

	test("the result line", () => {
		expect(resultText(result)).toBe(
			"1 project, 1 project merged into, 1 service; nothing deployed",
		);
		expect(resultText({ ...result, deployments: ["d1", "d2"] })).toContain(
			"2 deploying",
		);
	});
});

describe("followupSegments", () => {
	test("the Postgres follow-up: prose, command, prose, command, prose", () => {
		const text =
			"db: the data is not copied. Dump it on the Coolify host (docker exec abc pg_dump -U postgres -Fc app > db.dump), then run: nebulactrl databases restore db --file db.dump (see docs/operations/database-backups.md#restore-from-a-dump-file).";
		expect(followupSegments(text)).toEqual([
			{
				kind: "text",
				text: "db: the data is not copied. Dump it on the Coolify host",
			},
			{
				kind: "code",
				text: "docker exec abc pg_dump -U postgres -Fc app > db.dump",
			},
			{ kind: "text", text: "then run:" },
			{ kind: "code", text: "nebulactrl databases restore db --file db.dump" },
			{
				kind: "text",
				text: "(see docs/operations/database-backups.md#restore-from-a-dump-file).",
			},
		]);
	});

	test("a trailing command", () => {
		expect(
			followupSegments(
				"Review every service, then deploy from the staged bar or with nebulactrl services deploy <service> --wait.",
			),
		).toEqual([
			{
				kind: "text",
				text: "Review every service, then deploy from the staged bar or with",
			},
			{ kind: "code", text: "nebulactrl services deploy <service> --wait" },
			{ kind: "text", text: "." },
		]);
	});

	test("a for-example command", () => {
		const segments = followupSegments(
			"web: the data of volume v (/data) is not copied. Copy it from Coolify's Docker volume before the cutover (for example docker run --rm -v <coolify volume>:/from -v <new volume>:/to alpine cp -a /from/. /to/).",
		);
		expect(segments.filter((s) => s.kind === "code")).toEqual([
			{
				kind: "code",
				text: "docker run --rm -v <coolify volume>:/from -v <new volume>:/to alpine cp -a /from/. /to/",
			},
		]);
		expect(segments[0]?.text).toContain("before the cutover");
	});

	test("prose alone is one text segment", () => {
		const text =
			"DNS still points at Coolify: nothing is routed until you move the records.";
		expect(followupSegments(text)).toEqual([{ kind: "text", text }]);
		expect(followupSegments("")).toEqual([]);
	});
});

describe("unrestrictedNeeds", () => {
	const web = resource({
		uuid: "web",
		name: "web",
		needsUnrestricted: ["privileged", "devices"],
	});
	const gpu = resource({
		uuid: "gpu",
		name: "gpu",
		needsUnrestricted: ["gpus"],
		include: false,
	});
	const p = plan([project({ uuid: "p1" }, [web, gpu])]);

	test("lists included resources only", () => {
		expect(unrestrictedNeeds(p)).toEqual(["web: privileged, devices"]);
	});
	test("follows the reader's include and skip choices", () => {
		expect(
			unrestrictedNeeds(
				p,
				patchResource(EMPTY_DRAFT, "gpu", { include: true }),
			),
		).toEqual(["web: privileged, devices", "gpu: gpus"]);
		expect(
			unrestrictedNeeds(p, patchProject(EMPTY_DRAFT, "p1", { skip: true })),
		).toEqual([]);
	});
});
