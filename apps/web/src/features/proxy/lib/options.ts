// RouteOptions <-> form state for the per-domain editor. The form keeps text
// for every number and list so half-typed values survive; `formToOptions`
// turns it back into the wire shape (omitting defaults, since the backend
// treats `{}` as "no middleware" and PATCH replaces the whole set).

import type { RouteOptions } from "#/api/gen/types.gen";
import { checkRules, cleanList } from "#/features/access-rules/rules";
import {
	MAX_CORS_ORIGINS,
	MAX_HEADERS,
	MAX_REDIRECTS,
	validateCorsHeaderName,
	validateCorsMethod,
	validateCorsOrigin,
	validateHeaderName,
	validateHeaderValue,
	validateRedirectRegex,
	validateRedirectReplacement,
} from "./validate";

/** The editor's collapsible groups; each one saves only its own option fields. */
export type OptionGroup = "access" | "headers" | "redirects" | "limits" | "tls";

export const OPTION_GROUPS: readonly OptionGroup[] = [
	"access",
	"headers",
	"redirects",
	"limits",
	"tls",
];

export const GROUP_TITLE: Record<OptionGroup, string> = {
	access: "Access",
	headers: "Headers & CORS",
	redirects: "Redirects",
	limits: "Limits",
	tls: "TLS",
};

/** The RouteOptions keys each group owns (TLS also owns the domain's tlsMode and pinned certificate). */
export const GROUP_KEYS: Record<OptionGroup, readonly (keyof RouteOptions)[]> =
	{
		access: ["basicAuth", "access"],
		headers: [
			"requestHeaders",
			"responseHeaders",
			"cors",
			"securityHeaders",
			"noindex",
		],
		redirects: ["redirects"],
		limits: ["rateLimit", "compress"],
		tls: ["forceHttps"],
	};

export type SecurityHeaders = "" | "basic" | "strict";

export interface HeaderRow {
	name: string;
	value: string;
}

export interface RedirectRow {
	regex: string;
	replacement: string;
	permanent: boolean;
}

export interface OptionsForm {
	basicAuth: boolean;
	/** IP access rules are on: the allow and deny lists below apply. */
	accessOn: boolean;
	allowCidrs: string[];
	denyCidrs: string[];
	accessDepth: string;
	rejectStatus: string;
	rateOn: boolean;
	rateAverage: string;
	ratePeriod: string;
	rateBurst: string;
	rateDepth: string;
	compress: boolean;
	requestHeaders: HeaderRow[];
	responseHeaders: HeaderRow[];
	securityHeaders: SecurityHeaders;
	noindex: boolean;
	corsOn: boolean;
	corsOrigins: string;
	corsMethods: string;
	corsHeaders: string;
	corsExpose: string;
	corsCredentials: boolean;
	corsMaxAge: string;
	redirects: RedirectRow[];
	forceHttps: boolean;
}

export const RATE_DEFAULTS = { average: "50", period: "1", burst: "100" };

/** Split a comma/space/newline separated list, dropping blanks. */
export function splitList(text: string): string[] {
	return text
		.split(/[\s,]+/)
		.map((part) => part.trim())
		.filter((part) => part !== "");
}

const joinList = (items: readonly string[] | null | undefined): string =>
	(items ?? []).join(", ");

export function toForm(options: RouteOptions | null | undefined): OptionsForm {
	const o = options ?? {};
	const cors = o.cors;
	const rate = o.rateLimit;
	const access = o.access;
	return {
		basicAuth: o.basicAuth ?? false,
		accessOn: access !== undefined,
		allowCidrs: [...(access?.allowCidrs ?? [])],
		denyCidrs: [...(access?.denyCidrs ?? [])],
		accessDepth: String(access?.trustedProxyDepth ?? 0),
		rejectStatus: access?.rejectStatus ? String(access.rejectStatus) : "",
		rateOn: rate !== undefined,
		rateAverage: String(rate?.average ?? RATE_DEFAULTS.average),
		ratePeriod: String(rate?.periodSeconds ?? RATE_DEFAULTS.period),
		rateBurst: String(rate?.burst ?? RATE_DEFAULTS.burst),
		rateDepth: String(rate?.trustedProxyDepth ?? 0),
		compress: o.compress ?? false,
		requestHeaders: (o.requestHeaders ?? []).map((h) => ({ ...h })),
		responseHeaders: (o.responseHeaders ?? []).map((h) => ({ ...h })),
		securityHeaders:
			o.securityHeaders === "basic" || o.securityHeaders === "strict"
				? o.securityHeaders
				: "",
		noindex: o.noindex ?? false,
		corsOn: cors !== undefined,
		corsOrigins: joinList(cors?.origins),
		corsMethods: joinList(cors?.methods),
		corsHeaders: joinList(cors?.headers),
		corsExpose: joinList(cors?.exposeHeaders),
		corsCredentials: cors?.credentials ?? false,
		corsMaxAge: cors?.maxAgeSeconds ? String(cors.maxAgeSeconds) : "",
		redirects: (o.redirects ?? []).map((r) => ({
			regex: r.regex,
			replacement: r.replacement,
			permanent: r.permanent ?? false,
		})),
		forceHttps: o.forceHttps ?? true,
	};
}

