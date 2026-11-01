import { describe, expect, test } from "bun:test";
import type { LogDrainServerStatus, LogDrainView } from "#/api/gen/types.gen";
import {
	applyPreset,
	CENTRAL_LOGS_LINE,
	CENTRAL_RETENTION_DAYS,
	createBody,
	credentialFields,
	type Draft,
	deliveryParts,
	draftFor,
	draftProblem,
	emptyDraft,
	HTTP_PRESETS,
	kindLabel,
	lastErrorOf,
	mapFromRows,
	NEVER_REPORTED,
	newRow,
	orderDrains,
	parseBufferMb,
	patchBody,
	rowsFromMap,
	scopeSummary,
	secretsBody,
	serverState,
	targetLabel,
	testFailure,
	testOutcome,
	toggleToast,
	validAddress,
	visibleServiceIds,
} from "./model";

function drain(extra: Partial<LogDrainView> = {}): LogDrainView {
	const kind = extra.kind ?? "loki";
	return {
		id: "d1",
		name: "Grafana",
		kind,
		enabled: true,
		builtin: false,
		...(kind === "loki"
			? {
					loki: {
						url: "https://loki.example.com/loki/api/v1/push",
						tenantId: "t1",
					},
				}
			: {}),
		secrets: { username: "al••", password: "••••" },
		headerNames: [],
		allProjects: true,
		projectIds: [],
		serviceIds: [],
		query: "",
		bufferMb: 256,
		test: { message: "" },
		servers: [],
		createdAt: "2026-10-20T10:00:00Z",
		updatedAt: "2026-10-20T10:00:00Z",
		...extra,
	};
}

function server(
	extra: Partial<LogDrainServerStatus> = {},
): LogDrainServerStatus {
	return {
		serverId: "s1",
		serverName: "hetzner-01",
		state: "ok",
		lastError: "",
		sentLines: 1204,
		droppedLines: 0,
		backlogBytes: 0,
		reportedAt: "2026-10-20T10:00:00Z",
		...extra,
	};
}

function filled(kind: Draft["kind"]): Draft {
	const d = { ...emptyDraft(), name: "My drain", kind };
	if (kind === "loki") d.lokiUrl = "https://loki.example.com/loki/api/v1/push";
	if (kind === "otlp") d.otlpUrl = "https://otel.example.com/v1/logs";
	if (kind === "http") d.httpUrl = "https://intake.example.com/logs";
	if (kind === "syslog") d.syslogAddress = "logs.example.com:6514";
	return d;
}

describe("kinds", () => {
	test("each kind has a label", () => {
		expect(kindLabel("otlp")).toBe("OpenTelemetry (OTLP/HTTP)");
		expect(kindLabel("http")).toBe("HTTP JSON");
	});

	test("syslog takes no credentials, otlp no basic auth", () => {
		expect(credentialFields("syslog")).toEqual({
			basic: false,
			token: false,
			headers: false,
		});
		expect(credentialFields("otlp").basic).toBe(false);
		expect(credentialFields("loki")).toEqual({
			basic: true,
			token: true,
			headers: true,
		});
	});
});

describe("rows", () => {
	test("blank rows are dropped and a later row wins", () => {
		expect(
			mapFromRows([
				newRow("a", "1"),
				newRow("  ", "x"),
				newRow(" b ", "2"),
				newRow("a", "3"),
			]),
		).toEqual({ a: "3", b: "2" });
	});

	test("a map becomes rows with distinct keys", () => {
		const rows = rowsFromMap({ a: "1", b: "2" });
		expect(rows.map((r) => [r.name, r.value])).toEqual([
			["a", "1"],
			["b", "2"],
		]);
		expect(new Set(rows.map((r) => r.id)).size).toBe(2);
		expect(rowsFromMap(undefined)).toEqual([]);
	});
});

