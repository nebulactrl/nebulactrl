import { describe, expect, test } from "bun:test";
import {
	applyOutcome,
	autoApplySwitch,
	changeRow,
	changesForField,
	configBanner,
	configChip,
	exportWarning,
	fieldMark,
	fileUrl,
	firstErrorLine,
	markTooltip,
	needsApproval,
	originLabel,
	type PlanChangeLike,
	type PlanLike,
	planFooter,
	planView,
	SECRET_MASK,
	stalePlan,
	summaryLine,
	withFileNotes,
	worstState,
} from "./model";

function change(overrides: Partial<PlanChangeLike> = {}): PlanChangeLike {
	return {
		path: "services.web.port",
		line: 14,
		field: "config.port",
		op: "set",
		applies: "staged",
		reason: "file_changed",
		from: "3000",
		to: "8080",
		masked: false,
		...overrides,
	};
}

function plan(overrides: Partial<PlanLike> = {}): PlanLike {
	return {
		planId: "pl_7d3c1f0a9b2e4c55e6a1d8f0b3c47a92",
		valid: true,
		source: {
			kind: "git",
			repo: "acme/shop",
			branch: "main",
			path: "nebula.toml",
			commit: "3f9c2a1deadbeef",
			bytes: 14538,
		},
		summary: {
			create: 0,
			update: 1,
			unchanged: 2,
			notInFile: 0,
			staged: 1,
			immediate: 0,
			deferred: 0,
			errors: 0,
			warnings: 0,
		},
		services: [
			{
				slug: "web",
				kind: "git",
				action: "update",
				changes: [change()],
			},
		],
		...overrides,
	};
}

describe("chips", () => {
	test("a newer file or a conflict is the amber chip", () => {
		expect(configChip(true, "file_changed")?.kind).toBe("differs");
		expect(configChip(true, "conflict")?.kind).toBe("differs");
		expect(configChip(true, "file_changed")?.label).toBe("nebula.toml differs");
	});

	test("an edit in the dashboard is said in words", () => {
		const chip = configChip(true, "drift");
		expect(chip?.kind).toBe("edited");
		expect(chip?.label).toBe("edited here");
	});

	test("in sync is the neutral chip; unmanaged and plan-only states carry none", () => {
		expect(configChip(true, "in_sync")?.label).toBe("from nebula.toml");
		expect(configChip(false, "in_sync")).toBeNull();
		expect(configChip(true, "new")).toBeNull();
		expect(configChip(true, "adopt")).toBeNull();
		expect(configChip(true, undefined)).toBeNull();
		expect(configChip(true, "constructor")).toBeNull();
	});

	test("the worst state wins, unknown ones are ignored", () => {
		expect(worstState(["in_sync", "drift", "file_changed"])).toBe("drift");
		expect(worstState(["in_sync", "conflict", "drift"])).toBe("conflict");
		expect(worstState(["new", "adopt"])).toBeUndefined();
		expect(worstState([])).toBeUndefined();
	});
});

describe("field marks", () => {
	const source = {
		managed: true,
		repo: "acme/shop",
		commit: "3f9c2a1deadbeef",
		fields: [
			{ field: "config.port", state: "in_sync" },
			{ field: "config.healthcheck.path", state: "drift" },
			{ field: "variables.NODE_ENV", state: "in_sync" },
			{ field: "variables.API_URL", state: "conflict" },
		],
	};

	test("an exact field is marked with its state", () => {
		expect(fieldMark(source, "config.port")).toEqual({
			state: "in_sync",
			drifted: false,
			fields: ["config.port"],
		});
		expect(fieldMark(source, "config.healthcheck.path")?.drifted).toBe(true);
	});

	test("a group takes the worst state under it", () => {
		expect(fieldMark(source, "variables")?.state).toBe("conflict");
		expect(fieldMark(source, "config.healthcheck")?.state).toBe("drift");
	});

	test("a prefix is a whole path segment, not a substring", () => {
		expect(fieldMark(source, "config.por")).toBeNull();
		expect(fieldMark(source, "config.healthcheck.pa")).toBeNull();
	});

	test("exact leaves the fields under it out; the ids say what Stop managing releases", () => {
		expect(fieldMark(source, "variables", { exact: true })).toBeNull();
		expect(fieldMark(source, "variables")?.fields).toEqual([
			"variables.NODE_ENV",
			"variables.API_URL",
		]);
	});

	test("a field the file does not own, or an unmanaged service, has no mark", () => {
		expect(fieldMark(source, "config.command")).toBeNull();
		expect(fieldMark({ ...source, managed: false }, "config.port")).toBeNull();
		expect(fieldMark(undefined, "config.port")).toBeNull();
	});

	test("the tooltip names where the file came from", () => {
		expect(markTooltip(source)).toContain("acme/shop@3f9c2a1");
		expect(markTooltip(source)).toContain("Editing here is allowed");
		expect(originLabel({ origin: "cli" })).toBe("applied with the CLI");
		expect(originLabel({ repo: "acme/shop" })).toBe("acme/shop");
	});
});

