import { describe, expect, test } from "bun:test";
import {
	decide,
	type FirewallRule,
	parseCountry,
	ruleSentence,
	summary,
	type TestRequest,
	toBody,
	validateRules,
	withField,
	withOp,
} from "./model";

const req = (over: Partial<TestRequest> = {}): TestRequest => ({
	method: "GET",
	target: "/",
	ip: "",
	country: "",
	userAgent: "",
	headers: "",
	...over,
});

const rule = (over: Partial<FirewallRule>): FirewallRule => ({
	id: over.id ?? "r1",
	name: "Rule",
	action: "block",
	conditions: [],
	...over,
});

describe("decide", () => {
	const office = rule({
		id: "office",
		name: "Office",
		action: "allow",
		conditions: [{ field: "ip", op: "in", values: ["203.0.113.0/24"] }],
	});
	const admin = rule({
		id: "admin",
		name: "Admin",
		conditions: [{ field: "path", op: "prefix", values: ["/admin"] }],
	});

	test("the first matching rule decides, and allow skips the rules below it", () => {
		expect(
			decide([office, admin], req({ target: "/admin", ip: "203.0.113.9" })),
		).toMatchObject({
			kind: "allow",
			index: 0,
		});
		expect(
			decide(
				[office, admin],
				req({ target: "/admin/users", ip: "198.51.100.1" }),
			),
		).toMatchObject({
			kind: "block",
			index: 1,
		});
		expect(decide([office, admin], req({ target: "/shop" }))).toEqual({
			kind: "pass",
		});
	});

	test("a disabled rule is skipped", () => {
		expect(
			decide([{ ...admin, disabled: true }], req({ target: "/admin" })),
		).toEqual({ kind: "pass" });
	});

	test("conditions all must match; values of one condition are alternatives; not inverts", () => {
		const r = rule({
			conditions: [
				{ field: "method", op: "in", values: ["POST", "PUT"] },
				{ field: "path", op: "prefix", values: ["/api"], not: true },
			],
		});
		expect(decide([r], req({ method: "put", target: "/login" })).kind).toBe(
			"block",
		);
		expect(decide([r], req({ method: "POST", target: "/api/x" })).kind).toBe(
			"pass",
		);
		expect(decide([r], req({ method: "GET", target: "/login" })).kind).toBe(
			"pass",
		);
	});

	test("contains ignores case; regex is unanchored; query and headers by name", () => {
		const ua = rule({
			conditions: [{ field: "userAgent", op: "contains", values: ["SQLMap"] }],
		});
		expect(decide([ua], req({ userAgent: "sqlmap/1.7" })).kind).toBe("block");
		const re = rule({
			conditions: [{ field: "path", op: "regex", values: ["\\.php$"] }],
		});
		expect(decide([re], req({ target: "/x/index.php?a=1" })).kind).toBe(
			"block",
		);
		const q = rule({
			conditions: [
				{ field: "query", op: "equals", name: "debug", values: ["1"] },
			],
		});
		expect(decide([q], req({ target: "/?debug=1" })).kind).toBe("block");
		expect(decide([q], req({ target: "/?debug=0" })).kind).toBe("pass");
		const missing = rule({
			conditions: [
				{ field: "header", op: "present", name: "X-Api-Key", not: true },
			],
		});
		expect(decide([missing], req()).kind).toBe("block");
		expect(decide([missing], req({ headers: "x-api-key: abc" })).kind).toBe(
			"pass",
		);
	});

	test("an IPv4-mapped IPv6 visitor matches an IPv4 network; an unknown country never matches", () => {
		expect(decide([office], req({ ip: "::ffff:203.0.113.7" })).kind).toBe(
			"allow",
		);
		const notDE = rule({
			conditions: [{ field: "country", op: "in", values: ["DE"], not: true }],
		});
		expect(decide([notDE], req({ country: "FR" })).kind).toBe("block");
		expect(decide([notDE], req({ country: "DE" })).kind).toBe("pass");
	});
});

describe("validateRules", () => {
	test("names the rule and condition at fault, like the control plane", () => {
		const errors = validateRules([
			rule({ id: "a", name: " ", conditions: [] }),
			rule({
				id: "b",
				conditions: [
					{ field: "path", op: "prefix", values: ["admin"] },
					{ field: "ip", op: "in", values: ["10.0.0.0/33"] },
					{ field: "header", op: "equals", name: "Bad Name", values: ["x"] },
					{ field: "path", op: "regex", values: ["(?=x)"] },
					{ field: "country", op: "in", values: [] },
				],
			}),
		]);
		expect(Object.keys(errors).sort()).toEqual([
			"a.conditions",
			"a.name",
			"b.0.values",
			"b.1.values",
			"b.2.name",
			"b.3.values",
			"b.4.values",
		]);
		expect(errors["b.0.values"]).toBe("A path starts with /.");
	});

	test("a complete rule passes", () => {
		expect(
			validateRules([
				rule({
					conditions: [
						{ field: "path", op: "contains", values: ["/.env"] },
						{ field: "header", op: "present", name: "X-Debug" },
					],
				}),
			]),
		).toEqual({});
	});
});

describe("sentences and editing", () => {
	test("a rule reads as a sentence, countries by name", () => {
		expect(
			ruleSentence(
				rule({
					conditions: [
						{ field: "path", op: "prefix", values: ["/admin"] },
						{ field: "country", op: "in", not: true, values: ["DE", "FR"] },
					],
				}),
			),
		).toBe(
			"Block requests when the path starts with /admin and the country is not Germany or France",
		);
	});

	test("switching the field keeps a negation it supports and drops values that no longer fit", () => {
		const c = withField(
			{ field: "path", op: "prefix", not: true, values: ["/a"] },
			"header",
		);
		expect(c).toEqual({ field: "header", op: "equals", values: [], name: "" });
		expect(
			withOp(
				{ field: "header", op: "equals", name: "X", values: ["a"] },
				"present!",
			),
		).toEqual({
			field: "header",
			op: "present",
			not: true,
			name: "X",
			values: [],
		});
	});

	test("countries parse from a code, a name or a list pick", () => {
		expect(parseCountry("de")).toBe("DE");
		expect(parseCountry("Germany")).toBe("DE");
		expect(parseCountry("FR · France")).toBe("FR");
		expect(parseCountry("Atlantis")).toBeNull();
	});

	test("the body drops what the API does not take and the summary counts what is on", () => {
		const rules = [
			rule({
				id: "a",
				name: " Admin ",
				conditions: [
					{ field: "header", op: "present", name: "X", values: ["stale"] },
				],
			}),
			rule({
				id: "b",
				disabled: true,
				conditions: [{ field: "path", op: "prefix", values: ["/x"] }],
			}),
		];
		expect(toBody(rules)).toEqual({
			rules: [
				{
					id: "a",
					name: "Admin",
					action: "block",
					conditions: [{ field: "header", op: "present", name: "X" }],
				},
				{
					id: "b",
					name: "Rule",
					action: "block",
					disabled: true,
					conditions: [{ field: "path", op: "prefix", values: ["/x"] }],
				},
			],
		});
		expect(summary(rules)).toBe("1 of 2 rules on");
		expect(summary([])).toBe("Off");
	});
});
