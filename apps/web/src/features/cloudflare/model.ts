// Cloudflare slice, pure parts: the words and tones of a domain's DNS status,
// which action fixes it, zone matching for the zone picker. The shapes mirror
// internal/integrations/cloudflare (the generated client is the transport).

import type {
	CloudflareConnectionView,
	DnsStatus,
	DomainDns,
	IpListView,
	OriginLockView,
	Zone,
} from "#/api/gen/types.gen";
import type { LampTone } from "#/components/ui/lamp";
import type { StatusKind } from "#/components/ui/status-icon";
import { absoluteDate, relativeTime } from "#/lib/format";

type DnsState = Exclude<DnsStatus["status"], "">;

export interface DnsStateInfo {
	label: string;
	tone: LampTone;
	/** One sentence: what it means for the person looking at the domain. */
	hint: string;
}

const STATE_INFO: Record<DnsState, DnsStateInfo> = {
	managed: {
		label: "Managed",
		tone: "ok",
		hint: "NebulaCtrl created the record and keeps it in sync.",
	},
	present: {
		label: "Record present",
		tone: "ok",
		hint: "A record you created already points here. NebulaCtrl leaves it alone.",
	},
	conflict: {
		label: "Conflicting record",
		tone: "warn",
		hint: "A record NebulaCtrl did not create differs from the wanted one or blocks it. It stays as it is until you replace it.",
	},
	wrong_target: {
		label: "Wrong target",
		tone: "warn",
		hint: "A record exists but points somewhere else.",
	},
	missing: {
		label: "No record",
		tone: "warn",
		hint: "The hostname has no DNS record yet.",
	},
	not_in_zone: {
		label: "Not in a connected zone",
		tone: "off",
		hint: "No zone of the Cloudflare token contains this hostname, so DNS is yours to manage.",
	},
};

/** The label, lamp and hint of a DNS status; null before the first evaluation. */
export function dnsStateInfo(
	status: DnsStatus["status"] | undefined,
): DnsStateInfo | null {
	if (!status) return null;
	return STATE_INFO[status];
}

export type FixAction =
	| { kind: "none" }
	| { kind: "fix"; label: string }
	| { kind: "adopt"; label: string; confirm: string };

/** The one action of a conflict: replace what is there with NebulaCtrl's record. */
export const REPLACE_LABEL = "Replace with NebulaCtrl's record";

/** A record as one line: "A 203.0.113.9 (DNS only)". */
export interface RecordText {
	type: string;
	content: string;
	proxied: boolean;
}

function recordText(record: RecordText): string {
	return `${record.type} ${record.content} (${proxyLabel(record.proxied)})`;
}

/**
 * What the confirmation of a replace lists: the records NebulaCtrl did not
 * create, which are replaced or deleted, and the record NebulaCtrl puts in
 * their place.
 */
export function replaceConfirm(status: {
	records?: readonly (RecordText & { owned: boolean })[] | null;
	desired?: RecordText | null;
	desiredRecords?: readonly RecordText[] | null;
	effectiveProxied?: boolean;
}): string {
	const foreign = (status.records ?? []).filter((record) => !record.owned);
	const wanted =
		status.desiredRecords && status.desiredRecords.length > 0
			? status.desiredRecords
			: status.desired?.content
				? [status.desired]
				: [];
	const replaced =
		foreign.length > 0
			? foreign.map(recordText).join("; ")
			: "the existing records of this hostname";
	const created =
		wanted.length > 0
			? wanted
					.map((record) =>
						recordText({
							...record,
							proxied: status.effectiveProxied ?? record.proxied,
						}),
					)
					.join("; ")
			: "its own record";
	return `NebulaCtrl replaces or deletes: ${replaced}. NebulaCtrl's record will be: ${created}. The change is audited.`;
}

/**
 * What the one-click fix does for a live status: nothing when the record is
 * right, create or correct NebulaCtrl's own record, adopt a right record the
 * team created, or replace a conflicting record (both after asking).
 */
