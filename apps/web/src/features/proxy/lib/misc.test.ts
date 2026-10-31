import { describe, expect, test } from "bun:test";
import { ApiError } from "#/api/client-config";
import { certExpiry, namesSummary, pemProblem, shortFingerprint } from "./cert";
import { agentUpgradeCommand, errorField, isAgentTooOld } from "./errors";
import {
	accessSummary,
	checkPort,
	l4PlanLines,
	RESTART_WARNING,
	routeBadge,
	routeSubtitle,
	UDP_WARNING,
} from "./l4";
import { domainStaged } from "./staged";
import { canPickTailnetDevice, modeBlocker, tailnetChip } from "./tailnet";
import {
	parseTags,
	policySnippet,
	primaryAddress,
	validateTags,
} from "./tailscale";

const DAY = 86_400_000;
const at = (ms: number) => new Date(ms).toISOString();

describe("certExpiry", () => {
	const notBefore = at(Date.UTC(2026, 0, 1));
	const notAfter = at(Date.UTC(2026, 0, 1) + 90 * DAY);
	test("fraction is the share of the window still ahead", () => {
		const now = Date.UTC(2026, 0, 1) + 45 * DAY;
		const e = certExpiry({ notBefore, notAfter }, now);
		expect(e.fraction).toBeCloseTo(0.5, 5);
		expect(e.daysLeft).toBe(45);
		expect(e.tone).toBe("ok");
		expect(e.label).toBe("45 days left");
	});
	test("amber under 30 days, red under 7", () => {
		const base = Date.UTC(2026, 0, 1) + 90 * DAY;
		expect(certExpiry({ notBefore, notAfter }, base - 20 * DAY).tone).toBe(
			"warn",
		);
		expect(certExpiry({ notBefore, notAfter }, base - 6.5 * DAY).tone).toBe(
			"bad",
		);
	});
	test("expired and not-yet-valid certificates", () => {
		const expired = certExpiry(
			{ notBefore, notAfter },
			Date.parse(notAfter) + 3 * DAY,
		);
		expect(expired.fraction).toBe(0);
		expect(expired.tone).toBe("bad");
		expect(expired.label).toBe("Expired 3 days ago");
		const early = certExpiry(
			{ notBefore, notAfter },
			Date.parse(notBefore) - DAY,
		);
		expect(early.label).toContain("Not valid until 2026-01-01");
	});
	test("garbage dates are reported, not drawn", () => {
		expect(certExpiry({ notBefore: "x", notAfter: "y" }, 0).tone).toBe("bad");
	});
	test("names and fingerprints are shortened", () => {
		expect(namesSummary(["a", "b", "c", "d", "e"])).toBe("a, b, c +2 more");
		expect(shortFingerprint("abcdef0123456789")).toBe("AB:CD:EF:01:23:45…");
	});
	test("PEM sanity checks", () => {
		expect(pemProblem("", "certificate")).not.toBeNull();
		expect(
			pemProblem(
				"-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----",
				"certificate",
			),
		).toBeNull();
		expect(pemProblem("hello", "certificate")).not.toBeNull();
		expect(
			pemProblem("-----BEGIN ENCRYPTED PRIVATE KEY-----\nx", "key"),
		).toContain("passphrase");
		expect(
			pemProblem(
				"-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----",
				"key",
			),
		).toBeNull();
		expect(pemProblem("-----BEGIN EC PRIVATE KEY-----\nx", "key")).toBeNull();
	});
});