const int = (text: string): number => {
	const n = Number(text.trim());
	return Number.isFinite(n) ? Math.trunc(n) : Number.NaN;
};

/** Wire options from the form: defaults and switched-off sections are left out. */
export function formToOptions(form: OptionsForm): RouteOptions {
	const out: RouteOptions = {};
	if (!form.forceHttps) out.forceHttps = false;
	if (form.compress) out.compress = true;
	if (form.noindex) out.noindex = true;
	if (form.securityHeaders !== "") out.securityHeaders = form.securityHeaders;
	const requestHeaders = form.requestHeaders.filter(
		(h) => h.name.trim() !== "",
	);
	if (requestHeaders.length > 0) {
		out.requestHeaders = requestHeaders.map((h) => ({
			name: h.name.trim(),
			value: h.value,
		}));
	}
	const responseHeaders = form.responseHeaders.filter(
		(h) => h.name.trim() !== "",
	);
	if (responseHeaders.length > 0) {
		out.responseHeaders = responseHeaders.map((h) => ({
			name: h.name.trim(),
			value: h.value,
		}));
	}
	if (form.corsOn) {
		const cors: NonNullable<RouteOptions["cors"]> = {
			origins: splitList(form.corsOrigins),
		};
		const methods = splitList(form.corsMethods);
		if (methods.length > 0) cors.methods = methods;
		const headers = splitList(form.corsHeaders);
		if (headers.length > 0) cors.headers = headers;
		const expose = splitList(form.corsExpose);
		if (expose.length > 0) cors.exposeHeaders = expose;
		if (form.corsCredentials) cors.credentials = true;
		const maxAge = int(form.corsMaxAge);
		if (maxAge > 0) cors.maxAgeSeconds = maxAge;
		out.cors = cors;
	}
	if (form.redirects.length > 0) {
		out.redirects = form.redirects.map((r) => ({
			regex: r.regex,
			replacement: r.replacement,
			...(r.permanent ? { permanent: true } : {}),
		}));
	}
	if (form.rateOn) {
		const rate: NonNullable<RouteOptions["rateLimit"]> = {
			average: int(form.rateAverage),
			burst: int(form.rateBurst),
			periodSeconds: int(form.ratePeriod),
		};
		const depth = int(form.rateDepth);
		if (depth > 0) rate.trustedProxyDepth = depth;
		out.rateLimit = rate;
	}
	if (form.accessOn) {
		const access: NonNullable<RouteOptions["access"]> = {};
		const allow = cleanList(form.allowCidrs);
		if (allow.length > 0) access.allowCidrs = allow;
		const deny = cleanList(form.denyCidrs);
		if (deny.length > 0) access.denyCidrs = deny;
		const depth = int(form.accessDepth);
		if (depth > 0) access.trustedProxyDepth = depth;
		const reject = int(form.rejectStatus);
		if (reject > 0) access.rejectStatus = reject;
		out.access = access;
	}
	if (form.basicAuth) out.basicAuth = true;
	return out;
}

/** Only the option fields a group owns. */
export function pickGroup(
	options: RouteOptions,
	group: OptionGroup,
): RouteOptions {
	const out: RouteOptions = {};
	for (const key of GROUP_KEYS[group]) {
		if (options[key] !== undefined) {
			(out as Record<string, unknown>)[key] = options[key];
		}
	}
	return out;
}

