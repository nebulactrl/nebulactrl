import { describe, expect, test } from "bun:test";
import { PLAIN_DOMAIN_RULE, type ServiceDomain } from "#/api/models";
import {
	buildRulePatch,
	codeForFamily,
	redirectDiscards,
	redirectFamily,
	redirectLoops,
	ruleDirty,
	toRuleForm,
	validateRule,
	wwwCounterpart,
} from "./rule";

describe("redirect families", () => {
	test("301/308 are permanent, 302/307 temporary", () => {
		expect(redirectFamily(301)).toBe("permanent");
		expect(redirectFamily(308)).toBe("permanent");
		expect(redirectFamily(302)).toBe("temporary");
		expect(redirectFamily(307)).toBe("temporary");
	});
	test("switching family keeps a code that already fits", () => {
		expect(codeForFamily("permanent", 301)).toBe(301);
		expect(codeForFamily("permanent", 307)).toBe(308);
		expect(codeForFamily("temporary", 308)).toBe(307);
		expect(codeForFamily("temporary", 302)).toBe(302);
	});
});

const domain: ServiceDomain = {
	...PLAIN_DOMAIN_RULE,
	id: "d1",
	hostname: "example.com",
	targetPort: 80,
	component: "",
	process: "",
	generated: false,
	status: "active",
	statusMessage: null,
	dnsOk: true,
	dnsCheckedAt: null,
};
const context = { otherDomains: [], redirects: [] };

describe("buildRulePatch", () => {
	test("an untouched form is not dirty and sends nothing", () => {
		const form = toRuleForm(domain);
		expect(ruleDirty(domain, form)).toBe(false);
		expect(buildRulePatch(domain, form)).toEqual({});
	});
	test("only changed fields are sent, hostnames normalised", () => {
		const form = {
			...toRuleForm(domain),
			hostname: "  API.Example.com ",
			pathPrefix: "/v1",
			stripPrefix: true,
		};
		expect(buildRulePatch(domain, form)).toEqual({
			hostname: "api.example.com",
			pathPrefix: "/v1",
			stripPrefix: true,
		});
	});
	test("switching to redirect sends the target and clears prefixes and options", () => {
		const busy: ServiceDomain = {
			...domain,
			pathPrefix: "/v1",
			stripPrefix: true,
			addPrefix: "/x",
			options: { compress: true },
		};
		const form = {
			...toRuleForm(busy),
			kind: "redirect" as const,
			redirectTo: "WWW.example.com",
			redirectCode: 301 as const,
		};
		expect(buildRulePatch(busy, form)).toEqual({
			kind: "redirect",
			redirectTo: "www.example.com",
			redirectCode: 301,
			stripPrefix: false,
			addPrefix: "",
			options: {},
		});
		expect(redirectDiscards(busy).length).toBe(3);
	});
	test("switching back to serve clears the redirect target", () => {
		const redirecting: ServiceDomain = {
			...domain,
			kind: "redirect",
			redirectTo: "www.example.com",
			redirectCode: 301,
		};
		const form = { ...toRuleForm(redirecting), kind: "serve" as const };
		expect(buildRulePatch(redirecting, form)).toEqual({
			kind: "serve",
			redirectTo: "",
		});
	});
});

describe("validateRule", () => {
	test("strip needs a prefix; prefixes are validated", () => {
		const form = { ...toRuleForm(domain), stripPrefix: true };
		expect(validateRule(form, domain, context).stripPrefix).toBeDefined();
		expect(
			validateRule({ ...form, pathPrefix: "/api/" }, domain, context)
				.pathPrefix,
		).toBeDefined();
		expect(
			validateRule(
				{ ...form, pathPrefix: "/api", addPrefix: "x" },
				domain,
				context,
			).addPrefix,
		).toBeDefined();
		expect(
			validateRule({ ...form, pathPrefix: "/api" }, domain, context),
		).toEqual({});
	});
	test("a redirect needs a different valid host", () => {
		const form = { ...toRuleForm(domain), kind: "redirect" as const };
		expect(validateRule(form, domain, context).redirectTo).toBeDefined();
		expect(
			validateRule({ ...form, redirectTo: "EXAMPLE.com" }, domain, context)
				.redirectTo,
		).toBeDefined();
		expect(
			validateRule({ ...form, redirectTo: "www.example.com" }, domain, context),
		).toEqual({});
	});
	test("redirect loops are caught against the environment's redirects", () => {
		const form = {
			...toRuleForm(domain),
			kind: "redirect" as const,
			redirectTo: "www.example.com",
		};
		const loopy = {
			otherDomains: [],
			redirects: [{ from: "www.example.com", to: "example.com" }],
		};
		expect(validateRule(form, domain, loopy).redirectTo).toContain("loop");
		expect(redirectLoops("a", "b", [{ from: "b", to: "c" }])).toBe(false);
		expect(
			redirectLoops("a", "b", [
				{ from: "b", to: "c" },
				{ from: "c", to: "a" },
			]),
		).toBe(true);
	});
	test("a host and path pair must be unique on the service", () => {
		const clash = {
			otherDomains: [{ hostname: "api.example.com", pathPrefix: "" }],
			redirects: [],
		};
		expect(
			validateRule(
				{ ...toRuleForm(domain), hostname: "api.example.com" },
				domain,
				clash,
			).hostname,
		).toBeDefined();
		// the domain's own current row does not clash with itself
		expect(
			validateRule(toRuleForm(domain), domain, {
				otherDomains: [{ hostname: "example.com", pathPrefix: "" }],
				redirects: [],
			}),
		).toEqual({});
	});
});

describe("wwwCounterpart helper", () => {
	test("an apex pairs with www., a www. host with its apex", () => {
		expect(wwwCounterpart("example.com")).toBe("www.example.com");
		expect(wwwCounterpart("Example.COM.")).toBe("www.example.com");
		expect(wwwCounterpart("www.example.com")).toBe("example.com");
		expect(wwwCounterpart("example.co.uk")).toBe("www.example.co.uk");
		expect(wwwCounterpart("www.example.co.uk")).toBe("example.co.uk");
	});
	test("nothing is offered for a subdomain or a wildcard", () => {
		expect(wwwCounterpart("app.example.com")).toBeNull();
		expect(wwwCounterpart("www.app.example.com")).toBeNull();
		expect(wwwCounterpart("*.example.com")).toBeNull();
		expect(wwwCounterpart("www.com")).toBeNull();
	});
});
