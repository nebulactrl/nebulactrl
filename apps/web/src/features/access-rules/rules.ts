// IP access rules: an allow list and a deny list of networks (a CIDR or a
// single address, IPv4 or IPv6). Pure, so the acceptance the control plane
// applies (pkg/agentprotocol/access.go) is tested here: entries parse the way
// Go's netip does, each list holds at most 200, deny wins over allow, an empty
// allow list means "everyone except the denied", and the pair must admit at
// least one address. Prefix math is exact (BigInt), never string compares.

/** Each of the allow and deny lists holds at most this many entries. */
export const MAX_ACCESS_ENTRIES = 200;

/** The proxy renders one allow list of at most this many networks. */
export const MAX_EFFECTIVE_NETWORKS = 4096;

export type Family = 4 | 6;

/** A network: its address family, the masked base address and the prefix length. */
export interface Network {
	family: Family;
	/** The address with the host bits cleared. */
	addr: bigint;
	bits: number;
	/** Written as a bare address (no `/prefix`). */
	single: boolean;
}

const width = (family: Family): number => (family === 4 ? 32 : 128);

const OCTET = /^(0|[1-9]\d{0,2})$/;
const HEXTET = /^[0-9a-fA-F]{1,4}$/;
const PREFIX_LENGTH = /^(0|[1-9]\d{0,2})$/;

function parseV4(text: string): bigint | null {
	const parts = text.split(".");
	if (parts.length !== 4) return null;
	let value = 0n;
	for (const part of parts) {
		if (!OCTET.test(part) || Number(part) > 255) return null;
		value = (value << 8n) | BigInt(part);
	}
	return value;
}

function parseV6(text: string): bigint | null {
	if (!text.includes(":") || /[^0-9A-Fa-f:.]/.test(text)) return null;
	const sides = text.split("::");
	if (sides.length > 2) return null;
	const [left = "", right] = sides;
	const compressed = right !== undefined;
	const head = left === "" ? [] : left.split(":");
	const tail = compressed && right !== "" ? right.split(":") : [];
	// An embedded IPv4 tail ("::ffff:1.2.3.4") stands for two groups.
	const last = (compressed ? tail : head).at(-1);
	let v4: bigint | null = null;
	if (last?.includes(".")) {
		v4 = parseV4(last);
		if (v4 === null) return null;
		(compressed ? tail : head).pop();
	}
	const groups = [...head, ...tail];
	if (!groups.every((group) => HEXTET.test(group))) return null;
	const count = groups.length + (v4 === null ? 0 : 2);
	// "::" must stand for at least one zero group.
	if (compressed ? count > 7 : count !== 8) return null;
	const value = (items: readonly string[]): bigint =>
		items.reduce((acc, group) => (acc << 16n) | BigInt(`0x${group}`), 0n);
	const headValue = value(head);
	const tailValue = value(tail);
	const v4Value = v4 ?? 0n;
	const tailBits = BigInt(tail.length * 16 + (v4 === null ? 0 : 32));
	if (compressed) {
		const tailAll = (tailValue << (v4 === null ? 0n : 32n)) | v4Value;
		const zeros = BigInt(128 - head.length * 16) - tailBits;
		return (headValue << (zeros + tailBits)) | tailAll;
	}
	return (headValue << (v4 === null ? 0n : 32n)) | v4Value;
}

const mask = (addr: bigint, bits: number, w: number): bigint =>
	(addr >> BigInt(w - bits)) << BigInt(w - bits);

type Parsed = { net: Network } | { error: string };

function parse(value: string): Parsed {
	const text = value.trim();
	if (text === "") return { error: "Enter an address or network." };
	const slash = text.indexOf("/");
	const address = slash === -1 ? text : text.slice(0, slash);
	const v4 = parseV4(address);
	const v6 = v4 === null ? parseV6(address) : null;
	if (v4 === null && v6 === null) {
		return { error: `“${text}” is not a CIDR or IP address.` };
	}
	const family: Family = v4 === null ? 6 : 4;
	const w = width(family);
	const addr = (v4 ?? v6) as bigint;
	if (slash === -1) {
		return { net: { family, addr, bits: w, single: true } };
	}
	const bitsText = text.slice(slash + 1);
	if (!PREFIX_LENGTH.test(bitsText) || Number(bitsText) > w) {
		return { error: `The prefix length must be 0 to ${w}.` };
	}
	const bits = Number(bitsText);
	return {
		net: { family, addr: mask(addr, bits, w), bits, single: false },
	};
}

