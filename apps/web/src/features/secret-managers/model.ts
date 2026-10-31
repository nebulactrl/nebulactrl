// The form of a secret manager integration: draft, validation and the request
// bodies. Pure; the dialog renders it. The checks mirror the server's, so a
// bad value is caught before the request.

import type { SecretManager, SecretManagerBody } from "#/api/gen/types.gen";
import {
	DOPPLER_DEFAULT_URL,
	INFISICAL_DEFAULT_URL,
	type SecretManagerKind,
	type VaultAuth,
} from "#/lib/secret-manager-guides";

/** Who may reference an integration: the whole team, or only the selected projects and environments. */
export type IntegrationScope = "team" | "selected";

/** One allowed project (every environment, `environmentId` null) or one environment of it. */
export interface Grant {
	projectId: string;
	environmentId: string | null;
}

export interface Draft {
	name: string;
	kind: SecretManagerKind;
	// Vault / OpenBao
	address: string;
	mount: string;
	namespace: string;
	authMethod: VaultAuth;
	roleId: string;
	approleMount: string;
	caCert: string;
	// Infisical (and the API URL of Doppler)
	siteUrl: string;
	clientId: string;
	projectId: string;
	organizationSlug: string;
	// Doppler
	defaultProject: string;
	defaultConfig: string;
	/** Write-only: the token, the AppRole secret id, the client secret or the Doppler token. */
	secret: string;
	scope: IntegrationScope;
	grants: Grant[];
}

export type DraftField = keyof Draft;

const NAME = /^[a-z0-9][a-z0-9-]{0,62}$/;
const MOUNT = /^[A-Za-z0-9._-]+(\/[A-Za-z0-9._-]+)*$/;
const IDENT = /^[A-Za-z0-9._@:-]{1,256}$/;
const PROJECT = /^[A-Za-z0-9-]{1,64}$/;
const DOPPLER_SEGMENT = /^[A-Za-z0-9._-]{1,64}$/;

export function emptyDraft(kind: SecretManagerKind = "vault"): Draft {
	return {
		name: "",
		kind,
		address: "",
		mount: "",
		namespace: "",
		authMethod: "token",
		roleId: "",
		approleMount: "",
		caCert: "",
		siteUrl: "",
		clientId: "",
		projectId: "",
		organizationSlug: "",
		defaultProject: "",
		defaultConfig: "",
		secret: "",
		scope: "team",
		grants: [],
	};
}

export function draftFor(sm: SecretManager | null): Draft {
	if (sm === null) return emptyDraft();
	const c = sm.config;
	return {
		name: sm.name,
		kind: sm.kind,
		address: c.address ?? "",
		mount: c.mount ?? "",
		namespace: c.namespace ?? "",
		authMethod: c.authMethod ?? "token",
		roleId: c.roleId ?? "",
		approleMount: c.approleMount ?? "",
		caCert: c.caCert ?? "",
		siteUrl: c.siteUrl ?? "",
		clientId: c.clientId ?? "",
		projectId: c.projectId ?? "",
		organizationSlug: c.organizationSlug ?? "",
		defaultProject: c.defaultProject ?? "",
		defaultConfig: c.defaultConfig ?? "",
		secret: "",
		scope: sm.scope ?? "team",
		grants: (sm.grants ?? []).map((g) => ({
			projectId: g.projectId,
			environmentId: g.environmentId ?? null,
		})),
	};
}

/** An http(s) URL with a host and no credentials, query or fragment. */
export function isBaseUrl(raw: string): boolean {
	try {
		const url = new URL(raw.trim());
		return (
			(url.protocol === "http:" || url.protocol === "https:") &&
			url.host !== "" &&
			url.username === "" &&
			url.password === "" &&
			url.search === "" &&
			url.hash === ""
		);
	} catch {
		return false;
	}
}

/** Plain http to anything but this machine sends the credential in the clear. */
export function isInsecureAddress(raw: string): boolean {
	try {
		const url = new URL(raw.trim());
		if (url.protocol !== "http:") return false;
		const host = url.hostname;
		return !(host === "localhost" || host === "127.0.0.1" || host === "[::1]");
	} catch {
		return false;
	}
}

/**
 * True when the stored credential no longer fits the form: the address,
 * identity or auth method changed. The server then wants it again.
 */