/** `base` with the group's fields replaced by those of `edited`; every other group is untouched. */
export function applyGroup(
	base: RouteOptions,
	edited: RouteOptions,
	group: OptionGroup,
): RouteOptions {
	const out: RouteOptions = { ...base };
	for (const key of GROUP_KEYS[group]) delete out[key];
	return { ...out, ...pickGroup(edited, group) };
}

const stable = (value: unknown): string => JSON.stringify(value);

/** The group's saved options differ from what the form would send. */
export function groupDirty(
	saved: RouteOptions,
	form: OptionsForm,
	group: OptionGroup,
): boolean {
	return (
		stable(pickGroup(formToOptions(toForm(saved)), group)) !==
		stable(pickGroup(formToOptions(form), group))
	);
}

/** Any option set at all (a redirect domain must have none). */
export function hasOptions(options: RouteOptions): boolean {
	return Object.keys(formToOptions(toForm(options))).length > 0;
}

/** One chip per active group, for the collapsed header: "2 CIDRs", "Basic auth". */
export function groupSummary(
	options: RouteOptions,
	group: OptionGroup,
	extras: { authUsers?: number; tlsMode?: string } = {},
): string {
	const parts: string[] = [];
	switch (group) {
		case "access":
			if (options.basicAuth) {
				parts.push(`basic auth · ${extras.authUsers ?? 0} user(s)`);
			}
			if (options.access) {
				const allowed = options.access.allowCidrs?.length ?? 0;
				const denied = options.access.denyCidrs?.length ?? 0;
				if (allowed > 0 || denied === 0) {
					parts.push(`${allowed} allowed network(s)`);
				}
				if (denied > 0) parts.push(`${denied} denied network(s)`);
			}
			break;
		case "headers": {
			const n =
				(options.requestHeaders?.length ?? 0) +
				(options.responseHeaders?.length ?? 0);
			if (n > 0) parts.push(`${n} header(s)`);
			if (options.cors) parts.push("CORS");
			if (options.securityHeaders)
				parts.push(`${options.securityHeaders} security`);
			if (options.noindex) parts.push("noindex");
			break;
		}
		case "redirects":
			if (options.redirects?.length) {
				parts.push(`${options.redirects.length} rule(s)`);
			}
			break;
		case "limits":
			if (options.rateLimit) {
				parts.push(
					`${options.rateLimit.average} req / ${options.rateLimit.periodSeconds}s`,
				);
			}
			if (options.compress) parts.push("compression");
			break;
		case "tls":
			parts.push(extras.tlsMode ?? "auto");
			if (options.forceHttps === false) parts.push("HTTP allowed");
			break;
	}
	return parts.join(" · ");
}

// --- CORS presets -----------------------------------------------------------

export interface CorsPreset {
	id: "public-api" | "this-site" | "custom";
	label: string;
	description: string;
	/** The form fields the preset sets; `host` is the domain's hostname. */
	apply(
		host: string,
	): Pick<
		OptionsForm,
		| "corsOn"
		| "corsOrigins"
		| "corsMethods"
		| "corsHeaders"
		| "corsExpose"
		| "corsCredentials"
		| "corsMaxAge"
	>;
}

export const CORS_PRESETS: readonly CorsPreset[] = [
	{
		id: "public-api",
		label: "Public API",
		description: "Any origin may read; no cookies.",
		apply: () => ({
			corsOn: true,
			corsOrigins: "*",
			corsMethods: "GET, HEAD, OPTIONS",
			corsHeaders: "Content-Type, Authorization",
			corsExpose: "",
			corsCredentials: false,
			corsMaxAge: "600",
		}),
	},
	{
		id: "this-site",
		label: "This site only",
		description: "Only this domain's own origin, with cookies.",
		apply: (host) => ({
			corsOn: true,
			corsOrigins: `https://${host}`,
			corsMethods: "GET, POST, PUT, PATCH, DELETE, OPTIONS",
			corsHeaders: "Content-Type, Authorization",
			corsExpose: "",
			corsCredentials: true,
			corsMaxAge: "600",
		}),
	},
];

