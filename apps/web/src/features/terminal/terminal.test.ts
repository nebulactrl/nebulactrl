import { describe, expect, test } from "bun:test";
import { type SocketLike, TerminalConnection } from "./connection";
import {
	breadcrumbs,
	bytesLookBinary,
	entryAction,
	joinPath,
	looksBinary,
	modeString,
	parentPath,
	uploadBlocker,
	validUploadName,
} from "./files-model";
import {
	buildHostOpenMessage,
	buildOpenMessage,
	describeEnd,
	errorTitle,
	fileContentUrl,
	fileUploadUrl,
	hostShellSocketUrl,
	idleCountdown,
	parseServerMessage,
	terminalSocketUrl,
} from "./protocol";
import { reduceSession, SETUP, type SessionView } from "./session-state";
import {
	processQuery,
	replicaProcesses,
	replicasOfProcess,
	startProcess,
	startReplica,
	type TargetReplica,
	targetLabel,
} from "./targets";

describe("protocol", () => {
	test("socket URL follows the page's scheme and host", () => {
		expect(
			terminalSocketUrl("abc", { protocol: "https:", host: "cp.example.com" }),
		).toBe("wss://cp.example.com/api/v1/services/abc/terminal/ws");
		expect(
			terminalSocketUrl("abc", { protocol: "http:", host: "localhost:5173" }),
		).toBe("ws://localhost:5173/api/v1/services/abc/terminal/ws");
	});

	test("file URLs encode the path", () => {
		expect(fileContentUrl("s", 1, "/a b/c&d", true)).toBe(
			"/api/v1/services/s/files/content?replica=1&path=%2Fa+b%2Fc%26d&preview=true",
		);
		expect(fileContentUrl("s", 0, "/x", false)).not.toContain("preview");
		expect(fileUploadUrl("s", 0, "/data", "in.txt")).toBe(
			"/api/v1/services/s/files/upload?replica=0&path=%2Fdata&name=in.txt",
		);
	});

	test("open message omits an empty command and clamps the size", () => {
		const m = buildOpenMessage({
			replica: 2,
			command: [],
			cols: 0,
			rows: 24.7,
			confirmProduction: true,
		});
		expect(m).toEqual({
			type: "open",
			replica: 2,
			cols: 1,
			rows: 24,
			confirmProduction: true,
		});
		expect(
			buildOpenMessage({
				replica: 0,
				command: ["psql"],
				cols: 80,
				rows: 24,
				confirmProduction: false,
			}).command,
		).toEqual(["psql"]);
	});

	test("server messages parse; unknown and malformed ones are ignored", () => {
		expect(
			parseServerMessage(
				'{"type":"opened","replica":1,"container":"c","command":["sh"]}',
			),
		).toEqual({
			type: "opened",
			replica: 1,
			container: "c",
			command: ["sh"],
		});
		expect(parseServerMessage('{"type":"exit","exitCode":7}')).toEqual({
			type: "exit",
			exitCode: 7,
		});
		expect(
			parseServerMessage(
				'{"type":"error","code":"too_many_sessions","message":"m"}',
			),
		).toEqual({
			type: "error",
			code: "too_many_sessions",
			message: "m",
		});
		expect(parseServerMessage('{"type":"constructor"}')).toBeNull();
		expect(parseServerMessage("not json")).toBeNull();
		expect(parseServerMessage("42")).toBeNull();
	});

	test("end descriptions are words, not codes", () => {
		expect(describeEnd("exit", 0)).toBe("The shell exited.");
		expect(describeEnd("exit", 2)).toContain("code 2");
		expect(describeEnd("idle", null)).toContain("15 minutes");
		expect(describeEnd("agent_disconnected", null)).toContain("offline");
		expect(idleCountdown(60)).toBe("1 minute");
		expect(idleCountdown(45)).toBe("45 seconds");
		expect(idleCountdown(1)).toBe("1 second");
	});
});

