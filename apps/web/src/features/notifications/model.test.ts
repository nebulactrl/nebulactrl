import { describe, expect, test } from "bun:test";
import type {
	ChannelEventType,
	ChannelRule,
	ChannelView,
} from "#/api/gen/types.gen";
import {
	activePreset,
	attemptsLabel,
	createBody,
	type Draft,
	draftFor,
	draftProblem,
	groupEventTypes,
	hintHost,
	parseRecipients,
	patchBody,
	presetTypes,
	ruleSummary,
	targetLabel,
	VERIFY_SNIPPETS,
	validFrom,
} from "./model";

const TYPES: ChannelEventType[] = [
	{
		type: "deploy.failed",
		label: "Deployment failed",
		description: "",
		group: "Deployments",
		severity: "error",
		defaultOn: true,
	},
	{
		type: "deploy.succeeded",
		label: "Deployment succeeded",
		description: "",
		group: "Deployments",
		severity: "info",
		defaultOn: false,
	},
	{
		type: "server.offline",
		label: "Server offline",
		description: "",
		group: "Servers",
		severity: "error",
		defaultOn: true,
	},
];

function channel(over: Partial<ChannelView> = {}): ChannelView {
	return {
		id: "c1",
		name: "ops",
		kind: "slack",
		enabled: true,
		config: {},
		secrets: { url: "https://hooks.slack.com/••••••" },
		rule: {
			eventTypes: ["deploy.failed"],
			minSeverity: "info",
			allProjects: true,
			projectIds: [],
		},
		test: { message: "" },
		stats: { delivered24h: 0, failed24h: 0 },
		createdAt: "2026-10-26T10:00:00Z",
		updatedAt: "2026-10-26T10:00:00Z",
		...over,
	};
}

function draft(over: Partial<Draft> = {}): Draft {
	return {
		...draftFor(null, ["deploy.failed"]),
		name: "ops",
		url: "https://hooks.slack.com/services/a/b/c",
		...over,
	};
}

describe("event matrix helpers", () => {
	test("groups keep catalogue order", () => {
		const groups = groupEventTypes(TYPES);
		expect(groups.map((g) => g.group)).toEqual(["Deployments", "Servers"]);
		expect(groups[0]?.types.map((t) => t.type)).toEqual([
			"deploy.failed",
			"deploy.succeeded",
		]);
	});

	test("presets select failures and alerts, everything or nothing", () => {
		expect(presetTypes("alerts", TYPES)).toEqual([
			"deploy.failed",
			"server.offline",
		]);
		expect(presetTypes("all", TYPES)).toHaveLength(3);
		expect(presetTypes("none", TYPES)).toEqual([]);
	});

	test("the active preset is recognised regardless of order", () => {
		expect(activePreset(["server.offline", "deploy.failed"], TYPES)).toBe(
			"alerts",
		);
		expect(activePreset([], TYPES)).toBe("none");
		expect(activePreset(["deploy.failed"], TYPES)).toBeNull();
	});
});

describe("ruleSummary", () => {
	const rule = (over: Partial<ChannelRule>): ChannelRule => ({
		eventTypes: ["a", "b"],
		minSeverity: "info",
		allProjects: true,
		projectIds: [],
		...over,
	});
	const names = new Map([["p1", "Shop"]]);

	test("reads as words", () => {
		expect(ruleSummary(rule({}), names)).toBe(
			"2 events · every severity · whole team",
		);
		expect(
			ruleSummary(rule({ eventTypes: ["a"], minSeverity: "error" }), names),
		).toBe("1 event · errors only · whole team");
		expect(
			ruleSummary(
				rule({
					minSeverity: "warning",
					allProjects: false,
					projectIds: ["p1"],
				}),
				names,
			),
		).toBe("2 events · warnings and errors · Shop");
		expect(
			ruleSummary(
				rule({ allProjects: false, projectIds: ["p1", "p2"] }),
				names,
			),
		).toBe("2 events · every severity · 2 projects");
		expect(ruleSummary(rule({ eventTypes: [] }), names)).toBe(
			"no events · every severity · whole team",
		);
	});
});