/** Which preset the form currently equals, or "custom" when edited by hand. */
export function corsPresetOf(
	form: OptionsForm,
	host: string,
): CorsPreset["id"] | "off" | "custom" {
	if (!form.corsOn) return "off";
	for (const preset of CORS_PRESETS) {
		const want = preset.apply(host);
		if (
			want.corsOrigins === form.corsOrigins &&
			want.corsMethods === form.corsMethods &&
			want.corsHeaders === form.corsHeaders &&
			want.corsExpose === form.corsExpose &&
			want.corsCredentials === form.corsCredentials &&
			want.corsMaxAge === form.corsMaxAge
		) {
			return preset.id;
		}
	}
	return "custom";
}

// --- validation -------------------------------------------------------------

/** Error sentences keyed by field path: `allowCidrs.2`, `requestHeaders.0.name`, `redirects.1.regex`. */
export type FormErrors = Record<string, string>;

const isPositive = (text: string): boolean => int(text) >= 1;

function validateHeaderRows(
	rows: readonly HeaderRow[],
	key: "requestHeaders" | "responseHeaders",
	errors: FormErrors,
): void {
	const request = key === "requestHeaders";
	if (rows.length > MAX_HEADERS) {
		errors[key] = `At most ${MAX_HEADERS} headers.`;
	}
	rows.forEach((row, index) => {
		if (row.name.trim() === "" && row.value === "") return;
		const nameError = validateHeaderName(row.name.trim(), request);
		if (nameError) errors[`${key}.${index}.name`] = nameError;
		const valueError = validateHeaderValue(row.value);
		if (valueError) errors[`${key}.${index}.value`] = valueError;
	});
}

/** The form's errors for one group, mirroring `RouteOptions.Validate` on the backend. */
export function validateGroup(
	form: OptionsForm,
	group: OptionGroup,
	state: { authUsers?: number } = {},
): FormErrors {
	const errors: FormErrors = {};
	switch (group) {
		case "access": {
			if (form.accessOn) {
				const check = checkRules(form.allowCidrs, form.denyCidrs, {
					needOne: true,
				});
				Object.assign(errors, check.entries);
				if (check.allow) errors.allowCidrs = check.allow;
				if (check.deny) errors.denyCidrs = check.deny;
				if (check.rules) errors.accessRules = check.rules;
				const depth = int(form.accessDepth);
				if (!(depth >= 0 && depth <= 5)) {
					errors.accessDepth = "Use 0 to 5.";
				}
				if (form.rejectStatus.trim() !== "") {
					const status = int(form.rejectStatus);
					if (!(status >= 400 && status <= 599)) {
						errors.rejectStatus = "Use a status from 400 to 599.";
					}
				}
			}
			if (form.basicAuth && (state.authUsers ?? 0) === 0) {
				errors.basicAuth =
					"Add at least one user before turning basic auth on.";
			}
			break;
		}
		case "headers": {
			validateHeaderRows(form.requestHeaders, "requestHeaders", errors);
			validateHeaderRows(form.responseHeaders, "responseHeaders", errors);
			if (form.corsOn) {
				const origins = splitList(form.corsOrigins);
				if (origins.length === 0 || origins.length > MAX_CORS_ORIGINS) {
					errors.corsOrigins = `Give 1 to ${MAX_CORS_ORIGINS} origins.`;
				} else {
					const bad = origins
						.map(validateCorsOrigin)
						.find((problem) => problem !== null);
					if (bad) errors.corsOrigins = bad;
				}
				if (
					form.corsCredentials &&
					origins.length === 1 &&
					origins[0] === "*"
				) {
					errors.corsCredentials =
						"Credentials cannot be combined with the * origin.";
				}
				const badMethod = splitList(form.corsMethods)
					.map(validateCorsMethod)
					.find((problem) => problem !== null);
				if (badMethod) errors.corsMethods = badMethod;
				for (const key of ["corsHeaders", "corsExpose"] as const) {
					const bad = splitList(form[key])
						.map(validateCorsHeaderName)
						.find((problem) => problem !== null);
					if (bad) errors[key] = bad;
				}
				if (form.corsMaxAge.trim() !== "") {
					const age = int(form.corsMaxAge);
					if (!(age >= 0 && age <= 86400)) {
						errors.corsMaxAge = "Use 0 to 86400 seconds.";
					}
				}
			}
			break;
		}
		case "redirects": {
			if (form.redirects.length > MAX_REDIRECTS) {
				errors.redirects = `At most ${MAX_REDIRECTS} rules.`;
			}
			form.redirects.forEach((rule, index) => {
				const regex = validateRedirectRegex(rule.regex);
				if (regex) errors[`redirects.${index}.regex`] = regex;
				const replacement = validateRedirectReplacement(rule.replacement);
				if (replacement) errors[`redirects.${index}.replacement`] = replacement;
			});
			break;
		}
		case "limits": {
			if (form.rateOn) {
				if (!isPositive(form.rateAverage)) errors.rateAverage = "At least 1.";
				if (!isPositive(form.rateBurst)) errors.rateBurst = "At least 1.";
				const period = int(form.ratePeriod);
				if (!(period >= 1 && period <= 3600)) {
					errors.ratePeriod = "Use 1 to 3600 seconds.";
				}
				const depth = int(form.rateDepth);
				if (!(depth >= 0 && depth <= 5)) errors.rateDepth = "Use 0 to 5.";
			}
			break;
		}
		case "tls":
			break;
	}
	return errors;
}

