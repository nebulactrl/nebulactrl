import type { CentralLogPage } from "#/api/gen/types.gen";
import type { LogLine } from "#/api/sse";

// Pure parts of the Central logs view: the lines of the page as the log window
// takes them, who they came from, and what to say when there are none. No
// React and no network, so it is tested.

/** The name of the view's tab, with how long the log store keeps a line. */
export const CENTRAL_TAB_LABEL = "Central logs (7 days)";

/** Lines asked for in one request: the server's maximum. */
export const CENTRAL_LIMIT = 1000;

/**
 * The page's lines for the log window: the API answers newest first, the
 * window reads oldest first (the newest at the bottom, like the live tail).
 */
export function centralLines(
	page: Pick<CentralLogPage, "lines"> | undefined,
): LogLine[] {
	const lines = page?.lines ?? [];
	const out: LogLine[] = [];
	for (let index = lines.length - 1; index >= 0; index -= 1) {
		const entry = lines[index];
		if (!entry) continue;
		out.push({
			id: out.length,
			ts: entry.ts,
			line: entry.line,
			stream: entry.stream,
		});
	}
	return out;
}

/** The servers the lines came from, in the order they first appear, newest line first. */
export function centralServers(
	page: Pick<CentralLogPage, "lines"> | undefined,
): string[] {
	const names: string[] = [];
	for (const entry of page?.lines ?? []) {
		if (entry.server !== "" && !names.includes(entry.server)) {
			names.push(entry.server);
		}
	}
	return names;
}

/** The line under the log window: where the lines came from. */
export function centralFooter(
	page: Pick<CentralLogPage, "lines"> | undefined,
): string {
	const servers = centralServers(page);
	if (servers.length === 0)
		return "Lines from every server this service runs on.";
	return `From ${servers.length === 1 ? "server" : "servers"} ${servers.join(", ")}.`;
}

/** Whether the page has the lines of a collecting team, or says nothing is collected. */
export type CentralState =
	| { kind: "off"; hint: string }
	| { kind: "empty" }
	| { kind: "lines"; truncated: boolean };

/** What the view shows for an answer: the off hint, an empty window or the lines. */
export function centralState(
	page: Pick<CentralLogPage, "enabled" | "hint" | "lines">,
): CentralState {
	if (!page.enabled) {
		return {
			kind: "off",
			hint:
				page.hint ||
				"Central logs are off. Turn on Log forwarding in Team settings to collect logs here.",
		};
	}
	const lines = page.lines ?? [];
	if (lines.length === 0) return { kind: "empty" };
	return { kind: "lines", truncated: lines.length >= CENTRAL_LIMIT };
}

/**
 * What the view says when the request fails. A 404 means this control plane
 * has no central-logs route (an older build), not that the service is gone:
 * say that, instead of the router's "route not found".
 */
export function centralErrorText(
	status: number | undefined,
	message: string,
): string {
	if (status === 404)
		return "Central logs aren't available on this control plane.";
	return message;
}