export function fixAction(status: DnsStatus): FixAction {
	if (!status.configured) return { kind: "none" };
	switch (status.status) {
		case "":
		case "not_in_zone":
			return { kind: "none" };
		case "present":
			// The record is right but not NebulaCtrl's: adopting it lets
			// NebulaCtrl keep it in step (and lock a proxied domain to
			// Cloudflare); nothing about where it points changes.
			return {
				kind: "adopt",
				label: "Adopt record",
				confirm: `The existing record already points at ${targetText(status)}. NebulaCtrl will mark it as managed: it follows this service when it moves and is deleted if you remove the domain. A proxied domain is then locked to Cloudflare.`,
			};
		case "conflict":
			return {
				kind: "adopt",
				label: REPLACE_LABEL,
				confirm: replaceConfirm(status),
			};
		case "managed":
			return status.error
				? { kind: "fix", label: "Retry sync" }
				: { kind: "none" };
		case "missing":
			return { kind: "fix", label: "Create record" };
		case "wrong_target":
			return { kind: "fix", label: "Fix DNS" };
	}
}

/** "203.0.113.10" or "abc.cfargotunnel.com": where the record should point. */
function targetText(status: Pick<DnsStatus, "desired">): string {
	return status.desired?.content || "the server";
}

/** The proxy setting as the two words people know. */
export function proxyLabel(proxied: boolean): string {
	return proxied ? "Proxied" : "DNS only";
}

/** The longest zone whose name equals the hostname or one of its parent domains. */
export function matchZone(
	hostname: string,
	zones: readonly Zone[],
): Zone | null {
	const host = hostname
		.trim()
		.toLowerCase()
		.replace(/\.$/, "")
		.replace(/^\*\./, "");
	let best: Zone | null = null;
	for (const zone of zones) {
		if (!zone.name) continue;
		if (host === zone.name || host.endsWith(`.${zone.name}`)) {
			if (!best || zone.name.length > best.name.length) best = zone;
		}
	}
	return best;
}

/**
 * The hostname after picking a zone: the first label already typed (or `app`)
 * under the zone. `shop.example.org` with zone `example.com` becomes `shop.example.com`.
 */
export function applyZone(hostname: string, zoneName: string): string {
	const first = hostname.trim().toLowerCase().split(".")[0] ?? "";
	const label = first === "" || first === "*" ? "app" : first;
	const wildcard = hostname.trim().startsWith("*.");
	return `${wildcard ? "*" : label}.${zoneName}`;
}

/** The headline of the connection card from its view. */
export function connectionState(view: CloudflareConnectionView): {
	label: string;
	tone: LampTone;
} {
	if (!view.configured) return { label: "Not connected", tone: "off" };
	if (view.status === "error") return { label: "Token rejected", tone: "bad" };
	return { label: "Connected", tone: "ok" };
}

/** What the DNS header of a domain card reads from the domain. */
export interface DnsHeaderInput {
	hostname: string;
	dnsOk: boolean | null;
	statusMessage: string | null;
	/** The cached Cloudflare state; null or absent while the team has no connection. */
	dns?: Pick<DomainDns, "status" | "proxied" | "zoneName" | "message"> | null;
}

export interface DnsHeader {
	tone: LampTone;
	kind: StatusKind;
	label: string;
	/** The line under the hostname. */
	line: string;
}

function zoneText(zoneName: string | undefined): string {
	return zoneName ? `your Cloudflare zone ${zoneName}` : "your Cloudflare zone";
}

/**
 * The DNS chip of a domain card and the line under it. A domain in a connected
 * zone follows the cached Cloudflare state, never the probe (`dnsOk`,
 * `statusMessage`): the probe can be stale or not yet run, and the page must
 * not tell anyone to create a record NebulaCtrl creates itself. Any other
 * domain follows the probe: pointing, not pointing, or not checked yet, with
 * the record to create by hand.
 */
