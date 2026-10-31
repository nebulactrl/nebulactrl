import { describe, expect, test } from "bun:test";
import type {
	DnsStatus,
	IpListView,
	OriginLockView,
	Zone,
} from "#/api/gen/types.gen";
import {
	applyZone,
	connectionState,
	dnsHeader,
	dnsStateInfo,
	fixAction,
	ipListSummary,
	lockHeadline,
	lockLayers,
	lockReasonText,
	lockToggleEnabled,
	matchZone,
	needsAopPermission,
	ORIGIN_LOCK_OPT_OUT_WARNING,
	proxyLabel,
	showOptOutWarning,
} from "./model";

const zone = (name: string, id = name): Zone => ({
	accountId: "acc",
	accountName: "Acme",
	id,
	name,
	paused: false,
	status: "active",
});

function lock(overrides: Partial<OriginLockView> = {}): OriginLockView {
	return {
		aop: "off",
		aopReason: "",
		aopReasonCode: "",
		enabled: false,
		ipListSource: "builtin",
		ipLock: false,
		optIn: true,
		permissionHint: "Zone → Zone Settings → Edit",
		realClientIp: false,
		reason: "not_proxied",
		...overrides,
	};
}

const status = (overrides: Partial<DnsStatus> = {}): DnsStatus => ({
	checkedAt: null,
	configured: true,
	desired: { content: "203.0.113.10", owned: true, proxied: false, type: "A" },
	desiredRecords: [
		{ content: "203.0.113.10", owned: true, proxied: false, type: "A" },
	],
	effectiveProxied: false,
	error: "",
	manage: true,
	message: "",
	needsAdopt: false,
	originLock: lock(),
	proxied: false,
	records: [],
	sslMode: "",
	status: "managed",
	tunnel: false,
	warnings: [],
	zoneId: "z1",
	zoneName: "example.com",
	...overrides,
});

describe("status words", () => {
	test("every status has a label, a lamp and a hint", () => {
		for (const s of [
			"managed",
			"present",
			"conflict",
			"wrong_target",
			"missing",
			"not_in_zone",
		] as const) {
			const info = dnsStateInfo(s);
			expect(info?.label).toBeTruthy();
			expect(info?.tone).toBeTruthy();
			expect(info?.hint).toBeTruthy();
		}
		expect(dnsStateInfo("managed")?.tone).toBe("ok");
		expect(dnsStateInfo("conflict")).toMatchObject({
			label: "Conflicting record",
			tone: "warn",
		});
		expect(dnsStateInfo("wrong_target")?.tone).toBe("warn");
		expect(dnsStateInfo("not_in_zone")?.tone).toBe("off");
	});

	test("nothing is said before the first evaluation", () => {
		expect(dnsStateInfo("")).toBeNull();
		expect(dnsStateInfo(undefined)).toBeNull();
	});

	test("the proxy mode in two words", () => {
		expect(proxyLabel(true)).toBe("Proxied");
		expect(proxyLabel(false)).toBe("DNS only");
	});
});

describe("the one-click fix", () => {
	test("a record that is right needs nothing", () => {
		expect(fixAction(status({ status: "managed" }))).toEqual({ kind: "none" });
		expect(fixAction(status({ status: "not_in_zone" }))).toEqual({
			kind: "none",
		});
		expect(fixAction(status({ configured: false, status: "" }))).toEqual({
			kind: "none",
		});
	});

	test("a right record the team created can always be adopted", () => {
		for (const manage of [true, false]) {
			const action = fixAction(status({ status: "present", manage }));
			expect(action.kind).toBe("adopt");
			if (action.kind === "adopt") {
				expect(action.label).toBe("Adopt record");
				expect(action.confirm).toContain("203.0.113.10");
			}
		}
	});

	test("a missing record is created, NebulaCtrl's own drifted record is fixed", () => {
		expect(fixAction(status({ status: "missing" }))).toEqual({
			kind: "fix",
			label: "Create record",
		});
		expect(fixAction(status({ status: "wrong_target" }))).toEqual({
			kind: "fix",
			label: "Fix DNS",
		});
		expect(fixAction(status({ status: "managed", error: "boom" }))).toEqual({
			kind: "fix",
			label: "Retry sync",
		});
	});

	test("a conflict has the one replace action, whose confirmation lists what changes", () => {
		const action = fixAction(
			status({
				status: "conflict",
				needsAdopt: true,
				effectiveProxied: true,
				records: [
					{ type: "A", content: "198.51.100.7", proxied: false, owned: false },
					{
						type: "CNAME",
						content: "old.example.net",
						proxied: true,
						owned: false,
					},
					{ type: "A", content: "203.0.113.10", proxied: true, owned: true },
				],
			}),
		);
		expect(action.kind).toBe("adopt");
		if (action.kind !== "adopt") return;
		expect(action.label).toBe("Replace with NebulaCtrl's record");
		expect(action.confirm).toContain("A 198.51.100.7 (DNS only)");
		expect(action.confirm).toContain("CNAME old.example.net (Proxied)");
		// NebulaCtrl's own record is not among the replaced ones; it is the wanted one.
		expect(action.confirm).toContain("A 203.0.113.10 (Proxied)");
		expect(action.confirm.indexOf("A 203.0.113.10")).toBeGreaterThan(
			action.confirm.indexOf("will be"),
		);
		expect(action.confirm).toContain("audited");
	});
});