describe("session state", () => {
	const live: SessionView = {
		phase: "live",
		replica: 0,
		container: "c",
		idleSecondsLeft: null,
	};

	test("connect → opened → idle warning → activity", () => {
		let s = reduceSession(SETUP, { t: "connect" });
		expect(s.phase).toBe("connecting");
		s = reduceSession(s, {
			t: "message",
			message: { type: "opened", replica: 0, container: "c", command: [] },
		});
		expect(s).toEqual(live);
		s = reduceSession(s, {
			t: "message",
			message: { type: "idle_warning", secondsLeft: 60 },
		});
		expect(s).toEqual({ ...live, idleSecondsLeft: 60 });
		expect(reduceSession(s, { t: "activity" })).toEqual(live);
	});

	test("an exit ends the session and the closed message keeps its exit code", () => {
		let s = reduceSession(live, {
			t: "message",
			message: { type: "exit", exitCode: 3 },
		});
		expect(s).toEqual({ phase: "ended", reason: "exit", exitCode: 3 });
		s = reduceSession(s, {
			t: "message",
			message: { type: "closed", reason: "exit" },
		});
		expect(s).toEqual({ phase: "ended", reason: "exit", exitCode: 3 });
		s = reduceSession(s, { t: "socket-closed" });
		expect(s.phase).toBe("ended");
	});

	test("a close without an exit reports its reason", () => {
		expect(
			reduceSession(live, {
				t: "message",
				message: { type: "closed", reason: "idle" },
			}),
		).toEqual({
			phase: "ended",
			reason: "idle",
			exitCode: null,
		});
	});

	test("errors fail the session; a dropped socket while live is a lost connection", () => {
		expect(
			reduceSession(reduceSession(SETUP, { t: "connect" }), {
				t: "message",
				message: { type: "error", code: "server_offline", message: "offline" },
			}),
		).toEqual({ phase: "failed", code: "server_offline", message: "offline" });
		const lost = reduceSession(live, { t: "socket-closed" });
		expect(lost.phase === "failed" && lost.code).toBe("connection_lost");
		expect(reduceSession(lost, { t: "reset" })).toEqual(SETUP);
	});
});

class FakeSocket implements SocketLike {
	binaryType = "blob";
	readyState = 0;
	onopen: SocketLike["onopen"] = null;
	onmessage: SocketLike["onmessage"] = null;
	onclose: SocketLike["onclose"] = null;
	onerror: SocketLike["onerror"] = null;
	sent: (string | Uint8Array)[] = [];
	closedWith: number | undefined;
	send(data: string | Uint8Array) {
		this.sent.push(data);
	}
	close(code?: number) {
		this.closedWith = code;
		this.readyState = 3;
		this.onclose?.({});
	}
	open() {
		this.readyState = 1;
		this.onopen?.({});
	}
}

describe("TerminalConnection", () => {
	function setup() {
		const socket = new FakeSocket();
		const out: string[] = [];
		const messages: string[] = [];
		let closed = 0;
		const conn = new TerminalConnection(
			"ws://x",
			buildOpenMessage({
				replica: 1,
				command: [],
				cols: 100,
				rows: 30,
				confirmProduction: false,
			}),
			{
				onOutput: (d) => out.push(new TextDecoder().decode(d)),
				onMessage: (m) => messages.push(m.type),
				onClosed: () => closed++,
			},
			() => socket,
		);
		conn.start();
		return { socket, conn, out, messages, closed: () => closed };
	}

	test("sends the open message first; input waits for opened", () => {
		const { socket, conn, messages } = setup();
		socket.open();
		expect(JSON.parse(socket.sent[0] as string)).toMatchObject({
			type: "open",
			replica: 1,
			cols: 100,
		});
		conn.input("early");
		expect(socket.sent).toHaveLength(1);
		socket.onmessage?.({
			data: '{"type":"opened","replica":1,"container":"c"}',
		});
		expect(messages).toEqual(["opened"]);
		conn.input("ls\n");
		expect(new TextDecoder().decode(socket.sent[1] as Uint8Array)).toBe("ls\n");
		conn.resize(120, 40);
		expect(JSON.parse(socket.sent[2] as string)).toEqual({
			type: "resize",
			cols: 120,
			rows: 40,
		});
	});

	test("binary frames are terminal output", () => {
		const { socket, out } = setup();
		socket.open();
		socket.onmessage?.({ data: new TextEncoder().encode("hello").buffer });
		expect(out).toEqual(["hello"]);
	});

	test("end tells the server, then closes once", () => {
		const { socket, conn, closed } = setup();
		socket.open();
		conn.end();
		expect(JSON.parse(socket.sent.at(-1) as string)).toEqual({ type: "close" });
		expect(socket.closedWith).toBe(1000);
		conn.end();
		expect(closed()).toBe(1);
	});
});

