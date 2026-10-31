// The browser ⇄ control plane protocol of the web terminal
// (GET /api/v1/services/{serviceId}/terminal/ws), mirrored from
// internal/terminal/session.go. Pure: no DOM, no React.

import { processQuery } from "./targets";

/** The first message of a session. */
export interface OpenMessage {
	type: "open";
	/** Services with several processes: the process whose replica to open; omitted is the default process. */
	process?: string;
	/** Index within the process. */
	replica: number;
	/** Empty: bash when the image has it, else sh. */
	command?: string[];
	cols: number;
	rows: number;
	/** Production services refuse a session without it. */
	confirmProduction: boolean;
}

export type ServerMessage =
	| { type: "opened"; replica: number; container: string; command: string[] }
	| { type: "exit"; exitCode: number }
	| { type: "idle_warning"; secondsLeft: number }
	| { type: "error"; code: string; message: string }
	| { type: "closed"; reason: string };

/** Keep in step with terminal.IdleTimeout. */
export const IDLE_TIMEOUT_MINUTES = 15;

/** ws(s):// URL of a service's terminal socket on the page's own origin. */
export function terminalSocketUrl(
	serviceId: string,
	loc: { protocol: string; host: string },
	process = "",
): string {
	const scheme = loc.protocol === "https:" ? "wss:" : "ws:";
	const q = new URLSearchParams(processQuery(process)).toString();
	return `${scheme}//${loc.host}/api/v1/services/${encodeURIComponent(serviceId)}/terminal/ws${q === "" ? "" : `?${q}`}`;
}

/**
 * ws(s):// URL of a server's host shell socket on the page's own origin
 * (GET /api/v1/servers/{serverId}/shell/ws, internal/terminal/hostshell.go).
 * It speaks the same protocol as the service terminal.
 */
export function hostShellSocketUrl(
	serverId: string,
	loc: { protocol: string; host: string },
): string {
	const scheme = loc.protocol === "https:" ? "wss:" : "ws:";
	return `${scheme}//${loc.host}/api/v1/servers/${encodeURIComponent(serverId)}/shell/ws`;
}

/** Same-origin URL of a file download (or inline text preview). */
export function fileContentUrl(
	serviceId: string,
	replica: number,
	path: string,
	preview: boolean,
	process = "",
): string {
	const q = new URLSearchParams({
		...processQuery(process),
		replica: String(replica),
		path,
	});
	if (preview) q.set("preview", "true");
	return `/api/v1/services/${encodeURIComponent(serviceId)}/files/content?${q.toString()}`;
}

/** Same-origin URL a file upload is POSTed to. */
export function fileUploadUrl(
	serviceId: string,
	replica: number,
	dir: string,
	name: string,
	process = "",
): string {
	const q = new URLSearchParams({
		...processQuery(process),
		replica: String(replica),
		path: dir,
		name,
	});
	return `/api/v1/services/${encodeURIComponent(serviceId)}/files/upload?${q.toString()}`;
}

const MESSAGE_TYPES: Record<string, true> = {
	opened: true,
	exit: true,
	idle_warning: true,
	error: true,
	closed: true,
};

/** Parses a text frame of the server; anything unknown is ignored (null). */
export function parseServerMessage(raw: string): ServerMessage | null {
	let value: unknown;
	try {
		value = JSON.parse(raw);
	} catch {
		return null;
	}
	if (typeof value !== "object" || value === null) return null;
	const m = value as Record<string, unknown>;
	if (typeof m.type !== "string" || !Object.hasOwn(MESSAGE_TYPES, m.type))
		return null;
	switch (m.type) {
		case "opened":
			return {
				type: "opened",
				replica: typeof m.replica === "number" ? m.replica : 0,
				container: typeof m.container === "string" ? m.container : "",
				command: Array.isArray(m.command) ? m.command.map(String) : [],
			};
		case "exit":
			return { type: "exit", exitCode: Number(m.exitCode ?? 0) };
		case "idle_warning":
			return { type: "idle_warning", secondsLeft: Number(m.secondsLeft ?? 0) };
		case "error":
			return {
				type: "error",
				code: typeof m.code === "string" ? m.code : "error",
				message: typeof m.message === "string" ? m.message : "",
			};
		default:
			return {
				type: "closed",
				reason: typeof m.reason === "string" ? m.reason : "",
			};
	}
}

/** The open message for a session. */
export function buildOpenMessage(input: {
	process?: string;
	replica: number;
	command: readonly string[];
	cols: number;
	rows: number;
	confirmProduction: boolean;
}): OpenMessage {
	return {
		type: "open",
		...(input.process ? { process: input.process } : {}),
		replica: input.replica,
		...(input.command.length > 0 ? { command: [...input.command] } : {}),
		cols: Math.max(1, Math.floor(input.cols)),
		rows: Math.max(1, Math.floor(input.rows)),
		confirmProduction: input.confirmProduction,
	};
}

/** The open message of a host shell: no replica, no command. */
export function buildHostOpenMessage(input: {
	cols: number;
	rows: number;
	confirmProduction: boolean;
}): OpenMessage {
	return {
		type: "open",
		replica: 0,
		cols: Math.max(1, Math.floor(input.cols)),
		rows: Math.max(1, Math.floor(input.rows)),
		confirmProduction: input.confirmProduction,
	};
}

/** What a server error code means to the person at the keyboard. */
export function errorTitle(code: string): string {
	switch (code) {
		case "confirmation_required":
			return "Production needs a confirmation";
		case "too_many_sessions":
			return "Too many terminals open";
		case "server_offline":
			return "The server is offline";
		case "not_deployed":
			return "Not deployed yet";
		case "replica_not_running":
			return "That replica is not running";
		case "open_failed":
			return "The shell could not start";
		case "connection_lost":
			return "Connection lost";
		case "forbidden":
			return "Not allowed";
		case "host_shell_disabled":
			return "The host shell is off for this server";
		case "two_factor_required":
			return "Two-factor authentication is required";
		case "session_required":
			return "A signed-in session is required";
		default:
			return "The terminal could not open";
	}
}

/** A sentence for a session that ended. */
export function describeEnd(
	reason: string | null,
	exitCode: number | null,
): string {
	switch (reason) {
		case "exit":
			return exitCode === null || exitCode === 0
				? "The shell exited."
				: `The shell exited with code ${exitCode}.`;
		case "idle":
			return `Closed after ${IDLE_TIMEOUT_MINUTES} minutes without input.`;
		case "agent_disconnected":
			return "The server went offline, so the session ended.";
		case "host_shell_disabled":
			return "An admin turned the host shell off, so the session ended.";
		case "client_closed":
			return "Session closed.";
		case "error":
			return "The session ended with an error.";
		default:
			return exitCode === null
				? "Session ended."
				: `The shell exited with code ${exitCode}.`;
	}
}

/** The idle countdown the warning banner shows: "1 minute", "45 seconds". */
export function idleCountdown(secondsLeft: number): string {
	const s = Math.max(0, Math.round(secondsLeft));
	if (s >= 60) {
		const m = Math.round(s / 60);
		return m === 1 ? "1 minute" : `${m} minutes`;
	}
	return s === 1 ? "1 second" : `${s} seconds`;
}
