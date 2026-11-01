// Pure parts of the log drains settings: the kinds and their presets, the
// create / edit draft and its mapping to the API bodies, the scope summary and
// the health and test sentences. No React and no network, so it is tested.

import type {
	CreateDrainInputBody,
	DrainConfigBody,
	LogDrainSecretsInput,
	LogDrainServerStatus,
	LogDrainTestResult,
	LogDrainView,
	PatchDrainInputBody,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";
import { formatBytes } from "#/lib/format";
import { parseQuery } from "../logs/search/query";

export type DrainKind = LogDrainView["kind"];
export type SyslogProtocol = "udp" | "tcp" | "tcp+tls";
export type HttpMethod = "POST" | "PUT";
export type HttpFormat = "json_array" | "ndjson";

/** The credentials a drain can store; `headers` are the extra request headers. */
export type SecretName = "token" | "username" | "password" | "headers";

export const DEFAULT_BUFFER_MB = 256;
export const MIN_BUFFER_MB = 8;
export const MAX_BUFFER_MB = 5120;

export interface KindInfo {
	kind: DrainKind;
	label: string;
	/** One line under the label in the kind picker. */
	blurb: string;
}

export const DRAIN_KINDS: readonly KindInfo[] = [
	{
		kind: "loki",
		label: "Loki",
		blurb: "Grafana Loki or Grafana Cloud, over the push API",
	},
	{
		kind: "otlp",
		label: "OpenTelemetry (OTLP/HTTP)",
		blurb: "Any collector or service that takes OTLP logs over HTTP",
	},
	{
		kind: "syslog",
		label: "Syslog",
		blurb: "RFC 5424 messages over UDP, TCP or TLS",
	},
	{
		kind: "http",
		label: "HTTP JSON",
		blurb:
			"Batches of JSON to Datadog, Better Stack, Axiom or your own endpoint",
	},
];

export function kindLabel(kind: DrainKind): string {
	return DRAIN_KINDS.find((k) => k.kind === kind)?.label ?? kind;
}

/** Which credentials a kind takes: basic auth, a bearer token, extra headers. */
export function credentialFields(kind: DrainKind): {
	basic: boolean;
	token: boolean;
	headers: boolean;
} {
	switch (kind) {
		case "loki":
			return { basic: true, token: true, headers: true };
		case "otlp":
			return { basic: false, token: true, headers: true };
		case "http":
			return { basic: true, token: true, headers: true };
		case "syslog":
			return { basic: false, token: false, headers: false };
	}
}

// --- Rows (labels, attributes, headers, extra fields) -------------------------

export interface Row {
	/** A stable React key; not sent. */
	id: number;
	name: string;
	value: string;
}

let rowSeq = 0;

export function newRow(name = "", value = ""): Row {
	rowSeq += 1;
	return { id: rowSeq, name, value };
}

export function rowsFromMap(map: Record<string, string> | undefined): Row[] {
	return Object.entries(map ?? {}).map(([name, value]) => newRow(name, value));
}

/** Rows with a name, as a map; a blank row is ignored and a later row wins. */
export function mapFromRows(rows: readonly Row[]): Record<string, string> {
	const out: Record<string, string> = {};
	for (const row of rows) {
		const name = row.name.trim();
		if (name !== "") out[name] = row.value;
	}
	return out;
}

// --- HTTP presets --------------------------------------------------------------

export interface HttpPreset {
	id: "datadog" | "betterstack" | "axiom";
	label: string;
	url: string;
	/** Header names to add; the values are the person's key and are never prefilled. */
	headerNames: string[];
	extraFields: Record<string, string>;
	timestampField: string;
	messageField: string;
	/** Where the credential goes, in a sentence under the presets. */
	hint: string;
}

export const HTTP_PRESETS: readonly HttpPreset[] = [
	{
		id: "datadog",
		label: "Datadog",
		url: "https://http-intake.logs.datadoghq.com/api/v2/logs",
		headerNames: ["DD-API-KEY"],
		extraFields: { ddsource: "nebulactrl" },
		timestampField: "",
		messageField: "",
		hint: "Paste your API key as the value of the DD-API-KEY header. Other Datadog sites use another host (datadoghq.eu, us5.datadoghq.com): change the URL to match.",
	},
	{
		id: "betterstack",
		label: "Better Stack",
		url: "https://in.logs.betterstack.com",
		headerNames: [],
		extraFields: {},
		timestampField: "dt",
		messageField: "message",
		hint: "Use the source token as the bearer token. If the source shows its own ingesting host, put that in the URL.",
	},
	{
		id: "axiom",
		label: "Axiom",
		url: "https://api.axiom.co/v1/datasets/<dataset>/ingest",
		headerNames: [],
		extraFields: {},
		timestampField: "_time",
		messageField: "message",
		hint: "Replace <dataset> with the dataset's name, and use an API token with ingest permission as the bearer token.",
	},
];

// --- The create / edit draft --------------------------------------------------

export type HeadersMode = "keep" | "replace" | "clear";

export interface Draft {
	name: string;
	kind: DrainKind;
	enabled: boolean;
	// loki
	lokiUrl: string;
	lokiTenant: string;
	lokiLabels: Row[];
	// otlp
	otlpUrl: string;
	otlpAttributes: Row[];
	// syslog
	syslogAddress: string;
	syslogProtocol: SyslogProtocol;
	syslogAppName: string;
	syslogFacility: string;
	syslogTlsServerName: string;
	syslogTlsInsecure: boolean;
	// http
	httpUrl: string;
	httpMethod: HttpMethod;
	httpFormat: HttpFormat;
	httpTimestampField: string;
	httpMessageField: string;
	httpExtraFields: Row[];
	// credentials: write-only. An empty field keeps what is stored.
	token: string;
	username: string;
	password: string;
	/** Stored credentials to remove: the field was emptied on purpose. */
	clear: SecretName[];
	headersMode: HeadersMode;
	headers: Row[];
	// scope
	allProjects: boolean;
	projectIds: string[];
	serviceIds: string[];
	query: string;
	// advanced
	bufferMb: string;
}

export function emptyDraft(): Draft {
	return {
		name: "",
		kind: "loki",
		enabled: true,
		lokiUrl: "",
		lokiTenant: "",
		lokiLabels: [],
		otlpUrl: "",
		otlpAttributes: [],
		syslogAddress: "",
		syslogProtocol: "tcp+tls",
		syslogAppName: "",
		syslogFacility: "",
		syslogTlsServerName: "",
		syslogTlsInsecure: false,
		httpUrl: "",
		httpMethod: "POST",
		httpFormat: "json_array",
		httpTimestampField: "",
		httpMessageField: "",
		httpExtraFields: [],
		token: "",
		username: "",
		password: "",
		clear: [],
		headersMode: "replace",
		headers: [],
		allProjects: true,
		projectIds: [],
		serviceIds: [],
		query: "",
		bufferMb: String(DEFAULT_BUFFER_MB),
	};
}

function asProtocol(value: string): SyslogProtocol {
	return value === "udp" || value === "tcp" ? value : "tcp+tls";
}

/** The draft of a stored drain, or of a new one. Credentials are never in it. */
export function draftFor(drain: LogDrainView | null): Draft {
	const draft = emptyDraft();
	if (!drain) return draft;
	draft.name = drain.name;
	draft.kind = drain.kind;
	draft.enabled = drain.enabled;
	draft.allProjects = drain.allProjects;
	draft.projectIds = [...(drain.projectIds ?? [])];
	draft.serviceIds = [...(drain.serviceIds ?? [])];
	draft.query = drain.query;
	draft.bufferMb = String(drain.bufferMb);
	// Stored header values are secret: only their names are known, so the
	// editor starts on "keep" and offers replacing or removing them.
	draft.headersMode = (drain.headerNames ?? []).length > 0 ? "keep" : "replace";
	const { loki, otlp, syslog, http } = drain;
	if (loki) {
		draft.lokiUrl = loki.url;
		draft.lokiTenant = loki.tenantId ?? "";
		draft.lokiLabels = rowsFromMap(loki.labels);
	}
	if (otlp) {
		draft.otlpUrl = otlp.url;
		draft.otlpAttributes = rowsFromMap(otlp.resourceAttributes);
	}
	if (syslog) {
		draft.syslogAddress = syslog.address;
		draft.syslogProtocol = asProtocol(syslog.protocol);
		draft.syslogAppName = syslog.appName ?? "";
		draft.syslogFacility =
			syslog.facility === undefined ? "" : String(syslog.facility);
		draft.syslogTlsServerName = syslog.tlsServerName ?? "";
		draft.syslogTlsInsecure = syslog.tlsInsecureSkipVerify ?? false;
	}
	if (http) {
		draft.httpUrl = http.url;
		draft.httpMethod = http.method?.toUpperCase() === "PUT" ? "PUT" : "POST";
		draft.httpFormat = http.format === "ndjson" ? "ndjson" : "json_array";
		draft.httpTimestampField = http.timestampField ?? "";
		draft.httpMessageField = http.messageField ?? "";
		draft.httpExtraFields = rowsFromMap(http.extraFields);
	}
	return draft;
}

/** A preset fills the HTTP kind's non-secret fields and the names of its headers. */
export function applyPreset(draft: Draft, preset: HttpPreset): Draft {
	return {
		...draft,
		kind: "http",
		name: draft.name.trim() === "" ? preset.label : draft.name,
		httpUrl: preset.url,
		httpMethod: "POST",
		httpFormat: "json_array",
		httpTimestampField: preset.timestampField,
		httpMessageField: preset.messageField,
		httpExtraFields: rowsFromMap(preset.extraFields),
		headersMode: "replace",
		headers: preset.headerNames.map((name) => newRow(name, "")),
	};
}

// --- Validation ---------------------------------------------------------------

const URL_RE = /^https?:\/\/\S+$/u;
const ADDRESS_RE = /^(?:\[[^\]\s]+\]|[^\s:/[\]]+):(\d{1,5})$/u;