export function dnsHeader(
	domain: DnsHeaderInput,
	publicIp: string | null,
): DnsHeader {
	const dns = domain.dns;
	const zone = zoneText(dns?.zoneName);
	switch (dns?.status) {
		case "managed":
			return {
				tone: "ok",
				kind: "healthy",
				label: "DNS ok",
				line: `NebulaCtrl manages this record in ${zone}${dns.proxied ? ", proxied through Cloudflare" : ", DNS only"}.`,
			};
		case "present":
			return {
				tone: "ok",
				kind: "healthy",
				label: "DNS ok",
				line: `The record in ${zone} points at this server${dns.proxied ? ", proxied through Cloudflare" : ", DNS only"}.`,
			};
		case "conflict":
			return {
				tone: "warn",
				kind: "warning",
				label: STATE_INFO.conflict.label,
				line:
					dns.message ||
					`A record in ${zone} differs from the one NebulaCtrl wants.`,
			};
		case "missing":
		case "wrong_target":
			return {
				tone: "warn",
				kind: "pending",
				label: "DNS pending",
				line: `NebulaCtrl is setting up this record in ${zone}.`,
			};
		default:
			break;
	}
	const line = unzonedDnsLine(domain, publicIp);
	if (domain.dnsOk === true)
		return { tone: "ok", kind: "healthy", label: "DNS ok", line };
	if (domain.dnsOk === false)
		return { tone: "warn", kind: "warning", label: "DNS not pointing", line };
	return { tone: "off", kind: "unknown", label: "DNS unchecked", line };
}

/** The line of a domain outside a connected zone: what to do, or that it is already right. */
function unzonedDnsLine(
	domain: Pick<DnsHeaderInput, "hostname" | "dnsOk" | "statusMessage">,
	publicIp: string | null,
): string {
	if (domain.dnsOk === true) return "DNS points at this server.";
	if (domain.statusMessage) return domain.statusMessage;
	return `Point an A record for ${domain.hostname} to ${publicIp || "the server's public IP"}.`;
}

// ------------------------------------------------------------- origin lock

/** The warning shown while a proxied domain is opted out of the lock. */
export const ORIGIN_LOCK_OPT_OUT_WARNING =
	"Anyone who finds this server's IP can bypass Cloudflare.";

type LockLayerId = "ip" | "aop" | "realIp";
type LockLayerState = "on" | "off" | "unavailable";

export interface LockLayer {
	id: LockLayerId;
	title: string;
	state: LockLayerState;
	/** The word beside the status icon. */
	label: string;
	kind: StatusKind;
	/** One sentence: what the layer does, or why it is not applied. */
	detail: string;
}

const LAYER_LABEL: Record<LockLayerState, string> = {
	on: "On",
	off: "Off",
	unavailable: "Unavailable",
};

const LAYER_KIND: Record<LockLayerState, StatusKind> = {
	on: "live",
	off: "stopped",
	unavailable: "warning",
};

function layer(
	id: LockLayerId,
	title: string,
	state: LockLayerState,
	detail: string,
): LockLayer {
	return {
		id,
		title,
		state,
		label: LAYER_LABEL[state],
		kind: LAYER_KIND[state],
		detail,
	};
}

/**
 * The three layers of the origin lock and their state: the IP allow list of
 * Cloudflare's ranges, Authenticated Origin Pulls (Cloudflare's client
 * certificate) and the real visitor address.
 */
export function lockLayers(lock: OriginLockView): LockLayer[] {
	const aopDetail = (): string => {
		if (lock.aop === "on")
			return "The proxy requires the client certificate Cloudflare presents.";
		if (lock.aop === "unavailable")
			return lock.aopReason || "Cloudflare could not be asked to present it.";
		return lock.enabled
			? "Checked with the next DNS sync."
			: "Applies while the domain is locked.";
	};
	return [
		layer(
			"ip",
			"IP lock",
			lock.ipLock ? "on" : "off",
			lock.ipLock
				? "Only Cloudflare's IP ranges can connect; everything else gets 403."
				: "Every address can reach the server directly.",
		),
		layer("aop", "Authenticated Origin Pulls", lock.aop, aopDetail()),
		layer(
			"realIp",
			"Real client IP",
			lock.realClientIp ? "on" : "off",
			lock.realClientIp
				? "The visitor's address comes from Cloudflare's headers, only when the connection is from Cloudflare. IP rules and analytics see the visitor."
				: "IP rules and analytics see the address that connected.",
		),
	];
}