describe("presets", () => {
	test("Datadog fills the URL, the key header's name and the source field", () => {
		const preset = HTTP_PRESETS.find((p) => p.id === "datadog");
		expect(preset).toBeDefined();
		if (!preset) return;
		const next = applyPreset(emptyDraft(), preset);
		expect(next.kind).toBe("http");
		expect(next.name).toBe("Datadog");
		expect(next.httpUrl).toBe(
			"https://http-intake.logs.datadoghq.com/api/v2/logs",
		);
		expect(next.headers.map((r) => [r.name, r.value])).toEqual([
			["DD-API-KEY", ""],
		]);
		expect(mapFromRows(next.httpExtraFields)).toEqual({
			ddsource: "nebulactrl",
		});
		expect(next.token).toBe("");
	});

	test("a preset keeps a name that was already typed and never fills a secret", () => {
		for (const preset of HTTP_PRESETS) {
			const next = applyPreset(
				{ ...emptyDraft(), name: "Mine", token: "x" },
				preset,
			);
			expect(next.name).toBe("Mine");
			expect(next.headers.every((r) => r.value === "")).toBe(true);
		}
	});

	test("a preset whose URL still has a placeholder cannot be saved", () => {
		const axiom = HTTP_PRESETS.find((p) => p.id === "axiom");
		if (!axiom) throw new Error("no axiom preset");
		const draft = applyPreset(emptyDraft(), axiom);
		expect(draftProblem(draft, false)).toContain("placeholder");
		expect(
			draftProblem(
				{ ...draft, httpUrl: "https://api.axiom.co/v1/datasets/logs/ingest" },
				false,
			),
		).toBeNull();
	});
});

describe("validation", () => {
	test("a complete draft of every kind is fine", () => {
		for (const kind of ["loki", "otlp", "syslog", "http"] as const) {
			expect(draftProblem(filled(kind), false)).toBeNull();
		}
	});

	test("name, URL and address are required", () => {
		expect(draftProblem({ ...filled("loki"), name: " " }, false)).toBe(
			"Give the drain a name.",
		);
		expect(draftProblem({ ...filled("loki"), lokiUrl: "" }, false)).toBe(
			"Enter the push URL.",
		);
		expect(
			draftProblem({ ...filled("otlp"), otlpUrl: "ftp://x" }, false),
		).toContain("http://");
		expect(
			draftProblem(
				{ ...filled("syslog"), syslogAddress: "logs.example.com" },
				false,
			),
		).toContain("host:port");
	});

	test("addresses", () => {
		expect(validAddress("logs.example.com:6514")).toBe(true);
		expect(validAddress("192.0.2.2:514")).toBe(true);
		expect(validAddress("[::1]:514")).toBe(true);
		expect(validAddress("logs.example.com")).toBe(false);
		expect(validAddress("logs.example.com:0")).toBe(false);
		expect(validAddress("logs.example.com:70000")).toBe(false);
		expect(validAddress("http://logs.example.com:514")).toBe(false);
	});

	test("the syslog facility is 0 to 23", () => {
		expect(
			draftProblem({ ...filled("syslog"), syslogFacility: "24" }, false),
		).toContain("0 to 23");
		expect(
			draftProblem({ ...filled("syslog"), syslogFacility: "x" }, false),
		).toContain("0 to 23");
		expect(
			draftProblem({ ...filled("syslog"), syslogFacility: "16" }, false),
		).toBeNull();
	});

	test("a header being replaced needs a name and a value", () => {
		const base = filled("http");
		expect(
			draftProblem({ ...base, headers: [newRow("DD-API-KEY", "")] }, false),
		).toBe("The DD-API-KEY header needs a value.");
		expect(draftProblem({ ...base, headers: [newRow("", "v")] }, false)).toBe(
			"A header needs a name.",
		);
		expect(
			draftProblem({ ...base, headers: [newRow("bad name", "v")] }, false),
		).toContain("not a valid header name");
		expect(
			draftProblem(
				{ ...base, headers: [newRow(), newRow("X-Key", "v")] },
				false,
			),
		).toBeNull();
		// Kept headers are not checked: their values are not in the form.
		expect(
			draftProblem(
				{ ...base, headersMode: "keep", headers: [newRow("X", "")] },
				true,
			),
		).toBeNull();
	});

	test("basic auth comes as a pair when created", () => {
		const base = filled("loki");
		expect(draftProblem({ ...base, username: "al" }, false)).toBe(
			"A user name needs its password.",
		);
		expect(draftProblem({ ...base, password: "pw" }, false)).toBe(
			"A password needs its user name.",
		);
		expect(
			draftProblem({ ...base, username: "al", password: "pw" }, false),
		).toBeNull();
		expect(draftProblem({ ...base, username: "al" }, true)).toBeNull();
	});

	test("scope needs a project, the query must parse, the buffer must fit", () => {
		const base = filled("loki");
		expect(draftProblem({ ...base, allProjects: false }, false)).toBe(
			"Choose at least one project, or ship from all projects.",
		);
		expect(
			draftProblem({ ...base, allProjects: false, projectIds: ["p1"] }, false),
		).toBeNull();
		expect(draftProblem({ ...base, query: "level:error" }, false)).toBeNull();
		expect(
			draftProblem({ ...base, query: 'level:error "open' }, false),
		).toContain("The query has a mistake");
		expect(draftProblem({ ...base, bufferMb: "4" }, false)).toContain(
			"8 to 5120",
		);
		expect(draftProblem({ ...base, bufferMb: "" }, false)).toContain(
			"8 to 5120",
		);
		expect(parseBufferMb("256")).toBe(256);
		expect(parseBufferMb("5121")).toBeNull();
		expect(parseBufferMb("12.5")).toBeNull();
	});
});

