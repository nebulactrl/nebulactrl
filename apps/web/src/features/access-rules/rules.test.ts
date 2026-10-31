import { describe, expect, test } from "bun:test";
import {
	admitsNobody,
	checkRules,
	cleanList,
	DENY_EVERYTHING,
	describeRules,
	effectiveNetworks,
	formatNetwork,
	hasOpenCidr,
	isOpenCidr,
	MAX_ACCESS_ENTRIES,
	parseEntriesText,
	parseNetwork,
	rulesOk,
	sameNetworks,
	validateEntry,
} from "./rules";

describe("validateEntry (mirrors validCIDROrIP)", () => {
	test.each([
		"10.0.0.0/8",
		"192.168.1.20",
		"0.0.0.0/0",
		"10.1.2.3/32",
		"2001:db8::/32",
		"::1",
		"::",
		"::/0",
		"fd7a:115c:a1e0::/48",
		"2001:db8:0:0:0:0:0:1",
		"::ffff:1.2.3.4",
		"64:ff9b::192.0.2.33",
		"1:2:3:4:5:6:7::",
		"  10.0.0.0/8  ",
	])("accepts %p", (value) => {
		expect(validateEntry(value)).toBeNull();
	});
	test.each([
		"",
		"   ",
		"10.0.0.0/33",
		"10.0.0/8",
		"256.1.1.1",
		"01.2.3.4",
		"10.0.0.0/",
		"10.0.0.0/-1",
		"10.0.0.0/08",
		"10.0.0.0/8/8",
		"example.com",
		"2001:db8::/129",
		"2001:::1",
		"1::2::3",
		"1:2:3:4:5:6:7:8:9",
		"1:2:3:4:5:6:7",
		"1:2:3:4:5:6:7:8::",
		"12345::",
		"fe80::1%eth0",
		"::1.2.3",
		"1.2.3.4::",
		"[::1]",
	])("rejects %p", (value) => {
		expect(validateEntry(value)).not.toBeNull();
	});
	test("names the problem", () => {
		expect(validateEntry("")).toBe("Enter an address or network.");
		expect(validateEntry("nope")).toBe("“nope” is not a CIDR or IP address.");
		expect(validateEntry("10.0.0.0/33")).toBe(
			"The prefix length must be 0 to 32.",
		);
		expect(validateEntry("::/129")).toBe("The prefix length must be 0 to 128.");
	});
});

describe("normalizing", () => {
	const canonical = (value: string) => {
		const net = parseNetwork(value);
		return net ? formatNetwork(net) : null;
	};
	test("a CIDR is masked, a bare address stays an address", () => {
		expect(canonical("10.1.2.3/8")).toBe("10.0.0.0/8");
		expect(canonical(" 192.168.1.20 ")).toBe("192.168.1.20");
		expect(canonical("192.168.1.20/32")).toBe("192.168.1.20/32");
	});
	test("IPv6 prints compressed and lower case", () => {
		expect(canonical("2001:0DB8:0:0:0:0:0:1")).toBe("2001:db8::1");
		expect(canonical("2001:db8:0:0:1:0:0:1")).toBe("2001:db8::1:0:0:1");
		expect(canonical("2001:db8::ABCD/32")).toBe("2001:db8::/32");
		expect(canonical("0:0:0:0:0:0:0:0")).toBe("::");
		expect(canonical("0:0:0:0:0:0:0:1")).toBe("::1");
		expect(canonical("1:0:2:3:4:5:6:7")).toBe("1:0:2:3:4:5:6:7");
		expect(canonical("::ffff:1.2.3.4")).toBe("::ffff:1.2.3.4");
		expect(canonical("::/0")).toBe("::/0");
		expect(canonical("fd7a:115c:a1e0:0::/48")).toBe("fd7a:115c:a1e0::/48");
	});
	test("an invalid entry has no network", () => {
		expect(canonical(" nope ")).toBeNull();
	});
	test("formatNetwork round-trips parseNetwork", () => {
		for (const text of ["10.0.0.0/8", "1.2.3.4", "2001:db8::/32", "::1"]) {
			const net = parseNetwork(text);
			expect(net && formatNetwork(net)).toBe(text);
		}
	});
	test("cleanList trims, drops blanks and repeats, keeps the first spelling", () => {
		expect(
			cleanList([
				" 10.0.0.0/8 ",
				"",
				"10.1.2.3/8",
				"192.168.1.20",
				"nope",
				"nope",
			]),
		).toEqual(["10.0.0.0/8", "192.168.1.20", "nope"]);
		expect(cleanList(["1.2.3.4", "1.2.3.4/32"])).toEqual(["1.2.3.4"]);
	});
	test("sameNetworks ignores spelling, not order", () => {
		expect(sameNetworks(["10.1.2.3/8"], ["10.0.0.0/8"])).toBe(true);
		expect(sameNetworks(["10.0.0.0/8", "::1"], ["::1", "10.0.0.0/8"])).toBe(
			false,
		);
		expect(sameNetworks([""], [])).toBe(true);
	});
});

