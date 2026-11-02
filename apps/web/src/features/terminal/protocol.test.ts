import { describe, expect, test } from "bun:test";
import {
	describeEnd,
	errorTitle,
	fileContentUrl,
	fileUploadUrl,
	idleCountdown,
	parseServerMessage,
	terminalSocketUrl,
} from "./protocol";

describe("process-aware URLs", () => {
	test("the socket URL carries the process only when there is one", () => {
		const loc = { protocol: "https:", host: "cp.example.com" };
		expect(terminalSocketUrl("svc", loc, "worker")).toBe(
			"wss://cp.example.com/api/v1/services/svc/terminal/ws?process=worker",
		);
		expect(terminalSocketUrl("svc", loc, "")).toBe(
			"wss://cp.example.com/api/v1/services/svc/terminal/ws",
		);
		expect(terminalSocketUrl("a b", loc)).toContain("/services/a%20b/");
	});

	test("file URLs carry the process, replica and path", () => {
		expect(fileContentUrl("s", 2, "/data/a.txt", false, "worker")).toBe(
			"/api/v1/services/s/files/content?process=worker&replica=2&path=%2Fdata%2Fa.txt",
		);
		expect(fileUploadUrl("s", 1, "/tmp", "a b.txt", "worker")).toBe(
			"/api/v1/services/s/files/upload?process=worker&replica=1&path=%2Ftmp&name=a+b.txt",
		);
	});
});

describe("parseServerMessage defaults", () => {
	test("an opened message without fields falls back to empty values", () => {
		expect(parseServerMessage('{"type":"opened"}')).toEqual({
			type: "opened",
			replica: 0,
			container: "",
			command: [],
		});
	});

	test("the command of an opened message is read as strings", () => {
		expect(
			parseServerMessage('{"type":"opened","command":["sh",1]}'),
		).toMatchObject({ command: ["sh", "1"] });
	});

	test("an exit without a code means 0", () => {
		expect(parseServerMessage('{"type":"exit"}')).toEqual({
			type: "exit",
			exitCode: 0,
		});
	});

	test("an idle warning reads the seconds left, 0 when missing", () => {
		expect(
			parseServerMessage('{"type":"idle_warning","secondsLeft":42}'),
		).toEqual({ type: "idle_warning", secondsLeft: 42 });
		expect(parseServerMessage('{"type":"idle_warning"}')).toEqual({
			type: "idle_warning",
			secondsLeft: 0,
		});
	});

	test("an error without code or message gets a generic code and no text", () => {
		expect(parseServerMessage('{"type":"error"}')).toEqual({
			type: "error",
			code: "error",
			message: "",
		});
	});

	test("a closed message carries its reason, empty when missing", () => {
		expect(parseServerMessage('{"type":"closed","reason":"idle"}')).toEqual({
			type: "closed",
			reason: "idle",
		});
		expect(parseServerMessage('{"type":"closed"}')).toEqual({
			type: "closed",
			reason: "",
		});
	});

	test("null, an object without a type and a non-string type are ignored", () => {
		expect(parseServerMessage("null")).toBeNull();
		expect(parseServerMessage("{}")).toBeNull();
		expect(parseServerMessage('{"type":7}')).toBeNull();
	});
});

describe("error titles", () => {
	test.each([
		["too_many_sessions", "Too many terminals open"],
		["server_offline", "The server is offline"],
		["not_deployed", "Not deployed yet"],
		["replica_not_running", "That replica is not running"],
		["open_failed", "The shell could not start"],
		["connection_lost", "Connection lost"],
		["forbidden", "Not allowed"],
		["something_new", "The terminal could not open"],
	])("%s reads as %s", (code, title) => {
		expect(errorTitle(code)).toBe(title);
	});
});

describe("end descriptions", () => {
	test("each end reason has its own sentence", () => {
		expect(describeEnd("exit", null)).toBe("The shell exited.");
		expect(describeEnd("client_closed", null)).toBe("Session closed.");
		expect(describeEnd("error", null)).toBe("The session ended with an error.");
	});

	test("an unknown reason reports the exit code when there is one", () => {
		expect(describeEnd("weird", null)).toBe("Session ended.");
		expect(describeEnd(null, 137)).toBe("The shell exited with code 137.");
		expect(describeEnd(null, null)).toBe("Session ended.");
	});
});

describe("idleCountdown", () => {
	test("a minute or more rounds to whole minutes", () => {
		expect(idleCountdown(90)).toBe("2 minutes");
		expect(idleCountdown(120)).toBe("2 minutes");
		expect(idleCountdown(59.6)).toBe("1 minute");
	});

	test("never shows a negative time", () => {
		expect(idleCountdown(-5)).toBe("0 seconds");
		expect(idleCountdown(0)).toBe("0 seconds");
	});
});
