import type { ProviderBody, ProviderInputBody } from "#/api/gen/types.gen";
import { type ProviderPreset, placeholderProblem } from "./provider-presets";

export type ProviderKind = "oidc" | "github";

export interface ProviderFormState {
	kind: ProviderKind;
	name: string;
	issuerUrl: string;
	clientId: string;
	/** Write-only: empty on an existing provider keeps the stored secret. */
	clientSecret: string;
	/** Extra scopes beyond openid, email and profile. */
	scopes: string;
	allowedDomains: string;
	trustMfa: boolean;
	enabled: boolean;
}

export const EMPTY_PROVIDER_FORM: ProviderFormState = {
	kind: "oidc",
	name: "",
	issuerUrl: "",
	clientId: "",
	clientSecret: "",
	scopes: "",
	allowedDomains: "",
	trustMfa: false,
	enabled: true,
};

/** Splits free text on commas, whitespace and newlines, dropping blanks and repeats. */
export function parseList(text: string, lower = false): string[] {
	const seen = new Set<string>();
	const out: string[] = [];
	for (const raw of text.split(/[\s,;]+/)) {
		const item = lower ? raw.toLowerCase() : raw;
		if (item !== "" && !seen.has(item)) {
			seen.add(item);
			out.push(item);
		}
	}
	return out;
}

/** `@Example.com, corp.example` -> `example.com`, `corp.example`. */
export function parseDomains(text: string): string[] {
	return parseList(text.replace(/@/g, ""), true);
}

export function formState(provider: ProviderBody): ProviderFormState {
	return {
		kind: provider.kind,
		name: provider.name,
		issuerUrl: provider.issuerUrl,
		clientId: provider.clientId,
		clientSecret: "",
		scopes: (provider.scopes ?? []).join(" "),
		allowedDomains: (provider.allowedDomains ?? []).join(", "),
		trustMfa: provider.trustMfa,
		enabled: provider.enabled,
	};
}

/** The first thing wrong with the form, in the words the person would use. */
export function providerProblem(
	form: ProviderFormState,
	editing: boolean,
	preset?: ProviderPreset,
): string | undefined {
	if (form.name.trim() === "") return "Give the provider a name.";
	if (form.clientId.trim() === "") return "Enter the client ID.";
	if (!editing && form.clientSecret.trim() === "") {
		return "Enter the client secret.";
	}
	const issuer = form.issuerUrl.trim();
	if (form.kind === "oidc") {
		if (issuer === "") return "Enter the issuer URL.";
	}
	const unreplaced = placeholderProblem(issuer, preset);
	if (unreplaced) return unreplaced;
	if (issuer !== "" && !/^https?:\/\/\S+$/i.test(issuer)) {
		return "The URL must start with https://.";
	}
	return undefined;
}

/**
 * The request body. `id` is the new provider's pre-assigned ID (create only),
 * which is what makes its callback URL known before it is saved.
 */
export function providerBody(
	form: ProviderFormState,
	id?: string,
): ProviderInputBody {
	return {
		...(id ? { id } : {}),
		kind: form.kind,
		name: form.name.trim(),
		// An OIDC issuer must match its discovery document exactly (some end in "/").
		issuerUrl:
			form.kind === "github"
				? form.issuerUrl.trim().replace(/\/+$/, "")
				: form.issuerUrl.trim(),
		clientId: form.clientId.trim(),
		...(form.clientSecret.trim() !== ""
			? { clientSecret: form.clientSecret.trim() }
			: {}),
		scopes: parseList(form.scopes),
		allowedDomains: parseDomains(form.allowedDomains),
		trustMfa: form.kind === "oidc" && form.trustMfa,
		enabled: form.enabled,
	};
}

/** The callback URL to register at the identity provider for the provider with this ID. */
export function expectedRedirectUri(origin: string, id: string): string {
	return `${origin.replace(/\/+$/, "")}/api/v1/auth/oidc/${id}/callback`;
}

/**
 * A random version-4 UUID for a provider that is not saved yet. Not
 * `crypto.randomUUID()`, which browsers withhold on plain http, where a
 * self-hosted instance may well be reached.
 */
export function newProviderId(): string {
	const bytes = new Uint8Array(16);
	crypto.getRandomValues(bytes);
	bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40;
	bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80;
	const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0"));
	return [
		hex.slice(0, 4).join(""),
		hex.slice(4, 6).join(""),
		hex.slice(6, 8).join(""),
		hex.slice(8, 10).join(""),
		hex.slice(10).join(""),
	].join("-");
}