describe("zones", () => {
	const zones = [
		zone("example.com"),
		zone("apps.example.com"),
		zone("other.org"),
	];

	test("the longest matching zone wins", () => {
		expect(matchZone("web.apps.example.com", zones)?.name).toBe(
			"apps.example.com",
		);
		expect(matchZone("WEB.example.com.", zones)?.name).toBe("example.com");
		expect(matchZone("example.com", zones)?.name).toBe("example.com");
		expect(matchZone("*.apps.example.com", zones)?.name).toBe(
			"apps.example.com",
		);
	});

	test("a suffix without a dot boundary is no parent domain", () => {
		expect(matchZone("notexample.com", zones)).toBeNull();
		expect(matchZone("x.unknown.dev", zones)).toBeNull();
		expect(matchZone("", zones)).toBeNull();
	});

	test("picking a zone keeps the label typed so far", () => {
		expect(applyZone("shop.example.org", "example.com")).toBe(
			"shop.example.com",
		);
		expect(applyZone("shop", "example.com")).toBe("shop.example.com");
		expect(applyZone("", "example.com")).toBe("app.example.com");
		expect(applyZone("*.old.example.org", "example.com")).toBe("*.example.com");
	});
});

describe("connection state", () => {
	const base = {
		accountId: "acc",
		accountName: "Acme",
		accounts: [],
		configured: true,
		status: "ok" as const,
		statusMessage: "",
		tokenExpiresAt: null,
		verifiedAt: null,
		warnings: [],
		zoneCount: 1,
		zonesSyncedAt: null,
	};

	test("not connected, connected, token rejected", () => {
		expect(connectionState({ ...base, configured: false, status: "" })).toEqual(
			{ label: "Not connected", tone: "off" },
		);
		expect(connectionState(base)).toEqual({ label: "Connected", tone: "ok" });
		expect(connectionState({ ...base, status: "error" })).toEqual({
			label: "Token rejected",
			tone: "bad",
		});
	});
});