describe("request bodies", () => {
	test("create sends the kind's settings, the credentials typed and the scope", () => {
		const draft: Draft = {
			...filled("loki"),
			lokiTenant: "t1",
			lokiLabels: [newRow("env", "prod")],
			username: "al",
			password: "pw",
			headers: [newRow("X-Org", "acme")],
			allProjects: false,
			projectIds: ["p1", "p2"],
			serviceIds: ["s1"],
			query: " level:error ",
			bufferMb: "64",
		};
		expect(createBody(draft)).toEqual({
			name: "My drain",
			kind: "loki",
			enabled: true,
			config: {
				loki: {
					url: "https://loki.example.com/loki/api/v1/push",
					tenantId: "t1",
					labels: { env: "prod" },
				},
			},
			secrets: { username: "al", password: "pw", headers: { "X-Org": "acme" } },
			allProjects: false,
			projectIds: ["p1", "p2"],
			serviceIds: ["s1"],
			query: "level:error",
			bufferMb: 64,
		});
	});

	test("all projects sends no project ids; blank optional settings are left out", () => {
		const body = createBody({ ...filled("otlp"), projectIds: ["p1"] });
		expect(body.projectIds).toEqual([]);
		expect(body.config).toEqual({
			otlp: { url: "https://otel.example.com/v1/logs" },
		});
		expect(body.secrets).toEqual({});
		expect(body.query).toBe("");
	});

	test("syslog sends its TLS options only for tcp+tls and the facility as a number", () => {
		const tls = createBody({
			...filled("syslog"),
			syslogAppName: "web",
			syslogFacility: "16",
			syslogTlsServerName: "logs.example.com",
			syslogTlsInsecure: true,
		});
		expect(tls.config).toEqual({
			syslog: {
				address: "logs.example.com:6514",
				protocol: "tcp+tls",
				appName: "web",
				facility: 16,
				tlsServerName: "logs.example.com",
				tlsInsecureSkipVerify: true,
			},
		});
		const udp = createBody({
			...filled("syslog"),
			syslogProtocol: "udp",
			syslogTlsInsecure: true,
		});
		expect(udp.config).toEqual({
			syslog: { address: "logs.example.com:6514", protocol: "udp" },
		});
		expect(udp.secrets).toEqual({});
	});

	test("http sends method, format and the fields it names", () => {
		const body = createBody({
			...filled("http"),
			httpMethod: "PUT",
			httpFormat: "ndjson",
			httpTimestampField: "dt",
			httpExtraFields: [newRow("ddsource", "nebulactrl")],
			token: " tok ",
		});
		expect(body.config).toEqual({
			http: {
				url: "https://intake.example.com/logs",
				method: "PUT",
				format: "ndjson",
				timestampField: "dt",
				extraFields: { ddsource: "nebulactrl" },
			},
		});
		expect(body.secrets).toEqual({ token: "tok" });
	});

	test("credentials the kind does not take are not sent", () => {
		expect(
			secretsBody({
				...filled("syslog"),
				token: "t",
				username: "u",
				password: "p",
				headers: [newRow("A", "b")],
			}),
		).toEqual({});
		expect(
			secretsBody({
				...filled("otlp"),
				username: "u",
				password: "p",
				token: "t",
			}),
		).toEqual({ token: "t" });
	});
});