describe("banner", () => {
	test("differs names the commit and the services", () => {
		const banner = configBanner({
			mode: "derived",
			status: "differs",
			commit: "3f9c2a1deadbeef",
			services: [
				{ slug: "web", state: "file_changed" },
				{ slug: "worker", state: "drift" },
				{ slug: "db", state: "in_sync" },
				{ slug: "queue", state: "new" },
			],
		});
		expect(banner?.kind).toBe("differs");
		expect(banner?.title).toBe("nebula.toml @3f9c2a1 differs — 3 services");
		expect(banner?.count).toBe(3);
	});

	test("one service is singular, none leaves the count off", () => {
		expect(
			configBanner({
				mode: "explicit",
				status: "differs",
				commit: "abcdef0123",
				services: [{ slug: "web", state: "conflict" }],
			})?.title,
		).toBe("nebula.toml @abcdef0 differs — 1 service");
		expect(
			configBanner({ mode: "explicit", status: "differs", services: [] })
				?.title,
		).toBe("nebula.toml differs");
	});

	test("invalid carries the first error and its line", () => {
		const banner = configBanner({
			mode: "derived",
			status: "invalid",
			commit: "3f9c2a1deadbeef",
			statusDetail:
				'nebula.toml:19:1: error[unknown_key] services.web.healthcheck: unknown key "healthcheck"\nnebula.toml:40:3: error[wrong_type]',
		});
		expect(banner?.kind).toBe("invalid");
		expect(banner?.line).toBe(19);
		expect(banner?.detail).not.toContain("wrong_type");
	});

	test("nothing speaks for in sync, no file, unreachable or no binding", () => {
		for (const status of [
			"in_sync",
			"not_found",
			"unreachable",
			"error",
			"unknown",
		]) {
			expect(configBanner({ mode: "derived", status })).toBeNull();
		}
		expect(configBanner({ mode: "none", status: "differs" })).toBeNull();
		expect(configBanner(undefined)).toBeNull();
	});

	test("the line is read from the report, not from any number in it", () => {
		expect(firstErrorLine("config/nebula.toml:7:2: error[x]")).toBe(7);
		expect(firstErrorLine("unknown key at 12:4")).toBeUndefined();
		expect(firstErrorLine("nebula.toml:0: bad")).toBeUndefined();
		expect(firstErrorLine(undefined)).toBeUndefined();
	});
});

describe("file links", () => {
	const base = {
		repo: "acme/shop",
		ref: "3f9c2a1deadbeef",
		path: "nebula.toml",
		line: 19,
	};

	test("each provider anchors the line its own way", () => {
		expect(fileUrl({ provider: "github", ...base })).toBe(
			"https://github.com/acme/shop/blob/3f9c2a1deadbeef/nebula.toml#L19",
		);
		expect(fileUrl({ provider: "gitlab", ...base })).toBe(
			"https://gitlab.com/acme/shop/-/blob/3f9c2a1deadbeef/nebula.toml#L19",
		);
		expect(
			fileUrl({
				provider: "forgejo",
				baseUrl: "https://git.acme.com/",
				...base,
			}),
		).toBe(
			"https://git.acme.com/acme/shop/src/commit/3f9c2a1deadbeef/nebula.toml#L19",
		);
		expect(fileUrl({ provider: "bitbucket", ...base })).toBe(
			"https://bitbucket.org/acme/shop/src/3f9c2a1deadbeef/nebula.toml#lines-19",
		);
	});

	test("no line, no anchor; a path is encoded per segment", () => {
		expect(
			fileUrl({
				provider: "github",
				...base,
				line: undefined,
				path: "deploy/a b/nebula.toml",
			}),
		).toBe(
			"https://github.com/acme/shop/blob/3f9c2a1deadbeef/deploy/a%20b/nebula.toml",
		);
	});

	test("a self-hosted provider without an address has no link", () => {
		expect(fileUrl({ provider: "gitea", ...base })).toBeUndefined();
		expect(fileUrl({ provider: "github", ...base, ref: "" })).toBeUndefined();
	});
});

