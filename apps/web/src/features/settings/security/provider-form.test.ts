import { describe, expect, test } from "bun:test";
import {
	EMPTY_PROVIDER_FORM,
	expectedRedirectUri,
	newProviderId,
	parseDomains,
	parseList,
	providerBody,
	providerProblem,
} from "./provider-form";

describe("parseList", () => {
	test("splits on commas, spaces and newlines and drops repeats", () => {
		expect(parseList("a, b  c\nd,,a")).toEqual(["a", "b", "c", "d"]);
		expect(parseList("")).toEqual([]);
	});
	test("lowercases on request", () => {
		expect(parseList("A b", true)).toEqual(["a", "b"]);
	});
});

describe("parseDomains", () => {
	test("accepts @domain, mixed case and separators", () => {
		expect(parseDomains("@Example.com, corp.example;EXAMPLE.com")).toEqual([
			"example.com",
			"corp.example",
		]);
	});
});

describe("providerProblem", () => {
	const ok = {
		...EMPTY_PROVIDER_FORM,
		name: "Corp",
		clientId: "id",
		clientSecret: "secret",
		issuerUrl: "https://idp.example.com",
	};
	test("a complete form has no problem", () => {
		expect(providerProblem(ok, false)).toBeUndefined();
	});
	test("the secret is required on create only", () => {
		expect(providerProblem({ ...ok, clientSecret: "" }, false)).toContain(
			"secret",
		);
		expect(providerProblem({ ...ok, clientSecret: "" }, true)).toBeUndefined();
	});
	test("OIDC needs an issuer, GitHub does not", () => {
		expect(providerProblem({ ...ok, issuerUrl: "" }, false)).toContain(
			"issuer",
		);
		expect(
			providerProblem({ ...ok, kind: "github", issuerUrl: "" }, false),
		).toBeUndefined();
	});
	test("rejects a URL without a scheme", () => {
		expect(
			providerProblem({ ...ok, issuerUrl: "idp.example.com" }, false),
		).toContain("https://");
	});
});

describe("providerBody", () => {
	const form = {
		...EMPTY_PROVIDER_FORM,
		name: " Corp SSO ",
		issuerUrl: "https://idp.example.com/",
		clientId: " id ",
		clientSecret: "",
		scopes: "groups offline_access",
		allowedDomains: "@Corp.example",
		trustMfa: true,
	};
	test("trims, normalises and leaves an empty secret out (keeps the stored one)", () => {
		const body = providerBody(form);
		expect(body.name).toBe("Corp SSO");
		expect(body.issuerUrl).toBe("https://idp.example.com/");
		expect(providerBody({ ...form, kind: "github" }).issuerUrl).toBe(
			"https://idp.example.com",
		);
		expect(body.clientId).toBe("id");
		expect("clientSecret" in body).toBe(false);
		expect(body.scopes).toEqual(["groups", "offline_access"]);
		expect(body.allowedDomains).toEqual(["corp.example"]);
		expect(body.trustMfa).toBe(true);
	});
	test("sends a typed secret", () => {
		expect(
			providerBody({ ...form, clientSecret: " s3cret " }).clientSecret,
		).toBe("s3cret");
	});
	test("GitHub never trusts MFA", () => {
		expect(providerBody({ ...form, kind: "github" }).trustMfa).toBe(false);
	});
});

describe("providerBody id", () => {
	const form = { ...EMPTY_PROVIDER_FORM, name: "Corp", clientId: "id" };
	test("a new provider carries its pre-assigned id; an edit sends none", () => {
		const id = "6f1b2f6e-0c3a-4a43-9d57-0a2b1d5c9e11";
		expect(providerBody(form, id).id).toBe(id);
		expect("id" in providerBody(form)).toBe(false);
	});
});

describe("expectedRedirectUri", () => {
	test("is the server callback for the provider", () => {
		expect(expectedRedirectUri("https://ctl.example.com", "abc")).toBe(
			"https://ctl.example.com/api/v1/auth/oidc/abc/callback",
		);
	});
	test("ignores a trailing slash on the origin", () => {
		expect(expectedRedirectUri("https://ctl.example.com/", "abc")).toBe(
			"https://ctl.example.com/api/v1/auth/oidc/abc/callback",
		);
	});
	test("the pre-assigned id is what the server will use", () => {
		const id = newProviderId();
		const uri = expectedRedirectUri("http://localhost:5173", id);
		expect(uri).toBe(`http://localhost:5173/api/v1/auth/oidc/${id}/callback`);
	});
});

describe("newProviderId", () => {
	test("is a random version-4 UUID", () => {
		const id = newProviderId();
		expect(id).toMatch(
			/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
		);
		expect(newProviderId()).not.toBe(id);
	});
});
