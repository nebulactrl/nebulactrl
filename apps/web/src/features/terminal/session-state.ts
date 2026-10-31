import type { ServerMessage } from "./protocol";

/** What the terminal panel shows. Every phase has a text cue. */
export type SessionView =
	| { phase: "setup" }
	| { phase: "connecting" }
	| {
			phase: "live";
			replica: number;
			container: string;
			/** Seconds left when the idle warning arrived; null while active. */
			idleSecondsLeft: number | null;
	  }
	| {
			phase: "ended";
			reason: string | null;
			exitCode: number | null;
	  }
	| { phase: "failed"; code: string; message: string };

export type SessionEvent =
	| { t: "connect" }
	| { t: "message"; message: ServerMessage }
	| { t: "socket-closed" }
	| { t: "activity" }
	| { t: "reset" };

export const SETUP: SessionView = { phase: "setup" };

/** Pure transition function of one terminal session. */
export function reduceSession(
	state: SessionView,
	event: SessionEvent,
): SessionView {
	switch (event.t) {
		case "reset":
			return SETUP;
		case "connect":
			return { phase: "connecting" };
		case "activity":
			return state.phase === "live" && state.idleSecondsLeft !== null
				? { ...state, idleSecondsLeft: null }
				: state;
		case "socket-closed":
			if (state.phase === "connecting" || state.phase === "live") {
				return {
					phase: "failed",
					code: "connection_lost",
					message: "The connection to the control plane was lost.",
				};
			}
			return state;
		case "message":
			return reduceMessage(state, event.message);
	}
}

function reduceMessage(state: SessionView, m: ServerMessage): SessionView {
	switch (m.type) {
		case "opened":
			return {
				phase: "live",
				replica: m.replica,
				container: m.container,
				idleSecondsLeft: null,
			};
		case "idle_warning":
			return state.phase === "live"
				? { ...state, idleSecondsLeft: m.secondsLeft }
				: state;
		case "exit":
			return { phase: "ended", reason: "exit", exitCode: m.exitCode };
		case "closed":
			// The exit message (if any) came first and already ended the session.
			return state.phase === "ended"
				? state
				: { phase: "ended", reason: m.reason || null, exitCode: null };
		case "error":
			return { phase: "failed", code: m.code, message: m.message };
	}
}
