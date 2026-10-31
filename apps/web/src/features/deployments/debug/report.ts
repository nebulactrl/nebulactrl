import type { EventView } from "#/api/gen/types.gen";
import type { Deployment } from "#/api/models";
import { stepLabel } from "../labels";
import type { DebugCommand, DebugRelease } from "./queries";
import {
	buildTimeline,
	clockWithMs,
	formatOffset,
	summarizeTimeline,
} from "./timeline-model";

export interface ReportLine {
	ts?: string | undefined;
	stream?: string | undefined;
	line: string;
}

export interface DebugReportInput {
	deployment: Deployment;
	serviceName: string;
	serverName: string | null;
	buildServerName: string | null;
	events: readonly EventView[];
	release: DebugRelease | null;
	commands: readonly DebugCommand[] | null;
	logLines: readonly ReportLine[];
	/** Where the report was taken, ISO. */
	generatedAt: string;
	/** The address of the panel, so a reader can open the same view. */
	url?: string | undefined;
}

/** The most log lines a report carries: a bug report is read, not archived. */
export const REPORT_MAX_LOG_LINES = 400;

const MASK = "••••••";

// Backstops for a line that slipped past the control plane's redaction:
// bearer tokens, URL passwords and obvious credential assignments.
const BACKSTOPS: readonly [RegExp, string][] = [
	[/(\bbearer\s+)[A-Za-z0-9._~+/=-]{8,}/gi, `$1${MASK}`],
	[/(\b[a-z][a-z0-9+.-]*:\/\/[^/\s:@]*:)[^@/\s]+@/gi, `$1${MASK}@`],
	[
		/(\b[\w.-]*(?:password|passwd|secret|token|api[_-]?key)[\w.-]*\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;"']+)/gi,
		`$1${MASK}`,
	],
];

/** Masks credential-looking text; the control plane redacts first, this is a second net. */
export function scrubReportText(text: string): string {
	let out = text;
	for (const [pattern, replacement] of BACKSTOPS) {
		out = out.replace(pattern, replacement);
	}
	return out;
}

function section(title: string): string {
	return `\n## ${title}\n`;
}

function field(label: string, value: string | number | null | undefined) {
	const text =
		value === null || value === undefined || value === "" ? "—" : `${value}`;
	return `${label.padEnd(16)} ${text}`;
}

function json(value: unknown): string {
	return JSON.stringify(value, null, 2) ?? "null";
}

function describeSource(release: DebugRelease): string[] {
	const source = release.source;
	const out = [field("Type", source.type)];
	if (source.image) out.push(field("Image", source.image));
	if (source.digest) out.push(field("Digest", source.digest));
	if (source.gitUrl) out.push(field("Git URL", source.gitUrl));
	if (source.branch) out.push(field("Branch", source.branch));
	if (source.commitSha) out.push(field("Commit", source.commitSha));
	if (source.imageId) out.push(field("Image ID", source.imageId));
	return out;
}

/**
 * The debug panel as plain text, for a bug report: the deployment, its raw
 * timeline with offsets, the release (keys of variables only), the agent
 * commands (already redacted by the control plane) and the newest log lines.
 * Everything passes through `scrubReportText` once more.
 */
export function buildDebugReport(input: DebugReportInput): string {
	const { deployment: d } = input;
	const parts: string[] = [];
	parts.push(`NebulaCtrl deployment debug report`);
	parts.push(field("Generated", input.generatedAt));
	if (input.url) parts.push(field("Panel", input.url));

	parts.push(section("Deployment"));
	parts.push(
		[
			field("Deployment", d.id),
			field("Service", `${input.serviceName} (${d.serviceId})`),
			field("Status", d.status),
			field("Message", d.statusMessage),
			field("Trigger", d.trigger),
			field("Actor", d.actorLabel),
			field("Strategy", d.strategy),
			field("Generation", d.generation),
			field("Created", d.createdAt),
			field("Started", d.startedAt),
			field("Finished", d.finishedAt),
			field("Server", input.serverName ?? d.serverId),
			field("Build server", input.buildServerName ?? d.buildServerId),
			field("Release", d.releaseId),
		].join("\n"),
	);

	const rows = buildTimeline(input.events, d.createdAt);
	const summary = summarizeTimeline(rows);
	parts.push(section(`Timeline (${summary.count} events)`));
	if (rows.length === 0) {
		parts.push("No events recorded.");
	} else {
		parts.push(
			rows
				.map((row) => {
					const what =
						row.step === "status"
							? `status → ${row.status}`
							: `${stepLabel(row.step)} ${row.status}`;
					const message = row.message
						? `  ${row.message.replaceAll("\n", " ⏎ ")}`
						: "";
					return `${clockWithMs(row.at)}  ${formatOffset(row.sinceStart).padStart(14)}  ${formatOffset(row.sinceLast).padStart(12)}  ${what}${message}`;
				})
				.join("\n"),
		);
	}

	parts.push(section("Release"));
	if (input.release === null) {
		parts.push("No release recorded for this deployment.");
	} else {
		const r = input.release;
		parts.push(
			[
				field("Release", r.id),
				field("Generation", r.generation),
				field("Image ref", r.imageRef),
				...describeSource(r),
				field("Build method", r.build?.method),
				field("Target", r.placement.targetServerName),
				field("Build server", r.placement.buildServerName),
				field("Registry ref", r.placement.registryRef),
				field("Platform", r.placement.platform),
			].join("\n"),
		);
		parts.push("\nConfiguration (secrets masked):");
		parts.push(json(r.config));
		parts.push("\nVariables (keys only):");
		parts.push(
			r.variablesPruned
				? "The release's environment was pruned (outside the rollback window)."
				: r.variables
						.map((v) => {
							const flags = [
								v.secret ? "secret" : null,
								v.reference ? "reference" : null,
								v.platform ? "platform" : null,
								v.external ? "external" : null,
								v.scope === "runtime" ? null : v.scope,
							].filter(Boolean);
							return `  ${v.key}${flags.length > 0 ? `  [${flags.join(", ")}]` : ""}`;
						})
						.join("\n") || "  none",
		);
	}

	parts.push(section("Agent commands"));
	if (input.commands === null || input.commands.length === 0) {
		parts.push("No commands.");
	} else {
		for (const c of input.commands) {
			parts.push(
				[
					`${c.kind} ${c.id} on ${c.serverName}`,
					field("  Status", `${c.status}${c.outcome ? ` (${c.outcome})` : ""}`),
					field("  Enqueued", c.createdAt),
					field("  Sent", c.sentAt),
					field("  Acked", c.ackedAt),
					field("  Finished", c.finishedAt),
					field("  Error", c.error),
					`  Payload (redacted): ${json(c.payload).replaceAll("\n", "\n  ")}`,
					`  Result (redacted): ${json(c.result).replaceAll("\n", "\n  ")}`,
				].join("\n"),
			);
		}
	}

	const shown = input.logLines.slice(-REPORT_MAX_LOG_LINES);
	const cut = input.logLines.length - shown.length;
	parts.push(
		section(
			`Log (${shown.length} of ${input.logLines.length} lines, newest last)`,
		),
	);
	if (cut > 0) parts.push(`… ${cut} older lines left out`);
	parts.push(
		shown
			.map((l) =>
				`${clockWithMs(l.ts)} [${l.stream ?? "deploy"}] ${l.line}`.trimStart(),
			)
			.join("\n") || "No log lines.",
	);

	return `${scrubReportText(parts.join("\n"))}\n`;
}
