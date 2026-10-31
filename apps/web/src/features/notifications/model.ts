import type {
	ChannelConfig,
	ChannelDelivery,
	ChannelEventType,
	ChannelRule,
	ChannelRuleInput,
	ChannelView,
	CreateChannelInputBody,
	PatchChannelInputBody,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";

export type ChannelKind = ChannelView["kind"];
export type Severity = ChannelRule["minSeverity"];
export type TlsMode = NonNullable<ChannelConfig["tls"]>;
export type DeliveryStatus = ChannelDelivery["status"];

export interface KindInfo {
	kind: ChannelKind;
	label: string;
	/** One line under the label in the kind picker. */
	blurb: string;
}

export const CHANNEL_KINDS: readonly KindInfo[] = [
	{ kind: "slack", label: "Slack", blurb: "Incoming webhook" },
	{ kind: "discord", label: "Discord", blurb: "Channel webhook" },
	{ kind: "telegram", label: "Telegram", blurb: "Bot and chat" },
	{ kind: "email", label: "Email", blurb: "Your SMTP server" },
	{ kind: "webhook", label: "Webhook", blurb: "Signed JSON" },
];

export function kindLabel(kind: ChannelKind): string {
	return CHANNEL_KINDS.find((k) => k.kind === kind)?.label ?? kind;
}

export const SEVERITIES: readonly { value: Severity; label: string }[] = [
	{ value: "info", label: "Everything" },
	{ value: "warning", label: "Warnings and errors" },
	{ value: "error", label: "Errors only" },
];

/** Event types in catalogue order, grouped for the matrix. */
export function groupEventTypes(
	types: readonly ChannelEventType[],
): { group: string; types: ChannelEventType[] }[] {
	const out: { group: string; types: ChannelEventType[] }[] = [];
	for (const type of types) {
		let bucket = out.find((g) => g.group === type.group);
		if (!bucket) {
			bucket = { group: type.group, types: [] };
			out.push(bucket);
		}
		bucket.types.push(type);
	}
	return out;
}

export type MatrixPreset = "alerts" | "all" | "none";

/** The types a matrix preset selects: failures and alerts (the default), everything, nothing. */
export function presetTypes(
	preset: MatrixPreset,
	types: readonly ChannelEventType[],
): string[] {
	if (preset === "none") return [];
	if (preset === "all") return types.map((t) => t.type);
	return types.filter((t) => t.defaultOn).map((t) => t.type);
}

/** Which preset the selection equals, if any (for the highlighted chip). */
export function activePreset(
	selected: readonly string[],
	types: readonly ChannelEventType[],
): MatrixPreset | null {
	const same = (a: readonly string[], b: readonly string[]) =>
		a.length === b.length && a.every((x) => b.includes(x));
	for (const preset of ["alerts", "all", "none"] as const) {
		if (same(selected, presetTypes(preset, types))) return preset;
	}
	return null;
}

/** "8 events · everything · whole team" */
export function ruleSummary(
	rule: ChannelRule,
	projectNames: ReadonlyMap<string, string>,
): string {
	const n = (rule.eventTypes ?? []).length;
	const events = n === 0 ? "no events" : `${n} ${n === 1 ? "event" : "events"}`;
	const severity =
		rule.minSeverity === "error"
			? "errors only"
			: rule.minSeverity === "warning"
				? "warnings and errors"
				: "every severity";
	const ids = rule.projectIds ?? [];
	const scope = rule.allProjects
		? "whole team"
		: ids.length === 1
			? (projectNames.get(ids[0] ?? "") ?? "1 project")
			: `${ids.length} projects`;
	return `${events} · ${severity} · ${scope}`;
}

/** A webhook URL hint is "https://host/••••••": the host is what identifies the receiver. */
export function hintHost(hint: string | undefined): string | null {
	if (!hint) return null;
	try {
		return new URL(hint.replace(/\/[•]+$/u, "/")).host || null;
	} catch {
		return null;
	}
}

/** What identifies the channel's target on its card: the host, the chat or the recipients. */
export function targetLabel(channel: ChannelView): string {
	switch (channel.kind) {
		case "email": {
			const to = channel.config.to ?? [];
			const first = to[0] ?? "no recipients";
			return to.length > 1 ? `${first} +${to.length - 1}` : first;
		}
		case "telegram":
			return channel.config.chatId
				? `chat ${channel.config.chatId}`
				: "Telegram";
		default:
			return hintHost(channel.secrets.url) ?? kindLabel(channel.kind);
	}
}

// --- The create / edit draft --------------------------------------------------

export interface Draft {
	name: string;
	kind: ChannelKind;
	enabled: boolean;
	// email
	host: string;
	port: string;
	tls: TlsMode;
	username: string;
	password: string;
	from: string;
	to: string;
	insecureSkipVerify: boolean;
	// telegram
	chatId: string;
	botToken: string;
	// slack, discord, webhook
	url: string;
	signingSecret: string;
	// rule
	eventTypes: string[];
	minSeverity: Severity;
	allProjects: boolean;
	projectIds: string[];
}

export function draftFor(
	channel: ChannelView | null,
	defaultTypes: readonly string[],
): Draft {
	const c = channel?.config;
	return {
		name: channel?.name ?? "",
		kind: channel?.kind ?? "slack",
		enabled: channel?.enabled ?? true,
		host: c?.host ?? "",
		port: c?.port ? String(c.port) : "",
		tls: c?.tls ?? "starttls",
		username: c?.username ?? "",
		password: "",
		from: c?.from ?? "",
		to: (c?.to ?? []).join("\n"),
		insecureSkipVerify: c?.insecureSkipVerify ?? false,
		chatId: c?.chatId ?? "",
		botToken: "",
		url: "",
		signingSecret: "",
		eventTypes: channel
			? [...(channel.rule.eventTypes ?? [])]
			: [...defaultTypes],
		minSeverity: channel?.rule.minSeverity ?? "info",
		allProjects: channel?.rule.allProjects ?? true,
		projectIds: [...(channel?.rule.projectIds ?? [])],
	};
}

/** Recipients, one per line or separated by commas/semicolons/spaces. */
export function parseRecipients(text: string): string[] {
	const seen = new Set<string>();
	const out: string[] = [];
	for (const part of text.split(/[\s,;]+/u)) {
		const addr = part.trim();
		if (addr !== "" && !seen.has(addr.toLowerCase())) {
			seen.add(addr.toLowerCase());
			out.push(addr);
		}
	}
	return out;
}

/** Port the server will use when the field is left empty. */
export function defaultPort(tls: TlsMode): number {
	return tls === "implicit" ? 465 : tls === "none" ? 25 : 587;
}

const EMAIL_RE = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/u;

/** A plain address or `Name <address>`. */
export function validFrom(value: string): boolean {
	const v = value.trim();
	const m = /<([^<>]+)>\s*$/u.exec(v);
	return EMAIL_RE.test(m?.[1]?.trim() ?? v);
}

/**
 * The first thing wrong with a draft, or null when it can be sent. The server
 * validates again; this keeps the button honest and the message close to the
 * field.
 */
export function draftProblem(draft: Draft, editing: boolean): string | null {
	if (draft.name.trim() === "") return "Give the channel a name.";
	switch (draft.kind) {
		case "email": {
			if (draft.host.trim() === "") return "Enter the SMTP server's host.";
			if (draft.port !== "" && !/^\d{1,5}$/u.test(draft.port)) {
				return "The port must be a number.";
			}
			if (!validFrom(draft.from)) {
				return "The sender must be an email address, like alerts@example.com.";
			}
			const to = parseRecipients(draft.to);
			if (to.length === 0) return "Add at least one recipient.";
			if (to.some((a) => !EMAIL_RE.test(a))) {
				return "One of the recipients is not an email address.";
			}
			if (draft.username.trim() !== "" && !editing && draft.password === "") {
				return "A user name needs its password.";
			}
			break;
		}
		case "telegram":
			if (!editing && draft.botToken.trim() === "") {
				return "Paste the bot token from @BotFather.";
			}
			if (draft.chatId.trim() === "") {
				return "Enter the chat id or @channelname the bot posts to.";
			}
			break;
		case "slack":
		case "discord":
			if (!editing && !/^https:\/\/\S+$/u.test(draft.url.trim())) {
				return `Paste the ${kindLabel(draft.kind)} webhook URL (it starts with https://).`;
			}
			if (
				editing &&
				draft.url.trim() !== "" &&
				!/^https:\/\/\S+$/u.test(draft.url.trim())
			) {
				return "The webhook URL must start with https://.";
			}
			break;
		case "webhook":
			if (!editing && !/^https?:\/\/\S+$/u.test(draft.url.trim())) {
				return "Enter the URL that receives the POST requests.";
			}
			if (
				editing &&
				draft.url.trim() !== "" &&
				!/^https?:\/\/\S+$/u.test(draft.url.trim())
			) {
				return "The URL must start with http:// or https://.";
			}
			if (draft.signingSecret !== "" && draft.signingSecret.length < 16) {
				return "A signing secret needs at least 16 characters (or leave it empty to generate one).";
			}
			break;
	}
	if (!draft.allProjects && draft.projectIds.length === 0) {
		return "Choose at least one project, or send for the whole team.";
	}
	return null;
}

function ruleBody(draft: Draft): ChannelRuleInput {
	return {
		eventTypes: draft.eventTypes,
		minSeverity: draft.minSeverity,
		allProjects: draft.allProjects,
		projectIds: draft.allProjects ? [] : draft.projectIds,
	};
}

function configBody(draft: Draft): ChannelConfig | undefined {
	if (draft.kind === "email") {
		return {
			host: draft.host.trim(),
			...(draft.port !== "" ? { port: Number(draft.port) } : {}),
			tls: draft.tls,
			username: draft.username.trim(),
			from: draft.from.trim(),
			to: parseRecipients(draft.to),
			insecureSkipVerify: draft.insecureSkipVerify,
		};
	}
	if (draft.kind === "telegram") return { chatId: draft.chatId.trim() };
	return undefined;
}

function secretsBody(
	draft: Draft,
): NonNullable<PatchChannelInputBody["secrets"]> {
	const s: NonNullable<PatchChannelInputBody["secrets"]> = {};
	if (draft.kind === "email" && draft.password !== "")
		s.password = draft.password;
	if (draft.kind === "telegram" && draft.botToken.trim() !== "")
		s.botToken = draft.botToken.trim();
	if (draft.url.trim() !== "") s.url = draft.url.trim();
	if (draft.kind === "webhook" && draft.signingSecret !== "")
		s.signingSecret = draft.signingSecret;
	return s;
}

export function createBody(draft: Draft): CreateChannelInputBody {
	const config = configBody(draft);
	return {
		name: draft.name.trim(),
		kind: draft.kind,
		enabled: draft.enabled,
		...(config ? { config } : {}),
		secrets: secretsBody(draft),
		rule: ruleBody(draft),
	};
}

/** Empty secret fields are left out, so the server keeps the stored ones. */
export function patchBody(draft: Draft): PatchChannelInputBody {
	const config = configBody(draft);
	return {
		name: draft.name.trim(),
		enabled: draft.enabled,
		...(config ? { config } : {}),
		secrets: secretsBody(draft),
		rule: ruleBody(draft),
	};
}

// --- Delivery log -------------------------------------------------------------

interface StatusPresentation {
	kind: StatusKind;
	label: string;
}

export const DELIVERY_STATUS: Record<DeliveryStatus, StatusPresentation> = {
	pending: { kind: "pending", label: "Queued" },
	retrying: { kind: "building", label: "Retrying" },
	delivered: { kind: "healthy", label: "Delivered" },
	failed: { kind: "failed", label: "Failed" },
};

/** "2 of 6 attempts" */
export function attemptsLabel(
	d: Pick<ChannelDelivery, "attempts" | "maxAttempts">,
): string {
	if (d.attempts === 0) return "not tried yet";
	return `${d.attempts} of ${d.maxAttempts} ${d.maxAttempts === 1 ? "attempt" : "attempts"}`;
}

export const DELIVERY_FILTERS = [
	{ value: "all", label: "All" },
	{ value: "delivered", label: "Delivered" },
	{ value: "retrying", label: "Retrying" },
	{ value: "failed", label: "Failed" },
] as const;

export type DeliveryFilter = (typeof DELIVERY_FILTERS)[number]["value"];

// --- Verifying webhook signatures ---------------------------------------------

export type SnippetLang = "node" | "python" | "go";

export const SNIPPET_LABEL: Record<SnippetLang, string> = {
	node: "Node.js",
	python: "Python",
	go: "Go",
};

/**
 * Receiver code that verifies `X-NebulaCtrl-Signature`: the header is
 * `t=<unix seconds>,v1=<hex>` and v1 is HMAC-SHA256(secret, "<t>.<raw body>").
 * Reject timestamps more than five minutes off, compare in constant time and
 * hash the raw bytes, never a re-serialized JSON value.
 */
export const VERIFY_SNIPPETS: Record<SnippetLang, string> = {
	node: `import { createHmac, timingSafeEqual } from "node:crypto";

// rawBody: the request body as a Buffer, exactly as received.
export function verify(secret, header, rawBody, toleranceSec = 300) {
  const parts = Object.fromEntries(header.split(",").map((p) => p.split("=", 2)));
  const t = Number(parts.t);
  if (!parts.v1 || !Number.isFinite(t)) return false;
  if (Math.abs(Date.now() / 1000 - t) > toleranceSec) return false;
  const expected = createHmac("sha256", secret).update(\`\${t}.\`).update(rawBody).digest("hex");
  const a = Buffer.from(expected), b = Buffer.from(parts.v1);
  return a.length === b.length && timingSafeEqual(a, b);
}`,
	python: `import hashlib, hmac, time

def verify(secret: str, header: str, raw_body: bytes, tolerance: int = 300) -> bool:
    parts = dict(p.split("=", 1) for p in header.split(","))
    try:
        t = int(parts["t"])
    except (KeyError, ValueError):
        return False
    if abs(time.time() - t) > tolerance:
        return False
    signed = f"{t}.".encode() + raw_body
    expected = hmac.new(secret.encode(), signed, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, parts.get("v1", ""))`,
	go: `import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// rawBody: the request body exactly as received.
func Verify(secret, header string, rawBody []byte, tolerance time.Duration) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(sec, 0)).Abs() > tolerance {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(rawBody)
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sig))
}`,
};