describe("targets", () => {
	test("the masked hint still names the receiver's host", () => {
		expect(hintHost("https://hooks.slack.com/••••••")).toBe("hooks.slack.com");
		expect(hintHost(undefined)).toBeNull();
		expect(targetLabel(channel())).toBe("hooks.slack.com");
		expect(
			targetLabel(
				channel({
					kind: "telegram",
					config: { chatId: "-100123" },
					secrets: {},
				}),
			),
		).toBe("chat -100123");
		expect(
			targetLabel(
				channel({
					kind: "email",
					config: { to: ["a@x.io", "b@x.io", "c@x.io"] },
					secrets: {},
				}),
			),
		).toBe("a@x.io +2");
	});
});

describe("recipients and sender", () => {
	test("split on lines, commas and spaces; duplicates ignored", () => {
		expect(parseRecipients("a@x.io, b@x.io\nA@x.io;c@x.io  ")).toEqual([
			"a@x.io",
			"b@x.io",
			"c@x.io",
		]);
		expect(parseRecipients("  ")).toEqual([]);
	});

	test("a sender is an address, optionally with a display name", () => {
		expect(validFrom("alerts@example.com")).toBe(true);
		expect(validFrom("NebulaCtrl <alerts@example.com>")).toBe(true);
		expect(validFrom("alerts")).toBe(false);
		expect(validFrom("NebulaCtrl <alerts>")).toBe(false);
	});
});

describe("draftProblem", () => {
	test("a complete Slack draft is fine; the URL is required on create but not on edit", () => {
		expect(draftProblem(draft(), false)).toBeNull();
		expect(draftProblem(draft({ url: "" }), false)).toContain("webhook URL");
		expect(draftProblem(draft({ url: "" }), true)).toBeNull();
		expect(
			draftProblem(draft({ url: "http://hooks.slack.com/x" }), false),
		).toContain("https");
		expect(draftProblem(draft({ name: "  " }), false)).toContain("name");
	});

	test("email needs host, sender, recipients and a password with its user name", () => {
		const base = draft({
			kind: "email",
			host: "smtp.example.com",
			from: "alerts@example.com",
			to: "ops@example.com",
			url: "",
		});
		expect(draftProblem(base, false)).toBeNull();
		expect(draftProblem({ ...base, host: "" }, false)).toContain("host");
		expect(draftProblem({ ...base, from: "nope" }, false)).toContain("sender");
		expect(draftProblem({ ...base, to: "" }, false)).toContain("recipient");
		expect(
			draftProblem({ ...base, to: "ops@example.com, nope" }, false),
		).toContain("not an email");
		expect(draftProblem({ ...base, username: "mailer" }, false)).toContain(
			"password",
		);
		expect(draftProblem({ ...base, username: "mailer" }, true)).toBeNull();
		expect(draftProblem({ ...base, port: "25a" }, false)).toContain("port");
	});

	test("telegram needs a bot token on create and a chat id", () => {
		const base = draft({
			kind: "telegram",
			url: "",
			botToken: "123456:ABC",
			chatId: "-100123",
		});
		expect(draftProblem(base, false)).toBeNull();
		expect(draftProblem({ ...base, botToken: "" }, false)).toContain(
			"bot token",
		);
		expect(draftProblem({ ...base, botToken: "" }, true)).toBeNull();
		expect(draftProblem({ ...base, chatId: "" }, false)).toContain("chat");
	});

	test("webhook signing secrets are optional but not short", () => {
		const base = draft({ kind: "webhook", url: "http://localhost:9000/hook" });
		expect(draftProblem(base, false)).toBeNull();
		expect(draftProblem({ ...base, signingSecret: "short" }, false)).toContain(
			"16",
		);
		expect(
			draftProblem({ ...base, signingSecret: "x".repeat(20) }, false),
		).toBeNull();
	});

	test("a project-scoped rule needs a project", () => {
		expect(
			draftProblem(draft({ allProjects: false, projectIds: [] }), false),
		).toContain("project");
		expect(
			draftProblem(draft({ allProjects: false, projectIds: ["p1"] }), false),
		).toBeNull();
	});
});

