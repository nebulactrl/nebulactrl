// biome-ignore-all lint/suspicious/noTemplateCurlyInString: the product's own `${{ }}` reference syntax, not a JavaScript template
import { describe, expect, test } from "bun:test";
import {
	DOPPLER_DEFAULT_URL,
	DOPPLER_SERVICE_TOKENS_DOCS,
	dopplerGuide,
	INFISICAL_DEFAULT_URL,
	INFISICAL_UNIVERSAL_AUTH_DOCS,
	infisicalGuide,
	referenceExample,
	referenceHelp,
	vaultGuide,
	vaultMount,
	vaultPolicy,
} from "./secret-manager-guides";

describe("vault policy", () => {
	test("reads the KV v2 data path of the mount", () => {
		expect(vaultPolicy("secret")).toBe(
			'path "secret/data/*" {\n  capabilities = ["read"]\n}\n',
		);
		expect(vaultPolicy(" /kv/team/ ")).toContain('path "kv/team/data/*"');
	});

	test("falls back to secret for a blank or unsafe mount", () => {
		expect(vaultMount("")).toBe("secret");
		expect(vaultMount("a b")).toBe("secret");
		expect(vaultMount("../x")).toBe("secret");
		expect(vaultMount('x"}')).toBe("secret");
	});
});

describe("vaultGuide", () => {
	test("token auth has no link and a policy with the token command", () => {
		const guide = vaultGuide({ auth: "token", mount: "secret" });
		expect(guide.cta).toBeNull();
		expect(guide.url).toBeNull();
		expect(guide.whyNoLink).toContain("own server");
		expect(guide.policy?.text).toContain(
			"vault token create -policy=nebulactrl",
		);
		expect(guide.paste).toContain("token");
	});

	test("AppRole auth lists the role setup and asks for both ids", () => {
		const guide = vaultGuide({ auth: "approle", mount: "kv" });
		expect(guide.policy?.text).toContain('path "kv/data/*"');
		expect(guide.policy?.text).toContain("vault auth enable approle");
		expect(guide.policy?.text).toContain("token_type=batch");
		expect(guide.policy?.text).toContain("role-id");
		expect(guide.paste).toContain("secret id");
		expect(guide.docs).toContain("approle");
	});
});

describe("infisicalGuide", () => {
	test("links the universal auth guide and names the three steps", () => {
		const guide = infisicalGuide("");
		expect(guide.url).toBe(INFISICAL_UNIVERSAL_AUTH_DOCS);
		expect(guide.permissions).toHaveLength(3);
		expect(guide.note).toContain(INFISICAL_DEFAULT_URL);
	});

	test("names the instance it signs in to", () => {
		expect(infisicalGuide("https://inf.example.com/").note).toContain(
			"https://inf.example.com.",
		);
	});
});

describe("references", () => {
	test("examples match each kind's path syntax", () => {
		expect(referenceExample("vault", "bao")).toBe(
			"${{vault:bao/apps/shop/db#password}}",
		);
		expect(referenceExample("infisical", " ")).toBe(
			"${{vault:name/prod/backend#DATABASE_URL}}",
		);
		expect(referenceHelp("infisical")).toContain("environment slug");
		expect(referenceHelp("vault")).toContain("mount");
	});
});

describe("dopplerGuide", () => {
	test("links the service token guide and prefers a config-bound token", () => {
		const guide = dopplerGuide("");
		expect(guide.provider).toBe("Doppler");
		expect(guide.url).toBe(DOPPLER_SERVICE_TOKENS_DOCS);
		expect(guide.docs).toBe(DOPPLER_SERVICE_TOKENS_DOCS);
		expect(guide.permissions[0]?.label).toContain("Access");
		expect(guide.permissions[1]?.optional).toBe(true);
		expect(guide.note).toContain(DOPPLER_DEFAULT_URL);
		expect(guide.paste).toContain("token");
	});

	test("names a private Doppler API it talks to", () => {
		expect(dopplerGuide("https://doppler.example.com/").note).toContain(
			"from https://doppler.example.com and",
		);
	});

	test("references: the example follows the integration's defaults", () => {
		expect(referenceExample("doppler", "dp")).toBe(
			"${{doppler:dp/backend/prd#DATABASE_URL}}",
		);
		expect(referenceExample("doppler", "dp", true)).toBe(
			"${{doppler:dp#DATABASE_URL}}",
		);
		expect(referenceHelp("doppler")).toContain("project and config");
	});
});
