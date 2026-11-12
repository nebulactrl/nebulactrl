// The service firewall, as the editor sees it. Pure: the sentences a rule
// reads as, the checks the control plane applies (pkg/agentprotocol/
// firewall.go), and the decision the proxy makes for a request, so the
// "Try a request" panel answers before anything is applied.
import type {
	FirewallCondition,
	FirewallView,
	FirewallRule as RuleBody,
	StandardRule,
} from "#/api/gen/types.gen";
import { parseNetwork, validateEntry } from "#/features/access-rules/rules";

export type { FirewallCondition, FirewallView, StandardRule };
/** A rule in the editor: always has its id (the API fills in a missing one). */
export type FirewallRule = RuleBody & { id: string };
export type Field = FirewallCondition["field"];
export type Op = FirewallCondition["op"];

/** Limits of pkg/agentprotocol/firewall.go. */
export const MAX_RULES = 50;
export const MAX_CONDITIONS = 10;
export const MAX_VALUES = 50;
export const MAX_IP_VALUES = 200;
const MAX_NAME = 80;
const MAX_VALUE = 512;
const MAX_FIELD_NAME = 128;

export const METHODS = [
	"GET",
	"HEAD",
	"POST",
	"PUT",
	"PATCH",
	"DELETE",
	"OPTIONS",
	"CONNECT",
	"TRACE",
] as const;

export interface FieldInfo {
	value: Field;
	label: string;
	/** What the field is, for the picker. */
	hint: string;
	/** The field takes a header or parameter name. */
	named?: boolean;
	/** The values are written in mono (addresses, paths, codes). */
	mono?: boolean;
	placeholder?: string;
}

export const FIELDS: readonly FieldInfo[] = [
	{
		value: "path",
		label: "Path",
		hint: "The address after the domain, like /admin",
		mono: true,
		placeholder: "/admin",
	},
	{
		value: "ip",
		label: "IP address",
		hint: "The visitor's address or network",
		mono: true,
		placeholder: "203.0.113.7 or 10.0.0.0/8",
	},
	{
		value: "country",
		label: "Country",
		hint: "Where the visitor's address is registered",
		placeholder: "Germany or DE",
	},
	{ value: "method", label: "Method", hint: "GET, POST and the like" },
	{
		value: "userAgent",
		label: "User agent",
		hint: "The browser or tool the visitor names",
		placeholder: "curl",
	},
	{
		value: "header",
		label: "Header",
		hint: "A request header, by name",
		named: true,
		mono: true,
		placeholder: "value",
	},
	{
		value: "query",
		label: "Query parameter",
		hint: "A ?name=value in the address",
		named: true,
		mono: true,
		placeholder: "value",
	},
];

export function fieldInfo(field: Field): FieldInfo {
	return FIELDS.find((f) => f.value === field) ?? (FIELDS[0] as FieldInfo);
}

/** One entry of the operator picker: an operator, possibly negated. */
export interface OpChoice {
	key: string;
	op: Op;
	not: boolean;
	label: string;
}

const choice = (op: Op, not: boolean, label: string): OpChoice => ({
	key: `${op}${not ? "!" : ""}`,
	op,
	not,
	label,
});

/** The operators of a field with their negations, in reading order. */
export function opChoices(field: Field): OpChoice[] {
	switch (field) {
		case "ip":
		case "country":
		case "method":
			return [choice("in", false, "is"), choice("in", true, "is not")];
		case "path":
			return [
				choice("prefix", false, "starts with"),
				choice("prefix", true, "does not start with"),
				choice("exact", false, "is"),
				choice("exact", true, "is not"),
				choice("contains", false, "contains"),
				choice("contains", true, "does not contain"),
				choice("regex", false, "matches"),
				choice("regex", true, "does not match"),
			];
		case "userAgent":
			return [
				choice("contains", false, "contains"),
				choice("contains", true, "does not contain"),
				choice("regex", false, "matches"),
				choice("regex", true, "does not match"),
			];
		case "header":
		case "query":
			return [
				choice("equals", false, "is"),
				choice("equals", true, "is not"),
				choice("contains", false, "contains"),
				choice("contains", true, "does not contain"),
				choice("regex", false, "matches"),
				choice("regex", true, "does not match"),
				choice("present", false, "is present"),
				choice("present", true, "is missing"),
			];
	}
}

export function opKey(c: Pick<FirewallCondition, "op" | "not">): string {
	return `${c.op}${c.not ? "!" : ""}`;
}