describe("request bodies", () => {
	test("create sends the secret once, under its own key", () => {
		const body = createBody(draft({ kind: "slack" }));
		expect(body.kind).toBe("slack");
		expect(body.secrets).toEqual({
			url: "https://hooks.slack.com/services/a/b/c",
		});
		expect(body.config).toBeUndefined();
		expect(body.rule).toEqual({
			eventTypes: ["deploy.failed"],
			minSeverity: "info",
			allProjects: true,
			projectIds: [],
		});
	});

	test("email carries its settings in config and only the password in secrets", () => {
		const body = createBody(
			draft({
				kind: "email",
				url: "",
				host: " smtp.example.com ",
				port: "2525",
				tls: "implicit",
				username: "mailer",
				password: "pw",
				from: "alerts@example.com",
				to: "a@x.io\nb@x.io",
			}),
		);
		expect(body.config).toEqual({
			host: "smtp.example.com",
			port: 2525,
			tls: "implicit",
			username: "mailer",
			from: "alerts@example.com",
			to: ["a@x.io", "b@x.io"],
			insecureSkipVerify: false,
		});
		expect(body.secrets).toEqual({ password: "pw" });
	});

	test("an empty port is left to the server's default", () => {
		const body = createBody(
			draft({
				kind: "email",
				url: "",
				host: "h.io",
				from: "a@x.io",
				to: "b@x.io",
			}),
		);
		expect(body.config && "port" in body.config).toBe(false);
	});

	test("an edit leaves untouched secrets out so the server keeps them", () => {
		const d = draftFor(channel(), []);
		const body = patchBody({ ...d, name: "renamed" });
		expect(body.name).toBe("renamed");
		expect(body.secrets).toEqual({});
		expect(
			patchBody({ ...d, url: "https://hooks.slack.com/services/new" }).secrets,
		).toEqual({ url: "https://hooks.slack.com/services/new" });
	});

	test("a team-wide rule never sends project ids", () => {
		const body = createBody(draft({ allProjects: true, projectIds: ["p1"] }));
		expect(body.rule?.projectIds).toEqual([]);
	});

	test("an edit draft starts from the channel, with secrets empty", () => {
		const d = draftFor(
			channel({
				kind: "email",
				config: {
					host: "h",
					port: 587,
					tls: "starttls",
					username: "u",
					from: "a@x.io",
					to: ["b@x.io", "c@x.io"],
				},
			}),
			[],
		);
		expect(d.host).toBe("h");
		expect(d.port).toBe("587");
		expect(d.to).toBe("b@x.io\nc@x.io");
		expect(d.password).toBe("");
		expect(d.url).toBe("");
		expect(d.eventTypes).toEqual(["deploy.failed"]);
	});
});

describe("delivery log copy", () => {
	test("attempts read naturally", () => {
		expect(attemptsLabel({ attempts: 0, maxAttempts: 6 })).toBe(
			"not tried yet",
		);
		expect(attemptsLabel({ attempts: 1, maxAttempts: 6 })).toBe(
			"1 of 6 attempts",
		);
		expect(attemptsLabel({ attempts: 1, maxAttempts: 1 })).toBe(
			"1 of 1 attempt",
		);
	});
});

describe("verification snippets", () => {
	test("every language documents the same scheme", () => {
		for (const code of Object.values(VERIFY_SNIPPETS)) {
			expect(code).toContain("sha256");
			expect(code.toLowerCase()).toMatch(/tolerance/u);
		}
	});
});