describe("plan rows", () => {
	test("a set change reads from → to, with its pills", () => {
		const row = changeRow(change(), "web");
		expect(row.sign).toBe("~");
		expect(row.label).toBe("port");
		expect(row.value).toBe("3000 → 8080");
		expect(row.applies).toBe("staged");
		expect(row.reason).toBe("file changed");
		expect(row.line).toBe(14);
	});

	test("immediate and deferred changes say so", () => {
		expect(changeRow(change({ applies: "immediate" })).applies).toBe(
			"applies at once",
		);
		expect(changeRow(change({ applies: "deferred" })).applies).toBe("deferred");
	});

	test("reasons: the dashboard edit, both changed, new", () => {
		expect(changeRow(change({ reason: "drift" })).reason).toBe(
			"edited in dashboard",
		);
		expect(changeRow(change({ reason: "conflict" })).reason).toBe(
			"both changed",
		);
		expect(changeRow(change({ reason: "new" })).reason).toBeNull();
	});

	test("adds show the new value, removes the old one", () => {
		const add = changeRow(
			change({
				op: "add",
				from: "",
				to: "production",
				path: "services.web.variables.NODE_ENV",
				field: "variables.NODE_ENV",
				reason: "new",
			}),
			"web",
		);
		expect(add.sign).toBe("+");
		expect(add.value).toBe("production");
		expect(add.label).toBe("variables.NODE_ENV");
		const remove = changeRow(change({ op: "remove", from: "x", to: "" }));
		expect(remove.sign).toBe("−");
		expect(remove.value).toBe("x");
	});

	test("a secret never shows a value, a generator shows its description", () => {
		const secret = changeRow(
			change({ masked: true, from: "hunter2", to: "correct-horse" }),
		);
		expect(secret.value).toBe(`${SECRET_MASK} → ${SECRET_MASK}`);
		expect(secret.value).not.toContain("hunter2");
		expect(secret.value).not.toContain("correct-horse");
		const generated = changeRow(
			change({
				masked: true,
				op: "add",
				from: "",
				to: "generated(secret(48))",
			}),
		);
		expect(generated.value).toBe("generated(secret(48))");
		const empty = changeRow(
			change({ masked: true, op: "add", from: "", to: "" }),
		);
		expect(empty.value).toBe("");
	});

	test("a path outside the service keeps its full name", () => {
		expect(changeRow(change({ path: "variables.APP_NAME" }), "web").label).toBe(
			"variables.APP_NAME",
		);
	});
});

