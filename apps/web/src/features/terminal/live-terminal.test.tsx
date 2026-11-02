import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, screen, waitFor } from "@testing-library/react";
import type { Terminal as XTerm } from "@xterm/xterm";
import { LiveTerminal } from "./live-terminal";
import type { OpenMessage } from "./protocol";
import { SETUP, type SessionEvent, type SessionView } from "./session-state";
import { type FakeWebSocket, installFakeWebSocket } from "./test-support";

let sockets: FakeWebSocket[];
let restore: () => void;

beforeEach(() => {
	({ sockets, restore } = installFakeWebSocket());
});
afterEach(() => restore());

function mount(
	view: SessionView = SETUP,
	url = "ws://cp.test/api/v1/services/svc-1/terminal/ws",
) {
	const events: SessionEvent[] = [];
	const termRef: { current: XTerm | null } = { current: null };
	const props = {
		url,
		open: (size: { cols: number; rows: number }): OpenMessage => ({
			type: "open",
			replica: 3,
			cols: size.cols,
			rows: size.rows,
			confirmProduction: true,
		}),
		view,
		dispatch: (e: SessionEvent) => events.push(e),
		termRef,
	};
	const utils = render(<LiveTerminal {...props} />);
	return {
		...utils,
		events,
		termRef,
		props,
		rerenderWith: (next: Partial<typeof props>) =>
			utils.rerender(<LiveTerminal {...props} {...next} />),
	};
}

/** Mounts and waits for xterm to load and the socket to be created. */
async function mounted(view?: SessionView, url?: string) {
	const m = mount(view, url);
	await waitFor(() => expect(sockets).toHaveLength(1));
	const socket = sockets[0] as FakeWebSocket;
	const term = m.termRef.current as XTerm;
	expect(term).not.toBeNull();
	return { ...m, socket, term };
}

function screenText(term: XTerm): string {
	const buffer = term.buffer.active;
	const lines: string[] = [];
	for (let i = 0; i < buffer.length; i++) {
		lines.push(buffer.getLine(i)?.translateToString(true) ?? "");
	}
	return lines.join("\n");
}

const opened = {
	type: "opened",
	replica: 3,
	container: "c-1",
	command: ["sh"],
};