/** A condition switched to another field keeps what still makes sense. */
export function withField(
	c: FirewallCondition,
	field: Field,
): FirewallCondition {
	const first = opChoices(field)[0] as OpChoice;
	const keep = opChoices(field).find((o) => o.key === opKey(c));
	const op = keep ?? first;
	const next: FirewallCondition = { field, op: op.op, values: [] };
	if (op.not) next.not = true;
	if (fieldInfo(field).named) next.name = c.name ?? "";
	return next;
}

export function withOp(c: FirewallCondition, key: string): FirewallCondition {
	const op = opChoices(c.field).find((o) => o.key === key);
	if (!op) return c;
	const next: FirewallCondition = { ...c, op: op.op };
	if (op.not) next.not = true;
	else delete next.not;
	if (op.op === "present") next.values = [];
	return next;
}

// --- countries ---------------------------------------------------------------

/** ISO 3166-1 alpha-2 codes (and XK, which GeoIP databases use for Kosovo). */
export const COUNTRY_CODES =
	"AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS XK YE YT ZA ZM ZW".split(
		" ",
	);

const regionNames =
	typeof Intl !== "undefined" && "DisplayNames" in Intl
		? new Intl.DisplayNames(["en"], { type: "region" })
		: null;

/** "Germany" for DE; the code itself when the runtime has no names. */
export function countryName(code: string): string {
	try {
		return regionNames?.of(code) ?? code;
	} catch {
		return code;
	}
}

/** The code a person typed: "de", "DE", "Germany" or "DE · Germany". */
export function parseCountry(text: string): string | null {
	const t = text.trim();
	if (t === "") return null;
	const head = t.split(/[\s·—-]/)[0]?.toUpperCase() ?? "";
	if (head.length === 2 && COUNTRY_CODES.includes(head)) return head;
	const lower = t.toLowerCase();
	return (
		COUNTRY_CODES.find((c) => countryName(c).toLowerCase() === lower) ?? null
	);
}

// --- sentences ---------------------------------------------------------------

/** "a", "a or b", "a, b or c", "a, b and 3 more". */
export function listPhrase(values: readonly string[], joiner = "or"): string {
	if (values.length === 0) return "…";
	if (values.length > 3) {
		return `${values.slice(0, 2).join(", ")} and ${values.length - 2} more`;
	}
	if (values.length === 1) return values[0] as string;
	return `${values.slice(0, -1).join(", ")} ${joiner} ${values.at(-1)}`;
}

const SUBJECT: Record<Field, (c: FirewallCondition) => string> = {
	ip: () => "the IP address",
	country: () => "the country",
	path: () => "the path",
	method: () => "the method",
	userAgent: () => "the user agent",
	header: (c) => `the header ${c.name || "…"}`,
	query: (c) => `the query parameter ${c.name || "…"}`,
};

/** "the path starts with /admin", "the country is not Germany or France". */
export function conditionSentence(c: FirewallCondition): string {
	const op = opChoices(c.field).find((o) => o.key === opKey(c));
	const values = c.values ?? [];
	const shown =
		c.field === "country" ? values.map(countryName) : values.map((v) => v);
	const subject = SUBJECT[c.field](c);
	if (c.op === "present") return `${subject} ${op?.label ?? ""}`;
	return `${subject} ${op?.label ?? c.op} ${listPhrase(shown)}`;
}

/** "Block requests when the path starts with /admin and …". */
export function ruleSentence(rule: FirewallRule): string {
	const conds = rule.conditions ?? [];
	const verb = rule.action === "block" ? "Block" : "Allow";
	if (conds.length === 0) return `${verb} every request`;
	return `${verb} requests when ${conds.map(conditionSentence).join(" and ")}`;
}

// --- validation ----------------------------------------------------------------

/** Error keys: `rules`, `<ruleId>.name`, `<ruleId>.conditions`, `<ruleId>.<i>.name`, `<ruleId>.<i>.values`. */
export type RuleErrors = Record<string, string>;