/** Why a domain is not locked, in words. Empty when it is locked. */
export function lockReasonText(reason: OriginLockView["reason"]): string {
	switch (reason) {
		case "":
			return "";
		case "opted_out":
			return "You turned the lock off for this domain.";
		case "tunnel":
			return "This domain is served through a Cloudflare Tunnel. It has no public origin, so there is nothing to lock.";
		case "not_proxied":
			return "The lock applies while the domain is proxied (orange cloud). DNS-only domains stay reachable directly.";
		case "not_in_zone":
			return "The hostname is in no connected zone.";
		case "pending":
			return "Waiting for the DNS record to become proxied; the lock follows.";
		case "not_published":
			return "The status page is not published yet; it is locked as soon as it is.";
		case "wildcard":
			return "Wildcard hostnames cannot be locked.";
		case "redirect":
			return "Redirect rules are not locked.";
	}
}

/** The headline beside the shield: locked, or not and why. */
export function lockHeadline(lock: OriginLockView): {
	label: string;
	kind: StatusKind;
} {
	if (lock.enabled) return { label: "Locked", kind: "live" };
	if (lock.reason === "opted_out") return { label: "Open", kind: "warning" };
	if (lock.reason === "pending") return { label: "Pending", kind: "pending" };
	return { label: "Not locked", kind: "stopped" };
}

/** True when the toggle can be used: only a proxied, non-tunnel domain has a lock to switch. */
export function lockToggleEnabled(lock: OriginLockView): boolean {
	return lock.reason !== "tunnel" && lock.reason !== "not_in_zone";
}

/** True when the opt-out warning must stay visible: opted out while it would apply. */
export function showOptOutWarning(
	lock: OriginLockView,
	proxied: boolean,
): boolean {
	return !lock.optIn && proxied && lock.reason !== "tunnel";
}

/** The token permission to offer when Authenticated Origin Pulls was refused for lack of it. */
export function needsAopPermission(lock: OriginLockView): boolean {
	return lock.aop === "unavailable" && lock.aopReasonCode === "permission";
}

// ------------------------------------------------------------- IP list

export interface IpListSummary {
	/** "Cloudflare API", "Published text files" or "Built-in copy". */
	source: string;
	/** "42 ranges (15 IPv4, 7 IPv6)". */
	size: string;
	/** "Updated 2h ago", or the built-in date. */
	age: string;
	/** "Last changed 3d ago", empty while the built-in list is in use. */
	changed: string;
	/** Why fetching fails; empty when it works. */
	problem: string;
	/** Fetching has failed for a day or more. */
	stale: boolean;
}

const SOURCE_LABEL: Record<IpListView["source"], string> = {
	api: "Cloudflare API",
	text: "Published text files",
	builtin: "Built-in copy",
};

/** The words of the IP list card in Cloudflare settings. */
export function ipListSummary(view: IpListView): IpListSummary {
	const total = view.v4Count + view.v6Count;
	const age = view.fetchedAt
		? `Fetched ${relativeTime(view.fetchedAt)}`
		: `Shipped with this version (${view.builtinDate}); not fetched yet`;
	const changed = view.changedAt
		? `Content last changed ${relativeTime(view.changedAt)} (${absoluteDate(view.changedAt)})`
		: "";
	let problem = "";
	if (view.lastError) {
		const since = view.failingSince
			? ` (failing since ${absoluteDate(view.failingSince)})`
			: "";
		problem = `The last fetch failed${since}: ${view.lastError}. The last good list stays in use.`;
	}
	return {
		source: SOURCE_LABEL[view.source],
		size: `${total} ranges (${view.v4Count} IPv4, ${view.v6Count} IPv6)`,
		age,
		changed,
		problem,
		stale: view.stale,
	};
}