function urlProblem(value: string, what: string): string | null {
	const url = value.trim();
	if (url === "") return `Enter the ${what}.`;
	if (!URL_RE.test(url))
		return `The ${what} must start with http:// or https://.`;
	if (/[<>]/u.test(url)) {
		return `Replace the placeholder in the ${what} (the part in < >).`;
	}
	return null;
}

/** "host:port" with a port from 1 to 65535. */
export function validAddress(value: string): boolean {
	const match = ADDRESS_RE.exec(value.trim());
	if (!match) return false;
	const port = Number(match[1]);
	return port >= 1 && port <= 65535;
}

export function parseBufferMb(text: string): number | null {
	const value = Number(text);
	return text.trim() !== "" &&
		Number.isInteger(value) &&
		value >= MIN_BUFFER_MB &&
		value <= MAX_BUFFER_MB
		? value
		: null;
}

/**
 * The first thing wrong with a draft, or null when it can be sent. The
 * server validates again and its message is shown too; this keeps the button
 * honest and the message close to the field.
 */
export function draftProblem(draft: Draft, editing: boolean): string | null {
	if (draft.name.trim() === "") return "Give the drain a name.";
	switch (draft.kind) {
		case "loki": {
			const problem = urlProblem(draft.lokiUrl, "push URL");
			if (problem) return problem;
			break;
		}
		case "otlp": {
			const problem = urlProblem(draft.otlpUrl, "logs URL");
			if (problem) return problem;
			break;
		}
		case "http": {
			const problem = urlProblem(draft.httpUrl, "URL");
			if (problem) return problem;
			break;
		}
		case "syslog":
			if (!validAddress(draft.syslogAddress)) {
				return "Enter the address as host:port, for example logs.example.com:6514.";
			}
			if (draft.syslogFacility.trim() !== "") {
				const facility = Number(draft.syslogFacility);
				if (!Number.isInteger(facility) || facility < 0 || facility > 23) {
					return "The facility is a number from 0 to 23.";
				}
			}
			break;
	}
	const fields = credentialFields(draft.kind);
	if (fields.headers && draft.headersMode === "replace") {
		for (const row of draft.headers) {
			const name = row.name.trim();
			if (name === "" && row.value === "") continue;
			if (name === "") return "A header needs a name.";
			if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/u.test(name)) {
				return `“${name}” is not a valid header name.`;
			}
			if (row.value === "") return `The ${name} header needs a value.`;
		}
	}
	if (fields.basic && !editing) {
		if (draft.username.trim() !== "" && draft.password === "") {
			return "A user name needs its password.";
		}
		if (draft.password !== "" && draft.username.trim() === "") {
			return "A password needs its user name.";
		}
	}
	if (!draft.allProjects && draft.projectIds.length === 0) {
		return "Choose at least one project, or ship from all projects.";
	}
	const query = parseQuery(draft.query);
	if (draft.query.trim() !== "" && query.errors.length > 0) {
		return `The query has a mistake: ${query.errors[0]?.message ?? "check its syntax"}`;
	}
	if (parseBufferMb(draft.bufferMb) === null) {
		return `The buffer is ${MIN_BUFFER_MB} to ${MAX_BUFFER_MB} MB.`;
	}
	return null;
}

