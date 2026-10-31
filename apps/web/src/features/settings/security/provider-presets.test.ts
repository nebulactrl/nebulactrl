import { describe, expect, test } from "bun:test";
import { EMPTY_PROVIDER_FORM, providerProblem } from "./provider-form";
import {
	applyPreset,
	consoleUrlFor,
	firstPlaceholderRange,
	issuerParts,
	issuerWarning,
	PRESETS,
	placeholderHints,
	placeholdersIn,
	presetById,
	presetForProvider,
} from "./provider-presets";

describe("PRESETS", () => {
	test("offers every provider the owner asked for, once each", () => {
		expect(PRESETS.map((preset) => preset.id)).toEqual([
			"google",
			"entra",
			"okta",
			"auth0",
			"authentik",
			"keycloak",
			"zitadel",
			"pocket-id",
			"gitlab",
			"github",
			"oidc",
		]);
	});

	test("issuer patterns are the documented ones", () => {
		const issuer = (id: Parameters<typeof presetById>[0]) =>
			presetById(id).issuer;
		expect(issuer("google")).toBe("https://accounts.google.com");
		expect(issuer("entra")).toBe(
			"https://login.microsoftonline.com/{tenant}/v2.0",
		);
		expect(issuer("okta")).toBe("https://{domain}/oauth2/default");
		// Auth0 and authentik report their issuer with a trailing slash.
		expect(issuer("auth0")).toBe("https://{domain}/");
		expect(issuer("authentik")).toBe("https://{domain}/application/o/{slug}/");
		expect(issuer("keycloak")).toBe("https://{domain}/realms/{realm}");
		expect(issuer("zitadel")).toBe("https://{domain}");
		expect(issuer("pocket-id")).toBe("https://{domain}");
		expect(issuer("gitlab")).toBe("https://gitlab.com");
		expect(issuer("github")).toBe("");
		expect(issuer("oidc")).toBe("");
	});

	test("GitHub is OAuth, everything else OpenID Connect", () => {
		for (const preset of PRESETS) {
			expect(preset.kind).toBe(preset.id === "github" ? "github" : "oidc");
		}
	});

	test("every placeholder in an issuer is explained, and nothing else is", () => {
		for (const preset of PRESETS) {
			expect(Object.keys(preset.placeholders).sort()).toEqual(
				placeholdersIn(preset.issuer).sort(),
			);
		}
	});

	test("every preset says where the redirect URI goes and links its docs over https", () => {
		for (const preset of PRESETS) {
			expect(preset.redirectField).not.toBe("");
			expect(preset.note).not.toBe("");
			expect(preset.docsUrl.startsWith("https://")).toBe(true);
			if (preset.consoleUrl) {
				expect(preset.consoleUrl.startsWith("https://")).toBe(true);
			}
		}
	});

	test("the Entra preset tells people about xms_edov, which the server needs", () => {
		expect(presetById("entra").note).toContain("xms_edov");
	});
});

describe("placeholders", () => {
	test("placeholdersIn lists each {name} once, in order", () => {
		expect(placeholdersIn("https://{domain}/realms/{realm}")).toEqual([
			"domain",
			"realm",
		]);
		expect(placeholdersIn("https://{domain}/{domain}")).toEqual(["domain"]);
		expect(placeholdersIn("https://idp.example.com")).toEqual([]);
	});

	test("issuerParts splits into plain runs and highlighted tokens", () => {
		expect(issuerParts("https://{domain}/realms/{realm}")).toEqual([
			{ start: 0, text: "https://", placeholder: false },
			{ start: 8, text: "{domain}", placeholder: true },
			{ start: 16, text: "/realms/", placeholder: false },
			{ start: 24, text: "{realm}", placeholder: true },
		]);
		expect(issuerParts("")).toEqual([]);
	});

	test("firstPlaceholderRange selects the first token", () => {
		const text = "https://login.microsoftonline.com/{tenant}/v2.0";
		const range = firstPlaceholderRange(text);
		expect(range).toBeDefined();
		const [start, end] = range ?? [0, 0];
		expect(text.slice(start, end)).toBe("{tenant}");
		expect(
			firstPlaceholderRange("https://accounts.google.com"),
		).toBeUndefined();
	});

	test("placeholderHints carries the preset's explanation", () => {
		const hints = placeholderHints(
			"https://{domain}/application/o/{slug}/",
			presetById("authentik"),
		);
		expect(hints.map((hint) => hint.name)).toEqual(["domain", "slug"]);
		expect(hints.every((hint) => hint.hint)).toBe(true);
		expect(placeholderHints("https://{domain}")[0]?.hint).toBeUndefined();
	});
});

describe("applyPreset", () => {
	test("prefills kind, name, issuer pattern and scopes, and keeps credentials", () => {
		const typed = {
			...EMPTY_PROVIDER_FORM,
			clientId: "abc",
			clientSecret: "s3cret",
			allowedDomains: "corp.example",
			trustMfa: true,
		};
		const next = applyPreset(typed, presetById("entra"));
		expect(next.kind).toBe("oidc");
		expect(next.name).toBe("Microsoft");
		expect(next.issuerUrl).toBe(
			"https://login.microsoftonline.com/{tenant}/v2.0",
		);
		expect(next.clientId).toBe("abc");
		expect(next.clientSecret).toBe("s3cret");
		expect(next.allowedDomains).toBe("corp.example");
		expect(next.trustMfa).toBe(true);
	});

	test("GitHub clears the issuer and never trusts MFA", () => {
		const next = applyPreset(
			{
				...EMPTY_PROVIDER_FORM,
				issuerUrl: "https://{domain}",
				trustMfa: true,
			},
			presetById("github"),
		);
		expect(next.kind).toBe("github");
		expect(next.issuerUrl).toBe("");
		expect(next.trustMfa).toBe(false);
	});
});

