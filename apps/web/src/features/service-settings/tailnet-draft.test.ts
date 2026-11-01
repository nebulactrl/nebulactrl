import { describe, expect, test } from "bun:test";
import type { TailnetConfig } from "#/api/gen/types.gen";
import {
	buildTailnetConfig,
	defaultTailnetHostname,
	emptyTailnetDraft,
	hostnameProblem,
	normalizeTags,
	type TailnetContext,
	type TailnetDraft,
	type TailnetPortDraft,
	TCP_PRESETS,
	tagPolicy,
	tagsNotAllowed,
	toTailnetDraft,
	validateTailnet,
	validateTcpRows,
	withTcpPreset,
} from "./tailnet-draft";

const context: TailnetContext = { port: "8080", oneshot: false };

const draft = (overrides: Partial<TailnetDraft> = {}): TailnetDraft => ({
	...emptyTailnetDraft(true),
	...overrides,
});

const row = (listen: string, target = "", tls = false): TailnetPortDraft => ({
	listen,
	target,
	tls,
});

describe("round trip", () => {
	test("a stored config comes back unchanged through draft and build", () => {
		const config: TailnetConfig = {
			enabled: true,
			hostname: "web-prod",
			extraTags: ["tag:a", "tag:b"],
			web: true,
			tcp: [
				{ listen: 5432, target: 5433 },
				{ listen: 6379, target: 6379, tls: true },
			],
			funnel: true,
			hidePublic: true,
		};
		expect(buildTailnetConfig(toTailnetDraft(config))).toEqual(config);
	});

	test("a minimal config stays minimal", () => {
		const config: TailnetConfig = { enabled: true, web: true };
		expect(buildTailnetConfig(toTailnetDraft(config))).toEqual(config);
	});

	test("no config, or a disabled one, is not on a tailnet", () => {
		expect(toTailnetDraft(undefined)).toBeNull();
		expect(toTailnetDraft({ enabled: false, web: true })).toBeNull();
	});
});

describe("normalisation", () => {
	test("hostname is trimmed and lower-cased", () => {
		expect(
			buildTailnetConfig(draft({ hostname: "  Web-Prod " }))?.hostname,
		).toBe("web-prod");
	});

	test("an empty hostname is omitted so the default applies", () => {
		expect(buildTailnetConfig(draft({ hostname: "  " }))).not.toHaveProperty(
			"hostname",
		);
	});

	test("tags are trimmed, unique and sorted", () => {
		expect(normalizeTags(["tag:b", " tag:a", "tag:b", ""])).toEqual([
			"tag:a",
			"tag:b",
		]);
		expect(
			buildTailnetConfig(draft({ extraTags: ["tag:b", "tag:a", "tag:b"] }))
				?.extraTags,
		).toEqual(["tag:a", "tag:b"]);
		expect(buildTailnetConfig(draft({ extraTags: [""] }))).not.toHaveProperty(
			"extraTags",
		);
	});

	test("ports are sorted by listen port and the target defaults to listen", () => {
		const config = buildTailnetConfig(
			draft({
				web: false,
				tcp: [row("6379"), row("5432", "5433"), row("3306", " ")],
			}),
		);
		expect(config?.tcp).toEqual([
			{ listen: 3306, target: 3306 },
			{ listen: 5432, target: 5433 },
			{ listen: 6379, target: 6379 },
		]);
	});

	test("false flags are omitted", () => {
		expect(buildTailnetConfig(draft())).toEqual({ enabled: true, web: true });
	});
});

describe("disabled collapses to omitted", () => {
	test("null and disabled drafts build nothing", () => {
		expect(buildTailnetConfig(null)).toBeUndefined();
		expect(
			buildTailnetConfig(draft({ enabled: false, hostname: "kept" })),
		).toBeUndefined();
	});

	test("and are never invalid", () => {
		expect(validateTailnet(null, context)).toEqual({});
		expect(
			validateTailnet(
				draft({ enabled: false, hostname: "Not_Valid", web: false }),
				{ port: "", oneshot: true },
			),
		).toEqual({});
	});
});

