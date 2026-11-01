import "@xterm/xterm/css/xterm.css";

import type { Terminal as XTerm } from "@xterm/xterm";
import { useEffect, useRef } from "react";
import { prefersReducedMotion } from "#/lib/transitions";
import { TerminalConnection } from "./connection";
import { describeEnd, type OpenMessage } from "./protocol";
import type { SessionEvent, SessionView } from "./session-state";

/**
 * xterm paints to a canvas and needs concrete colours, so the theme is read
 * once from the design tokens. The canvas round-trip turns any CSS colour
 * (the tokens are oklch) into sRGB.
 */
function resolveToken(name: string): string {
	const probe = document.createElement("canvas");
	probe.width = 1;
	probe.height = 1;
	const ctx = probe.getContext("2d", { willReadFrequently: true });
	if (!ctx) return "";
	const value = getComputedStyle(document.documentElement)
		.getPropertyValue(name)
		.trim();
	ctx.clearRect(0, 0, 1, 1);
	ctx.fillStyle = value;
	ctx.fillRect(0, 0, 1, 1);
	const [r = 0, g = 0, b = 0, a = 255] = ctx.getImageData(0, 0, 1, 1).data;
	if (a === 255) {
		return `#${[r, g, b].map((c) => c.toString(16).padStart(2, "0")).join("")}`;
	}
	return `rgba(${r}, ${g}, ${b}, ${(a / 255).toFixed(2)})`;
}

/** The well surface behind the terminal, matched by `bg-surface-well` on the host. */
function terminalTheme() {
	const well = resolveToken("--color-surface-well");
	return {
		background: well,
		foreground: resolveToken("--color-ink-1"),
		cursor: resolveToken("--color-accent-ink"),
		cursorAccent: well,
		selectionBackground: resolveToken("--selection"),
		black: resolveToken("--color-surface-raised"),
		brightBlack: resolveToken("--color-ink-4"),
	};
}

/** The live terminal: xterm on a TerminalConnection. Remount to start over. */
export function LiveTerminal({
	url,
	open,
	view,
	dispatch,
	termRef,
}: {
	/** The session's socket (ws:// or wss://). A different URL starts over. */
	url: string;
	/** Builds the first message from the terminal's size once it is laid out. */
	open: (size: { cols: number; rows: number }) => OpenMessage;
	view: SessionView;
	dispatch: (e: SessionEvent) => void;
	termRef: React.RefObject<XTerm | null>;
}) {
	const hostRef = useRef<HTMLDivElement | null>(null);
	// The session is fixed at mount (a new one remounts this component), so the
	// effect reads these through refs and only the socket's URL restarts it.
	const openRef = useRef(open);
	const dispatchRef = useRef(dispatch);
	useEffect(() => {
		openRef.current = open;
		dispatchRef.current = dispatch;
	}, [open, dispatch]);

	useEffect(() => {
		let disposed = false;
		let cleanup: (() => void) | undefined;
		void (async () => {
			const [{ Terminal }, { FitAddon }] = await Promise.all([
				import("@xterm/xterm"),
				import("@xterm/addon-fit"),
			]);
			const host = hostRef.current;
			if (disposed || !host) return;
			const term = new Terminal({
				cursorBlink: !prefersReducedMotion(),
				fontFamily: '"IBM Plex Mono", ui-monospace, Menlo, monospace',
				fontSize: 13,
				scrollback: 5000,
				theme: terminalTheme(),
				allowProposedApi: false,
			});
			const fit = new FitAddon();
			term.loadAddon(fit);
			term.open(host);
			fit.fit();
			termRef.current = term;

			let exitCode: number | null = null;
			const conn = new TerminalConnection(
				url,
				openRef.current({ cols: term.cols, rows: term.rows }),
				{
					onOutput: (data) => term.write(data),
					onMessage: (message) => {
						if (message.type === "exit") exitCode = message.exitCode;
						if (message.type === "closed" || message.type === "exit") {
							const reason = message.type === "exit" ? "exit" : message.reason;
							if (message.type === "closed" || reason === "exit") {
								term.write(
									`\r\n\x1b[2m[${describeEnd(reason, exitCode)}]\x1b[0m\r\n`,
								);
							}
						}
						dispatchRef.current({ t: "message", message });
					},
					onClosed: () => dispatchRef.current({ t: "socket-closed" }),
				},
			);
			const dataSub = term.onData((data) => {
				conn.input(data);
				dispatchRef.current({ t: "activity" });
			});
			const resizeSub = term.onResize(({ cols, rows }) =>
				conn.resize(cols, rows),
			);
			const observer = new ResizeObserver(() => fit.fit());
			observer.observe(host);
			// Escape belongs to the program in the terminal (vim, less), not to the
			// panel around it.
			const keepEscape = (e: KeyboardEvent) => {
				if (e.key === "Escape") e.stopPropagation();
			};
			host.addEventListener("keydown", keepEscape);
			conn.start();
			term.focus();
			cleanup = () => {
				host.removeEventListener("keydown", keepEscape);
				observer.disconnect();
				dataSub.dispose();
				resizeSub.dispose();
				conn.end();
				term.dispose();
				termRef.current = null;
			};
			if (disposed) cleanup();
		})();
		return () => {
			disposed = true;
			cleanup?.();
		};
	}, [url, termRef]);

	return (
		<div
			ref={hostRef}
			role="application"
			aria-label="Terminal"
			data-phase={view.phase}
			className="h-full min-h-[240px] w-full overflow-hidden rounded-card bg-surface-well p-2"
		/>
	);
}