describe("LiveTerminal", () => {
	test("is a labelled application region that shows the session phase", async () => {
		const m = mount({ phase: "connecting" });
		const region = screen.getByRole("application", { name: "Terminal" });
		expect(region).toHaveAttribute("data-phase", "connecting");
		m.rerenderWith({ view: { phase: "failed", code: "x", message: "m" } });
		expect(region).toHaveAttribute("data-phase", "failed");
		await waitFor(() => expect(sockets).toHaveLength(1));
	});

	test("connects to the session's URL and sends the open message with the terminal's size", async () => {
		const { socket, term } = await mounted();
		expect(socket.url).toBe("ws://cp.test/api/v1/services/svc-1/terminal/ws");
		expect(socket.binaryType).toBe("arraybuffer");
		// Nothing is sent before the socket opens.
		expect(socket.sent).toEqual([]);
		act(() => socket.open());
		expect(socket.messages).toEqual([
			{
				type: "open",
				replica: 3,
				cols: term.cols,
				rows: term.rows,
				confirmProduction: true,
			},
		]);
	});

	test("server output appears in the terminal", async () => {
		const { socket, term } = await mounted();
		act(() => socket.open());
		act(() => socket.output("hello from the container\r\n"));
		await waitFor(() =>
			expect(screenText(term)).toContain("hello from the container"),
		);
	});

	test("protocol messages are passed on to the session", async () => {
		const { socket, events } = await mounted();
		act(() => socket.open());
		act(() => socket.serve(opened));
		act(() => socket.serve({ type: "idle_warning", secondsLeft: 30 }));
		expect(events).toEqual([
			{
				t: "message",
				message: {
					type: "opened",
					replica: 3,
					container: "c-1",
					command: ["sh"],
				},
			},
			{
				t: "message",
				message: { type: "idle_warning", secondsLeft: 30 },
			},
		]);
	});

	test("keystrokes go out as binary frames once the session is open, and count as activity", async () => {
		const { socket, term, events } = await mounted();
		act(() => socket.open());
		act(() => term.input("early"));
		// Before `opened` the keystrokes are dropped, but the person was active.
		expect(socket.typed).toEqual([]);
		act(() => socket.serve(opened));
		act(() => term.input("ls -la\r"));
		expect(socket.typed).toEqual(["ls -la\r"]);
		expect(events.filter((e) => e.t === "activity")).toHaveLength(2);
	});

	test("resizing the terminal tells the server the new size", async () => {
		const { socket, term } = await mounted();
		act(() => socket.open());
		act(() => socket.serve(opened));
		act(() => term.resize(100, 30));
		expect(socket.messages.at(-1)).toEqual({
			type: "resize",
			cols: 100,
			rows: 30,
		});
	});

	test("a shell that exits is announced in the terminal with its code", async () => {
		const { socket, term, events } = await mounted();
		act(() => socket.open());
		act(() => socket.serve(opened));
		act(() => socket.serve({ type: "exit", exitCode: 3 }));
		await waitFor(() =>
			expect(screenText(term)).toContain("[The shell exited with code 3.]"),
		);
		expect(events.at(-1)).toEqual({
			t: "message",
			message: { type: "exit", exitCode: 3 },
		});
	});

	test("a closed session is announced with the reason; a late closed after an exit keeps the exit code", async () => {
		const { socket, term } = await mounted();
		act(() => socket.open());
		act(() => socket.serve(opened));
		act(() => socket.serve({ type: "exit", exitCode: 2 }));
		act(() => socket.serve({ type: "closed", reason: "idle" }));
		await waitFor(() =>
			expect(screenText(term)).toContain(
				"[Closed after 15 minutes without input.]",
			),
		);
		expect(screenText(term)).toContain("[The shell exited with code 2.]");
	});

	test("a server error is reported to the session, not written into the terminal", async () => {
		const { socket, term, events } = await mounted();
		act(() => socket.open());
		act(() =>
			socket.serve({
				type: "error",
				code: "replica_not_running",
				message: "gone",
			}),
		);
		expect(events.at(-1)).toEqual({
			t: "message",
			message: { type: "error", code: "replica_not_running", message: "gone" },
		});
		expect(screenText(term).trim()).toBe("");
	});

	test("a connection that drops tells the session once", async () => {
		const { socket, events } = await mounted();
		act(() => socket.open());
		act(() => socket.serve(opened));
		act(() => socket.drop());
		expect(events.filter((e) => e.t === "socket-closed")).toHaveLength(1);
	});

	test("Escape stays inside the terminal; other keys still reach the page", async () => {
		const m = await mounted();
		const region = screen.getByRole("application", { name: "Terminal" });
		const seen: string[] = [];
		const listener = (e: KeyboardEvent) => seen.push(e.key);
		document.body.addEventListener("keydown", listener);
		try {
			region.dispatchEvent(
				new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
			);
			region.dispatchEvent(
				new KeyboardEvent("keydown", { key: "a", bubbles: true }),
			);
		} finally {
			document.body.removeEventListener("keydown", listener);
		}
		expect(seen).toEqual(["a"]);
		m.unmount();
	});

	test("unmounting ends the session: the server is told, the socket closed, the handle released", async () => {
		const { socket, termRef, unmount, events } = await mounted();
		act(() => socket.open());
		act(() => socket.serve(opened));
		unmount();
		expect(socket.messages.at(-1)).toEqual({ type: "close" });
		expect(socket.closedWith).toBe(1000);
		expect(termRef.current).toBeNull();
		// The close the app asked for is still reported once, as a closed socket.
		expect(events.filter((e) => e.t === "socket-closed")).toHaveLength(1);
	});

	test("unmounting before xterm has loaded never opens a socket", async () => {
		const { unmount } = mount();
		unmount();
		// Let the dynamic imports settle.
		const { promise, resolve } = Promise.withResolvers<void>();
		setTimeout(resolve, 0);
		await act(async () => {
			await promise;
		});
		expect(sockets).toEqual([]);
	});

	test("a different URL starts a new connection and ends the old one", async () => {
		const m = await mounted();
		act(() => m.socket.open());
		m.rerenderWith({ url: "ws://cp.test/api/v1/servers/srv-1/shell/ws" });
		await waitFor(() => expect(sockets).toHaveLength(2));
		expect(m.socket.closedWith).toBe(1000);
		expect(sockets[1]?.url).toBe("ws://cp.test/api/v1/servers/srv-1/shell/ws");
		expect(m.termRef.current).not.toBeNull();
	});
});
