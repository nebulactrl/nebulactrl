import { describe, expect, test } from "bun:test";
import type { SecretManager } from "#/api/gen/types.gen";
import {
	draftFor,
	emptyDraft,
	isBaseUrl,
	isEnvironmentGranted,
	isInsecureAddress,
	isProjectGranted,
	needsSecret,
	scopeSummary,
	toBody,
	toggleEnvironment,
	toggleProject,
	validateDraft,
	whereOf,
} from "./model";

const vault: SecretManager = {
	id: "1",
	name: "bao",
	kind: "vault",
	config: {
		address: "https://bao.example.com:8200",
		mount: "secret",
		authMethod: "token",
	},
	secretSet: true,
	scope: "team",
	grants: [],
	lastTest: { at: null, ok: null },
	example: "",
	createdAt: "",
	updatedAt: "",
};

describe("validateDraft", () => {
	test("a new Vault integration needs a name, an address and the credential", () => {
		const errors = validateDraft(emptyDraft("vault"), null);
		expect(Object.keys(errors).sort()).toEqual(["address", "name", "secret"]);
	});

	test("a complete Vault draft is valid", () => {
		const draft = {
			...emptyDraft("vault"),
			name: "bao",
			address: "https://bao.example.com:8200",
			secret: "s.token",
		};
		expect(validateDraft(draft, null)).toEqual({});
	});

	test("AppRole needs the role id", () => {
		const draft = {
			...emptyDraft("vault"),
			name: "bao",
			address: "https://bao.example.com",
			authMethod: "approle" as const,
			secret: "sid",
		};
		expect(validateDraft(draft, null).roleId).toBeDefined();
		expect(validateDraft({ ...draft, roleId: "role-1" }, null)).toEqual({});
	});

	test("names are lowercase slugs", () => {
		const base = {
			...emptyDraft("vault"),
			address: "http://x:8200",
			secret: "t",
		};
		expect(validateDraft({ ...base, name: "My Bao" }, null).name).toBeDefined();
		expect(validateDraft({ ...base, name: "-bao" }, null).name).toBeDefined();
		expect(
			validateDraft({ ...base, name: "bao-2" }, null).name,
		).toBeUndefined();
	});

	test("Infisical needs a client id and a project id", () => {
		const draft = { ...emptyDraft("infisical"), name: "inf", secret: "s" };
		expect(Object.keys(validateDraft(draft, null)).sort()).toEqual([
			"clientId",
			"projectId",
		]);
		expect(
			validateDraft({ ...draft, clientId: "cid", projectId: "proj-1" }, null),
		).toEqual({});
		expect(
			validateDraft(
				{ ...draft, clientId: "cid", projectId: "proj-1", siteUrl: "nope" },
				null,
			).siteUrl,
		).toBeDefined();
	});

	test("a certificate must be PEM", () => {
		const draft = {
			...draftFor(vault),
			caCert: "not a cert",
		};
		expect(validateDraft(draft, vault).caCert).toBeDefined();
	});
});

describe("editing keeps the stored credential", () => {
	test("an unchanged identity needs no credential", () => {
		const draft = draftFor(vault);
		expect(needsSecret(draft, vault)).toBe(false);
		expect(validateDraft(draft, vault)).toEqual({});
	});

	test("another mount is fine without the credential", () => {
		expect(needsSecret({ ...draftFor(vault), mount: "kv" }, vault)).toBe(false);
	});

	test("another address or auth method asks for it again", () => {
		expect(
			needsSecret(
				{ ...draftFor(vault), address: "https://other.example.com" },
				vault,
			),
		).toBe(true);
		expect(
			needsSecret({ ...draftFor(vault), authMethod: "approle" }, vault),
		).toBe(true);
		const errors = validateDraft(
			{ ...draftFor(vault), address: "https://other.example.com" },
			vault,
		);
		expect(errors.secret).toContain("again");
	});

	test("a trailing slash is not a change", () => {
		expect(
			needsSecret(
				{ ...draftFor(vault), address: "https://bao.example.com:8200/" },
				vault,
			),
		).toBe(false);
	});
});