// --- Request bodies -----------------------------------------------------------

/** Rows as a map, or undefined when there are none (an omitted map is an empty one). */
function optionalMap(rows: readonly Row[]): Record<string, string> | undefined {
	const map = mapFromRows(rows);
	return Object.keys(map).length > 0 ? map : undefined;
}

/** The non-secret settings of the draft's kind. Blank optional fields are left out. */
export function configBody(draft: Draft): DrainConfigBody {
	switch (draft.kind) {
		case "loki": {
			const labels = optionalMap(draft.lokiLabels);
			return {
				loki: {
					url: draft.lokiUrl.trim(),
					...(draft.lokiTenant.trim() !== ""
						? { tenantId: draft.lokiTenant.trim() }
						: {}),
					...(labels ? { labels } : {}),
				},
			};
		}
		case "otlp": {
			const resourceAttributes = optionalMap(draft.otlpAttributes);
			return {
				otlp: {
					url: draft.otlpUrl.trim(),
					...(resourceAttributes ? { resourceAttributes } : {}),
				},
			};
		}
		case "syslog":
			return {
				syslog: {
					address: draft.syslogAddress.trim(),
					protocol: draft.syslogProtocol,
					...(draft.syslogAppName.trim() !== ""
						? { appName: draft.syslogAppName.trim() }
						: {}),
					...(draft.syslogFacility.trim() !== ""
						? { facility: Number(draft.syslogFacility) }
						: {}),
					...(draft.syslogProtocol === "tcp+tls"
						? {
								...(draft.syslogTlsServerName.trim() !== ""
									? { tlsServerName: draft.syslogTlsServerName.trim() }
									: {}),
								tlsInsecureSkipVerify: draft.syslogTlsInsecure,
							}
						: {}),
				},
			};
		case "http": {
			const extraFields = optionalMap(draft.httpExtraFields);
			return {
				http: {
					url: draft.httpUrl.trim(),
					method: draft.httpMethod,
					format: draft.httpFormat,
					...(draft.httpTimestampField.trim() !== ""
						? { timestampField: draft.httpTimestampField.trim() }
						: {}),
					...(draft.httpMessageField.trim() !== ""
						? { messageField: draft.httpMessageField.trim() }
						: {}),
					...(extraFields ? { extraFields } : {}),
				},
			};
		}
	}
}