describe("parseEntriesText", () => {
	test("one per line, comma separated, or both", () => {
		expect(parseEntriesText("10.0.0.0/8\n192.168.1.20")).toEqual([
			"10.0.0.0/8",
			"192.168.1.20",
		]);
		expect(parseEntriesText("10.0.0.0/8, 192.168.1.20,::1")).toEqual([
			"10.0.0.0/8",
			"192.168.1.20",
			"::1",
		]);
		expect(parseEntriesText("10.0.0.0/8,\r\n\n  172.16.0.0/12 ;")).toEqual([
			"10.0.0.0/8",
			"172.16.0.0/12",
		]);
	});
	test("comments and blanks are dropped", () => {
		expect(parseEntriesText("# office\n10.0.0.0/8 # lan\n\n")).toEqual([
			"10.0.0.0/8",
		]);
		expect(parseEntriesText("   \n")).toEqual([]);
	});
});

describe("open ranges", () => {
	test("0.0.0.0/0 and ::/0, however they are written", () => {
		expect(isOpenCidr("0.0.0.0/0")).toBe(true);
		expect(isOpenCidr("::/0")).toBe(true);
		expect(isOpenCidr("1.2.3.4/0")).toBe(true);
		expect(isOpenCidr("0000::/0")).toBe(true);
		expect(isOpenCidr("0.0.0.0/1")).toBe(false);
		expect(isOpenCidr("0.0.0.0")).toBe(false);
		expect(isOpenCidr("nope")).toBe(false);
	});
	test("hasOpenCidr looks at every entry", () => {
		expect(hasOpenCidr(["10.0.0.0/8", "::/0"])).toBe(true);
		expect(hasOpenCidr(["10.0.0.0/8"])).toBe(false);
		expect(hasOpenCidr([])).toBe(false);
	});
});