describe("files model", () => {
	test("paths", () => {
		expect(joinPath("/", "etc")).toBe("/etc");
		expect(joinPath("/var/log", "app.log")).toBe("/var/log/app.log");
		expect(parentPath("/var/log")).toBe("/var");
		expect(parentPath("/var")).toBe("/");
		expect(parentPath("/")).toBe("/");
		expect(breadcrumbs("/var/log")).toEqual([
			{ label: "/", path: "/" },
			{ label: "var", path: "/var" },
			{ label: "log", path: "/var/log" },
		]);
		expect(breadcrumbs("/")).toHaveLength(1);
	});

	test("entry actions and binary detection", () => {
		expect(entryAction({ type: "dir" })).toBe("open");
		expect(entryAction({ type: "file" })).toBe("preview");
		expect(entryAction({ type: "other" })).toBe("none");
		expect(looksBinary("logo.PNG")).toBe(true);
		expect(looksBinary("Dockerfile")).toBe(false);
		expect(bytesLookBinary(new Uint8Array([104, 105, 0]))).toBe(true);
		expect(bytesLookBinary(new TextEncoder().encode("plain"))).toBe(false);
	});

	test("uploads are validated before anything is sent", () => {
		expect(validUploadName("a.txt")).toBeNull();
		expect(validUploadName("a/b")).not.toBeNull();
		expect(validUploadName("..")).not.toBeNull();
		expect(uploadBlocker({ name: "a", size: 10 }, 50 << 20)).toBeNull();
		expect(uploadBlocker({ name: "a", size: 51 << 20 }, 50 << 20)).toContain(
			"50 MB",
		);
	});

	test("mode strings", () => {
		expect(modeString("0644")).toBe("rw-r--r--");
		expect(modeString("0755")).toBe("rwxr-xr-x");
		expect(modeString(undefined)).toBe("");
	});
});

describe("host shell protocol", () => {
	test("socket URL points at the server's shell endpoint", () => {
		expect(
			hostShellSocketUrl("srv 1", {
				protocol: "https:",
				host: "cp.example.com",
			}),
		).toBe("wss://cp.example.com/api/v1/servers/srv%201/shell/ws");
		expect(
			hostShellSocketUrl("s", { protocol: "http:", host: "localhost:5173" }),
		).toBe("ws://localhost:5173/api/v1/servers/s/shell/ws");
	});

	test("the open message has no command and clamps the size", () => {
		expect(
			buildHostOpenMessage({ cols: 0, rows: 30.9, confirmProduction: true }),
		).toEqual({
			type: "open",
			replica: 0,
			cols: 1,
			rows: 30,
			confirmProduction: true,
		});
	});

	test("every refusal has a title and a closed session has a sentence", () => {
		expect(errorTitle("host_shell_disabled")).toBe(
			"The host shell is off for this server",
		);
		expect(errorTitle("two_factor_required")).toBe(
			"Two-factor authentication is required",
		);
		expect(errorTitle("session_required")).toBe(
			"A signed-in session is required",
		);
		expect(errorTitle("confirmation_required")).toBe(
			"Production needs a confirmation",
		);
		expect(describeEnd("host_shell_disabled", null)).toBe(
			"An admin turned the host shell off, so the session ended.",
		);
	});

	test("the shared reducer takes a host session from connect to ended", () => {
		let view = reduceSession(SETUP, { t: "connect" });
		view = reduceSession(view, {
			t: "message",
			message: {
				type: "opened",
				replica: 0,
				container: "host",
				command: ["/bin/bash", "-l"],
			},
		});
		expect(view.phase).toBe("live");
		view = reduceSession(view, {
			t: "message",
			message: { type: "closed", reason: "host_shell_disabled" },
		});
		expect(view).toEqual({
			phase: "ended",
			reason: "host_shell_disabled",
			exitCode: null,
		});
	});
});