describe("editing", () => {
	test("the draft of a stored drain has settings and scope but no credential", () => {
		const draft = draftFor(
			drain({
				allProjects: false,
				projectIds: ["p1"],
				serviceIds: ["s1", "s2"],
				query: "level:error",
				bufferMb: 64,
				loki: { url: "https://l/loki/api/v1/push", labels: { env: "prod" } },
			}),
		);
		expect(draft.name).toBe("Grafana");
		expect(draft.lokiUrl).toBe("https://l/loki/api/v1/push");
		expect(mapFromRows(draft.lokiLabels)).toEqual({ env: "prod" });
		expect(draft.allProjects).toBe(false);
		expect(draft.projectIds).toEqual(["p1"]);
		expect(draft.serviceIds).toEqual(["s1", "s2"]);
		expect(draft.bufferMb).toBe("64");
		expect([draft.token, draft.username, draft.password]).toEqual(["", "", ""]);
		expect(draft.clear).toEqual([]);
	});

	test("stored headers start as kept; none stored starts as an empty editor", () => {
		expect(draftFor(drain({ headerNames: ["DD-API-KEY"] })).headersMode).toBe(
			"keep",
		);
		expect(draftFor(drain({ headerNames: null })).headersMode).toBe("replace");
		expect(draftFor(null).headersMode).toBe("replace");
	});

	test("an untouched edit sends no credential, so the server keeps them all", () => {
		const draft = draftFor(drain({ headerNames: ["X-Org"] }));
		expect(patchBody(draft).secrets).toEqual({});
		expect(patchBody(draft)).not.toHaveProperty("kind");
	});

	test("typing replaces one credential, clear removes another", () => {
		const draft = {
			...draftFor(drain()),
			password: "new",
			clear: ["username" as const],
		};
		expect(patchBody(draft).secrets).toEqual({
			password: "new",
			clear: ["username"],
		});
	});

	test("typing into a field wins over a clear flag on it", () => {
		const draft = {
			...draftFor(drain()),
			token: "t",
			clear: ["token" as const],
		};
		expect(patchBody(draft).secrets).toEqual({ token: "t" });
	});

	test("headers are replaced as a whole or removed", () => {
		const replaced = {
			...draftFor(drain({ headerNames: ["A", "B"] })),
			headersMode: "replace" as const,
			headers: [newRow("A", "1")],
		};
		expect(patchBody(replaced).secrets).toEqual({ headers: { A: "1" } });
		const removed = {
			...draftFor(drain({ headerNames: ["A"] })),
			headersMode: "clear" as const,
		};
		expect(patchBody(removed).secrets).toEqual({ clear: ["headers"] });
	});

	test("an edit sends the whole settings of the kind so a removed field is cleared", () => {
		const draft = { ...draftFor(drain()), lokiTenant: "" };
		expect(patchBody(draft).config).toEqual({
			loki: { url: "https://loki.example.com/loki/api/v1/push" },
		});
	});

	test("an http drain keeps its method and format", () => {
		const draft = draftFor(
			drain({
				kind: "http",
				http: {
					url: "https://x/logs",
					method: "PUT",
					format: "ndjson",
					extraFields: { a: "b" },
				},
			}),
		);
		expect([draft.httpMethod, draft.httpFormat]).toEqual(["PUT", "ndjson"]);
		expect(mapFromRows(draft.httpExtraFields)).toEqual({ a: "b" });
	});
});

describe("target", () => {
	test("the host of the URL, or the syslog address", () => {
		expect(targetLabel(drain())).toBe("loki.example.com");
		expect(
			targetLabel(
				drain({
					kind: "syslog",
					syslog: { address: "logs.example.com:6514", protocol: "tcp+tls" },
				}),
			),
		).toBe("logs.example.com:6514");
		expect(
			targetLabel(drain({ kind: "http", http: { url: "not a url" } })),
		).toBe("not a url");
		expect(targetLabel(drain({ kind: "http" }))).toBe("");
	});
});

describe("scope summary", () => {
	test("all projects, some projects, some services", () => {
		const names = new Map([["p1", "Shop"]]);
		expect(
			scopeSummary({ allProjects: true, projectIds: [], serviceIds: [] }),
		).toBe("All projects");
		expect(
			scopeSummary({ allProjects: true, projectIds: null, serviceIds: null }),
		).toBe("All projects");
		expect(
			scopeSummary({
				allProjects: false,
				projectIds: ["p1", "p2"],
				serviceIds: [],
			}),
		).toBe("2 projects");
		expect(
			scopeSummary(
				{ allProjects: false, projectIds: ["p1"], serviceIds: [] },
				names,
			),
		).toBe("Shop");
		expect(
			scopeSummary(
				{ allProjects: false, projectIds: ["p9"], serviceIds: [] },
				names,
			),
		).toBe("1 project");
		expect(
			scopeSummary({
				allProjects: true,
				projectIds: [],
				serviceIds: ["a", "b", "c"],
			}),
		).toBe("3 services");
		expect(
			scopeSummary({ allProjects: true, projectIds: [], serviceIds: ["a"] }),
		).toBe("1 service");
		expect(
			scopeSummary({
				allProjects: false,
				projectIds: ["p1", "p2"],
				serviceIds: ["a", "b"],
			}),
		).toBe("2 services in 2 projects");
	});

	test("services outside the chosen projects drop out", () => {
		const services = [
			{ id: "a", projectId: "p1" },
			{ id: "b", projectId: "p2" },
		];
		expect(visibleServiceIds(["a", "b", "gone"], services, true, [])).toEqual([
			"a",
			"b",
		]);
		expect(visibleServiceIds(["a", "b"], services, false, ["p2"])).toEqual([
			"b",
		]);
	});
});

