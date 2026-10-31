import { describe, expect, test } from "bun:test";
import {
	HEADSCALE_CREDENTIALS,
	headscalePolicySnippet,
	keepsStoredSecret,
	managedEgressBlocker,
	validateLoginServer,
} from "./tailscale";

describe("headscale integration", () => {
	test("the login server is an http(s) URL without credentials, query or fragment", () => {
		expect(validateLoginServer("https://hs.example.com")).toBeNull();
		expect(validateLoginServer("http://10.0.0.2:8080/")).toBeNull();
		expect(validateLoginServer("  https://hs.example.com/base  ")).toBeNull();
		expect(validateLoginServer("")).not.toBeNull();
		expect(validateLoginServer("hs.example.com")).not.toBeNull();
		expect(validateLoginServer("ftp://hs.example.com")).not.toBeNull();
		expect(
			validateLoginServer("https://user:pw@hs.example.com"),
		).not.toBeNull();
		expect(validateLoginServer("https://hs.example.com?x=1")).not.toBeNull();
		expect(validateLoginServer("https://hs.example.com#frag")).not.toBeNull();
	});

	test("both credential kinds say what NebulaCtrl can do with them", () => {
		expect(HEADSCALE_CREDENTIALS.map((kind) => kind.id)).toEqual([
			"apikey",
			"preauth",
		]);
		expect(HEADSCALE_CREDENTIALS[0]?.hint).toContain("delete nodes");
		expect(HEADSCALE_CREDENTIALS[1]?.hint).toContain("cannot be removed");
	});

	test("the policy hint makes the user own the tags", () => {
		const text = headscalePolicySnippet(["tag:nebula-svc", "tag:db"], "alice");
		expect(text).toContain('"tagOwners"');
		expect(text).toContain('"tag:nebula-svc": ["alice@"]');
		expect(text).toContain('"tag:db": ["alice@"]');
		expect(text).toContain('"tag:db:*"');
		expect(headscalePolicySnippet([], "")).toContain(
			'"tag:nebula-svc": ["your-user@"]',
		);
	});

	test("the per-server egress node is unavailable on Headscale only", () => {
		expect(managedEgressBlocker("headscale")).toContain("OAuth");
		expect(managedEgressBlocker("tailscale_oauth")).toBeNull();
		expect(managedEgressBlocker(undefined)).toBeNull();
	});
});

describe("stored secret", () => {
	const oauth = {
		secretSet: true,
		provider: "tailscale_oauth",
		clientId: "k123",
		loginServer: "",
		headscaleCredential: "apikey",
	} as const;
	const headscale = {
		secretSet: true,
		provider: "headscale",
		clientId: "",
		loginServer: "https://hs.example.com",
		headscaleCredential: "apikey",
	} as const;

	test("an unchanged connection keeps its secret", () => {
		expect(
			keepsStoredSecret(
				{
					provider: "tailscale_oauth",
					clientId: " k123 ",
					loginServer: "",
					credential: "apikey",
				},
				oauth,
			),
		).toBe(true);
		expect(
			keepsStoredSecret(
				{
					provider: "headscale",
					clientId: "",
					loginServer: "https://hs.example.com/",
					credential: "apikey",
				},
				headscale,
			),
		).toBe(true);
	});

	test("a different provider, client, server or credential kind needs a new one", () => {
		const base = {
			provider: "headscale",
			clientId: "",
			loginServer: "https://hs.example.com",
			credential: "apikey",
		} as const;
		expect(
			keepsStoredSecret({ ...base, provider: "tailscale_oauth" }, headscale),
		).toBe(false);
		expect(
			keepsStoredSecret({ ...base, loginServer: "https://other" }, headscale),
		).toBe(false);
		expect(
			keepsStoredSecret({ ...base, credential: "preauth" }, headscale),
		).toBe(false);
		expect(
			keepsStoredSecret(
				{ ...base, provider: "tailscale_oauth", clientId: "other" },
				oauth,
			),
		).toBe(false);
		expect(keepsStoredSecret(base, { ...headscale, secretSet: false })).toBe(
			false,
		);
	});
});
