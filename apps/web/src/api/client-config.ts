// The one place the generated hey-api client (src/api/gen) is configured.
//
// - Same-origin cookies on every call (`credentials: "include"`); the base URL
//   (`/api/v1`) comes from the OpenAPI document's `servers`.
// - RFC 9457 problem+json bodies become a typed `ApiError`.
// - Cross-cutting reactions to failures live here and nowhere else: 401 sends
//   the visitor to /login, 403 explains itself in a toast, and a 412
//   `generation_conflict` toasts and refetches ("Someone else changed this
//   service — reloaded").

import { client } from "./gen/client.gen";
import type { Error as Problem } from "./gen/types.gen";

export class ApiError extends Error {
	readonly status: number;
	readonly code: string;
	readonly details: Record<string, unknown> | undefined;

	constructor(
		status: number,
		code: string,
		message: string,
		details?: Record<string, unknown>,
	) {
		super(message);
		this.name = "ApiError";
		this.status = status;
		this.code = code;
		this.details = details;
	}
}

export function isApiError(error: unknown): error is ApiError {
	return error instanceof ApiError;
}

export function errorStatus(error: unknown): number | undefined {
	return error instanceof ApiError ? error.status : undefined;
}

export function errorCode(error: unknown): string | undefined {
	return error instanceof ApiError ? error.code : undefined;
}

export function errorMessage(error: unknown): string {
	if (error instanceof Error && error.message) return error.message;
	return "Something went wrong.";
}

export interface ApiHandlers {
	/** The session ended (401 from a session-protected call). */
	onUnauthorized?: () => void;
	/** The caller's token or role lacks a scope (403). */
	onForbidden?: (error: ApiError) => void;
	/** PATCH/PUT lost an optimistic-concurrency race (412). */
	onConflict?: (error: ApiError) => void;
}

let handlers: ApiHandlers = {};

/** Installed once from main.tsx, where the router and query client exist. */
export function setApiHandlers(next: ApiHandlers): void {
	handlers = next;
}

function describe(problem: Problem | undefined, fallback: string): string {
	if (!problem) return fallback;
	const fieldErrors = (problem.errors ?? [])
		.map((entry) => entry.message)
		.filter((message): message is string => Boolean(message));
	return (
		problem.detail ||
		(fieldErrors.length > 0 ? fieldErrors.join("; ") : "") ||
		problem.title ||
		fallback
	);
}

// Calls where a 401 is an expected answer rather than "the session ended".
const QUIET_401 = ["/auth/login", "/auth/2fa", "/auth/logout", "/setup", "/me"];

function isQuiet401(url: string | undefined): boolean {
	if (!url) return false;
	const path = new URL(url, window.location.origin).pathname.replace(
		/^\/api\/v1/,
		"",
	);
	return QUIET_401.includes(path);
}

/** Runs the cross-cutting reaction to a failed call (401, 403, 412). */
export function reportFailure(error: ApiError): void {
	if (error.status === 401) handlers.onUnauthorized?.();
	else if (error.status === 403) handlers.onForbidden?.(error);
	else if (error.status === 412) handlers.onConflict?.(error);
}

client.setConfig({ credentials: "include" });

client.interceptors.error.use((error, response, request) => {
	if (error instanceof ApiError) return error;
	if (!response) {
		// fetch itself failed: offline, the server is down, or the call was aborted.
		if (error instanceof DOMException && error.name === "AbortError") {
			return error;
		}
		return new ApiError(
			0,
			"network_error",
			"The control plane did not answer. Check that the server is running.",
		);
	}
	const problem =
		error && typeof error === "object" ? (error as Problem) : undefined;
	const apiError = new ApiError(
		response.status,
		problem?.code ?? "unknown",
		describe(
			problem,
			typeof error === "string" && error
				? error
				: response.statusText ||
						`Request failed with status ${response.status}.`,
		),
		problem?.details,
	);
	if (!(response.status === 401 && isQuiet401(request?.url))) {
		reportFailure(apiError);
	}
	return apiError;
});
