// The service firewall, as the editor sees it. Pure: the sentences a rule
// reads as, the checks the control plane applies (pkg/agentprotocol/
// firewall.go), and the decision the proxy makes for a request, so the
// "Try a request" panel answers before anything is applied.
import type {
	AutoBan,
	BanView,
	FirewallBlocks,
	FirewallCondition,
	FirewallRuleCount,
	FirewallView,
	FirewallRule as RuleBody,
	StandardRule,
} from "#/api/gen/types.gen";
import { parseNetwork, validateEntry } from "#/features/access-rules/rules";
import { formatValue } from "#/features/logs/search/facets";

export type { FirewallCondition, FirewallView, StandardRule };
/** A rule in the editor: always has its id (the API fills in a missing one). */
export type FirewallRule = RuleBody & { id: string };
export type Field = FirewallCondition["field"];
type Op = FirewallCondition["op"];

/** Limits of pkg/agentprotocol/firewall.go. */
export const MAX_RULES = 50;
export const MAX_CONDITIONS = 10;
const MAX_VALUES = 50;
const MAX_IP_VALUES = 200;
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
function conditionSentence(c: FirewallCondition): string {
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
function jsRegex(source: string): RegExp {
	const lead = /^\(\?([imsU]+)\)/.exec(source);
	if (!lead) return new RegExp(source);
	const flags = [...new Set((lead[1] ?? "").replace(/U/g, ""))].join("");
	return new RegExp(source.slice(lead[0].length), flags);
}

function regexProblem(source: string): string | null {
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

export type RecipeGroup = "probes" | "bots" | "access";

/** The headings recipes are listed under, in display order. */
export const RECIPE_GROUPS: readonly { id: RecipeGroup; title: string }[] = [
	{ id: "probes", title: "Probes and exploits" },
	{ id: "bots", title: "Bots and crawlers" },
	{ id: "access", title: "Access" },
];

export interface Recipe {
	id: string;
	title: string;
	/** What it blocks, in one line. */
	description: string;
	group: RecipeGroup;
	/** Who should not use it, in one short line. */
	caution?: string;
	/**
	 * The rules, with fresh ids. A signature that spans fields, or a list over
	 * the values-per-condition limit, takes several; values a person must fill
	 * in are left empty.
	 */
	rules(): FirewallRule[];
	/** It matches countries: the server needs geolocation. */
	geo?: boolean;
}

const block = (
	name: string,
	...conditions: FirewallCondition[]
): FirewallRule => ({
	id: newRuleId(),
	name,
	action: "block",
	conditions,
});

const pathRegex = (...values: string[]): FirewallCondition => ({
	field: "path",
	op: "regex",
	values,
});

const uaContains = (...values: string[]): FirewallCondition => ({
	field: "userAgent",
	op: "contains",
	values,
});

const uaRegex = (...values: string[]): FirewallCondition => ({
	field: "userAgent",
	op: "regex",
	values,
});

const headerRegex = (name: string, ...values: string[]): FirewallCondition => ({
	field: "header",
	op: "regex",
	name,
	values,
});

/** `${jndi:`, `${lower:` and nested `${${`: the Log4Shell lookups. */
const LOG4SHELL = [
	String.raw`(?i)\$\{\s*(jndi|ctx|lower|upper|env|sys|java|date|base64|::-|[a-z]{0,10}:-)`,
	String.raw`(?i)\$\{[^}]{0,40}\$\{`,
];

/** `() {` at the start of a function definition: Shellshock. */
const SHELLSHOCK = String.raw`\(\)\s*\{\s*[:;]`;

/**
 * Every recipe, listed within its group in the order the picker shows. Regular
 * expressions are Go (RE2) and unanchored: the ones that must match from the
 * start say so with ^.
 */
export const RECIPES: readonly Recipe[] = [
	{
		id: "php-files",
		title: "Block PHP file probes",
		description:
			"Scanners request random .php files and web shells at the site root.",
		group: "probes",
		caution: "Not for apps written in PHP.",
		rules: () => [
			block(
				"Block PHP file probes",
				pathRegex(
					String.raw`(?i)^/[^/]+\.(php[0-9s]?|phtml|phar)(/.*)?$`,
					String.raw`(?i)^/\.well-known/.*\.php[0-9s]?$`,
					String.raw`(?i)^/(wp-content|wp-admin|wp-includes|vendor|uploads|assets|images|admin|administrator|includes|plugins|themes|modules|storage|cgi-bin)/.*\.php[0-9s]?(/.*)?$`,
				),
			),
		],
	},
	{
		id: "php-framework",
		title: "Block PHP framework and debug probes",
		description:
			"PHPUnit, Laravel Ignition, Symfony profiler and other leftovers no app should serve.",
		group: "probes",
		rules: () => [
			block(
				"Block PHP framework and debug probes",
				pathRegex(
					"(?i)/vendor/(phpunit|composer|laravel|symfony|guzzlehttp|monolog|doctrine|twig|psr|mockery|yiisoft|bin)/",
					String.raw`(?i)/vendor/autoload\.php$`,
					String.raw`(?i)/eval-stdin\.php$`,
					String.raw`(?i)^/(_ignition|_profiler|_wdt|_debugbar|_fragment|app_dev\.php|app_test\.php|debug/default)(/|$)`,
				),
			),
			block("Block ThinkPHP exploits", {
				field: "query",
				op: "regex",
				name: "s",
				values: [
					String.raw`(?i)(\\think\\|invokefunction|call_user_func|think\\app)`,
				],
			}),
		],
	},
	{
		id: "wordpress",
		title: "Block WordPress probes",
		description:
			"wp-login.php, xmlrpc.php, wp-admin, wp-content, phpMyAdmin and other WordPress targets.",
		group: "probes",
		caution: "Not for WordPress sites.",
		rules: () => [
			block("Block WordPress probes", {
				field: "path",
				op: "contains",
				values: [
					"wp-login.php",
					"xmlrpc.php",
					"/wp-admin",
					"/wp-includes",
					"phpmyadmin",
					"/wp-content",
					"/wp-json",
					"wp-cron.php",
					"wp-config",
					"wp-signup.php",
					"wp-trackback.php",
					"wp-comments-post.php",
					"wp-links-opml.php",
					"wlwmanifest.xml",
					"xmrlpc.php",
					"wp-conflg.php",
					"/adminer.php",
					"/pma/",
					"/myadmin/",
					"/mysqladmin",
				],
			}),
		],
	},
	{
		id: "exposed-services",
		title: "Block exposed-service probes",
		description:
			"server-status, Docker and Elasticsearch APIs, Vite dev endpoints, phpinfo pages.",
		group: "probes",
		caution: "Not for dev servers or Docker and Kubernetes API hosts.",
		rules: () => [
			block(
				"Block exposed-service probes",
				pathRegex(
					"(?i)^/(server-status|server-info|nginx_status|stub_status|_all_dbs|_cat|_cluster|_nodes|containers/json|images/json|api/v1/(pods|namespaces|secrets)|@fs|@vite/env|jolokia|heapdump)(/|$)",
					String.raw`(?i)^/(phpinfo|php_info)(\.php)?$`,
					String.raw`(?i)^/(info|test)\.php$`,
				),
			),
		],
	},
	{
		id: "java",
		title: "Block Java and Spring probes",
		description:
			"Spring actuator, Tomcat manager, Jenkins, JSP and other server-script pages.",
		group: "probes",
		caution: "Not for Java, Spring, ASP.NET or ColdFusion apps.",
		rules: () => [
			block(
				"Block Java and Spring probes",
				pathRegex(
					"(?i)^/(actuator|jolokia|heapdump|threaddump|h2-console|jmx-console|web-console|invoker|manager/html|manager/text|host-manager|axis2|struts|solr|jenkins|hudson|nacos|druid|geoserver|console/login|WEB-INF|META-INF)(/|$|;)",
					"(?i)^(/[a-z0-9_-]+)?/actuator(/|$)",
					String.raw`(?i)\.(jsp|jspx|jspa|do|action|aspx?|ashx|asmx|axd|cfm|cfc)$`,
				),
			),
		],
	},
	{
		id: "cms",
		title: "Block CMS and admin-panel probes",
		description:
			"Joomla, Drupal, Magento, TYPO3, phpMyAdmin, Adminer and file-manager paths.",
		group: "probes",
		caution: "Not for Joomla, Drupal, Magento, TYPO3 or phpMyAdmin.",
		rules: () => [
			block(
				"Block CMS and admin-panel probes",
				pathRegex(
					String.raw`(?i)^/(administrator|joomla|typo3|typo3conf|fileadmin|umbraco|sitecore|bitrix|ckeditor|fckeditor|kcfinder|elfinder|filemanager|ueditor|magmi|downloader|webalizer|awstats|phpmyadmin[0-9.-]*|pma|myadmin|mysqladmin|dbadmin|sqladmin|websql|adminer(\.php)?)(/|$)`,
					String.raw`(?i)^/(sites/default|core/install\.php|misc/drupal\.js|rest/V1|app/etc|components/com_|language/en-GB|customer/account/create|checkout/cart/add)`,
				),
			),
		],
	},
	{
		id: "backups",
		title: "Block backup and database dumps",
		description:
			"Requests for backup.sql, site.zip, wp-config.php.bak and similar leftovers.",
		group: "probes",
		caution: "Not if you serve downloads named like backup.zip or dump.sql.",
		rules: () => [
			block(
				"Block backup and database dumps",
				pathRegex(
					String.raw`(?i)^(/[^/]+)?/[^/]+\.(sql|sql\.gz|sql\.zip|sql\.bz2|sqlite3?|bak|old|orig|swp|save)$`,
					String.raw`(?i)^/(backup|backups|bak|db|database|dump|site|sitebackup|www|wwwroot|web|website|public_html|htdocs|old)[-_.0-9a-z]*\.(zip|tar|tar\.gz|tgz|gz|7z|rar|bz2)$`,
				),
			),
		],
	},
	{
		id: "routers",
		title: "Block router, camera and VPN probes",
		description:
			"Home-router, camera and VPN-appliance exploits, plus /cgi-bin/ scripts.",
		group: "probes",
		caution: "Not if you serve real CGI scripts or an Axis camera.",
		rules: () => [
			block(
				"Block router, camera and VPN probes",
				pathRegex(
					String.raw`(?i)^/(boaform|hnap1|gponform|sdk/weblanguage|isapi|goform|cgi-bin|dana-na|dana-ws|global-protect|mgmt/tm|tmui|sslvpn|\+cscoe\+|stalker_portal|setup\.cgi|login\.cgi|ssi\.cgi|remote/(logincheck|fgt_lang))(/|$)`,
				),
			),
		],
	},
	{
		id: "exchange",
		title: "Block Exchange and Outlook probes",
		description:
			"Autodiscover, OWA, ECP and ProxyShell paths that only an Exchange server answers.",
		group: "probes",
		caution: "Not for mail servers (Exchange, Mailcow, Mailu, Zimbra).",
		rules: () => [
			block(
				"Block Exchange and Outlook probes",
				pathRegex(
					String.raw`(?i)^(/[a-z]{2}(-[a-z]{2})?)?/autodiscover/autodiscover\.(xml|json)$`,
					"(?i)^/(owa|ecp|ews|mapi|rpc|oab|powershell|aspnet_client|microsoft-server-activesync)(/|$)",
				),
			),
		],
	},
	{
		id: "signatures",
		title: "Block attack signatures",
		description:
			"Log4Shell and Shellshock strings in headers or paths, and requests for OS files.",
		group: "probes",
		rules: () => [
			block("Block Log4Shell in user agents", uaRegex(...LOG4SHELL)),
			block(
				"Block Log4Shell in referers",
				headerRegex("Referer", ...LOG4SHELL),
			),
			block(
				"Block Log4Shell in forwarded addresses",
				headerRegex("X-Forwarded-For", ...LOG4SHELL),
			),
			block("Block Log4Shell in paths", pathRegex(...LOG4SHELL)),
			block("Block Shellshock in user agents", uaRegex(SHELLSHOCK)),
			block("Block Shellshock in referers", headerRegex("Referer", SHELLSHOCK)),
			block(
				"Block requests for OS files",
				pathRegex(
					String.raw`(?i)^/(etc/(passwd|shadow|group|hosts|issue|os-release)|proc/(self|version|cpuinfo|\d+)|bin/(ba|da)?sh|usr/bin/|windows/(win|system)\.ini|winnt/|boot\.ini|var/(log|run)/)`,
				),
			),
		],
	},
	{
		id: "tools",
		title: "Block attack tools",
		description:
			"Scanners and exploit tools that name themselves: sqlmap, Nikto, Nuclei, masscan, ffuf.",
		group: "bots",
		rules: () => [
			block(
				"Block attack tools",
				uaContains(
					"sqlmap",
					"nikto",
					"nuclei",
					"masscan",
					"zgrab",
					"nmap",
					"whatweb",
					"whatwaf",
					"dirbuster",
					"gobuster",
					"feroxbuster",
					"fuzz faster u fool",
					"wfuzz",
					"commix",
					"havij",
					"sqlninja",
					"jsql",
					"ghauri",
					"libwhisker",
					"dotdotpwn",
					"xsstrike",
					"xsser",
					"lfisuite",
					"trufflehog",
					"libredtail",
					"muhstik",
					"jorgee",
					"zmeu",
					"morfeus",
					"l9explore",
					"l9tcpid",
					"l9scan",
					"leakix",
					"censysinspect",
					"internetmeasurement",
					"sysscan",
				),
			),
			block(
				"Block vulnerability scanners",
				uaContains(
					"wpscan",
					"wpprobe",
					"wprecon",
					"joomscan",
					"droopescan",
					"cmsmap",
					"acunetix",
					"netsparker",
					"nessus",
					"openvas",
					"arachni",
					"w3af",
					"skipfish",
					"wapiti",
					"detectify",
					"appscan",
					"webinspect",
					"qualysguard",
					"ironwasp",
					"tsunamisecurityscanner",
				),
			),
			block(
				"Block fake browser agents",
				uaRegex(
					"(?i)^(Mozlila|Mozila)/",
					String.raw`^Mozilla/[6-9]\.[0-9]`,
					String.raw`(?i)^user-?agent:?\s`,
					String.raw`\(Hydra\)`,
					"(?i)fuzz faster u fool",
				),
			),
		],
	},
	{
		id: "ai-training",
		title: "Block AI training crawlers",
		description:
			"GPTBot, ClaudeBot, CCBot, Bytespider and other AI data collectors.",
		group: "bots",
		caution: "Not if you want to appear in AI training data.",
		rules: () => [
			block(
				"Block AI training crawlers",
				uaContains(
					"gptbot",
					"claudebot",
					"anthropic-ai",
					"claude-web",
					"ccbot",
					"bytespider",
					"tiktokspider",
					"amazonbot",
					"meta-externalagent",
					"facebookbot",
					"cohere-ai",
					"cohere-training-data-crawler",
					"diffbot",
					"imagesiftbot",
					"timpibot",
					"omgili",
					"pangubot",
					"ai2bot",
					"chatglm-spider",
					"friendlycrawler",
					"img2dataset",
					"sbintuitionsbot",
					"kangaroo bot",
					"brightbot",
					"crawl4ai",
					"deepseekbot",
					"webzio",
					"icc-crawler",
					"laiondownloader",
					"googleother",
				),
			),
		],
	},
	{
		id: "ai-answers",
		title: "Block AI answer and search bots",
		description:
			"ChatGPT-User, Claude-User, PerplexityBot and other AI search fetchers.",
		group: "bots",
		caution: "Not if you want AI assistants to cite or open your pages.",
		rules: () => [
			block(
				"Block AI answer and search bots",
				uaContains(
					"oai-searchbot",
					"chatgpt-user",
					"claude-user",
					"claude-searchbot",
					"perplexitybot",
					"perplexity-user",
					"duckassistbot",
					"amzn-searchbot",
					"amzn-user",
					"meta-externalfetcher",
					"meta-webindexer",
					"mistralai-user",
					"manus-user",
					"kimi-user",
					"diffbot-user",
					"gemini-deep-research",
					"google-notebooklm",
				),
			),
		],
	},
	{
		id: "seo",
		title: "Block SEO crawlers",
		description:
			"AhrefsBot, SemrushBot, MJ12bot, DotBot, PetalBot and similar backlink crawlers.",
		group: "bots",
		caution: "Not if you track backlinks with Ahrefs, Semrush or Majestic.",
		rules: () => [
			block(
				"Block SEO crawlers",
				uaContains(
					"ahrefsbot",
					"semrushbot",
					"mj12bot",
					"dotbot",
					"blexbot",
					"dataforseobot",
					"petalbot",
					"serpstatbot",
					"barkrowler",
					"megaindex",
					"seekportbot",
					"seokicks",
					"sistrix",
					"linkdexbot",
					"rogerbot",
					"zoominfobot",
					"spbot",
					"mauibot",
					"mediatoolkitbot",
					"awariobot",
					"oncrawl",
					"cognitiveseo",
					"majestic",
				),
			),
		],
	},
	{
		id: "scripts",
		title: "Block script-library user agents",
		description:
			"curl, python-requests, Go-http-client and similar tools that name themselves.",
		group: "bots",
		caution:
			"Websites only: breaks APIs, webhooks, health checks and monitors.",
		rules: () => [
			block(
				"Block script-library user agents",
				uaRegex(
					String.raw`(?i)^(python-requests|python-urllib|python-httpx|aiohttp|Go-http-client|okhttp|Java/|Apache-HttpClient|libwww-perl|lwp-|curl/|Wget|node-fetch|axios|undici|scrapy|PHP/|Ruby|Faraday|reqwest|http\.rb|fasthttp|HTTPie)`,
				),
			),
		],
	},
	{
		id: "empty-user-agent",
		title: "Block empty user agents",
		description: "Requests that send no User-Agent header, or a blank one.",
		group: "bots",
		caution: "Not for APIs, webhooks or health checks.",
		rules: () => [
			block("Block empty user agents", {
				field: "userAgent",
				op: "regex",
				not: true,
				values: [String.raw`\S`],
			}),
		],
	},
	{
		id: "admin",
		title: "Lock a path to your network",
		description: "Only your addresses reach /admin; everyone else gets 403.",
		group: "access",
		rules: () => [
			block(
				"Admin from our network only",
				{ field: "path", op: "prefix", values: ["/admin"] },
				{ field: "ip", op: "in", not: true, values: [] },
			),
		],
	},
	{
		id: "countries",
		title: "Block countries",
		description: "Refuse visitors from the countries you pick.",
		group: "access",
		geo: true,
		rules: () => [
			block("Block countries", { field: "country", op: "in", values: [] }),
		],
	},
	{
		id: "trace-connect",
		title: "Block TRACE and CONNECT",
		description:
			"Refuse the TRACE debugging and CONNECT proxy-tunnel HTTP methods.",
		group: "access",
		rules: () => [
			block("Block TRACE and CONNECT", {
				field: "method",
				op: "in",
				values: ["TRACE", "CONNECT"],
			}),
		],
	},
];

/** How many rules each recipe adds, by id (rules() makes fresh ids, so count once). */
const RECIPE_SIZE = new Map(RECIPES.map((r) => [r.id, r.rules().length]));

/** The recipe's rules fit next to count rules without passing the limit. */
export function recipeFits(recipe: Recipe, count: number): boolean {
	return count + (RECIPE_SIZE.get(recipe.id) ?? 1) <= MAX_RULES;
}

// --- the state line -------------------------------------------------------------

function activeCount(rules: readonly FirewallRule[]): number {
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

// --- blocked requests -----------------------------------------------------------

/** The windows the block counts can be read over, shortest first. */
export const BLOCK_RANGES = [
	"24h",
	"7d",
	"30d",
] as const satisfies readonly FirewallBlocks["range"][];
export type BlockRange = (typeof BLOCK_RANGES)[number];

/** The window as a phrase: "the last 24 hours". */
export const BLOCK_RANGE_PHRASE: Record<BlockRange, string> = {
	"24h": "the last 24 hours",
	"7d": "the last 7 days",
	"30d": "the last 30 days",
};

/** Blocked requests per rule id; a rule with no blocks is absent. */
export function blockedCounts(
	blocks: FirewallBlocks | undefined,
): ReadonlyMap<string, number> {
	const counts = new Map<string, number>();
	for (const row of blocks?.byRule ?? []) {
		if (row.requests > 0) counts.set(row.id, row.requests);
	}
	return counts;
}

/** Rules that blocked requests in the window and are gone now, largest first. */
export function removedBlocks(
	blocks: FirewallBlocks | undefined,
): FirewallRuleCount[] {
	return (blocks?.byRule ?? []).filter(
		(row) => row.kind === "removed" && row.requests > 0,
	);
}

/** What a rule is called next to its count: its name, or "Removed rule <id>" once it is gone. */
export function blockedRuleLabel(row: FirewallRuleCount): string {
	return row.kind === "removed" ? `Removed rule ${row.id}` : row.name;
}

/** A count as the rows print it: "—" for none, else grouped digits ("1,204"). */
export function blockedCountText(count: number | undefined): string {
	if (!count || count <= 0) return "—";
	return count.toLocaleString("en-US");
}

/**
 * The Logs explorer query for the blocked request lines of one service:
 * every rule's, or just `ruleId`'s. Request lines carry the rule in
 * `blocked_by` (docs/operations/firewall.md).
 */
export function blockedLogsQuery(serviceName: string, ruleId?: string): string {
	return [
		"stream:requests",
		`service:${formatValue(serviceName)}`,
		`@blocked_by:${ruleId ?? "*"}`,
	].join(" ");
}

/** Limits of auto-ban (docs/operations/firewall.md#auto-ban). */
export const AUTOBAN_THRESHOLD = { min: 3, max: 1000 } as const;
export const AUTOBAN_WINDOW_SECONDS = { min: 60, max: 24 * 3600 } as const;
export const AUTOBAN_DURATION_SECONDS = {
	min: 5 * 60,
	max: 7 * 24 * 3600,
} as const;
export const MAX_AUTOBAN_ALLOW = 200;

export type AutoBanUnit = "minutes" | "hours" | "days";

/** Seconds in one unit. */
export const AUTOBAN_UNIT_SECONDS: Record<AutoBanUnit, number> = {
	minutes: 60,
	hours: 3600,
	days: 86400,
};

export const AUTOBAN_UNITS: readonly AutoBanUnit[] = [
	"minutes",
	"hours",
	"days",
];

/** A length of time as the inputs hold it: the text typed and its unit. */
export interface AutoBanSpan {
	amount: string;
	unit: AutoBanUnit;
}

/** The policy as the card edits it. Numbers stay text until validated. */
export interface AutoBanDraft {
	enabled: boolean;
	threshold: string;
	window: AutoBanSpan;
	duration: AutoBanSpan;
	allow: string[];
}

export type AutoBanFieldErrors = {
	threshold?: string | undefined;
	window?: string | undefined;
	duration?: string | undefined;
	/** The allow list as a whole (too long). */
	allow?: string | undefined;
	/** One entry's problem, by position. */
	allowRows: Record<number, string>;
};

/** Seconds as the largest unit that divides them evenly (minutes when none does). */
export function spanOf(seconds: number): AutoBanSpan {
	if (seconds >= 86400 && seconds % 86400 === 0) {
		return { amount: String(seconds / 86400), unit: "days" };
	}
	if (seconds >= 3600 && seconds % 3600 === 0) {
		return { amount: String(seconds / 3600), unit: "hours" };
	}
	return { amount: String(seconds / 60), unit: "minutes" };
}

/** The seconds a span states, or null when its text is not a positive number. */
export function spanSeconds(span: AutoBanSpan): number | null {
	const text = span.amount.trim();
	if (!/^\d+(\.\d+)?$/.test(text)) return null;
	const seconds = Math.round(Number(text) * AUTOBAN_UNIT_SECONDS[span.unit]);
	return Number.isFinite(seconds) && seconds > 0 ? seconds : null;
}

/** "1 minute", "24 hours": a limit as a sentence. */
export function spanPhrase(seconds: number): string {
	const { amount, unit } = spanOf(seconds);
	return `${amount} ${amount === "1" ? unit.slice(0, -1) : unit}`;
}

export function autobanDraftOf(policy: AutoBan): AutoBanDraft {
	return {
		enabled: policy.enabled,
		threshold: String(policy.threshold),
		window: spanOf(policy.windowSeconds),
		duration: spanOf(policy.durationSeconds),
		allow: [...(policy.allow ?? [])],
	};
}

function spanProblem(
	span: AutoBanSpan,
	label: string,
	limit: { min: number; max: number },
): string | undefined {
	const seconds = spanSeconds(span);
	if (seconds === null) return `${label} is a number greater than 0.`;
	if (seconds < limit.min || seconds > limit.max) {
		return `${label} is ${spanPhrase(limit.min)} to ${spanPhrase(limit.max)}.`;
	}
	return undefined;
}

/** What the control plane would refuse, keyed for the card. Empty when fine. */
export function validateAutoBan(draft: AutoBanDraft): AutoBanFieldErrors {
	const errors: AutoBanFieldErrors = { allowRows: {} };
	const threshold = draft.threshold.trim();
	if (
		!/^\d+$/.test(threshold) ||
		Number(threshold) < AUTOBAN_THRESHOLD.min ||
		Number(threshold) > AUTOBAN_THRESHOLD.max
	) {
		errors.threshold = `Blocked requests to ban is a whole number from ${AUTOBAN_THRESHOLD.min} to ${AUTOBAN_THRESHOLD.max}.`;
	}
	errors.window = spanProblem(
		draft.window,
		"Counting window",
		AUTOBAN_WINDOW_SECONDS,
	);
	errors.duration = spanProblem(
		draft.duration,
		"Ban length",
		AUTOBAN_DURATION_SECONDS,
	);
	if (draft.allow.length > MAX_AUTOBAN_ALLOW) {
		errors.allow = `The allow list holds at most ${MAX_AUTOBAN_ALLOW} entries.`;
	}
	draft.allow.forEach((entry, index) => {
		const problem = validateEntry(entry);
		if (problem) errors.allowRows[index] = problem;
	});
	return errors;
}

export function hasAutoBanErrors(errors: AutoBanFieldErrors): boolean {
	return (
		errors.threshold !== undefined ||
		errors.window !== undefined ||
		errors.duration !== undefined ||
		errors.allow !== undefined ||
		Object.keys(errors.allowRows).length > 0
	);
}

/** The PUT body of a draft that passed validation. */
export function autobanBody(draft: AutoBanDraft): AutoBan {
	return {
		enabled: draft.enabled,
		threshold: Number(draft.threshold.trim()),
		windowSeconds: spanSeconds(draft.window) ?? AUTOBAN_WINDOW_SECONDS.min,
		durationSeconds:
			spanSeconds(draft.duration) ?? AUTOBAN_DURATION_SECONDS.min,
		allow: draft.allow.map((entry) => entry.trim()),
	};
}

/** Whether a draft says what the live policy says (units do not matter, seconds do). */
export function autobanUnchanged(draft: AutoBanDraft, live: AutoBan): boolean {
	const body = autobanBody(draft);
	const allow = live.allow ?? [];
	return (
		body.enabled === live.enabled &&
		body.threshold === live.threshold &&
		body.windowSeconds === live.windowSeconds &&
		body.durationSeconds === live.durationSeconds &&
		body.allow?.length === allow.length &&
		(body.allow ?? []).every((entry, i) => entry === allow[i])
	);
}

export type AutoBanField =
	| "threshold"
	| "window"
	| "duration"
	| "allow"
	| "enabled";

/** Where a refused save belongs: a field of the card, or the card as a whole. */
export interface AutoBanRefusal {
	field?: AutoBanField | undefined;
	message: string;
}

/** The 422 codes of PUT /services/{id}/autoban as the card shows them. */
export function autobanRefusal(
	code: string | undefined,
	message: string,
): AutoBanRefusal {
	switch (code) {
		case "analytics_required":
			return {
				field: "enabled",
				message:
					"Auto-ban decides from the proxy's access log, so traffic analytics must be on for this service's server.",
			};
		case "agent_too_old":
			return {
				field: "enabled",
				message:
					"The agent on this service's server is too old for auto-ban. Update the agent first.",
			};
		case "autoban_unsupported":
			return {
				field: "enabled",
				message:
					"Auto-ban is for HTTP services only: this service has no firewall.",
			};
		case "invalid_autoban": {
			const field = /threshold/i.test(message)
				? "threshold"
				: /window/i.test(message)
					? "window"
					: /duration/i.test(message)
						? "duration"
						: /allow/i.test(message)
							? "allow"
							: undefined;
			return { field, message };
		}
		default:
			return { message };
	}
}

/**
 * The names a ban's rule ids go by: the service's own rules, then the
 * standard protection. An id in neither is a rule since removed.
 */
export function banRuleNames(
	rules: Readonly<Record<string, number>> | undefined,
	names: ReadonlyMap<string, string>,
): string[] {
	return Object.entries(rules ?? {})
		.sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
		.map(([id]) =>
			id === "autoban" ? "Auto-ban" : (names.get(id) ?? `Removed rule ${id}`),
		);
}

/** Rule id → name for the own rules and the standard protection of a view. */
export function ruleNameIndex(
	view: FirewallView | undefined,
): Map<string, string> {
	const names = new Map<string, string>();
	for (const entry of view?.standard ?? []) {
		if (entry.rule.id) names.set(entry.rule.id, entry.rule.name);
	}
	for (const rule of view?.rules ?? []) {
		if (rule.id) names.set(rule.id, rule.name);
	}
	return names;
}

/** Why a ban ended, as a sentence fragment. */
export function banEndReason(reason: BanView["endReason"]): string {
	switch (reason) {
		case "expired":
			return "Expired";
		case "unbanned":
			return "Unbanned";
		case "lifted":
			return "Lifted (policy changed or route removed)";
		case "resync":
			return "Dropped by the agent";
		default:
			return "Ended";
	}
}
