import { describe, expect, test } from "bun:test";
import type { RouteOptions } from "#/api/gen/types.gen";
import {
	applyGroup,
	CORS_PRESETS,
	corsPresetOf,
	formToOptions,
	groupDirty,
	groupSummary,
	hasOptions,
	optionsErrorTarget,
	pickGroup,
	splitList,
	toForm,
	validateGroup,
} from "./options";

const full: RouteOptions = {
	compress: true,
	noindex: true,
	securityHeaders: "strict",
	forceHttps: false,
	requestHeaders: [{ name: "X-Env", value: "prod" }],
	responseHeaders: [{ name: "Cache-Control", value: "no-store" }],
	cors: {
		origins: ["https://a.example.com", "https://b.example.com"],
		methods: ["GET", "POST"],
		headers: ["Content-Type"],
		exposeHeaders: ["X-Total"],
		credentials: true,
		maxAgeSeconds: 600,
	},
	redirects: [
		// biome-ignore lint/suspicious/noTemplateCurlyInString: ${1} is the Go regexp group reference sent to the proxy, not a JS template
		{ regex: "^/old/(.*)$", replacement: "/new/${1}", permanent: true },
	],
	rateLimit: { average: 20, periodSeconds: 5, burst: 40, trustedProxyDepth: 1 },
	access: {
		allowCidrs: ["10.0.0.0/8", "192.168.1.20"],
		denyCidrs: ["10.1.0.0/16"],
		trustedProxyDepth: 2,
		rejectStatus: 404,
	},
	basicAuth: true,
};

describe("toForm / formToOptions", () => {
	test("round-trips every option", () => {
		expect(formToOptions(toForm(full))).toEqual(full);
	});
	test("an empty option set stays empty and omits defaults", () => {
		expect(formToOptions(toForm({}))).toEqual({});
		expect(formToOptions(toForm(undefined))).toEqual({});
		// forceHttps defaults to true on the wire, so it is never sent as true
		expect(formToOptions(toForm({ forceHttps: true }))).toEqual({});
	});
	test("switched-off sections drop their values", () => {
		const form = toForm(full);
		form.corsOn = false;
		form.rateOn = false;
		form.accessOn = false;
		const out = formToOptions(form);
		expect(out.cors).toBeUndefined();
		expect(out.rateLimit).toBeUndefined();
		expect(out.access).toBeUndefined();
	});
	test("blank header rows are not sent", () => {
		const form = toForm({});
		form.requestHeaders = [
			{ name: "", value: "" },
			{ name: " X-A ", value: "1" },
		];
		expect(formToOptions(form).requestHeaders).toEqual([
			{ name: "X-A", value: "1" },
		]);
	});
	test("splitList handles commas, spaces and newlines", () => {
		expect(splitList("GET, POST\nPUT  ,")).toEqual(["GET", "POST", "PUT"]);
	});
});

describe("groups", () => {
	test("pickGroup and applyGroup touch only the group's keys", () => {
		expect(pickGroup(full, "limits")).toEqual({
			rateLimit: {
				average: 20,
				periodSeconds: 5,
				burst: 40,
				trustedProxyDepth: 1,
			},
			compress: true,
		});
		const next = applyGroup(
			full,
			{ rateLimit: { average: 1, burst: 1, periodSeconds: 1 } },
			"limits",
		);
		expect(next.rateLimit).toEqual({ average: 1, burst: 1, periodSeconds: 1 });
		expect(next.compress).toBeUndefined();
		expect(next.access).toEqual(full.access);
		expect(next.cors).toEqual(full.cors);
		expect(next.redirects).toEqual(full.redirects);
	});
	test("groupDirty is per group", () => {
		const form = toForm(full);
		expect(groupDirty(full, form, "access")).toBe(false);
		form.allowCidrs = [...form.allowCidrs, "172.16.0.0/12"];
		expect(groupDirty(full, form, "access")).toBe(true);
		expect(groupDirty(full, form, "headers")).toBe(false);
		expect(groupDirty(full, form, "limits")).toBe(false);
	});
	test("hasOptions: a redirect domain must have none", () => {
		expect(hasOptions({})).toBe(false);
		expect(hasOptions({ forceHttps: true })).toBe(false);
		expect(hasOptions({ compress: true })).toBe(true);
	});
	test("summaries say what is set", () => {
		expect(groupSummary({}, "access")).toBe("");
		expect(groupSummary(full, "access", { authUsers: 2 })).toBe(
			"basic auth · 2 user(s) · 2 allowed network(s) · 1 denied network(s)",
		);
		expect(groupSummary(full, "limits")).toBe("20 req / 5s · compression");
		expect(
			groupSummary({ forceHttps: false }, "tls", { tlsMode: "custom" }),
		).toBe("custom · HTTP allowed");
	});
});