describe("L4 plan text", () => {
	const base = {
		protocol: "tcp" as const,
		listenPort: 25565,
		newPort: true,
		serverName: "home",
		allowCidrs: [] as string[],
		denyCidrs: [] as string[],
		openToInternet: false,
	};
	test("a new port announces the proxy restart", () => {
		const lines = l4PlanLines(base);
		expect(lines[0]).toContain("restarts once");
		expect(lines[0]).toContain("home");
		expect(RESTART_WARNING).toContain("about a second");
	});
	test("an existing port does not", () => {
		expect(l4PlanLines({ ...base, newPort: false })).toEqual([]);
	});
	test("UDP always says it cannot be restricted", () => {
		expect(l4PlanLines({ ...base, protocol: "udp", newPort: false })).toEqual([
			UDP_WARNING,
		]);
	});
	test("TCP says open or counts the allow-list", () => {
		expect(
			l4PlanLines({ ...base, newPort: false, openToInternet: true })[0],
		).toContain("Open to the internet");
		expect(
			l4PlanLines({ ...base, newPort: false, allowCidrs: ["10.0.0.0/8"] })[0],
		).toBe("TCP 25565 accepts 1 allowed network only.");
	});
	test("TCP names the denied networks; UDP says neither list applies", () => {
		const lines = l4PlanLines({
			...base,
			newPort: false,
			allowCidrs: ["10.0.0.0/8"],
			denyCidrs: ["10.1.0.0/16", "10.2.0.0/16"],
		});
		expect(lines).toEqual([
			"TCP 25565 accepts 1 allowed network only.",
			"2 denied networks are refused first.",
		]);
		expect(
			l4PlanLines({
				...base,
				newPort: false,
				openToInternet: true,
				denyCidrs: ["203.0.113.0/24"],
			}),
		).toEqual([
			"Open to the internet: anyone who can reach this server can connect to this port.",
			"1 denied network is refused first.",
		]);
		expect(UDP_WARNING).toContain("neither an allow list nor a deny list");
	});
	test("accessSummary: allow, deny, open and UDP", () => {
		expect(
			accessSummary({
				protocol: "tcp",
				allowCidrs: ["10.0.0.0/8"],
				denyCidrs: ["10.1.0.0/16"],
			}),
		).toBe("Allow 10.0.0.0/8, deny 10.1.0.0/16");
		expect(
			accessSummary({
				protocol: "tcp",
				allowCidrs: ["10.0.0.0/8", "192.168.0.0/16", "::1"],
			}),
		).toBe("Allow 10.0.0.0/8 +2");
		expect(
			accessSummary({ protocol: "tcp", allowCidrs: [], denyCidrs: ["::/1"] }),
		).toBe("Open to the internet, deny ::/1");
		expect(accessSummary({ protocol: "tcp", allowCidrs: null })).toBe(
			"Open to the internet",
		);
		expect(
			accessSummary({
				protocol: "udp",
				allowCidrs: ["10.0.0.0/8"],
				denyCidrs: ["10.1.0.0/16"],
			}),
		).toBe("No IP filter (UDP)");
	});
	test("checkPort: invalid, taken by another route, free, own port", () => {
		const taken = [
			{ protocol: "tcp", port: 25565, serviceId: "a", serviceName: "mc" },
		];
		expect(checkPort("tcp", 25565, "bad", taken).state).toBe("invalid");
		expect(checkPort("tcp", 25565, null, taken).state).toBe("taken");
		expect(checkPort("udp", 25565, null, taken).state).toBe("ok");
		expect(checkPort("tcp", 25565, null, taken, "a").state).toBe("ok");
	});
});

describe("route summaries", () => {
	const nameOf = (id: string) => (id === "svc" ? "minecraft" : undefined);
	test("http: the upstream address, with a count", () => {
		expect(
			routeSubtitle(
				{ protocol: "http", upstreams: [{ address: "192.168.1.20:8123" }] },
				nameOf,
			),
		).toBe("192.168.1.20:8123");
		expect(
			routeSubtitle(
				{
					protocol: "http",
					upstreams: [
						{ address: "a:1" },
						{ address: "b:2" },
						{ address: "c:3" },
					],
				},
				nameOf,
			),
		).toBe("a:1 +2");
	});
	test("tcp/udp: protocol, listen port and target", () => {
		expect(
			routeSubtitle(
				{
					protocol: "tcp",
					listenPort: 25565,
					upstreams: [{ serviceId: "svc", port: 25565 }],
				},
				nameOf,
			),
		).toBe("tcp :25565 -> minecraft:25565");
		expect(routeSubtitle(undefined, nameOf)).toBe("external route");
		expect(routeBadge({ protocol: "udp", upstreams: [] })).toBe("UDP");
		expect(routeBadge(undefined)).toBe("HTTP");
	});
});