describe("validation", () => {
	test("a good draft passes", () => {
		expect(validateTailnet(draft(), context)).toEqual({});
		expect(
			validateTailnet(
				draft({
					hostname: "db",
					extraTags: ["tag:db"],
					web: false,
					tcp: [row("5432")],
				}),
				{ port: "", oneshot: false },
			),
		).toEqual({});
	});

	test("hostname is a lower-case RFC 1123 label of at most 63 characters", () => {
		expect(hostnameProblem("")).toBeUndefined();
		expect(hostnameProblem("Web-Prod")).toBeUndefined();
		expect(hostnameProblem("a".repeat(63))).toBeUndefined();
		expect(hostnameProblem("a".repeat(64))).toBeDefined();
		expect(hostnameProblem("web_prod")).toBeDefined();
		expect(hostnameProblem("-web")).toBeDefined();
		expect(hostnameProblem("web-")).toBeDefined();
		expect(hostnameProblem("web.prod")).toBeDefined();
		expect(
			validateTailnet(draft({ hostname: "Not_Valid" }), context)
				.tailnetHostname,
		).toBeDefined();
	});

	test("extra tags match tag:name and are limited to ten", () => {
		expect(
			validateTailnet(draft({ extraTags: ["nebula"] }), context).tailnetTags,
		).toBeDefined();
		expect(
			validateTailnet(draft({ extraTags: ["tag:1abc"] }), context).tailnetTags,
		).toBeDefined();
		expect(
			validateTailnet(draft({ extraTags: ["tag:db", "tag:A-1"] }), context)
				.tailnetTags,
		).toBeUndefined();
		const eleven = Array.from({ length: 11 }, (_, i) => `tag:t${i}`);
		expect(
			validateTailnet(draft({ extraTags: eleven }), context).tailnetTags,
		).toBeDefined();
		expect(
			validateTailnet(draft({ extraTags: eleven.slice(0, 10) }), context)
				.tailnetTags,
		).toBeUndefined();
	});

	test("something must be exposed", () => {
		expect(
			validateTailnet(draft({ web: false }), context).tailnetWeb,
		).toBeDefined();
		expect(
			validateTailnet(draft({ web: false, tcp: [row("5432")] }), context)
				.tailnetWeb,
		).toBeUndefined();
	});

	test("the web exposure needs an HTTP port", () => {
		expect(
			validateTailnet(draft(), { port: "", oneshot: false }).tailnetWeb,
		).toBeDefined();
		expect(
			validateTailnet(draft(), { port: "0", oneshot: false }).tailnetWeb,
		).toBeDefined();
	});

	test("funnel needs the web exposure", () => {
		expect(
			validateTailnet(
				draft({ web: false, funnel: true, tcp: [row("5432")] }),
				context,
			).tailnetFunnel,
		).toBeDefined();
		expect(
			validateTailnet(draft({ funnel: true }), context).tailnetFunnel,
		).toBeUndefined();
	});

	test("tcp ports are in range, whole numbers and unique", () => {
		const tcp = (rows: TailnetPortDraft[]) =>
			validateTailnet(draft({ web: false, tcp: rows }), context).tailnetTcp;
		expect(tcp([row("0")])).toBeDefined();
		expect(tcp([row("65536")])).toBeDefined();
		expect(tcp([row("65535")])).toBeUndefined();
		expect(tcp([row("12a")])).toBeDefined();
		expect(tcp([row("")])).toBeDefined();
		expect(tcp([row("5432", "0")])).toBeDefined();
		expect(tcp([row("5432", "70000")])).toBeDefined();
		expect(tcp([row("5432", "5433"), row("5432", "5434")])).toContain(
			"used twice",
		);
	});

	test("at most sixteen tcp ports", () => {
		const rows = (count: number) =>
			Array.from({ length: count }, (_, i) => row(String(2000 + i)));
		expect(
			validateTailnet(draft({ web: false, tcp: rows(16) }), context).tailnetTcp,
		).toBeUndefined();
		expect(
			validateTailnet(draft({ web: false, tcp: rows(17) }), context).tailnetTcp,
		).toContain("16");
	});

	test("with the web exposure, ports 80 and 443 are taken", () => {
		expect(
			validateTailnet(draft({ tcp: [row("443")] }), context).tailnetTcp,
		).toContain("80 and 443");
		expect(
			validateTailnet(draft({ tcp: [row("80")] }), context).tailnetTcp,
		).toBeDefined();
		expect(
			validateTailnet(draft({ web: false, tcp: [row("443")] }), context)
				.tailnetTcp,
		).toBeUndefined();
	});

	test("rows are reported one by one", () => {
		expect(
			validateTcpRows([row("5432"), row("x"), row("5432")], false),
		).toEqual([
			undefined,
			"Listen port must be a whole number.",
			"Listen port 5432 is used twice.",
		]);
	});

	test("a one-shot job cannot be on a tailnet", () => {
		expect(
			validateTailnet(draft(), { port: "", oneshot: true }).tailnet,
		).toBeDefined();
	});
});