describe("CORS presets", () => {
	test("a preset round-trips through the form and is recognised", () => {
		for (const preset of CORS_PRESETS) {
			const form = { ...toForm({}), ...preset.apply("app.example.com") };
			expect(validateGroup(form, "headers")).toEqual({});
			expect(corsPresetOf(form, "app.example.com")).toBe(preset.id);
			expect((formToOptions(form).cors?.origins ?? []).length).toBeGreaterThan(
				0,
			);
		}
	});
	test("editing a preset makes it custom; switching CORS off reads off", () => {
		const form = { ...toForm({}), ...CORS_PRESETS[0]?.apply("h") };
		expect(corsPresetOf({ ...form, corsMethods: "GET" }, "h")).toBe("custom");
		expect(corsPresetOf({ ...form, corsOn: false }, "h")).toBe("off");
	});
});

describe("validateGroup (mirrors RouteOptions.Validate)", () => {
	test("access: allow-list needs valid entries; basic auth needs users", () => {
		const form = toForm({ access: { allowCidrs: [] } });
		expect(validateGroup(form, "access").accessRules).toBeDefined();
		form.allowCidrs = ["10.0.0.0/8", "nope"];
		const errors = validateGroup(form, "access");
		expect(errors["allowCidrs.1"]).toBeDefined();
		expect(errors["allowCidrs.0"]).toBeUndefined();
		const auth = toForm({ basicAuth: true });
		expect(
			validateGroup(auth, "access", { authUsers: 0 }).basicAuth,
		).toBeDefined();
		expect(
			validateGroup(auth, "access", { authUsers: 1 }).basicAuth,
		).toBeUndefined();
	});
	test("access: trusted proxy depth 0-5, reject status 400-599", () => {
		const form = toForm({ access: { allowCidrs: ["10.0.0.0/8"] } });
		form.accessDepth = "6";
		form.rejectStatus = "200";
		const errors = validateGroup(form, "access");
		expect(errors.accessDepth).toBeDefined();
		expect(errors.rejectStatus).toBeDefined();
	});
	test("headers: names, forbidden request headers, values", () => {
		const form = toForm({});
		form.requestHeaders = [
			{ name: "Host", value: "x" },
			{ name: "X-Ok", value: "a\nb" },
		];
		const errors = validateGroup(form, "headers");
		expect(errors["requestHeaders.0.name"]).toBeDefined();
		expect(errors["requestHeaders.1.value"]).toBeDefined();
	});
	test("headers: CORS origins, credentials with *, methods", () => {
		const form = toForm({});
		form.corsOn = true;
		form.corsOrigins = "*";
		form.corsCredentials = true;
		form.corsMethods = "get";
		const errors = validateGroup(form, "headers");
		expect(errors.corsCredentials).toBeDefined();
		expect(errors.corsMethods).toBeDefined();
		form.corsOrigins = "not-an-origin";
		expect(validateGroup(form, "headers").corsOrigins).toBeDefined();
		form.corsOrigins = "";
		expect(validateGroup(form, "headers").corsOrigins).toBeDefined();
	});
	test("redirects: regex and replacement per rule, at most ten", () => {
		const form = toForm({});
		form.redirects = [
			{ regex: "(", replacement: "/x", permanent: false },
			{ regex: "^/a", replacement: "", permanent: false },
		];
		const errors = validateGroup(form, "redirects");
		expect(errors["redirects.0.regex"]).toBeDefined();
		expect(errors["redirects.1.replacement"]).toBeDefined();
		form.redirects = Array.from({ length: 11 }, () => ({
			regex: "^/a",
			replacement: "/b",
			permanent: false,
		}));
		expect(validateGroup(form, "redirects").redirects).toBeDefined();
	});
	test("limits: rate limit bounds", () => {
		const form = toForm({
			rateLimit: { average: 0, burst: 0, periodSeconds: 4000 },
		});
		const errors = validateGroup(form, "limits");
		expect(errors.rateAverage).toBeDefined();
		expect(errors.rateBurst).toBeDefined();
		expect(errors.ratePeriod).toBeDefined();
		expect(validateGroup(toForm(full), "limits")).toEqual({});
	});
});