describe("admitsNobody (prefix math, not string compares)", () => {
	test("a deny entry that covers the allow entry", () => {
		expect(admitsNobody(["10.0.0.0/8"], ["10.0.0.0/8"])).toBe(true);
		expect(admitsNobody(["10.0.0.0/8"], ["10.0.0.0/7"])).toBe(true);
		expect(admitsNobody(["10.1.2.3"], ["10.0.0.0/8"])).toBe(true);
	});
	test("a deny entry inside the allow entry leaves the rest", () => {
		expect(admitsNobody(["10.0.0.0/7"], ["10.0.0.0/8"])).toBe(false);
		expect(admitsNobody(["10.0.0.0/8"], ["10.1.0.0/16"])).toBe(false);
	});
	test("several deny entries together cover one allow entry", () => {
		expect(admitsNobody(["10.0.0.0/8"], ["10.0.0.0/9", "10.128.0.0/9"])).toBe(
			true,
		);
		expect(admitsNobody(["10.0.0.0/8"], ["10.0.0.0/9", "10.128.0.0/10"])).toBe(
			false,
		);
	});
	test("one allow entry left uncovered is enough", () => {
		expect(admitsNobody(["10.0.0.0/8", "192.168.0.0/16"], ["10.0.0.0/8"])).toBe(
			false,
		);
		expect(
			admitsNobody(
				["10.0.0.0/8", "192.168.0.0/16"],
				["10.0.0.0/8", "192.168.0.0/15"],
			),
		).toBe(true);
	});
	test("families never cover each other", () => {
		expect(admitsNobody(["::1"], ["0.0.0.0/0"])).toBe(false);
		expect(admitsNobody(["10.0.0.0/8"], ["::/0"])).toBe(false);
		expect(admitsNobody(["10.0.0.0/8", "::1"], ["0.0.0.0/0", "::/0"])).toBe(
			true,
		);
	});
	test("an empty allow list is every address of both families", () => {
		expect(admitsNobody([], ["10.0.0.0/8"])).toBe(false);
		expect(admitsNobody([], ["0.0.0.0/0"])).toBe(false);
		expect(admitsNobody([], ["0.0.0.0/0", "::/0"])).toBe(true);
		expect(admitsNobody([], ["0.0.0.0/1", "128.0.0.0/1", "::/0"])).toBe(true);
	});
	test("IPv6 prefixes beyond 53 bits keep their precision", () => {
		expect(admitsNobody(["2001:db8::1"], ["2001:db8::/126"])).toBe(true);
		expect(admitsNobody(["2001:db8::4"], ["2001:db8::/126"])).toBe(false);
	});
	test("no deny list never denies everything; invalid entries are ignored", () => {
		expect(admitsNobody(["10.0.0.0/8"], [])).toBe(false);
		expect(admitsNobody([], [])).toBe(false);
		expect(admitsNobody(["nope"], ["10.0.0.0/8"])).toBe(false);
		expect(admitsNobody(["10.0.0.0/8"], ["nope"])).toBe(false);
	});
});

describe("effectiveNetworks", () => {
	test("allow minus deny, as disjoint prefixes", () => {
		const nets = effectiveNetworks(["10.0.0.0/8"], ["10.0.0.0/9"]) ?? [];
		expect(nets.map(formatNetwork)).toEqual(["10.128.0.0/9"]);
		const split = effectiveNetworks(["10.0.0.0/8"], ["10.1.0.0/16"]) ?? [];
		expect(split).toHaveLength(8);
	});
	test("null when the deny list splits into too many networks", () => {
		const deny = Array.from(
			{ length: 200 },
			(_, i) => `2001:db8:${i.toString(16)}::1`,
		);
		expect(effectiveNetworks(["2001:db8::/16"], deny)).toBeNull();
	});
});

describe("describeRules", () => {
	test("deny lines, then allow lines, then the default", () => {
		const rows = describeRules(
			["10.0.0.0/8", "192.168.1.20"],
			["10.1.0.0/16", "2001:db8::/32"],
		);
		expect(rows.map((row) => row.text)).toEqual([
			"Deny 10.1.0.0/16",
			"Deny 2001:db8::/32",
			"Allow 10.0.0.0/8",
			"Allow 192.168.1.20",
			"Everything else: denied",
		]);
		expect(rows.map((row) => row.position)).toEqual([1, 2, 3, 4, 5]);
		expect(rows.map((row) => row.effect)).toEqual([
			"deny",
			"deny",
			"allow",
			"allow",
			"deny",
		]);
		expect(rows.map((row) => row.fallback)).toEqual([
			false,
			false,
			false,
			false,
			true,
		]);
	});
	test("an empty allow list lets everyone else in", () => {
		const rows = describeRules([], ["10.0.0.0/8"]);
		expect(rows.map((row) => row.text)).toEqual([
			"Deny 10.0.0.0/8",
			"Everything else: allowed",
		]);
		expect(rows.at(-1)?.effect).toBe("allow");
		expect(describeRules([], []).map((row) => row.text)).toEqual([
			"Everything else: allowed",
		]);
	});
	test("a required allow list that is empty lets nobody in", () => {
		const rows = describeRules([], ["10.0.0.0/8"], { allowRequired: true });
		expect(rows.at(-1)?.text).toBe("Everything else: denied");
		expect(rows.at(-1)?.effect).toBe("deny");
	});
	test("entries print canonical, once; invalid and blank ones are left out", () => {
		const rows = describeRules(
			["10.1.2.3/8", "10.0.0.0/8", "", "nope"],
			["ABCD::1", "abcd:0::1"],
		);
		expect(rows.map((row) => row.text)).toEqual([
			"Deny abcd::1",
			"Allow 10.0.0.0/8",
			"Everything else: denied",
		]);
	});
});