describe("plan view", () => {
	test("groups by what the file does to each service", () => {
		const view = planView(
			plan({
				summary: {
					create: 1,
					update: 1,
					unchanged: 1,
					notInFile: 1,
					staged: 3,
					immediate: 1,
					deferred: 0,
					errors: 0,
					warnings: 1,
					requiresDeploy: ["web"],
				},
				diagnostics: [
					{
						severity: "warning",
						code: "secret_missing_value",
						message: "STRIPE has no value yet",
					},
				],
				services: [
					{ slug: "queue", kind: "image", action: "create", changes: [] },
					{
						slug: "web",
						kind: "git",
						action: "update",
						changes: [
							change(),
							change({ op: "keep", path: "services.web.variables.OLD" }),
							change({ op: "release", path: "services.web.tasks.old" }),
						],
						released: ["services.web.tasks.report"],
					},
					{ slug: "worker", kind: "git", action: "unchanged" },
					{
						slug: "legacy",
						kind: "image",
						action: "not_in_file",
						managed: true,
					},
				],
			}),
		);
		expect(view.creates.map((s) => s.slug)).toEqual(["queue"]);
		expect(view.updates.map((s) => s.slug)).toEqual(["web"]);
		expect(view.notInFile.map((s) => s.slug)).toEqual(["legacy"]);
		expect(view.released).toEqual([
			"services.web.tasks.report",
			"services.web.tasks.old",
		]);
		expect(view.warnings).toHaveLength(1);
		expect(view.blockers).toHaveLength(0);
		expect(view.requiresDeploy).toEqual(["web"]);
		expect(view.canStage).toBe(true);
	});

	test("an error blocks staging even when the plan says valid", () => {
		const view = planView(
			plan({
				diagnostics: [
					{ severity: "error", code: "hostname_in_use", message: "in use" },
				],
			}),
		);
		expect(view.blockers).toHaveLength(1);
		expect(view.canStage).toBe(false);
	});

	test("an invalid plan cannot be staged", () => {
		expect(planView(plan({ valid: false })).canStage).toBe(false);
	});

	test("nothing to write cannot be staged", () => {
		const view = planView(
			plan({
				services: [{ slug: "web", kind: "git", action: "unchanged" }],
			}),
		);
		expect(view.hasChanges).toBe(false);
		expect(view.canStage).toBe(false);
	});

	test("shared variable changes count as changes", () => {
		const view = planView(
			plan({
				services: [],
				shared: {
					project: [
						change({ path: "variables.APP_NAME", op: "add", from: "" }),
					],
					environment: [],
				},
			}),
		);
		expect(view.shared.map((s) => s.scope)).toEqual(["project"]);
		expect(view.canStage).toBe(true);
	});

	test("tolerates null lists from the wire", () => {
		const view = planView(
			plan({ diagnostics: null, services: null, shared: null }),
		);
		expect(view.hasChanges).toBe(false);
		expect(view.blockers).toEqual([]);
	});

	test("the summary line and the footer", () => {
		expect(summaryLine(plan())).toBe("1 update, 2 unchanged · 1 staged");
		expect(
			summaryLine(
				plan({
					summary: {
						create: 0,
						update: 0,
						unchanged: 0,
						notInFile: 0,
						staged: 0,
						immediate: 0,
						deferred: 0,
						errors: 0,
						warnings: 0,
					},
				}),
			),
		).toBe("Nothing to change");
		const footer = planFooter(plan());
		expect(footer[0]).toBe("acme/shop@3f9c2a1");
		expect(footer).toContain("nebula.toml");
		expect(footer).toContain("14.2 KiB");
		expect(footer.at(-1)).toBe("pl_7d3c1f0a…");
	});

	test("production approval is read from the guard", () => {
		expect(
			needsApproval(plan({ guard: { productionApproval: "required" } })),
		).toBe(true);
		expect(
			needsApproval(plan({ guard: { productionApproval: "not_required" } })),
		).toBe(false);
		expect(needsApproval(plan())).toBe(false);
	});
});

describe("secret flag marks", () => {
	test("a declared secret marks its variable row", () => {
		const source = {
			managed: true,
			fields: [{ field: "variables.STRIPE_KEY#secret", state: "in_sync" }],
		};
		expect(fieldMark(source, "variables.STRIPE_KEY")?.state).toBe("in_sync");
		expect(fieldMark(source, "variables.STRIPE")).toBeNull();
	});
});

describe("staged line notes", () => {
	const source = {
		managed: true,
		fields: [
			{ field: "config.port", state: "in_sync" },
			{ field: "variables.NODE_ENV", state: "drift" },
		],
	};

	test("a line the file owns says so; the others stay as they were", () => {
		const lines = [
			{ field: "config.port" },
			{ field: "config.command" },
			{ field: "variables.NODE_ENV", note: "secret" },
		];
		expect(withFileNotes(source, lines)).toEqual([
			{ field: "config.port", note: "set by nebula.toml" },
			{ field: "config.command" },
			{ field: "variables.NODE_ENV", note: "secret · set by nebula.toml" },
		]);
	});

	test("an unmanaged service, or none yet, leaves the lines alone", () => {
		const lines = [{ field: "config.port" }];
		expect(withFileNotes({ ...source, managed: false }, lines)).toEqual(lines);
		expect(withFileNotes(undefined, lines)).toEqual(lines);
	});
});