describe("optionsErrorTarget", () => {
	test("points a backend message at the group and field it is about", () => {
		expect(optionsErrorTarget("redirect 2: invalid regex: x")).toEqual({
			group: "redirects",
			path: "redirects.1.regex",
		});
		expect(
			optionsErrorTarget('access: "x" is not a CIDR or IP address').group,
		).toBe("access");
		expect(optionsErrorTarget("invalid cors origin").group).toBe("headers");
		expect(optionsErrorTarget("rateLimit needs average>=1").group).toBe(
			"limits",
		);
		expect(
			optionsErrorTarget("the Host header cannot be set on requests").group,
		).toBe("headers");
	});
});

describe("access rules: allow and deny lists", () => {
	test("a deny-only rule set round-trips", () => {
		const options: RouteOptions = { access: { denyCidrs: ["203.0.113.0/24"] } };
		const form = toForm(options);
		expect(form.accessOn).toBe(true);
		expect(form.allowCidrs).toEqual([]);
		expect(form.denyCidrs).toEqual(["203.0.113.0/24"]);
		expect(formToOptions(form)).toEqual(options);
		expect(validateGroup(form, "access")).toEqual({});
	});
	test("allow and deny together round-trip; blank rows and repeats are not sent", () => {
		const options: RouteOptions = {
			access: { allowCidrs: ["10.0.0.0/8"], denyCidrs: ["10.1.0.0/16"] },
		};
		expect(formToOptions(toForm(options))).toEqual(options);
		const form = toForm(options);
		form.allowCidrs = [" 10.0.0.0/8 ", "", "10.1.2.3/8"];
		form.denyCidrs = ["10.1.0.0/16", "10.1.0.0/16"];
		expect(formToOptions(form)).toEqual(options);
	});
	test("an empty list is left out of the wire shape", () => {
		const form = toForm({ access: { allowCidrs: ["10.0.0.0/8"] } });
		expect(formToOptions(form).access).toEqual({ allowCidrs: ["10.0.0.0/8"] });
		form.denyCidrs = [];
		expect(formToOptions(form).access).not.toHaveProperty("denyCidrs");
	});
	test("turning the rules on with no entry says so; either list is enough", () => {
		const form = toForm({});
		form.accessOn = true;
		expect(validateGroup(form, "access").accessRules).toBeDefined();
		form.denyCidrs = ["10.0.0.0/8"];
		expect(validateGroup(form, "access")).toEqual({});
	});
	test("rules that deny every address are refused, like the backend", () => {
		const form = toForm({
			access: { allowCidrs: ["10.0.0.0/8"], denyCidrs: ["10.0.0.0/7"] },
		});
		expect(validateGroup(form, "access").accessRules).toContain(
			"deny every address",
		);
	});
	test("deny rows are checked and named under their own key", () => {
		const form = toForm({ access: { denyCidrs: ["10.0.0.0/8", "x"] } });
		const errors = validateGroup(form, "access");
		expect(errors["denyCidrs.1"]).toBeDefined();
		expect(errors["denyCidrs.0"]).toBeUndefined();
	});
	test("groupDirty sees a deny edit; a respelling is not an edit", () => {
		const saved: RouteOptions = { access: { denyCidrs: ["10.0.0.0/8"] } };
		const form = toForm(saved);
		expect(groupDirty(saved, form, "access")).toBe(false);
		form.denyCidrs = ["10.0.0.0/8", "192.168.0.0/16"];
		expect(groupDirty(saved, form, "access")).toBe(true);
	});
	test("the summary counts denied networks", () => {
		expect(
			groupSummary({ access: { denyCidrs: ["10.0.0.0/8", "::1"] } }, "access"),
		).toBe("2 denied network(s)");
		expect(
			groupSummary(
				{ access: { allowCidrs: ["10.0.0.0/8"], denyCidrs: ["10.1.0.0/16"] } },
				"access",
			),
		).toBe("1 allowed network(s) · 1 denied network(s)");
	});
	test("backend messages about the rules point at the access group", () => {
		expect(optionsErrorTarget("access: the rules deny every address")).toEqual({
			group: "access",
			path: "accessRules",
		});
		expect(
			optionsErrorTarget('access: "x" is not a CIDR or IP address'),
		).toEqual({ group: "access", path: "allowCidrs" });
	});
});