describe("helpers", () => {
	test("the default hostname is <service>-<environment> as one label", () => {
		expect(defaultTailnetHostname("web", "prod")).toBe("web-prod");
		expect(defaultTailnetHostname("web", "web")).toBe("web");
		expect(defaultTailnetHostname("My_API", "Staging 1")).toBe(
			"my-api-staging-1",
		);
		expect(defaultTailnetHostname("", "")).toBe("service");
		const long = defaultTailnetHostname("a".repeat(50), "b".repeat(50));
		expect(long.length).toBeLessThanOrEqual(63);
		expect(long.endsWith("-")).toBe(false);
	});

	test("tags outside the allow-list are reported", () => {
		expect(tagsNotAllowed(["tag:a", "tag:b"], ["tag:a"])).toEqual(["tag:b"]);
		expect(tagsNotAllowed(["tag:a"], null)).toEqual([]);
		expect(tagsNotAllowed(["tag:a"], undefined)).toEqual([]);
	});

	test("presets fill a blank row, else add one, and never repeat a port", () => {
		expect(TCP_PRESETS.map((preset) => preset.port)).toEqual([
			5432, 6379, 3306, 25565,
		]);
		expect(withTcpPreset([], 5432)).toEqual([row("5432", "5432")]);
		expect(withTcpPreset([row("")], 6379)).toEqual([row("6379", "6379")]);
		expect(withTcpPreset([row("5432", "5432")], 5432)).toEqual([
			row("5432", "5432"),
		]);
		expect(withTcpPreset([row("8000", "8000")], 3306)).toEqual([
			row("8000", "8000"),
			row("3306", "3306"),
		]);
		const full = Array.from({ length: 16 }, (_, i) => row(String(2000 + i)));
		expect(withTcpPreset(full, 5432)).toHaveLength(16);
	});
});

describe("tag policy", () => {
	test("unknown without a connected integration", () => {
		expect(tagPolicy(undefined)).toBeNull();
		expect(
			tagPolicy({ configured: false, defaultTags: null, allowedTags: null }),
		).toBeNull();
	});

	test("an empty allow-list means the default tags", () => {
		expect(
			tagPolicy({
				configured: true,
				defaultTags: ["tag:nebula-svc"],
				allowedTags: null,
			}),
		).toEqual({ defaults: ["tag:nebula-svc"], allowed: ["tag:nebula-svc"] });
	});

	test("the allow-list always includes the defaults and is sorted", () => {
		expect(
			tagPolicy({
				configured: true,
				defaultTags: ["tag:nebula-svc"],
				allowedTags: ["tag:db", "tag:app"],
			}),
		).toEqual({
			defaults: ["tag:nebula-svc"],
			allowed: ["tag:app", "tag:db", "tag:nebula-svc"],
		});
	});
});