export function needsSecret(
	draft: Draft,
	editing: SecretManager | null,
): boolean {
	if (editing === null) return true;
	const was = draftFor(editing);
	if (draft.kind === "infisical" || draft.kind === "doppler") {
		const fallback =
			draft.kind === "doppler" ? DOPPLER_DEFAULT_URL : INFISICAL_DEFAULT_URL;
		const site = (v: string) =>
			v.trim() === "" ? fallback : v.trim().replace(/\/+$/, "");
		if (site(draft.siteUrl) !== site(was.siteUrl)) return true;
		// A Doppler token is bound to the API URL only; the defaults are not an identity.
		return draft.kind === "infisical" && draft.clientId.trim() !== was.clientId;
	}
	const addr = (v: string) => v.trim().replace(/\/+$/, "");
	const ns = (v: string) => v.trim().replace(/^\/+|\/+$/g, "");
	const mountOf = (v: string) => (v.trim() === "" ? "approle" : v.trim());
	return (
		addr(draft.address) !== addr(was.address) ||
		draft.authMethod !== was.authMethod ||
		ns(draft.namespace) !== ns(was.namespace) ||
		(draft.authMethod === "approle" &&
			(draft.roleId.trim() !== was.roleId ||
				mountOf(draft.approleMount) !== mountOf(was.approleMount)))
	);
}

/** The first problem of each field, in plain words; empty when the form is valid. */
export function validateDraft(
	draft: Draft,
	editing: SecretManager | null,
): Partial<Record<DraftField, string>> {
	const errors: Partial<Record<DraftField, string>> = {};
	if (editing === null && !NAME.test(draft.name.trim())) {
		errors.name =
			"Use 1 to 63 lowercase letters, digits and dashes. It appears in references.";
	}
	if (draft.kind === "vault") {
		if (!isBaseUrl(draft.address)) {
			errors.address = "Enter a full URL, like https://vault.example.com:8200.";
		}
		const mount = draft.mount.trim().replace(/^\/+|\/+$/g, "");
		if (mount !== "" && (!MOUNT.test(mount) || mount.includes(".."))) {
			errors.mount = "A mount path such as secret or kv/team.";
		}
		if (draft.authMethod === "approle") {
			if (!IDENT.test(draft.roleId.trim()))
				errors.roleId = "Enter the role id.";
			const am = draft.approleMount.trim().replace(/^\/+|\/+$/g, "");
			if (am !== "" && (!MOUNT.test(am) || am.includes(".."))) {
				errors.approleMount =
					"The path of the AppRole auth method, like approle.";
			}
		}
		const pem = draft.caCert.trim();
		if (pem !== "" && !pem.includes("-----BEGIN CERTIFICATE-----")) {
			errors.caCert = "Paste a PEM certificate: -----BEGIN CERTIFICATE-----";
		}
	} else if (draft.kind === "doppler") {
		if (draft.siteUrl.trim() !== "" && !isBaseUrl(draft.siteUrl)) {
			errors.siteUrl = "Enter a full URL, like https://api.doppler.com.";
		}
		const project = draft.defaultProject.trim();
		const config = draft.defaultConfig.trim();
		if ((project === "") !== (config === "")) {
			const field = project === "" ? "defaultProject" : "defaultConfig";
			errors[field] = "Set both the default project and config, or neither.";
		}
		for (const [field, value] of [
			["defaultProject", project],
			["defaultConfig", config],
		] as const) {
			if (
				value !== "" &&
				(!DOPPLER_SEGMENT.test(value) || value === "." || value === "..")
			) {
				errors[field] = "Letters, digits and . _ - only, up to 64 characters.";
			}
		}
	} else {
		if (draft.siteUrl.trim() !== "" && !isBaseUrl(draft.siteUrl)) {
			errors.siteUrl = "Enter a full URL, like https://infisical.example.com.";
		}
		if (!IDENT.test(draft.clientId.trim())) {
			errors.clientId = "Enter the machine identity's client id.";
		}
		if (!PROJECT.test(draft.projectId.trim())) {
			errors.projectId = "Enter the project id (letters, digits and dashes).";
		}
	}
	if (draft.scope === "selected" && draft.grants.length === 0) {
		errors.grants =
			"Choose at least one project or environment, or make it available to the whole team.";
	}
	if (needsSecret(draft, editing) && draft.secret === "") {
		errors.secret =
			editing === null
				? "Paste the credential."
				: "Paste the credential again: the address or identity changed.";
	}
	if (/[\r\n]/.test(draft.secret))
		errors.secret = "The credential is a single line.";
	return errors;
}

