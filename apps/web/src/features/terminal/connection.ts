import {
	type OpenMessage,
	parseServerMessage,
	type ServerMessage,
} from "./protocol";

/** The slice of WebSocket the connection uses (tests pass a fake). */
export interface SocketLike {
	binaryType: string;
	readyState: number;
	onopen: ((ev: unknown) => void) | null;
	onmessage: ((ev: { data: unknown }) => void) | null;
	onclose: ((ev: unknown) => void) | null;
	onerror: ((ev: unknown) => void) | null;
	send(data: string | Uint8Array): void;
	close(code?: number, reason?: string): void;
}

const OPEN = 1;

export interface ConnectionHandlers {
	/** Terminal output. */
	onOutput(data: Uint8Array): void;
	onMessage(message: ServerMessage): void;
	/** The socket closed (whatever the reason); fires once. */
	onClosed(): void;
}

/**
 * One terminal WebSocket: sends the open message when the socket opens,
 * relays input as binary frames and the size as resize messages, and splits
 * the server's frames into terminal output (binary) and protocol messages
 * (text). The terminal's content is never inspected or stored here.
 */
export class TerminalConnection {
	private socket: SocketLike | null = null;
	private closed = false;
	private live = false;
	private readonly encoder = new TextEncoder();

	constructor(
		private readonly url: string,
		private readonly open: OpenMessage,
		private readonly handlers: ConnectionHandlers,
		private readonly create: (url: string) => SocketLike = (u) =>
			new WebSocket(u) as unknown as SocketLike,
	) {}

	start(): void {
		const socket = this.create(this.url);
		this.socket = socket;
		socket.binaryType = "arraybuffer";
		socket.onopen = () => {
			socket.send(JSON.stringify(this.open));
		};
		socket.onmessage = (ev) => {
			if (typeof ev.data === "string") {
				const message = parseServerMessage(ev.data);
				if (!message) return;
				if (message.type === "opened") this.live = true;
				this.handlers.onMessage(message);
				return;
			}
			if (ev.data instanceof ArrayBuffer) {
				this.handlers.onOutput(new Uint8Array(ev.data));
			} else if (ev.data instanceof Uint8Array) {
				this.handlers.onOutput(ev.data);
			}
		};
		const finish = () => {
			if (this.closed) return;
			this.closed = true;
			this.live = false;
			this.handlers.onClosed();
		};
		socket.onclose = finish;
		socket.onerror = () => {
			/* the close event follows and reports it */
		};
	}

	/** Keystrokes and pasted text; dropped until the session is open. */
	input(text: string): void {
		if (!this.live || !this.socket || this.socket.readyState !== OPEN) return;
		this.socket.send(this.encoder.encode(text));
	}

	resize(cols: number, rows: number): void {
		if (!this.live || !this.socket || this.socket.readyState !== OPEN) return;
		this.socket.send(
			JSON.stringify({
				type: "resize",
				cols: Math.max(1, Math.floor(cols)),
				rows: Math.max(1, Math.floor(rows)),
			}),
		);
	}

	/** Ends the session: the server kills the shell and closes the socket. */
	end(): void {
		const socket = this.socket;
		if (!socket || this.closed) return;
		if (socket.readyState === OPEN) {
			socket.send(JSON.stringify({ type: "close" }));
		}
		socket.close(1000, "closed by user");
	}
}
