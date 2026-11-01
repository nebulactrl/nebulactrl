// The domain-level half of the editor: host, path prefix, strip/add prefix and
// the serve/redirect mode. Pure so the patch it sends and the mistakes it
// catches (the same ones domains/rule.go answers with 422) are tested.

import type { UpdateInputBody } from "#/api/gen/types.gen";
import type { ServiceDomain } from "#/api/models";
import { hasOptions } from "./options";
import {
	normalizeHostname,
	validateDomainHostname,
	validateHostname,
	validatePathPrefix,
} from "./validate";

export const REDIRECT_CODES = [301, 302, 307, 308] as const;
export type RedirectCode = (typeof REDIRECT_CODES)[number];

export type RedirectFamily = "permanent" | "temporary";

/**
 * Traefik's redirect middleware only distinguishes permanent from temporary:
 * 301 and 308 both answer permanently (301 to a GET, 308 otherwise), 302 and
 * 307 temporarily. The UI offers the two families, never an exact status.
 */
export const REDIRECT_FAMILY_HELP: Record<RedirectFamily, string> = {
	permanent:
		"Browsers and search engines remember it (answers 301 or 308 depending on the request method).",
	temporary:
		"Nothing is remembered; use it while the move may change (answers 302 or 307).",
};

export function redirectFamily(code: RedirectCode): RedirectFamily {
	return code === 301 || code === 308 ? "permanent" : "temporary";
}

/** The code to send for a family, keeping the current one when it already fits. */
export function codeForFamily(
	family: RedirectFamily,
	current: RedirectCode,
): RedirectCode {
	if (redirectFamily(current) === family) return current;
	return family === "permanent" ? 308 : 307;
}

export interface RuleForm {
	hostname: string;
	pathPrefix: string;
	stripPrefix: boolean;
	addPrefix: string;
	kind: "serve" | "redirect";
	redirectTo: string;
	redirectCode: RedirectCode;
	preservePath: boolean;
}

export function toRuleForm(domain: ServiceDomain): RuleForm {
	return {
		hostname: domain.hostname,
		pathPrefix: domain.pathPrefix,
		stripPrefix: domain.stripPrefix,
		addPrefix: domain.addPrefix,
		kind: domain.kind,
		redirectTo: domain.redirectTo,
		redirectCode: domain.redirectCode,
		preservePath: domain.preservePath,
	};
}

/** Normalised form: what would be compared and sent. */
function cleaned(form: RuleForm): RuleForm {
	const redirect = form.kind === "redirect";
	return {
		hostname: normalizeHostname(form.hostname),
		pathPrefix: form.pathPrefix.trim(),
		stripPrefix: !redirect && form.stripPrefix,
		addPrefix: redirect ? "" : form.addPrefix.trim(),
		kind: form.kind,
		redirectTo: redirect ? normalizeHostname(form.redirectTo) : "",
		redirectCode: redirect ? form.redirectCode : 308,
		preservePath: redirect ? form.preservePath : true,
	};
}

export function ruleDirty(domain: ServiceDomain, form: RuleForm): boolean {
	const a = cleaned(toRuleForm(domain));
	const b = cleaned(form);
	return (Object.keys(a) as (keyof RuleForm)[]).some(
		(key) => a[key] !== b[key],
	);
}

export type RuleErrors = Partial<
	Record<
		"hostname" | "pathPrefix" | "addPrefix" | "stripPrefix" | "redirectTo",
		string
	>
>;

export interface RuleContext {
	/** The service's other domain hostnames (including this one's current name). */
	otherDomains: readonly { hostname: string; pathPrefix: string }[];
	/** Whole-host redirects already in the environment: from -> to. */
	redirects: readonly { from: string; to: string }[];
}

/** True when pointing `from` at `to` closes a cycle of whole-host redirects. */
export function redirectLoops(
	from: string,
	to: string,
	redirects: readonly { from: string; to: string }[],
): boolean {
	const next = new Map<string, string>();
	for (const r of redirects) if (r.from !== from) next.set(r.from, r.to);
	next.set(from, to);
	let host = to;
	for (let hops = 0; hops < 20; hops++) {
		if (host === from) return true;
		const step = next.get(host);
		if (step === undefined) return false;
		host = step;
	}
	return true;
}