describe("health", () => {
	test("each state is an icon kind and a word", () => {
		expect(serverState({ state: "ok" })).toEqual({
			kind: "live",
			label: "Delivering",
		});
		expect(serverState({ state: "retrying" })).toEqual({
			kind: "degraded",
			label: "Retrying",
		});
		expect(serverState({ state: "failing" })).toEqual({
			kind: "failed",
			label: "Failing",
		});
		expect(serverState({ state: "disabled" })).toEqual({
			kind: "stopped",
			label: "Disabled",
		});
		expect(NEVER_REPORTED).toEqual({
			kind: "unknown",
			label: "Waiting for a server",
		});
	});

	test("counts and backlog", () => {
		expect(
			deliveryParts(
				server({
					sentLines: 1204,
					droppedLines: 3,
					backlogBytes: 2 * 1024 * 1024,
				}),
			),
		).toEqual(["1,204 sent", "3 dropped", "2 MiB waiting"]);
	});

	test("the last error shows only while a server is not delivering", () => {
		expect(lastErrorOf(server({ state: "ok", lastError: "old" }))).toBe("");
		expect(
			lastErrorOf(
				server({ state: "failing", lastError: " 401 unauthorized " }),
			),
		).toBe("401 unauthorized");
	});
});

describe("test result", () => {
	test("success names the server and the status code", () => {
		expect(
			testOutcome({
				ok: true,
				message: "accepted",
				serverId: "s1",
				serverName: "hetzner-01",
				statusCode: 204,
			}),
		).toEqual({
			tone: "success",
			title: "Test line delivered",
			detail: "From hetzner-01 · HTTP 204 · accepted",
		});
	});

	test("failure keeps the receiver's message", () => {
		expect(
			testOutcome({
				ok: false,
				message: "unauthorized",
				serverId: "s1",
				serverName: "hetzner-01",
				statusCode: 401,
			}),
		).toEqual({
			tone: "danger",
			title: "Test line failed",
			detail: "From hetzner-01 · HTTP 401 · unauthorized",
		});
		expect(
			testOutcome({ ok: false, message: "", serverId: "s1", serverName: "a" })
				.detail,
		).toBe("From a");
	});

	test("no connected server is a warning that says what to do", () => {
		const outcome = testFailure({ code: "no_online_server", message: "none" });
		expect(outcome.tone).toBe("warning");
		expect(outcome.title).toBe("No server is connected");
		expect(testFailure({ code: "x", message: "boom" })).toEqual({
			tone: "danger",
			title: "Could not send the test",
			detail: "boom",
		});
	});
});

describe("the built-in central logs drain", () => {
	test("it keeps logs for 7 days and says so in one line", () => {
		expect(CENTRAL_RETENTION_DAYS).toBe(7);
		expect(CENTRAL_LOGS_LINE).toBe("Built-in · kept 7 days");
	});

	test("it comes first, the team's own drains keep their order", () => {
		const ordered = orderDrains([
			drain({ id: "a", name: "A" }),
			drain({ id: "b", name: "Central", builtin: true }),
			drain({ id: "c", name: "C" }),
		]);
		expect(ordered.map((d) => d.id)).toEqual(["b", "a", "c"]);
	});

	test("its switch talks about central logs, the others about pausing", () => {
		const builtin = drain({ builtin: true, name: "Central logs" });
		expect(toggleToast(builtin, true).title).toBe("Central logs are on");
		expect(toggleToast(builtin, false).title).toBe("Central logs are off");
		expect(toggleToast(drain(), false)).toEqual({
			title: "Paused Grafana",
			body: "Nothing is shipped until you resume it.",
		});
		expect(toggleToast(drain(), true).title).toBe("Resumed Grafana");
	});
});