describe("placeholder validation blocks saving", () => {
	const filled = {
		...EMPTY_PROVIDER_FORM,
		name: "Microsoft",
		clientId: "id",
		clientSecret: "secret",
	};

	test("an unreplaced {tenant} is a problem that names it and says what it is", () => {
		const entra = presetById("entra");
		const form = applyPreset(filled, entra);
		const problem = providerProblem(form, false, entra);
		expect(problem).toContain("{tenant}");
		expect(problem).toContain("Directory (tenant) ID");
	});

	test("replacing it clears the problem", () => {
		const entra = presetById("entra");
		const form = {
			...applyPreset(filled, entra),
			issuerUrl:
				"https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/v2.0",
		};
		expect(providerProblem(form, false, entra)).toBeUndefined();
	});

	test("names every placeholder still left, even without a preset", () => {
		const keycloak = applyPreset(filled, presetById("keycloak"));
		const partly = {
			...keycloak,
			issuerUrl: "https://kc.example.com/realms/{realm}",
		};
		expect(providerProblem(partly, false)).toContain("{realm}");
		expect(providerProblem(partly, false)).not.toContain("{domain}");
		expect(providerProblem(keycloak, false)).toContain("{domain} and {realm}");
	});

	test("{realm} left in while editing blocks the save too", () => {
		const form = { ...filled, issuerUrl: "https://kc.example.com/{realm}" };
		expect(providerProblem(form, true)).toContain("{realm}");
	});

	test("presets without placeholders pass as they are", () => {
		const google = applyPreset(filled, presetById("google"));
		expect(providerProblem(google, false)).toBeUndefined();
		const github = applyPreset(filled, presetById("github"));
		expect(providerProblem(github, false)).toBeUndefined();
	});
});

describe("issuerWarning", () => {
	test("Entra's common, organizations and consumers never match its discovery issuer", () => {
		for (const tenant of ["common", "organizations", "consumers", "Common"]) {
			expect(
				issuerWarning(
					"entra",
					`https://login.microsoftonline.com/${tenant}/v2.0`,
				),
			).toContain("Directory (tenant) ID");
		}
	});
	test("a concrete tenant, other presets and an unfilled pattern are fine", () => {
		expect(
			issuerWarning(
				"entra",
				"https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/v2.0",
			),
		).toBeUndefined();
		expect(
			issuerWarning("entra", "https://login.microsoftonline.com/{tenant}/v2.0"),
		).toBeUndefined();
		expect(
			issuerWarning("okta", "https://login.microsoftonline.com/common/v2.0"),
		).toBeUndefined();
	});
});

describe("consoleUrlFor", () => {
	test("SaaS providers link their own page", () => {
		expect(consoleUrlFor(presetById("google"), "")).toBe(
			"https://console.developers.google.com/auth/clients",
		);
		expect(consoleUrlFor(presetById("github"), "")).toBe(
			"https://github.com/settings/developers",
		);
	});
	test("self-hosted providers have no page to open", () => {
		expect(
			consoleUrlFor(presetById("keycloak"), "https://kc.example.com"),
		).toBeNull();
	});
	test("GitLab follows a self-managed issuer once it is a real URL", () => {
		const gitlab = presetById("gitlab");
		expect(consoleUrlFor(gitlab, "https://gitlab.com")).toBe(
			"https://gitlab.com/-/user_settings/applications",
		);
		expect(consoleUrlFor(gitlab, "https://git.corp.example/")).toBe(
			"https://git.corp.example/-/user_settings/applications",
		);
		expect(consoleUrlFor(gitlab, "https://{domain}")).toBe(
			"https://gitlab.com/-/user_settings/applications",
		);
		expect(consoleUrlFor(gitlab, "not a url")).toBe(
			"https://gitlab.com/-/user_settings/applications",
		);
	});
});

describe("presetForProvider", () => {
	test("recognises a saved provider by issuer host", () => {
		expect(presetForProvider("oidc", "https://accounts.google.com").id).toBe(
			"google",
		);
		expect(
			presetForProvider("oidc", "https://login.microsoftonline.com/x/v2.0").id,
		).toBe("entra");
		expect(
			presetForProvider("oidc", "https://dev-1.okta.com/oauth2/default").id,
		).toBe("okta");
		expect(presetForProvider("oidc", "https://acme.eu.auth0.com/").id).toBe(
			"auth0",
		);
		expect(presetForProvider("github", "").id).toBe("github");
	});
	test("anything else is a generic OpenID Connect provider", () => {
		expect(
			presetForProvider("oidc", "https://kc.example.com/realms/a").id,
		).toBe("oidc");
		expect(presetForProvider("oidc", "nonsense").id).toBe("oidc");
	});
});