/** The network an entry writes, or null when it is not a CIDR or address. */
export function parseNetwork(value: string): Network | null {
	const parsed = parse(value);
	return "net" in parsed ? parsed.net : null;
}

/** The sentence for an entry the control plane would refuse, or null. */
export function validateEntry(value: string): string | null {
	const parsed = parse(value);
	return "error" in parsed ? parsed.error : null;
}

const dotted = (addr: bigint): string =>
	[24n, 16n, 8n, 0n].map((shift) => String((addr >> shift) & 0xffn)).join(".");

function formatAddress(net: Network): string {
	if (net.family === 4) return dotted(net.addr);
	// IPv4-mapped addresses print the way Go prints them.
	if (net.addr >> 32n === 0xffffn) return `::ffff:${dotted(net.addr)}`;
	const groups = Array.from({ length: 8 }, (_, i) =>
		Number((net.addr >> BigInt((7 - i) * 16)) & 0xffffn),
	);
	let bestStart = -1;
	let bestLength = 0;
	for (let i = 0; i < 8; ) {
		if (groups[i] !== 0) {
			i++;
			continue;
		}
		let end = i;
		while (end < 8 && groups[end] === 0) end++;
		if (end - i > bestLength) {
			bestStart = i;
			bestLength = end - i;
		}
		i = end;
	}
	const hex = (list: readonly number[]) =>
		list.map((group) => group.toString(16)).join(":");
	if (bestLength < 2) return hex(groups);
	return `${hex(groups.slice(0, bestStart))}::${hex(groups.slice(bestStart + bestLength))}`;
}

/** Canonical text: a bare address stays one, a CIDR is masked (`10.1.2.3/8` is `10.0.0.0/8`). */
export function formatNetwork(net: Network): string {
	const address = formatAddress(net);
	return net.single ? address : `${address}/${net.bits}`;
}

/** Two entries name the same network (`10.1.2.3/8` and `10.0.0.0/8`; a bare address is its /32 or /128). */
function entryKey(text: string): string {
	const net = parseNetwork(text);
	return net
		? `${net.family}/${net.addr.toString(16)}/${net.bits}`
		: `raw:${text.trim()}`;
}

/**
 * What goes on the wire: trimmed, blanks dropped, repeats dropped (the first
 * spelling stays). Invalid entries are kept so validation can name them.
 */
export function cleanList(values: readonly string[]): string[] {
	const seen = new Set<string>();
	const out: string[] = [];
	for (const raw of values) {
		const text = raw.trim();
		if (text === "") continue;
		const key = entryKey(text);
		if (seen.has(key)) continue;
		seen.add(key);
		out.push(text);
	}
	return out;
}

/** Two lists name the same networks in the same order, spelling aside. */
export function sameNetworks(
	a: readonly string[],
	b: readonly string[],
): boolean {
	const left = cleanList(a);
	const right = cleanList(b);
	return (
		left.length === right.length &&
		left.every((item, i) => entryKey(item) === entryKey(right[i] ?? ""))
	);
}