/**
 * The credentials to send. A field left empty is omitted, so the server keeps
 * the stored one; typing replaces it; `clear` removes it; the header set is
 * replaced as a whole (or removed), because its stored values are not known.
 * Only the credentials the kind takes are sent.
 */
export function secretsBody(draft: Draft): LogDrainSecretsInput {
	const fields = credentialFields(draft.kind);
	const out: LogDrainSecretsInput = {};
	const clear: string[] = [];
	const take = (name: SecretName, value: string, allowed: boolean) => {
		if (!allowed) return;
		if (value !== "") {
			if (name === "token") out.token = value;
			else if (name === "username") out.username = value;
			else if (name === "password") out.password = value;
		} else if (draft.clear.includes(name)) {
			clear.push(name);
		}
	};
	take("token", draft.token.trim(), fields.token);
	take("username", draft.username.trim(), fields.basic);
	take("password", draft.password, fields.basic);
	if (fields.headers) {
		if (draft.headersMode === "replace") {
			const headers = mapFromRows(draft.headers);
			if (Object.keys(headers).length > 0) out.headers = headers;
		} else if (draft.headersMode === "clear") {
			clear.push("headers");
		}
	}
	if (clear.length > 0) out.clear = clear;
	return out;
}

function scopeBody(draft: Draft) {
	return {
		allProjects: draft.allProjects,
		projectIds: draft.allProjects ? [] : draft.projectIds,
		serviceIds: draft.serviceIds,
		query: draft.query.trim(),
		bufferMb: parseBufferMb(draft.bufferMb) ?? DEFAULT_BUFFER_MB,
	};
}