/** The request body: only the fields of the draft's kind; an empty secret is left out. */
export function toBody(draft: Draft): SecretManagerBody {
	let body: SecretManagerBody;
	switch (draft.kind) {
		case "vault":
			body = {
				address: draft.address.trim(),
				mount: draft.mount.trim(),
				namespace: draft.namespace.trim(),
				authMethod: draft.authMethod,
				...(draft.authMethod === "approle"
					? {
							roleId: draft.roleId.trim(),
							approleMount: draft.approleMount.trim(),
						}
					: {}),
				caCert: draft.caCert.trim(),
			};
			break;
		case "doppler":
			body = {
				siteUrl: draft.siteUrl.trim(),
				defaultProject: draft.defaultProject.trim(),
				defaultConfig: draft.defaultConfig.trim(),
			};
			break;
		default:
			body = {
				siteUrl: draft.siteUrl.trim(),
				clientId: draft.clientId.trim(),
				projectId: draft.projectId.trim(),
				organizationSlug: draft.organizationSlug.trim(),
			};
	}
	body.scope = draft.scope;
	body.grants =
		draft.scope === "selected"
			? draft.grants.map((g) =>
					g.environmentId === null
						? { projectId: g.projectId }
						: { projectId: g.projectId, environmentId: g.environmentId },
				)
			: [];
	if (draft.secret !== "") body.secret = draft.secret;
	return body;
}

/** "bao.example.com:8200" or the Infisical / Doppler URL: where the integration points. */
export function whereOf(sm: SecretManager): string {
	const raw =
		sm.kind === "vault"
			? sm.config.address
			: sm.config.siteUrl ||
				(sm.kind === "doppler" ? DOPPLER_DEFAULT_URL : INFISICAL_DEFAULT_URL);
	if (!raw) return "";
	try {
		return new URL(raw).host;
	} catch {
		return raw;
	}
}

export const KIND_LABEL: Record<SecretManagerKind, string> = {
	vault: "Vault / OpenBao",
	infisical: "Infisical",
	doppler: "Doppler",
};

// ------------------------------------------------------------------- scope

/** True when the grants allow the whole project (every environment, also later ones). */
export function isProjectGranted(grants: readonly Grant[], projectId: string) {
	return grants.some(
		(g) => g.projectId === projectId && g.environmentId === null,
	);
}

/** True when the environment is allowed, directly or through its project. */
export function isEnvironmentGranted(
	grants: readonly Grant[],
	projectId: string,
	environmentId: string,
) {
	return grants.some(
		(g) =>
			g.projectId === projectId &&
			(g.environmentId === null || g.environmentId === environmentId),
	);
}

/** Allow or forbid a whole project; forbidding clears its environments too. */
export function toggleProject(
	grants: readonly Grant[],
	projectId: string,
	on: boolean,
): Grant[] {
	const rest = grants.filter((g) => g.projectId !== projectId);
	return on ? [...rest, { projectId, environmentId: null }] : rest;
}

/**
 * Allow or forbid one environment. Forbidding one of a fully allowed project
 * keeps the others (listed in `siblings`); allowing the last missing one folds
 * back into "the whole project", so a later environment is covered too.
 */
export function toggleEnvironment(
	grants: readonly Grant[],
	projectId: string,
	environmentId: string,
	on: boolean,
	siblings: readonly string[],
): Grant[] {
	const others = grants.filter((g) => g.projectId !== projectId);
	const chosen = new Set<string>();
	if (isProjectGranted(grants, projectId)) {
		for (const id of siblings) chosen.add(id);
	} else {
		for (const g of grants) {
			if (g.projectId === projectId && g.environmentId !== null)
				chosen.add(g.environmentId);
		}
	}
	if (on) chosen.add(environmentId);
	else chosen.delete(environmentId);
	if (chosen.size === 0) return others;
	if (siblings.length > 0 && siblings.every((id) => chosen.has(id))) {
		return [...others, { projectId, environmentId: null }];
	}
	return [
		...others,
		...[...chosen].map((id) => ({ projectId, environmentId: id })),
	];
}

/** "Whole team", "2 projects" or "1 project, 2 environments": the scope of a row. */
export function scopeSummary(sm: SecretManager): string {
	if (sm.scope !== "selected") return "Whole team";
	const grants = sm.grants ?? [];
	const projects = grants.filter((g) => !g.environmentId).length;
	const environments = grants.length - projects;
	const parts: string[] = [];
	if (projects > 0)
		parts.push(`${projects} project${projects === 1 ? "" : "s"}`);
	if (environments > 0)
		parts.push(`${environments} environment${environments === 1 ? "" : "s"}`);
	return parts.length > 0 ? parts.join(", ") : "No project";
}