describe("processes", () => {
	const replicas: TargetReplica[] = [
		{ process: "web", index: 0, running: true, state: "running" },
		{ process: "worker", index: 0, running: true, state: "running" },
		{ process: "worker", index: 1, running: false, state: "exited" },
		{ process: "clock", index: 0, running: false, state: "exited" },
	];

	test("a service without processes has no process to pick", () => {
		const plain: TargetReplica[] = [
			{ process: "", index: 0, running: true, state: "running" },
			{ process: "", index: 1, running: true, state: "running" },
		];
		expect(replicaProcesses(plain)).toEqual([]);
		expect(startProcess(plain, null)).toBe("");
		expect(replicasOfProcess(plain, "")).toHaveLength(2);
		expect(startReplica(plain, "", null)).toBe(0);
	});

	test("lists the processes in the order the replicas give them", () => {
		expect(replicaProcesses(replicas)).toEqual(["web", "worker", "clock"]);
	});

	test("a replica is addressed inside its process", () => {
		expect(replicasOfProcess(replicas, "worker").map((r) => r.index)).toEqual([
			0, 1,
		]);
		expect(startReplica(replicas, "worker", 1)).toBe(1);
		// A preselected index the process does not have falls back to its first running replica.
		expect(startReplica(replicas, "web", 3)).toBe(0);
		expect(startReplica(replicas, "clock", null)).toBe(0);
	});

	test("starts on the process the opener named, else the first that runs", () => {
		expect(startProcess(replicas, "worker")).toBe("worker");
		expect(startProcess(replicas, "gone")).toBe("web");
		expect(startProcess(replicas.slice(3), null)).toBe("clock");
	});

	test("labels a replica with its process", () => {
		expect(targetLabel("worker", 1)).toBe("worker · replica 1");
		expect(targetLabel("", 2)).toBe("Replica 2");
	});

	test("the process travels in the socket and file URLs, and only when there is one", () => {
		const loc = { protocol: "https:", host: "cp.example.com" };
		expect(terminalSocketUrl("abc", loc, "worker")).toBe(
			"wss://cp.example.com/api/v1/services/abc/terminal/ws?process=worker",
		);
		expect(terminalSocketUrl("abc", loc, "")).toBe(
			"wss://cp.example.com/api/v1/services/abc/terminal/ws",
		);
		expect(fileContentUrl("s", 1, "/x", false, "worker")).toBe(
			"/api/v1/services/s/files/content?process=worker&replica=1&path=%2Fx",
		);
		expect(fileUploadUrl("s", 0, "/data", "in.txt", "worker")).toBe(
			"/api/v1/services/s/files/upload?process=worker&replica=0&path=%2Fdata&name=in.txt",
		);
		expect(processQuery("")).toEqual({});
	});

	test("the open message names the process only when there is one", () => {
		const base = {
			replica: 1,
			command: [],
			cols: 80,
			rows: 24,
			confirmProduction: false,
		};
		expect(buildOpenMessage({ ...base, process: "worker" })).toMatchObject({
			process: "worker",
			replica: 1,
		});
		expect(buildOpenMessage({ ...base, process: "" })).not.toHaveProperty(
			"process",
		);
		expect(buildOpenMessage(base)).not.toHaveProperty("process");
	});
});