/** The inline error key a backend `invalid_options` message points at. */
export function optionsErrorTarget(message: string): {
	group: OptionGroup;
	path: string;
} {
	const redirect = /redirect (\d+)/i.exec(message);
	if (redirect) {
		return {
			group: "redirects",
			path: `redirects.${Number(redirect[1]) - 1}.regex`,
		};
	}
	if (/redirect/i.test(message))
		return { group: "redirects", path: "redirects" };
	if (/deny every address/i.test(message)) {
		return { group: "access", path: "accessRules" };
	}
	if (/^access|allow|deny|cidr/i.test(message)) {
		return { group: "access", path: "allowCidrs" };
	}
	if (/cors/i.test(message)) return { group: "headers", path: "corsOrigins" };
	if (/request header/i.test(message)) {
		return { group: "headers", path: "requestHeaders" };
	}
	if (/response header|set-cookie/i.test(message)) {
		return { group: "headers", path: "responseHeaders" };
	}
	if (/securityHeaders/i.test(message)) {
		return { group: "headers", path: "securityHeaders" };
	}
	if (/rateLimit/i.test(message))
		return { group: "limits", path: "rateAverage" };
	return { group: "headers", path: "" };
}

// --- per-group edits --------------------------------------------------------

/** The form fields each group edits, so one group can be reset without touching the rest. */
export const GROUP_FIELDS: Record<OptionGroup, readonly (keyof OptionsForm)[]> =
	{
		access: [
			"basicAuth",
			"accessOn",
			"allowCidrs",
			"denyCidrs",
			"accessDepth",
			"rejectStatus",
		],
		headers: [
			"requestHeaders",
			"responseHeaders",
			"securityHeaders",
			"noindex",
			"corsOn",
			"corsOrigins",
			"corsMethods",
			"corsHeaders",
			"corsExpose",
			"corsCredentials",
			"corsMaxAge",
		],
		redirects: ["redirects"],
		limits: [
			"rateOn",
			"rateAverage",
			"ratePeriod",
			"rateBurst",
			"rateDepth",
			"compress",
		],
		tls: ["forceHttps"],
	};

/** Edits kept per group on top of the saved options, so a refetch never resurrects stale fields of another group. */
export type GroupEdits = Partial<Record<OptionGroup, Partial<OptionsForm>>>;

/** The saved form with every group's pending edits laid over it. */
export function mergeEdits(base: OptionsForm, edits: GroupEdits): OptionsForm {
	let form = base;
	for (const group of OPTION_GROUPS) {
		const edit = edits[group];
		if (edit) form = { ...form, ...edit };
	}
	return form;
}

/** Records an edit under the group that owns each changed field. */
export function setGroupEdit(
	edits: GroupEdits,
	base: OptionsForm,
	group: OptionGroup,
	patch: Partial<OptionsForm>,
): GroupEdits {
	const merged = { ...base, ...edits[group], ...patch };
	const own: Partial<OptionsForm> = {};
	for (const field of GROUP_FIELDS[group]) {
		(own as Record<string, unknown>)[field] = merged[field];
	}
	return { ...edits, [group]: own };
}