/** A pasted list: one entry per line or comma separated; `#` starts a comment. */
export function parseEntriesText(text: string): string[] {
	return text
		.split("\n")
		.map((line) => line.replace(/#.*$/, ""))
		.flatMap((line) => line.split(/[\s,;]+/))
		.map((part) => part.trim())
		.filter((part) => part !== "");
}

/** `0.0.0.0/0` or `::/0`: every address of a family. */
export function isOpenCidr(value: string): boolean {
	return parseNetwork(value)?.bits === 0;
}

/** Any entry of the list matches every address of a family. */
export function hasOpenCidr(values: readonly string[]): boolean {
	return values.some(isOpenCidr);
}

// --- prefix math ------------------------------------------------------------

const contains = (outer: Network, inner: Network): boolean => {
	if (outer.family !== inner.family || outer.bits > inner.bits) return false;
	const shift = BigInt(width(outer.family) - outer.bits);
	return outer.addr >> shift === inner.addr >> shift;
};

/** `net` without `hole`, as disjoint prefixes (the removed side splits, like the proxy renders it). */
function subtract(net: Network, hole: Network, out: Network[]): void {
	if (hole.family !== net.family) {
		out.push(net);
		return;
	}
	if (contains(hole, net)) return;
	if (!contains(net, hole)) {
		out.push(net);
		return;
	}
	const bits = net.bits + 1;
	const half = 1n << BigInt(width(net.family) - bits);
	const base = { family: net.family, bits, single: false };
	subtract({ ...base, addr: net.addr }, hole, out);
	subtract({ ...base, addr: net.addr + half }, hole, out);
}

const EVERYTHING: readonly Network[] = [
	{ family: 4, addr: 0n, bits: 0, single: false },
	{ family: 6, addr: 0n, bits: 0, single: false },
];

/**
 * The networks that are let in: the allow list (every address when it is
 * empty) minus the deny list. Only valid entries count. `null` when the deny
 * list splits the result into more networks than the proxy takes.
 */
export function effectiveNetworks(
	allow: readonly string[],
	deny: readonly string[],
): Network[] | null {
	const allowed = cleanList(allow)
		.map(parseNetwork)
		.filter((net): net is Network => net !== null);
	let base: Network[] = allow.every((entry) => entry.trim() === "")
		? [...EVERYTHING]
		: allowed;
	for (const entry of cleanList(deny)) {
		const hole = parseNetwork(entry);
		if (hole === null) continue;
		const next: Network[] = [];
		for (const net of base) {
			subtract(net, hole, next);
			if (next.length > MAX_EFFECTIVE_NETWORKS) return null;
		}
		base = next;
	}
	return base;
}

/**
 * The rules deny every address: each allowed network (or everything, when the
 * allow list is empty) is covered by the deny list. Entries that do not parse
 * are ignored here; they have their own problem.
 */
export function admitsNobody(
	allow: readonly string[],
	deny: readonly string[],
): boolean {
	if (cleanList(deny).length === 0) return false;
	const hasAllow = cleanList(allow).length > 0;
	if (hasAllow && cleanList(allow).every((e) => parseNetwork(e) === null)) {
		return false;
	}
	return effectiveNetworks(allow, deny)?.length === 0;
}

// --- the effective rules, in order -----------------------------------------

export interface RuleRow {
	/** 1-based, in evaluation order. */
	position: number;
	effect: "deny" | "allow";
	/** The last row: what happens to everything the lines above did not match. */
	fallback: boolean;
	/** "Deny 10.0.0.0/8", "Allow 192.168.1.20", "Everything else: denied". */
	text: string;
	/** The canonical network of a deny or allow row. */
	network?: string;
}

export interface DescribeOptions {
	/** The allow list may not be empty (a public database port): empty means nothing is let in. */
	allowRequired?: boolean;
}

function networksOf(values: readonly string[]): string[] {
	const out: string[] = [];
	for (const entry of cleanList(values)) {
		const net = parseNetwork(entry);
		if (net) out.push(formatNetwork(net));
	}
	return out;
}

/**
 * The ordered preview of the rules as the proxy applies them: 1. every
 * denied network, 2. every allowed network, 3. the default for everything
 * else. A client is let in exactly when it matches an allow line and no deny
 * line, so the order reads like a firewall without being one.
 */
export function describeRules(
	allow: readonly string[],
	deny: readonly string[],
	options: DescribeOptions = {},
): RuleRow[] {
	const denied = networksOf(deny);
	const allowed = networksOf(allow);
	const rows: RuleRow[] = [];
	for (const network of denied) {
		rows.push({
			position: rows.length + 1,
			effect: "deny",
			fallback: false,
			text: `Deny ${network}`,
			network,
		});
	}
	for (const network of allowed) {
		rows.push({
			position: rows.length + 1,
			effect: "allow",
			fallback: false,
			text: `Allow ${network}`,
			network,
		});
	}
	const everyone = allowed.length === 0 && !options.allowRequired;
	rows.push({
		position: rows.length + 1,
		effect: everyone ? "allow" : "deny",
		fallback: true,
		text: everyone ? "Everything else: allowed" : "Everything else: denied",
	});
	return rows;
}

// --- validation -------------------------------------------------------------

export const DENY_EVERYTHING =
	"The rules deny every address. Remove a denied network or add an allowed one.";

export interface RulesCheck {
	/** `allowCidrs.2` or `denyCidrs.0`: one sentence per entry the control plane would refuse. */
	entries: Record<string, string>;
	allow?: string;
	deny?: string;
	/** About the pair: nobody gets in, or the deny list splits too much. */
	rules?: string;
}

export interface CheckOptions extends DescribeOptions {
	/** Keys of `entries`; default `allowCidrs` and `denyCidrs`. */
	allowKey?: string;
	denyKey?: string;
	/** At least one entry in either list (a domain's access rules). */
	needOne?: boolean;
}

/** Whether a check found nothing wrong. */
export function rulesOk(check: RulesCheck): boolean {
	return (
		Object.keys(check.entries).length === 0 &&
		check.allow === undefined &&
		check.deny === undefined &&
		check.rules === undefined
	);
}

/** Mirrors `ValidateAccessLists` of the control plane. Blank rows count as problems: they are removed or filled. */
export function checkRules(
	allow: readonly string[],
	deny: readonly string[],
	options: CheckOptions = {},
): RulesCheck {
	const allowKey = options.allowKey ?? "allowCidrs";
	const denyKey = options.denyKey ?? "denyCidrs";
	const check: RulesCheck = { entries: {} };
	allow.forEach((entry, index) => {
		const problem = validateEntry(entry);
		if (problem) check.entries[`${allowKey}.${index}`] = problem;
	});
	deny.forEach((entry, index) => {
		const problem = validateEntry(entry);
		if (problem) check.entries[`${denyKey}.${index}`] = problem;
	});
	const allowed = cleanList(allow);
	const denied = cleanList(deny);
	if (allowed.length > MAX_ACCESS_ENTRIES) {
		check.allow = `At most ${MAX_ACCESS_ENTRIES} allowed networks.`;
	}
	if (denied.length > MAX_ACCESS_ENTRIES) {
		check.deny = `At most ${MAX_ACCESS_ENTRIES} denied networks.`;
	}
	if (options.allowRequired && allowed.length === 0) {
		check.allow = "Add at least one allowed network.";
	}
	if (options.needOne && allowed.length === 0 && denied.length === 0) {
		check.rules =
			"Add an allowed or a denied network, or switch the rules off.";
	}
	if (Object.keys(check.entries).length === 0 && check.rules === undefined) {
		if (effectiveNetworks(allow, deny) === null) {
			check.rules = `The deny list splits the allowed networks into more than ${MAX_EFFECTIVE_NETWORKS} ranges. Use fewer or broader denied networks.`;
		} else if (admitsNobody(allow, deny)) {
			check.rules = DENY_EVERYTHING;
		}
	}
	return check;
}

/**
 * A check rebuilt from a form's flat error map (`allowCidrs.2`, `allowCidrs`,
 * `denyCidrs`, `accessRules`), for a form that keeps its errors in one record.
 * The row errors are looked up by key, so the map itself serves as `entries`.
 */
export function checkFromErrors(
	errors: Readonly<Record<string, string>>,
	keys: { allow?: string; deny?: string; rules?: string } = {},
): RulesCheck {
	const allow = errors[keys.allow ?? "allowCidrs"];
	const deny = errors[keys.deny ?? "denyCidrs"];
	const rules = errors[keys.rules ?? "accessRules"];
	return {
		entries: errors,
		...(allow ? { allow } : {}),
		...(deny ? { deny } : {}),
		...(rules ? { rules } : {}),
	};
}