describe("revert to file", () => {
	const withTwo = plan({
		services: [
			{
				slug: "web",
				kind: "git",
				action: "update",
				changes: [
					change(),
					change({
						field: "config.command",
						path: "services.web.command",
						op: "release",
					}),
				],
			},
			{
				slug: "worker",
				kind: "git",
				action: "update",
				changes: [change({ path: "services.worker.port" })],
			},
		],
	});

	test("finds the changes of this service, by field", () => {
		expect(
			changesForField(withTwo, "web", "config.port").map((c) => c.path),
		).toEqual(["services.web.port"]);
		expect(
			changesForField(withTwo, "worker", "config.port").map((c) => c.path),
		).toEqual(["services.worker.port"]);
	});

	test("a group takes every field under it; exact does not", () => {
		const grouped = plan({
			services: [
				{
					slug: "web",
					kind: "git",
					action: "update",
					changes: [
						change({
							field: "scale.replicas",
							path: "services.web.scale.replicas",
						}),
						change({
							field: "scale.autoscaling.min",
							path: "services.web.scale.autoscaling.min",
						}),
						change({ field: "scales", path: "services.web.scales" }),
					],
				},
			],
		});
		expect(changesForField(grouped, "web", "scale")).toHaveLength(2);
		expect(changesForField(grouped, "web", "scale", true)).toHaveLength(0);
	});

	test("a field the file agrees with, or a service the plan lacks, has none", () => {
		expect(changesForField(withTwo, "web", "config.memoryMb")).toEqual([]);
		expect(changesForField(withTwo, "web", "config.command")).toEqual([]);
		expect(changesForField(withTwo, "db", "config.port")).toEqual([]);
		expect(
			changesForField(plan({ services: null }), "web", "config.port"),
		).toEqual([]);
	});
});

describe("apply outcome", () => {
	test("staged says what was written and that nothing deployed", () => {
		const outcome = applyOutcome({
			status: "staged",
			steps: [{ step: "config:web", status: "applied" }],
			plan: plan({
				summary: { ...plan().summary, staged: 4, immediate: 1 },
			}),
		});
		expect(outcome.tone).toBe("success");
		expect(outcome.detail).toContain("4 staged, 1 applied at once");
		expect(outcome.detail).toContain("Nothing is deployed yet");
	});

	test("a deploy is named", () => {
		const outcome = applyOutcome({
			status: "staged",
			deployments: ["d1", "d2"],
		});
		expect(outcome.title).toBe("Staged and deploying");
		expect(outcome.detail).toContain("Deploying 2 services");
	});

	test("partial lists the steps that failed", () => {
		const outcome = applyOutcome({
			status: "partial",
			steps: [
				{ step: "config:web", status: "applied" },
				{ step: "tasks:web:digest", status: "failed", message: "bad cron" },
				{ step: "uptime:web", status: "deferred" },
			],
		});
		expect(outcome.tone).toBe("warning");
		expect(outcome.failed.map((s) => s.step)).toEqual(["tasks:web:digest"]);
		expect(outcome.deferred.map((s) => s.step)).toEqual(["uptime:web"]);
		expect(outcome.detail).toContain("1 step failed");
	});

	test("noop and failed", () => {
		expect(applyOutcome({ status: "noop" }).tone).toBe("info");
		expect(applyOutcome({ status: "failed" }).tone).toBe("error");
	});
});

describe("stale plan", () => {
	test("the fresh plan comes out of the error details", () => {
		const fresh = plan({ planId: "pl_fresh" });
		expect(stalePlan({ plan: fresh })?.planId).toBe("pl_fresh");
	});

	test("anything else is no plan", () => {
		expect(stalePlan(undefined)).toBeUndefined();
		expect(stalePlan({})).toBeUndefined();
		expect(stalePlan({ plan: "x" })).toBeUndefined();
		expect(stalePlan({ plan: null })).toBeUndefined();
		expect(stalePlan({ plan: { planId: "pl_x" } })).toBeUndefined();
	});
});

describe("export warnings", () => {
	test("a diagnostic or a sentence", () => {
		expect(
			exportWarning({ code: "secret_flag_differs", message: "mark it secret" }),
		).toEqual({ code: "secret_flag_differs", message: "mark it secret" });
		expect(exportWarning("not representable: tailnet")).toEqual({
			code: "",
			message: "not representable: tailnet",
		});
		expect(exportWarning(42)).toEqual({ code: "", message: "" });
	});
});

describe("auto-apply switch", () => {
	test("production never auto-applies", () => {
		const s = autoApplySwitch({ isProduction: true, bound: true });
		expect(s.disabled).toBe(true);
		expect(s.reason).toContain("production");
	});

	test("needs a linked repository", () => {
		expect(
			autoApplySwitch({ isProduction: false, bound: false }).disabled,
		).toBe(true);
	});

	test("otherwise it is a free switch", () => {
		expect(autoApplySwitch({ isProduction: false, bound: true })).toEqual({
			disabled: false,
			reason: undefined,
		});
	});
});