describe("checkRules (mirrors ValidateAccessLists)", () => {
	test("a valid pair has no problem", () => {
		expect(rulesOk(checkRules(["10.0.0.0/8"], ["10.1.0.0/16"]))).toBe(true);
		expect(rulesOk(checkRules([], ["10.0.0.0/8"]))).toBe(true);
		expect(rulesOk(checkRules([], []))).toBe(true);
	});
	test("each bad entry is named under its list and row", () => {
		const check = checkRules(["10.0.0.0/8", "nope"], ["", "1.2.3.4/40"]);
		expect(Object.keys(check.entries).sort()).toEqual([
			"allowCidrs.1",
			"denyCidrs.0",
			"denyCidrs.1",
		]);
		const custom = checkRules(["x"], ["y"], {
			allowKey: "cidrs",
			denyKey: "deny",
		});
		expect(Object.keys(custom.entries).sort()).toEqual(["cidrs.0", "deny.0"]);
	});
	test("at most 200 entries in each list", () => {
		const many = Array.from(
			{ length: MAX_ACCESS_ENTRIES + 1 },
			(_, i) => `10.${i}.0.0/16`,
		);
		const wide = Array.from(
			{ length: MAX_ACCESS_ENTRIES + 1 },
			(_, i) => `172.${i}.0.0/16`,
		);
		expect(checkRules(many, []).allow).toBeDefined();
		expect(checkRules([], wide).deny).toBeDefined();
		expect(
			checkRules(many.slice(0, MAX_ACCESS_ENTRIES), []).allow,
		).toBeUndefined();
	});
	test("repeats count once toward the limit", () => {
		const repeated = Array.from({ length: 300 }, () => "10.0.0.0/8");
		expect(checkRules(repeated, []).allow).toBeUndefined();
	});
	test("a rule set that denies every address says so", () => {
		const check = checkRules(["10.0.0.0/8"], ["10.0.0.0/7"]);
		expect(check.rules).toBe(DENY_EVERYTHING);
		expect(check.rules).toContain("deny every address");
		expect(rulesOk(check)).toBe(false);
	});
	test("a bad entry hides the pair-level verdict until it is fixed", () => {
		expect(
			checkRules(["10.0.0.0/8", "x"], ["10.0.0.0/8"]).rules,
		).toBeUndefined();
	});
	test("a required allow list must have an entry", () => {
		expect(checkRules([], [], { allowRequired: true }).allow).toBe(
			"Add at least one allowed network.",
		);
		expect(
			checkRules([" "], ["10.0.0.0/8"], { allowRequired: true }).allow,
		).toBeDefined();
		expect(
			checkRules(["10.0.0.0/8"], [], { allowRequired: true }).allow,
		).toBeUndefined();
	});
	test("needOne wants an entry in either list", () => {
		expect(checkRules([], [], { needOne: true }).rules).toBeDefined();
		expect(
			checkRules([], ["10.0.0.0/8"], { needOne: true }).rules,
		).toBeUndefined();
		expect(
			checkRules(["10.0.0.0/8"], [], { needOne: true }).rules,
		).toBeUndefined();
	});
	test("a deny list that splits too much is refused", () => {
		const deny = Array.from(
			{ length: 200 },
			(_, i) => `2001:db8:${i.toString(16)}::1`,
		);
		expect(checkRules(["2001:db8::/16"], deny).rules).toContain(
			"more than 4096",
		);
	});
});
