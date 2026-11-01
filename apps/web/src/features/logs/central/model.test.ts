import { describe, expect, test } from "bun:test";
import type { CentralLogPage } from "#/api/gen/types.gen";
import {
	CENTRAL_LIMIT,
	CENTRAL_TAB_LABEL,
	centralErrorText,
	centralFooter,
	centralLines,
	centralServers,
	centralState,
} from "./model";

const line = (ts: string, server: string, text: string) => ({
	ts,
	server,
	stream: "stdout" as const,
	line: text,
});

const page = (over: Partial<CentralLogPage> = {}): CentralLogPage => ({
	enabled: true,
	hint: "",
	lines: [
		line("2026-10-30T10:00:02Z", "web-02", "third"),
		line("2026-10-30T10:00:01Z", "web-01", "second"),
		line("2026-10-30T10:00:00Z", "web-02", "first"),
	],
	...over,
});

describe("centralLines", () => {
	test("reverses the newest-first answer so the newest line is last", () => {
		const lines = centralLines(page());
		expect(lines.map((l) => l.line)).toEqual(["first", "second", "third"]);
		expect(lines.map((l) => l.id)).toEqual([0, 1, 2]);
		expect(lines[0]?.ts).toBe("2026-10-30T10:00:00Z");
		expect(lines[0]?.stream).toBe("stdout");
	});

	test("no page is no lines", () => {
		expect(centralLines(undefined)).toEqual([]);
		expect(centralLines(page({ lines: [] }))).toEqual([]);
	});
});

describe("servers and footer", () => {
	test("each server once, in the order they appear", () => {
		expect(centralServers(page())).toEqual(["web-02", "web-01"]);
	});

	test("the footer names them, singular or plural", () => {
		expect(centralFooter(page())).toBe("From servers web-02, web-01.");
		expect(
			centralFooter(
				page({ lines: [line("2026-10-30T10:00:00Z", "web-01", "x")] }),
			),
		).toBe("From server web-01.");
		expect(centralFooter(page({ lines: [] }))).toBe(
			"Lines from every server this service runs on.",
		);
	});
});

describe("centralState", () => {
	test("while forwarding is off the hint is shown instead of lines", () => {
		expect(
			centralState(page({ enabled: false, hint: "Turn it on.", lines: [] })),
		).toEqual({ kind: "off", hint: "Turn it on." });
	});

	test("an empty hint still says how to turn it on", () => {
		const state = centralState(page({ enabled: false, hint: "", lines: [] }));
		expect(state.kind).toBe("off");
		expect(state.kind === "off" && state.hint).toMatch(/Log forwarding/);
	});

	test("on with nothing in the window is empty", () => {
		expect(centralState(page({ lines: [] }))).toEqual({ kind: "empty" });
	});

	test("a full page says there may be older lines", () => {
		expect(centralState(page())).toEqual({ kind: "lines", truncated: false });
		const many = Array.from({ length: CENTRAL_LIMIT }, (_, i) =>
			line("2026-10-30T10:00:00Z", "web-01", `l${i}`),
		);
		expect(centralState(page({ lines: many }))).toEqual({
			kind: "lines",
			truncated: true,
		});
	});
});

test("the tab says how long lines are kept", () => {
	expect(CENTRAL_TAB_LABEL).toBe("Central logs (7 days)");
});

describe("centralErrorText", () => {
	test("a missing route reads as unavailable on this control plane", () => {
		expect(centralErrorText(404, "route not found")).toBe(
			"Central logs aren't available on this control plane.",
		);
	});

	test("any other failure keeps its own message", () => {
		expect(centralErrorText(500, "log store is down")).toBe(
			"log store is down",
		);
		expect(centralErrorText(undefined, "Something went wrong.")).toBe(
			"Something went wrong.",
		);
	});
});