describe("toBody", () => {
	test("leaves out an empty secret and the other kind's fields", () => {
		const body = toBody({ ...draftFor(vault), mount: "kv" });
		expect(body.secret).toBeUndefined();
		expect(body.mount).toBe("kv");
		expect(body.clientId).toBeUndefined();
		expect(body.roleId).toBeUndefined();
	});

	test("sends the secret once it is typed", () => {
		expect(toBody({ ...draftFor(vault), secret: "s.new" }).secret).toBe(
			"s.new",
		);
	});

	test("AppRole carries the role id", () => {
		const body = toBody({
			...emptyDraft("vault"),
			address: "https://x",
			authMethod: "approle",
			roleId: " r1 ",
		});
		expect(body.roleId).toBe("r1");
	});
});

describe("helpers", () => {
	test("isBaseUrl", () => {
		expect(isBaseUrl("https://a.example.com:8200")).toBe(true);
		expect(isBaseUrl("a.example.com")).toBe(false);
		expect(isBaseUrl("https://u:p@a.example.com")).toBe(false);
		expect(isBaseUrl("https://a.example.com?x=1")).toBe(false);
	});

	test("isInsecureAddress warns about plain http off this machine", () => {
		expect(isInsecureAddress("http://bao.internal:8200")).toBe(true);
		expect(isInsecureAddress("http://localhost:8200")).toBe(false);
		expect(isInsecureAddress("https://bao.example.com")).toBe(false);
	});

	test("whereOf names the host", () => {
		expect(whereOf(vault)).toBe("bao.example.com:8200");
	});
});

const doppler: SecretManager = {
	...vault,
	id: "2",
	name: "dp",
	kind: "doppler",
	config: { defaultProject: "backend", defaultConfig: "prd" },
};

describe("Doppler", () => {
	test("a new Doppler integration needs a name and the token only", () => {
		const errors = validateDraft(emptyDraft("doppler"), null);
		expect(Object.keys(errors).sort()).toEqual(["name", "secret"]);
		expect(
			validateDraft(
				{ ...emptyDraft("doppler"), name: "dp", secret: "dp.st.x" },
				null,
			),
		).toEqual({});
	});

	test("the default project and config come together", () => {
		const base = { ...emptyDraft("doppler"), name: "dp", secret: "t" };
		expect(
			validateDraft({ ...base, defaultProject: "backend" }, null).defaultConfig,
		).toBeDefined();
		expect(
			validateDraft({ ...base, defaultConfig: "prd" }, null).defaultProject,
		).toBeDefined();
		expect(
			validateDraft(
				{ ...base, defaultProject: "back end", defaultConfig: "prd" },
				null,
			).defaultProject,
		).toBeDefined();
		expect(
			validateDraft(
				{ ...base, defaultProject: "backend", defaultConfig: "prd_us" },
				null,
			),
		).toEqual({});
		expect(
			validateDraft({ ...base, siteUrl: "nope" }, null).siteUrl,
		).toBeDefined();
	});

	test("changing the defaults keeps the token, changing the API URL asks for it again", () => {
		expect(
			needsSecret({ ...draftFor(doppler), defaultConfig: "stg" }, doppler),
		).toBe(false);
		expect(
			needsSecret(
				{ ...draftFor(doppler), siteUrl: "https://doppler.internal" },
				doppler,
			),
		).toBe(true);
		expect(
			needsSecret(
				{ ...draftFor(doppler), siteUrl: "https://api.doppler.com/" },
				doppler,
			),
		).toBe(false);
	});

	test("the body carries the Doppler fields only", () => {
		const body = toBody({ ...draftFor(doppler), secret: "dp.st.new" });
		expect(body.defaultProject).toBe("backend");
		expect(body.defaultConfig).toBe("prd");
		expect(body.secret).toBe("dp.st.new");
		expect(body.address).toBeUndefined();
		expect(body.clientId).toBeUndefined();
		expect(whereOf(doppler)).toBe("api.doppler.com");
	});
});