describe("origin lock", () => {
	const locked = lock({
		aop: "on",
		enabled: true,
		ipLock: true,
		reason: "",
		realClientIp: true,
	});

	test("a locked domain shows the three layers on", () => {
		const layers = lockLayers(locked);
		expect(layers.map((l) => l.id)).toEqual(["ip", "aop", "realIp"]);
		expect(layers.map((l) => l.state)).toEqual(["on", "on", "on"]);
		expect(layers.map((l) => l.kind)).toEqual(["live", "live", "live"]);
		expect(layers.map((l) => l.label)).toEqual(["On", "On", "On"]);
		expect(lockHeadline(locked)).toEqual({ label: "Locked", kind: "live" });
		expect(lockReasonText(locked.reason)).toBe("");
	});

	test("AOP unavailable keeps the IP lock and says why", () => {
		const l = lock({
			...locked,
			aop: "unavailable",
			aopReason: "The Cloudflare token cannot change zone settings.",
			aopReasonCode: "permission",
		});
		const [ip, aop, realIp] = lockLayers(l);
		expect(ip?.state).toBe("on");
		expect(aop?.state).toBe("unavailable");
		expect(aop?.kind).toBe("warning");
		expect(aop?.detail).toContain("cannot change zone settings");
		expect(realIp?.state).toBe("on");
		expect(needsAopPermission(l)).toBe(true);
		expect(needsAopPermission(lock({ ...l, aopReasonCode: "ssl_mode" }))).toBe(
			false,
		);
		expect(needsAopPermission(locked)).toBe(false);
	});

	test("an unlocked domain has every layer off and a reason", () => {
		for (const reason of [
			"opted_out",
			"tunnel",
			"not_proxied",
			"not_in_zone",
			"pending",
			"not_published",
			"wildcard",
			"redirect",
		] as const) {
			expect(lockReasonText(reason)).toBeTruthy();
		}
		const l = lock({ reason: "not_proxied" });
		expect(lockLayers(l).map((x) => x.state)).toEqual(["off", "off", "off"]);
		expect(lockHeadline(l).label).toBe("Not locked");
		expect(lockHeadline(lock({ reason: "pending" })).kind).toBe("pending");
	});

	test("opting out warns while the domain is proxied", () => {
		const out = lock({ optIn: false, reason: "opted_out" });
		expect(lockHeadline(out)).toEqual({ label: "Open", kind: "warning" });
		expect(showOptOutWarning(out, true)).toBe(true);
		expect(showOptOutWarning(out, false)).toBe(false);
		expect(showOptOutWarning(locked, true)).toBe(false);
		expect(ORIGIN_LOCK_OPT_OUT_WARNING).toContain("bypass Cloudflare");
		// A tunnel has no public origin: nothing to warn about.
		expect(
			showOptOutWarning(lock({ optIn: false, reason: "tunnel" }), true),
		).toBe(false);
	});

	test("the switch is unavailable for tunnel and unzoned domains", () => {
		expect(lockToggleEnabled(lock({ reason: "tunnel" }))).toBe(false);
		expect(lockToggleEnabled(lock({ reason: "not_in_zone" }))).toBe(false);
		expect(lockToggleEnabled(lock({ reason: "not_proxied" }))).toBe(true);
		expect(lockToggleEnabled(locked)).toBe(true);
	});
});

describe("IP list summary", () => {
	const base: IpListView = {
		builtinDate: "2026-10-27",
		changedAt: null,
		failingSince: null,
		fetchedAt: null,
		hash: "h",
		lastError: "",
		source: "builtin",
		stale: false,
		v4Count: 15,
		v6Count: 7,
	};

	test("the built-in list says it was shipped and not fetched", () => {
		const s = ipListSummary(base);
		expect(s.source).toBe("Built-in copy");
		expect(s.size).toBe("22 ranges (15 IPv4, 7 IPv6)");
		expect(s.age).toContain("2026-10-27");
		expect(s.changed).toBe("");
		expect(s.problem).toBe("");
	});

	test("a fetched list shows its source, age and the last change", () => {
		const hourAgo = new Date(Date.now() - 3_600_000).toISOString();
		const s = ipListSummary({
			...base,
			changedAt: hourAgo,
			fetchedAt: hourAgo,
			source: "api",
		});
		expect(s.source).toBe("Cloudflare API");
		expect(s.age).toBe("Fetched 1h ago");
		expect(s.changed).toContain("Content last changed 1h ago");
		expect(ipListSummary({ ...base, source: "text" }).source).toBe(
			"Published text files",
		);
	});

	test("a failing refresh keeps the list and says so", () => {
		const s = ipListSummary({
			...base,
			failingSince: "2026-10-26T10:00:00Z",
			lastError: "API: answered 503",
			stale: true,
		});
		expect(s.problem).toContain("API: answered 503");
		expect(s.problem).toContain("2026-10-26");
		expect(s.problem).toContain("last good list stays in use");
		expect(s.stale).toBe(true);
	});
});

