// Test-only helpers of the terminal feature's tests: API fixtures and a fake
// WebSocket. Not imported by production code.
import { spyOn } from "bun:test";
import { act, fireEvent, screen } from "@testing-library/react";
import type {
	FileEntry,
	FileListing,
	HostShellInfoView,
	ServiceView,
	TerminalInfoView,
} from "#/api/gen/types.gen";
import { make, type Overrides } from "#/test/factories";

export function terminalInfo(
	overrides: Partial<TerminalInfoView> = {},
): TerminalInfoView {
	return {
		activeSessions: 0,
		available: true,
		fileWrites: { allowed: true },
		idleTimeoutSeconds: 900,
		maxSessions: 4,
		maxUploadBytes: 50 << 20,
		previewBytes: 1 << 20,
		production: false,
		replicas: [{ index: 0, process: "", running: true, state: "running" }],
		serverName: "edge-1",
		serviceId: "svc-1",
		...overrides,
	};
}

export function hostShellInfo(
	overrides: Partial<HostShellInfoView> = {},
): HostShellInfoView {
	return {
		activeSessions: 0,
		available: true,
		controlPlane: false,
		enabled: true,
		idleTimeoutSeconds: 900,
		maxSessions: 2,
		production: false,
		restricted: false,
		serverId: "srv-1",
		serverName: "edge-1",
		twoFactorEnabled: true,
		...overrides,
	};
}

/** The service the terminal panels open: the default scene's, as "api" of svc-1. */
export function serviceView(overrides: Overrides<ServiceView> = {}) {
	return make("ServiceView", {
		id: "svc-1",
		name: "api",
		slug: "api",
		projectId: "proj-1",
		environmentId: "env-1",
		kind: "app",
		sourceType: "image",
		config: { healthcheck: { type: "http" } },
		...overrides,
	});
}

export function fileEntry(
	name: string,
	overrides: Partial<FileEntry> = {},
): FileEntry {
	return { name, size: 0, type: "file", ...overrides };
}

export function listing(
	entries: FileEntry[] | null,
	overrides: Partial<FileListing> = {},
): FileListing {
	return {
		entries,
		path: "/",
		replica: 0,
		truncated: false,
		writable: true,
		...overrides,
	};
}

/** A WebSocket that records what is sent and lets a test play the server. */
export class FakeWebSocket {
	static readonly OPEN = 1;
	readonly sent: (string | Uint8Array)[] = [];
	binaryType = "blob";
	readyState = 0;
	closedWith: number | undefined;
	onopen: ((ev: unknown) => void) | null = null;
	onmessage: ((ev: { data: unknown }) => void) | null = null;
	onclose: ((ev: unknown) => void) | null = null;
	onerror: ((ev: unknown) => void) | null = null;

	constructor(readonly url: string) {}

	send(data: string | Uint8Array) {
		this.sent.push(data);
	}

	close(code?: number) {
		this.closedWith = code;
		this.readyState = 3;
		this.onclose?.({});
	}

	/** The socket connects. */
	open() {
		this.readyState = 1;
		this.onopen?.({});
	}

	/** The server sends a protocol message. */
	serve(message: Record<string, unknown>) {
		this.onmessage?.({ data: JSON.stringify(message) });
	}

	/** The server sends terminal output. */
	output(text: string) {
		this.onmessage?.({ data: new TextEncoder().encode(text).buffer });
	}

	/** The connection drops without the app closing it. */
	drop() {
		this.readyState = 3;
		this.onclose?.({});
	}

	/** The JSON frames the app sent. */
	get messages(): Record<string, unknown>[] {
		return this.sent
			.filter((d): d is string => typeof d === "string")
			.map((d) => JSON.parse(d) as Record<string, unknown>);
	}

	/** The binary (keystroke) frames the app sent, decoded. */
	get typed(): string[] {
		return this.sent
			.filter((d): d is Uint8Array => typeof d !== "string")
			.map((d) => new TextDecoder().decode(d));
	}
}

/** Replaces the global WebSocket; call the returned function to restore it. */
export function installFakeWebSocket(): {
	sockets: FakeWebSocket[];
	restore: () => void;
} {
	const original = globalThis.WebSocket;
	const sockets: FakeWebSocket[] = [];
	class Tracked extends FakeWebSocket {
		constructor(url: string) {
			super(url);
			sockets.push(this);
		}
	}
	globalThis.WebSocket = Tracked as unknown as typeof WebSocket;
	return {
		sockets,
		restore: () => {
			globalThis.WebSocket = original;
		},
	};
}

/**
 * Completes a hold-to-confirm button: starts the hold with the keyboard and
 * fires the 1.3 s timer by hand instead of waiting for it.
 */
export function holdToConfirm(name: RegExp) {
	const button = screen.getByRole("button", { name });
	const realSetTimeout = window.setTimeout.bind(window);
	let finishHold: (() => void) | undefined;
	const spy = spyOn(window, "setTimeout").mockImplementation(((
		handler: () => void,
		ms?: number,
	) => {
		if (ms === 1300) {
			finishHold = handler;
			return 0;
		}
		return realSetTimeout(handler, ms);
	}) as typeof window.setTimeout);
	try {
		fireEvent.keyDown(button, { key: "Enter" });
	} finally {
		spy.mockRestore();
	}
	if (!finishHold) throw new Error("pressing the button did not start a hold");
	act(() => finishHold?.());
}