const HEADER_NAME = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
const PLAIN = /^[^`\r\n]*$/;
/** RE2 (Go) has no lookaround and no backreferences; JavaScript does. */
const NOT_RE2 = /\(\?<?[=!]|\\[1-9]|\\k</;

/**
 * A Go regular expression as a JavaScript one: a leading inline flag group
 * like (?i) becomes flags (JavaScript has no inline flags; U has no
 * counterpart and is dropped).
 */
export function jsRegex(source: string): RegExp {
	const lead = /^\(\?([imsU]+)\)/.exec(source);
	if (!lead) return new RegExp(source);
	const flags = [...new Set((lead[1] ?? "").replace(/U/g, ""))].join("");
	return new RegExp(source.slice(lead[0].length), flags);
}

export function regexProblem(source: string): string | null {
	if (NOT_RE2.test(source)) {
		return "Lookarounds and backreferences are not supported (Go regular expressions).";
	}
	try {
		jsRegex(source);
		return null;
	} catch (error) {
		return `Not a valid regular expression: ${(error as Error).message}`;
	}
}

function valueProblem(c: FirewallCondition, v: string): string | null {
	if (v.length > MAX_VALUE)
		return `Values are at most ${MAX_VALUE} characters.`;
	if (!PLAIN.test(v)) return "Values cannot contain backticks or line breaks.";
	switch (c.field) {
		case "ip":
			return validateEntry(v);
		case "country":
			return COUNTRY_CODES.includes(v) ? null : `“${v}” is not a country code.`;
		case "method":
			return (METHODS as readonly string[]).includes(v)
				? null
				: `“${v}” is not an HTTP method.`;
	}
	if (c.field === "path" && (c.op === "prefix" || c.op === "exact")) {
		return v.startsWith("/") ? null : "A path starts with /.";
	}
	if (c.op === "regex") return regexProblem(v);
	return null;
}

/** What the control plane would refuse, keyed for the editor. */
export function validateRules(rules: readonly FirewallRule[]): RuleErrors {
	const errors: RuleErrors = {};
	if (rules.length > MAX_RULES) errors.rules = `At most ${MAX_RULES} rules.`;
	for (const rule of rules) {
		const name = rule.name.trim();
		if (name === "") errors[`${rule.id}.name`] = "Name the rule.";
		else if (name.length > MAX_NAME) {
			errors[`${rule.id}.name`] = `At most ${MAX_NAME} characters.`;
		}
		const conds = rule.conditions ?? [];
		if (conds.length === 0) {
			errors[`${rule.id}.conditions`] = "Add at least one condition.";
		} else if (conds.length > MAX_CONDITIONS) {
			errors[`${rule.id}.conditions`] = `At most ${MAX_CONDITIONS} conditions.`;
		}
		conds.forEach((c, i) => {
			const at = `${rule.id}.${i}`;
			if (fieldInfo(c.field).named) {
				const n = (c.name ?? "").trim();
				if (n === "")
					errors[`${at}.name`] =
						`Name the ${c.field === "header" ? "header" : "parameter"}.`;
				else if (n.length > MAX_FIELD_NAME) {
					errors[`${at}.name`] = `At most ${MAX_FIELD_NAME} characters.`;
				} else if (c.field === "header" && !HEADER_NAME.test(n)) {
					errors[`${at}.name`] = `“${n}” is not a header name.`;
				} else if (!PLAIN.test(n)) {
					errors[`${at}.name`] =
						"Names cannot contain backticks or line breaks.";
				}
			}
			if (c.op === "present") return;
			const values = c.values ?? [];
			const limit = c.field === "ip" ? MAX_IP_VALUES : MAX_VALUES;
			if (values.length === 0) {
				errors[`${at}.values`] = "Add at least one value.";
				return;
			}
			if (values.length > limit) {
				errors[`${at}.values`] = `At most ${limit} values.`;
				return;
			}
			for (const v of values) {
				const problem = valueProblem(c, v);
				if (problem) {
					errors[`${at}.values`] = problem;
					return;
				}
			}
		});
	}
	return errors;
}

// --- evaluation ----------------------------------------------------------------

/** A request as the "Try a request" panel describes it. */
export interface TestRequest {
	method: string;
	/** Path, optionally with ?query. */
	target: string;
	ip: string;
	/** ISO code, "" unknown. */
	country: string;
	userAgent: string;
	/** "Name: value" lines. */
	headers: string;
}

export type Decision =
	| { kind: "pass" }
	/** A rule of the service, by its position. */
	| {
			kind: "block" | "allow";
			index: number;
			rule: FirewallRule;
			standard?: undefined;
	  }
	/** A rule of the standard protection. */
	| {
			kind: "block" | "allow";
			index: number;
			rule: FirewallRule;
			standard: StandardRule;
	  };

interface Parsed {
	method: string;
	path: string;
	query: Map<string, string[]>;
	headers: Map<string, string[]>;
	ip: string;
	country: string;
}

function parseRequest(req: TestRequest): Parsed {
	const target = req.target.trim() || "/";
	const [rawPath = "/", rawQuery = ""] = target.split("?", 2);
	let path = rawPath.startsWith("/") ? rawPath : `/${rawPath}`;
	try {
		path = decodeURIComponent(path);
	} catch {
		// Keep it as typed.
	}
	const query = new Map<string, string[]>();
	for (const [k, v] of new URLSearchParams(rawQuery)) {
		query.set(k, [...(query.get(k) ?? []), v]);
	}
	const headers = new Map<string, string[]>();
	const add = (name: string, value: string) => {
		const key = name.trim().toLowerCase();
		if (key) headers.set(key, [...(headers.get(key) ?? []), value.trim()]);
	};
	for (const line of req.headers.split("\n")) {
		const colon = line.indexOf(":");
		if (colon > 0) add(line.slice(0, colon), line.slice(colon + 1));
	}
	if (req.userAgent.trim()) add("user-agent", req.userAgent);
	return {
		method: req.method.toUpperCase(),
		path,
		query,
		headers,
		ip: req.ip.trim(),
		country: req.country,
	};
}

const containsRe = (v: string) =>
	new RegExp(v.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "i");

function testValue(
	c: FirewallCondition,
	got: readonly string[] | undefined,
): boolean {
	if (c.op === "present") return got !== undefined && got.length > 0;
	if (!got) return false;
	const values = c.values ?? [];
	return got.some((g) =>
		values.some((v) => {
			switch (c.op) {
				case "prefix":
					return g.startsWith(v);
				case "exact":
				case "equals":
					return g === v;
				case "contains":
					return containsRe(v).test(g);
				case "regex":
					try {
						return jsRegex(v).test(g);
					} catch {
						return false;
					}
				default:
					return false;
			}
		}),
	);
}

/** The visitor address as a network of one, IPv4-mapped IPv6 as IPv4. */
function clientNetwork(ip: string) {
	const mapped = /^::ffff:(\d+\.\d+\.\d+\.\d+)$/i.exec(ip);
	const net = parseNetwork(mapped?.[1] ?? ip);
	return net?.single ? net : null;
}

function matches(c: FirewallCondition, r: Parsed): boolean {
	switch (c.field) {
		case "ip": {
			const client = clientNetwork(r.ip);
			if (!client) return false;
			const w = client.family === 4 ? 32 : 128;
			return (c.values ?? []).some((v) => {
				const net = parseNetwork(v);
				if (!net || net.family !== client.family) return false;
				const shift = BigInt(w - net.bits);
				return client.addr >> shift === net.addr >> shift;
			});
		}
		case "country":
			return r.country !== "" && (c.values ?? []).includes(r.country);
		case "method":
			return (c.values ?? []).includes(r.method);
		case "path":
			return testValue(c, [r.path]);
		case "userAgent":
			return testValue(c, r.headers.get("user-agent"));
		case "header":
			return testValue(c, r.headers.get((c.name ?? "").toLowerCase()));
		case "query":
			return testValue(c, r.query.get(c.name ?? ""));
	}
}

/**
 * What the proxy does with req: the service's enabled rules in order, then the
 * standard protection; the first rule whose conditions all match decides, and
 * no match lets the request through (FirewallMatcher.Decide on the route's
 * firewall).
 */
export function decide(
	rules: readonly FirewallRule[],
	req: TestRequest,
	standard: readonly StandardRule[] = [],
): Decision {
	const r = parseRequest(req);
	const hit = (rule: RuleBody) => {
		const conds = rule.conditions ?? [];
		return (
			!rule.disabled &&
			conds.length > 0 &&
			conds.every((c) => matches(c, r) !== Boolean(c.not))
		);
	};
	for (const [index, rule] of rules.entries()) {
		if (hit(rule)) return { kind: rule.action, index, rule };
	}
	for (const [index, s] of standard.entries()) {
		if (hit(s.rule))
			return {
				kind: s.rule.action,
				index,
				rule: { ...s.rule, id: s.rule.id ?? "" },
				standard: s,
			};
	}
	return { kind: "pass" };
}

// --- drafts ---------------------------------------------------------------------

export function newRuleId(): string {
	const bytes = new Uint8Array(5);
	crypto.getRandomValues(bytes);
	return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

export function blankRule(): FirewallRule {
	return {
		id: newRuleId(),
		name: "New rule",
		action: "block",
		conditions: [{ field: "path", op: "prefix", values: [] }],
	};
}

/** Rules as the API returns them, normalised for comparison. */
export function rulesOf(view: FirewallView | undefined): FirewallRule[] {
	return (view?.rules ?? []).map((r) => ({
		...r,
		id: r.id || newRuleId(),
		conditions: (r.conditions ?? []).map((c) => ({
			...c,
			values: c.values ?? [],
		})),
	}));
}

const canonical = (rules: readonly FirewallRule[]) =>
	JSON.stringify(
		rules.map((r) => ({
			id: r.id,
			name: r.name.trim(),
			action: r.action,
			disabled: Boolean(r.disabled),
			conditions: (r.conditions ?? []).map((c) => ({
				field: c.field,
				op: c.op,
				not: Boolean(c.not),
				name: (c.name ?? "").trim(),
				values: c.values ?? [],
			})),
		})),
	);

export function rulesEqual(
	a: readonly FirewallRule[],
	b: readonly FirewallRule[],
): boolean {
	return canonical(a) === canonical(b);
}

/** The body of PUT /services/{id}/firewall. */
export function toBody(rules: readonly FirewallRule[]): {
	rules: FirewallRule[];
} {
	return {
		rules: rules.map((r) => {
			const out: FirewallRule = {
				id: r.id,
				name: r.name.trim(),
				action: r.action,
				conditions: (r.conditions ?? []).map((c) => {
					const cond: FirewallCondition = { field: c.field, op: c.op };
					if (c.not) cond.not = true;
					if (fieldInfo(c.field).named) cond.name = (c.name ?? "").trim();
					if (c.op !== "present") cond.values = c.values ?? [];
					return cond;
				}),
			};
			if (r.disabled) out.disabled = true;
			return out;
		}),
	};
}

export function moveRule<T>(list: readonly T[], from: number, to: number): T[] {
	if (to < 0 || to >= list.length || from === to) return [...list];
	const next = [...list];
	const [item] = next.splice(from, 1);
	next.splice(to, 0, item as T);
	return next;
}

// --- recipes --------------------------------------------------------------------

export interface Recipe {
	id: string;
	title: string;
	/** What it does, in one line. */
	description: string;
	/** The rule; values a person must fill in are left empty. */
	build(): FirewallRule;
	/** It matches countries: the server needs geolocation. */
	geo?: boolean;
}

export const RECIPES: readonly Recipe[] = [
	{
		id: "wordpress",
		title: "Block WordPress probes",
		description:
			"For apps that are not WordPress: wp-login.php, xmlrpc.php, wp-admin and phpMyAdmin.",
		build: () => ({
			id: newRuleId(),
			name: "Block WordPress probes",
			action: "block",
			conditions: [
				{
					field: "path",
					op: "contains",
					values: [
						"wp-login.php",
						"xmlrpc.php",
						"/wp-admin",
						"/wp-includes",
						"phpmyadmin",
					],
				},
			],
		}),
	},
	{
		id: "tools",
		title: "Block attack tools",
		description:
			"Scanners that name themselves: sqlmap, Nikto, Nuclei, masscan, zgrab.",
		build: () => ({
			id: newRuleId(),
			name: "Block attack tools",
			action: "block",
			conditions: [
				{
					field: "userAgent",
					op: "contains",
					values: ["sqlmap", "nikto", "nuclei", "masscan", "zgrab"],
				},
			],
		}),
	},
	{
		id: "admin",
		title: "Lock a path to your network",
		description: "Only your addresses reach /admin; everyone else gets 403.",
		build: () => ({
			id: newRuleId(),
			name: "Admin from our network only",
			action: "block",
			conditions: [
				{ field: "path", op: "prefix", values: ["/admin"] },
				{ field: "ip", op: "in", not: true, values: [] },
			],
		}),
	},
	{
		id: "countries",
		title: "Block countries",
		description: "Refuse visitors from the countries you pick.",
		geo: true,
		build: () => ({
			id: newRuleId(),
			name: "Block countries",
			action: "block",
			conditions: [{ field: "country", op: "in", values: [] }],
		}),
	},
];

// --- the state line -------------------------------------------------------------

export function activeCount(rules: readonly FirewallRule[]): number {
	return rules.filter((r) => !r.disabled).length;
}

/** The section's aside: "Standard protection", "3 rules + standard", "2 of 3 rules on + standard". */
export function summary(rules: readonly FirewallRule[]): string {
	const active = activeCount(rules);
	if (active === 0) return "Standard protection";
	const total = rules.length;
	const noun = total === 1 ? "rule" : "rules";
	return active === total
		? `${total} ${noun} + standard`
		: `${active} of ${total} ${noun} on + standard`;
}

export function usesCountry(rules: readonly FirewallRule[]): boolean {
	return rules.some(
		(r) =>
			!r.disabled && (r.conditions ?? []).some((c) => c.field === "country"),
	);
}