export function createBody(draft: Draft): CreateDrainInputBody {
	return {
		name: draft.name.trim(),
		kind: draft.kind,
		enabled: draft.enabled,
		config: configBody(draft),
		secrets: secretsBody(draft),
		...scopeBody(draft),
	};
}

/** A PATCH never changes the kind; the settings are replaced, the credentials kept unless sent. */
export function patchBody(draft: Draft): PatchDrainInputBody {
	return {
		name: draft.name.trim(),
		enabled: draft.enabled,
		config: configBody(draft),
		secrets: secretsBody(draft),
		...scopeBody(draft),
	};
}

/** Where a drain ships to, short: the host of its URL, or its syslog address. */
export function targetLabel(
	drain: Pick<LogDrainView, "kind" | "loki" | "otlp" | "http" | "syslog">,
): string {
	const url =
		drain.kind === "loki"
			? drain.loki?.url
			: drain.kind === "otlp"
				? drain.otlp?.url
				: drain.kind === "http"
					? drain.http?.url
					: undefined;
	if (drain.kind === "syslog") return drain.syslog?.address ?? "";
	if (!url) return "";
	try {
		return new URL(url).host || url;
	} catch {
		return url;
	}
}

// --- Scope --------------------------------------------------------------------

function plural(n: number, one: string, many: string): string {
	return `${n} ${n === 1 ? one : many}`;
}

/** "All projects", "2 projects", "3 services", "3 services in 2 projects". */
export function scopeSummary(
	drain: Pick<LogDrainView, "allProjects" | "projectIds" | "serviceIds">,
	projectNames: ReadonlyMap<string, string> = new Map(),
): string {
	const services = (drain.serviceIds ?? []).length;
	const projects = drain.projectIds ?? [];
	const inProjects = drain.allProjects
		? null
		: projects.length === 1
			? (projectNames.get(projects[0] ?? "") ?? "1 project")
			: plural(projects.length, "project", "projects");
	if (services > 0) {
		const base = plural(services, "service", "services");
		return inProjects ? `${base} in ${inProjects}` : base;
	}
	return inProjects ?? "All projects";
}

/** The service ids that still belong to the scope the draft describes. */
export function visibleServiceIds(
	selected: readonly string[],
	services: readonly { id: string; projectId: string }[],
	allProjects: boolean,
	projectIds: readonly string[],
): string[] {
	const allowed = new Set(
		services
			.filter((s) => allProjects || projectIds.includes(s.projectId))
			.map((s) => s.id),
	);
	return selected.filter((id) => allowed.has(id));
}

// --- Health -------------------------------------------------------------------

export interface Presentation {
	kind: StatusKind;
	label: string;
}

