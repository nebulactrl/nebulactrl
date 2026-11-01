import type {
	DashboardAccessView,
	UpdateDashboardAccessInputBody,
} from "#/api/gen/types.gen";
import {
	checkRules,
	cleanList,
	type Network,
	parseNetwork,
	type RulesCheck,
	rulesOk,
	sameNetworks,
} from "#/features/access-rules/rules";

/** The "Tailnet only" preset: Tailscale's CGNAT IPv4 range and its IPv6 ULA range. */
export const TAILNET_ONLY: readonly string[] = [
	"100.64.0.0/10",
	"fd7a:115c:a1e0::/48",
];

/** What the form edits: the allow list (rows as typed) and the DNS-01 provider ("" is HTTP-01). */
export interface Draft {
	allow: string[];
	resolver: string;
}

export function draftFor(view: DashboardAccessView): Draft {
	return { allow: [...(view.allowCidrs ?? [])], resolver: view.dnsResolver };
}

export function isDirty(draft: Draft, view: DashboardAccessView): boolean {
	return (
		!sameNetworks(draft.allow, view.allowCidrs ?? []) ||
		draft.resolver !== view.dnsResolver
	);
}

/** Per-row problems and list problems, the way the control plane would answer. */
export function checkDraft(draft: Draft): RulesCheck {
	return checkRules(draft.allow, []);
}

export function draftValid(draft: Draft): boolean {
	return rulesOk(checkDraft(draft));
}

/** An address as the network it is on the wire (an IPv4-mapped IPv6 address is the IPv4 one, as the control plane reads it). */
function addressNetwork(ip: string): Network | null {
	const net = parseNetwork(ip);
	if (net === null || !net.single) return null;
	if (net.family === 6 && net.addr >> 32n === 0xffffn) {
		return {
			family: 4,
			addr: net.addr & 0xffffffffn,
			bits: 32,
			single: true,
		};
	}
	return net;
}

/** Whether `list` lets `ip` in. An empty list lets everybody in; entries that do not parse match nothing. */
export function listAdmits(list: readonly string[], ip: string): boolean {
	const entries = cleanList(list);
	if (entries.length === 0) return true;
	const addr = addressNetwork(ip);
	if (addr === null) return false;
	return entries.some((entry) => {
		const net = parseNetwork(entry);
		if (net === null || net.family !== addr.family) return false;
		const w = net.family === 4 ? 32 : 128;
		const shift = BigInt(w - net.bits);
		return net.addr >> shift === addr.addr >> BigInt(w - net.bits);
	});
}

export interface Lockout {
	/** Saving would lock the caller out of the dashboard. */
	locksOut: boolean;
	/** The caller's address as the allow list sees it. */
	ip: string;
}

/**
 * Whether saving `draft` would lock the caller out. Only a caller who comes
 * through the dashboard route meets the allow list; one who reaches the
 * control plane directly (an SSH tunnel, a plain IP) never does.
 */
export function lockoutOf(draft: Draft, view: DashboardAccessView): Lockout {
	const ip = view.clientIp;
	const locksOut =
		view.viaDashboardRoute &&
		cleanList(draft.allow).length > 0 &&
		!listAdmits(draft.allow, ip);
	return { locksOut, ip };
}

/** The list with the caller's address added (nothing when it is already let in or unknown). */
export function withCurrentIp(allow: readonly string[], ip: string): string[] {
	const trimmed = ip.trim();
	if (trimmed === "") return cleanList(allow);
	// An empty list lets everybody in: adding the address alone would restrict it to this one.
	if (cleanList(allow).length > 0 && listAdmits(allow, trimmed)) {
		return cleanList(allow);
	}
	return cleanList([...allow, trimmed]);
}

/** The request body for the draft. `confirmLockout` is sent only when the caller confirmed. */
export function toBody(
	draft: Draft,
	confirmLockout: boolean,
): UpdateDashboardAccessInputBody {
	return {
		allowCidrs: cleanList(draft.allow),
		dnsResolver: draft.resolver,
		...(confirmLockout ? { confirmLockout: true } : {}),
	};
}

export interface CertificateChoice {
	/** "" is automatic HTTP-01. */
	value: string;
	label: string;
	disabled?: boolean;
}

/** The certificate methods the form offers: HTTP-01 and every provider that has a resolver on the control-plane server. */
export function certificateChoices(
	view: DashboardAccessView,
): CertificateChoice[] {
	const choices: CertificateChoice[] = [
		{ value: "", label: "Automatic (HTTP-01)" },
	];
	for (const r of view.resolverAvailable ?? []) {
		choices.push({ value: r.provider, label: `DNS-01 ${r.providerName}` });
	}
	// A provider the settings name but the server lost its resolver for stays visible.
	if (
		view.dnsResolver !== "" &&
		!choices.some((c) => c.value === view.dnsResolver)
	) {
		choices.push({
			value: view.dnsResolver,
			label: `DNS-01 ${view.dnsResolver} (resolver missing)`,
			disabled: true,
		});
	}
	return choices;
}

/** The sentence under the list: who gets in. */
export function allowSummary(allow: readonly string[]): string {
	const n = cleanList(allow).length;
	if (n === 0) {
		return "No restriction: everybody who can reach this host may open the dashboard.";
	}
	return `Only ${n === 1 ? "this network" : `these ${n} networks`} may open the dashboard. Everybody else gets 403.`;
}