describe("the DNS header of a domain card", () => {
	type Dns = NonNullable<Parameters<typeof dnsHeader>[0]["dns"]>;
	const domain = (
		dns: Partial<Dns> | null,
		probe: { dnsOk?: boolean | null; statusMessage?: string | null } = {},
	) => ({
		hostname: "app.example.com",
		dnsOk: probe.dnsOk ?? null,
		statusMessage: probe.statusMessage ?? null,
		dns: dns
			? {
					status: "managed" as const,
					proxied: true,
					zoneName: "example.com",
					message: "",
					...dns,
				}
			: null,
	});
	const STALE = "Point an A record for app.example.com to 203.0.113.10.";
	const IP = "203.0.113.10";

	test("a managed domain reads DNS ok, proxied or DNS only, whatever the probe says", () => {
		for (const proxied of [true, false]) {
			for (const dnsOk of [true, false, null]) {
				const header = dnsHeader(
					domain(
						{ status: "managed", proxied },
						{ dnsOk, statusMessage: STALE },
					),
					IP,
				);
				expect(header).toMatchObject({
					tone: "ok",
					kind: "healthy",
					label: "DNS ok",
				});
				expect(header.line).toContain("example.com");
				expect(header.line).toContain(
					proxied ? "proxied through Cloudflare" : "DNS only",
				);
				expect(header.line).not.toMatch(/Point an A record|not pointing/);
			}
		}
	});

	test("a present record is ok too", () => {
		const header = dnsHeader(
			domain({ status: "present" }, { dnsOk: false, statusMessage: STALE }),
			IP,
		);
		expect(header).toMatchObject({ tone: "ok", label: "DNS ok" });
		expect(header.line).toContain("proxied through Cloudflare");
		expect(header.line).not.toContain("Point an A record");
	});

	test("a conflict shows the Cloudflare message as a warning", () => {
		const message =
			"app.example.com already has A 198.51.100.7 in example.com, which NebulaCtrl did not create.";
		expect(
			dnsHeader(
				domain(
					{ status: "conflict", message },
					{ dnsOk: false, statusMessage: STALE },
				),
				IP,
			),
		).toEqual({
			tone: "warn",
			kind: "warning",
			label: "Conflicting record",
			line: message,
		});
	});

	test("a missing or drifted record reads DNS pending, never an instruction to create it", () => {
		for (const status of ["missing", "wrong_target"] as const) {
			const header = dnsHeader(
				domain({ status }, { dnsOk: false, statusMessage: STALE }),
				IP,
			);
			expect(header).toEqual({
				tone: "warn",
				kind: "pending",
				label: "DNS pending",
				line: "NebulaCtrl is setting up this record in your Cloudflare zone example.com.",
			});
		}
	});

	test("no state of a connected zone says to point a record or that DNS is not pointing", () => {
		for (const status of [
			"managed",
			"present",
			"conflict",
			"missing",
			"wrong_target",
		] as const) {
			for (const dnsOk of [true, false, null]) {
				const header = dnsHeader(
					domain({ status }, { dnsOk, statusMessage: STALE }),
					null,
				);
				expect(header.label).not.toBe("DNS not pointing");
				expect(header.label).not.toBe("DNS unchecked");
				expect(`${header.label} ${header.line}`).not.toMatch(
					/Point an A record|not pointing/,
				);
			}
		}
	});

	test("outside a connected zone the probe decides and the record to create is named", () => {
		for (const dns of [
			null,
			{ status: "not_in_zone" as const },
			{ status: "" as const },
		]) {
			expect(dnsHeader(domain(dns, { dnsOk: true }), IP)).toEqual({
				tone: "ok",
				kind: "healthy",
				label: "DNS ok",
				line: "DNS points at this server.",
			});
			expect(dnsHeader(domain(dns, { dnsOk: false }), IP)).toEqual({
				tone: "warn",
				kind: "warning",
				label: "DNS not pointing",
				line: `Point an A record for app.example.com to ${IP}.`,
			});
			expect(dnsHeader(domain(dns, { dnsOk: false }), null).line).toBe(
				"Point an A record for app.example.com to the server's public IP.",
			);
			expect(
				dnsHeader(
					domain(dns, { dnsOk: false, statusMessage: "No A record found." }),
					IP,
				).line,
			).toBe("No A record found.");
			expect(dnsHeader(domain(dns), IP)).toMatchObject({
				tone: "off",
				kind: "unknown",
				label: "DNS unchecked",
			});
		}
	});
});