export const SERVER_STATE: Record<LogDrainServerStatus["state"], Presentation> =
	{
		ok: { kind: "live", label: "Delivering" },
		retrying: { kind: "degraded", label: "Retrying" },
		failing: { kind: "failed", label: "Failing" },
		disabled: { kind: "stopped", label: "Disabled" },
	};

/** A drain no server has reported on yet. */
export const NEVER_REPORTED: Presentation = {
	kind: "unknown",
	label: "Waiting for a server",
};

export function serverState(
	status: Pick<LogDrainServerStatus, "state">,
): Presentation {
	return SERVER_STATE[status.state] ?? NEVER_REPORTED;
}

/** "1,204 sent", "3 dropped", "2.1 MiB waiting": what a server's drain has done. */
export function deliveryParts(
	status: Pick<
		LogDrainServerStatus,
		"sentLines" | "droppedLines" | "backlogBytes"
	>,
): string[] {
	return [
		`${status.sentLines.toLocaleString()} sent`,
		`${status.droppedLines.toLocaleString()} dropped`,
		`${formatBytes(status.backlogBytes)} waiting`,
	];
}

/** The last error of a server that is not delivering; empty when there is none to show. */
export function lastErrorOf(status: LogDrainServerStatus): string {
	return status.state === "ok" ? "" : status.lastError.trim();
}

// --- Test ---------------------------------------------------------------------

export interface TestOutcome {
	tone: "success" | "danger" | "warning";
	title: string;
	detail: string;
}

/** What a finished test says: the status code and the receiver's own message. */
export function testOutcome(result: LogDrainTestResult): TestOutcome {
	const parts = [
		`From ${result.serverName}`,
		result.statusCode ? `HTTP ${result.statusCode}` : "",
		result.message.trim(),
	].filter((part) => part !== "");
	return {
		tone: result.ok ? "success" : "danger",
		title: result.ok ? "Test line delivered" : "Test line failed",
		detail: parts.join(" · "),
	};
}

/** A test that could not run: no connected server (409), or another API error. */
export function testFailure(error: {
	code?: string | undefined;
	message: string;
}): TestOutcome {
	if (error.code === "no_online_server") {
		return {
			tone: "warning",
			title: "No server is connected",
			detail:
				"A test line is sent from one of your servers, and none is online. Connect a server and try again.",
		};
	}
	return {
		tone: "danger",
		title: "Could not send the test",
		detail: error.message,
	};
}

// --- The built-in central logs drain -------------------------------------------

/** How long the built-in log store keeps a team's lines. */
export const CENTRAL_RETENTION_DAYS = 7;

/** What the team's built-in drain is called on the page. */
export const CENTRAL_LOGS_TITLE = "Central logs";

/** The one line under its title: where the lines go and for how long. */
export const CENTRAL_LOGS_LINE = `Built-in · kept ${CENTRAL_RETENTION_DAYS} days`;

/**
 * The drains in page order: the built-in central logs first, the team's own
 * drains after it in the order the control plane gave them.
 */
export function orderDrains<T extends Pick<LogDrainView, "builtin">>(
	drains: readonly T[],
): T[] {
	return [
		...drains.filter((drain) => drain.builtin),
		...drains.filter((drain) => !drain.builtin),
	];
}

/** The toast after a switch: the built-in drain talks about central logs, the others about pausing. */
export function toggleToast(
	drain: Pick<LogDrainView, "name" | "builtin">,
	enabled: boolean,
): { title: string; body: string } {
	if (drain.builtin) {
		return enabled
			? {
					title: "Central logs are on",
					body: `Servers send new lines to the control plane. They are kept ${CENTRAL_RETENTION_DAYS} days.`,
				}
			: {
					title: "Central logs are off",
					body: "Nothing new is sent. Lines already collected stay until they expire.",
				};
	}
	return enabled
		? {
				title: `Resumed ${drain.name}`,
				body: "Servers ship logs to it again.",
			}
		: {
				title: `Paused ${drain.name}`,
				body: "Nothing is shipped until you resume it.",
			};
}