describe("scope", () => {
	const shop = "project-shop";
	const prod = "env-prod";
	const stg = "env-stg";
	const siblings = [prod, stg];

	test("a new integration is available to the whole team", () => {
		const draft = emptyDraft();
		expect(draft.scope).toBe("team");
		expect(toBody({ ...draft, address: "https://x" }).scope).toBe("team");
		expect(toBody({ ...draft, address: "https://x" }).grants).toEqual([]);
	});

	test("selected projects need at least one grant", () => {
		const base = {
			...emptyDraft("doppler"),
			name: "dp",
			secret: "t",
			scope: "selected" as const,
		};
		expect(validateDraft(base, null).grants).toBeDefined();
		expect(
			validateDraft(
				{ ...base, grants: [{ projectId: shop, environmentId: null }] },
				null,
			),
		).toEqual({});
	});

	test("the body sends grants only for a selected scope", () => {
		const grants = [
			{ projectId: shop, environmentId: null },
			{ projectId: "other", environmentId: "e1" },
		];
		const selected = toBody({
			...draftFor(doppler),
			scope: "selected",
			grants,
		});
		expect(selected.scope).toBe("selected");
		expect(selected.grants).toEqual([
			{ projectId: shop },
			{ projectId: "other", environmentId: "e1" },
		]);
		expect(
			toBody({ ...draftFor(doppler), scope: "team", grants }).grants,
		).toEqual([]);
	});

	test("allowing a project covers its environments; forbidding clears them", () => {
		let grants = toggleProject([], shop, true);
		expect(isProjectGranted(grants, shop)).toBe(true);
		expect(isEnvironmentGranted(grants, shop, prod)).toBe(true);
		expect(isEnvironmentGranted(grants, "other", prod)).toBe(false);
		grants = toggleProject(grants, shop, false);
		expect(grants).toEqual([]);
	});

	test("one environment of an allowed project can be taken out", () => {
		const whole = toggleProject([], shop, true);
		const without = toggleEnvironment(whole, shop, stg, false, siblings);
		expect(without).toEqual([{ projectId: shop, environmentId: prod }]);
		expect(isProjectGranted(without, shop)).toBe(false);
		expect(isEnvironmentGranted(without, shop, stg)).toBe(false);
	});

	test("allowing every environment folds back into the whole project", () => {
		let grants = toggleEnvironment([], shop, prod, true, siblings);
		expect(grants).toEqual([{ projectId: shop, environmentId: prod }]);
		grants = toggleEnvironment(grants, shop, stg, true, siblings);
		expect(grants).toEqual([{ projectId: shop, environmentId: null }]);
		grants = toggleEnvironment(grants, shop, prod, false, siblings);
		grants = toggleEnvironment(grants, shop, stg, false, siblings);
		expect(grants).toEqual([]);
	});

	test("other projects' grants are left alone", () => {
		const grants = [{ projectId: "blog", environmentId: null }];
		expect(toggleEnvironment(grants, shop, prod, true, siblings)).toEqual([
			{ projectId: "blog", environmentId: null },
			{ projectId: shop, environmentId: prod },
		]);
	});

	test("the summary names the reach", () => {
		expect(scopeSummary(vault)).toBe("Whole team");
		const sel = (grants: SecretManager["grants"]): SecretManager => ({
			...vault,
			scope: "selected",
			grants,
		});
		expect(scopeSummary(sel([]))).toBe("No project");
		expect(scopeSummary(sel([{ projectId: "a" }, { projectId: "b" }]))).toBe(
			"2 projects",
		);
		expect(
			scopeSummary(
				sel([{ projectId: "a" }, { projectId: "b", environmentId: "e" }]),
			),
		).toBe("1 project, 1 environment");
	});
});