export function validateRule(
	form: RuleForm,
	domain: ServiceDomain,
	context: RuleContext,
): RuleErrors {
	const errors: RuleErrors = {};
	const f = cleaned(form);
	const host = validateDomainHostname(form.hostname);
	if (host) errors.hostname = host;
	const prefix = validatePathPrefix(f.pathPrefix);
	if (prefix) errors.pathPrefix = prefix;
	const add = validatePathPrefix(f.addPrefix);
	if (add) errors.addPrefix = add;
	if (form.kind === "serve" && form.stripPrefix && f.pathPrefix === "") {
		errors.stripPrefix = "Strip needs a path prefix.";
	}
	if (!errors.hostname) {
		const clash = context.otherDomains.some(
			(other) =>
				other.hostname === f.hostname &&
				other.pathPrefix === f.pathPrefix &&
				!(
					other.hostname === domain.hostname &&
					other.pathPrefix === domain.pathPrefix
				),
		);
		if (clash)
			errors.hostname = "This host and path already exist on this service.";
	}
	if (form.kind === "redirect") {
		const to = validateHostname(form.redirectTo);
		if (to) {
			errors.redirectTo = to;
		} else if (f.redirectTo === f.hostname) {
			errors.redirectTo = "A domain cannot redirect to itself.";
		} else if (
			f.pathPrefix === "" &&
			redirectLoops(f.hostname, f.redirectTo, context.redirects)
		) {
			errors.redirectTo = `This would loop: ${f.redirectTo} redirects back to ${f.hostname}.`;
		}
	}
	return errors;
}

/**
 * The PATCH body for a rule edit: only changed fields. Switching to a redirect
 * clears the prefixes and every option (the backend refuses a redirect that
 * carries them); switching back clears the redirect target.
 */
export function buildRulePatch(
	domain: ServiceDomain,
	form: RuleForm,
): UpdateInputBody {
	const before = cleaned(toRuleForm(domain));
	const after = cleaned(form);
	const patch: UpdateInputBody = {};
	if (after.hostname !== before.hostname) patch.hostname = after.hostname;
	if (after.pathPrefix !== before.pathPrefix)
		patch.pathPrefix = after.pathPrefix;
	if (after.stripPrefix !== before.stripPrefix)
		patch.stripPrefix = after.stripPrefix;
	if (after.addPrefix !== before.addPrefix) patch.addPrefix = after.addPrefix;
	if (after.kind !== before.kind) patch.kind = after.kind;
	if (after.redirectTo !== before.redirectTo)
		patch.redirectTo = after.redirectTo;
	if (after.kind === "redirect") {
		if (after.redirectCode !== before.redirectCode) {
			patch.redirectCode = after.redirectCode;
		}
		if (after.preservePath !== before.preservePath) {
			patch.preservePath = after.preservePath;
		}
		if (hasOptions(domain.options)) patch.options = {};
		if (domain.stripPrefix) patch.stripPrefix = false;
		if (domain.addPrefix !== "") patch.addPrefix = "";
	}
	return patch;
}

/** Switching to redirect throws away prefixes and options: say so before it happens. */
export function redirectDiscards(domain: ServiceDomain): string[] {
	const lost: string[] = [];
	if (domain.stripPrefix) lost.push("the strip-prefix setting");
	if (domain.addPrefix !== "") lost.push("the added prefix");
	if (hasOptions(domain.options))
		lost.push("its access, header and limit options");
	if (domain.authUsers.length > 0) lost.push("its basic-auth users");
	return lost;
}

// --- www <-> apex ------------------------------------------------------------

const SECOND_LEVEL = /^(co|com|org|net|gov|ac|edu)$/;

/** Whether `hostname` is a registrable apex (example.com, example.co.uk). */
function isApex(hostname: string): boolean {
	const labels = hostname.split(".");
	if (labels.length === 2) return true;
	const [, second, tld] = labels;
	return (
		labels.length === 3 &&
		SECOND_LEVEL.test(second ?? "") &&
		(tld ?? "").length === 2
	);
}

/**
 * The hostname a www redirect would answer for `hostname`: `www.<host>` for an
 * apex, the apex for a `www.` host. Null for any other host (a subdomain,
 * a wildcard), where a www redirect is not offered. The control plane decides
 * what it accepts; this only decides when the UI offers the switch.
 */
export function wwwCounterpart(hostname: string): string | null {
	const host = normalizeHostname(hostname);
	if (host.startsWith("www.")) {
		const apex = host.slice(4);
		return isApex(apex) ? apex : null;
	}
	return isApex(host) ? `www.${host}` : null;
}