describe("domainStaged", () => {
	const changes = [
		{ field: "domains.a.example.com/api", from: "", to: "added" },
		{ field: "domains.example.com.stripPrefix", from: "false", to: "true" },
		{ field: "domains.example.com.tlsMode", from: "auto", to: "none" },
		{ field: "domains.example.com.authUsers", from: "", to: "bob" },
		{
			field: "domains.example.com.options",
			from: '{"compress":true}',
			to: '{"compress":true,"noindex":true,"rateLimit":{"average":1,"burst":1,"periodSeconds":1}}',
		},
		{ field: "variables.FOO", from: "1", to: "2" },
	];
	test("a new domain is flagged added", () => {
		const s = domainStaged(changes, "a.example.com", "/api");
		expect(s.added).toBe(true);
		expect(s.buckets.size).toBe(0);
	});
	test("rule, tls, access and per-group option changes", () => {
		const s = domainStaged(changes, "example.com", "");
		expect(s.added).toBe(false);
		expect([...s.buckets].sort()).toEqual([
			"access",
			"headers",
			"limits",
			"rule",
			"tls",
		]);
		expect(s.buckets.has("redirects")).toBe(false);
	});
	test("other domains and variables are ignored", () => {
		expect(domainStaged(changes, "b.example.com", "").buckets.size).toBe(0);
	});
});

describe("tailnet", () => {
	test("status chip words", () => {
		expect(tailnetChip("online", "managed").label).toBe("Online");
		expect(tailnetChip("provisioning", "managed").label).toBe("Provisioning");
		expect(tailnetChip("needs_attention", "managed").tone).toBe("failed");
		expect(tailnetChip("online", "off").label).toBe("Off");
		expect(tailnetChip("", "host").label).toBe("Host mode");
	});
	test("modes are blocked until their prerequisite exists", () => {
		const none = {
			integrationConfigured: false,
			hostTailscale: { detected: false },
		};
		expect(modeBlocker("managed", none)).toContain("Integrations");
		expect(modeBlocker("host", none)).toContain("not detected");
		expect(modeBlocker("off", none)).toBeNull();
		expect(
			modeBlocker("managed", { ...none, integrationConfigured: true }),
		).toBeNull();
	});
	test("the device picker needs the integration and a tailnet mode", () => {
		expect(
			canPickTailnetDevice({ integrationConfigured: true, mode: "managed" }),
		).toBe(true);
		expect(
			canPickTailnetDevice({ integrationConfigured: true, mode: "off" }),
		).toBe(false);
		expect(
			canPickTailnetDevice({ integrationConfigured: false, mode: "host" }),
		).toBe(false);
	});
});

describe("tailscale integration helpers", () => {
	test("tags are split, de-duplicated and checked", () => {
		expect(parseTags("tag:a, tag:b tag:a\n")).toEqual(["tag:a", "tag:b"]);
		expect(validateTags(["tag:a"], ["tag:a", "tag:b"])).toEqual({});
		expect(validateTags(["a"], []).defaultTags).toBeDefined();
		expect(validateTags([], []).defaultTags).toBeDefined();
		expect(validateTags(["tag:a"], ["tag:b"]).allowedTags).toContain("tag:a");
	});
	test("the policy snippet owns the tags and names the ACL", () => {
		const text = policySnippet(["tag:nebula-svc"]);
		expect(text).toContain('"tagOwners"');
		expect(text).toContain('"tag:nebula-svc": ["tag:nebula"]');
		expect(text).toContain('"tag:nebula": ["autogroup:admin"]');
		expect(text).toContain('"acls"');
	});
	test("primary address prefers IPv4", () => {
		expect(primaryAddress(["fd7a::1", "100.64.0.2"])).toBe("100.64.0.2");
		expect(primaryAddress(null)).toBe("");
	});
});

describe("errors", () => {
	test("codes map to fields; agent_too_old is its own banner", () => {
		const denied = new ApiError(422, "upstream_denied", "loopback");
		expect(errorField(denied)).toBe("upstream");
		expect(errorField(new ApiError(409, "port_in_use", "x"))).toBe(
			"listenPort",
		);
		expect(errorField(new ApiError(422, "basic_auth_needs_users", "x"))).toBe(
			"basicAuth",
		);
		expect(errorField(new Error("x"))).toBeUndefined();
		expect(isAgentTooOld(new ApiError(422, "agent_too_old", "x"))).toBe(true);
		expect(isAgentTooOld(denied)).toBe(false);
	});
	test("the upgrade command targets the control plane origin", () => {
		expect(agentUpgradeCommand("https://nebula.example.com")).toBe(
			"curl -fsSL https://nebula.example.com/install-agent.sh | sudo sh -s -- --url https://nebula.example.com",
		);
	});
});
