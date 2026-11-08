import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import type { LogLine } from "#/api/sse";
import { FacetPanel } from "#/features/logs/search/facet-panel";
import { FacetStats } from "#/features/logs/search/facets";
import { parseLine } from "#/features/logs/search/line";
import { LineDetail } from "./line-detail";
import { RequestLine } from "./request-line";
import { controlSegments, escapeControls, SafeText } from "./safe-text";

const RLO = "\u202E";

describe("escapeControls", () => {
	test("writes bidi, C0 and C1 controls as escapes and leaves other text alone", () => {
		expect(escapeControls("/a\u202Eb")).toBe("/a\\u202Eb");
		expect(escapeControls("x\u2066y\u2069\u200E\u200F")).toBe(
			"x\\u2066y\\u2069\\u200E\\u200F",
		);
		expect(escapeControls("a\u0000b\u001Bc\u007Fd\u0085e")).toBe(
			"a\\u0000b\\u001Bc\\u007Fd\\u0085e",
		);
		expect(escapeControls("/api/orders é 日本")).toBe("/api/orders é 日本");
	});
});

describe("controlSegments", () => {
	test("cuts around each control and keeps the offsets of the text", () => {
		expect(controlSegments(`ab${RLO}cd\u0007e`)).toEqual([
			{ start: 0, end: 2, control: false },
			{ start: 2, end: 3, control: true },
			{ start: 3, end: 5, control: false },
			{ start: 5, end: 6, control: true },
			{ start: 6, end: 7, control: false },
		]);
	});

	test("text without controls is one plain stretch, and no text is none", () => {
		expect(controlSegments("plain")).toEqual([
			{ start: 0, end: 5, control: false },
		]);
		expect(controlSegments("")).toEqual([]);
	});

	test("keepTabs leaves a tab inside its stretch and still cuts the rest", () => {
		expect(controlSegments(`a\tb${RLO}`, { keepTabs: true })).toEqual([
			{ start: 0, end: 3, control: false },
			{ start: 3, end: 4, control: true },
		]);
		expect(controlSegments("a\tb")).toHaveLength(3);
	});
});

describe("SafeText", () => {
	test("shows an escape in a muted style instead of the character", () => {
		const { container } = render(<SafeText text={`/a${RLO}b`} />);
		expect(container.textContent).toBe("/a\\u202Eb");
		expect(container.textContent).not.toContain(RLO);
		expect(screen.getByText("\\u202E")).toHaveClass("text-ink-4");
	});

	test("layout keeps line breaks and tabs but still escapes the rest", () => {
		const { container } = render(<SafeText layout text={`a\nb\t${RLO}`} />);
		expect(container.textContent).toBe("a\nb\t\\u202E");
	});
});

describe("where client-controlled text is drawn", () => {
	test("a request row shows a path with U+202E as an escape, in the row and its tooltip", () => {
		const { container } = render(
			<RequestLine
				fields={{ method: "GET", host: "", path: `/evil${RLO}gnp.exe` }}
				terms={[]}
			/>,
		);
		expect(container.textContent).toContain("/evil\\u202Egnp.exe");
		expect(container.textContent).not.toContain(RLO);
		expect(screen.getByTitle("/evil\\u202Egnp.exe")).toBeInTheDocument();
	});

	const line: LogLine = {
		id: 1,
		ts: "2026-10-29T10:00:20.000Z",
		stream: "requests",
		serviceName: "api",
		line: JSON.stringify({
			method: "GET",
			host: "a.example.com",
			path: `/evil${RLO}gnp.exe`,
			status: 200,
			ua: `curl${RLO}`,
		}),
	};

	test("the detail pane escapes the summary, the fields and the raw line, and copies the original", () => {
		render(
			<LineDetail
				line={line}
				query=""
				onQueryChange={() => {}}
				onClose={() => {}}
			/>,
		);
		const pane = screen.getByRole("complementary", { name: "Log line detail" });
		expect(pane.textContent).toContain("/evil\\u202Egnp.exe");
		expect(pane.textContent).toContain("curl\\u202E");
		expect(pane.textContent).not.toContain(RLO);
		expect(
			screen.getByRole("button", { name: "Filter @path:/evil\\u202Egnp.exe" }),
		).toBeInTheDocument();
		// The copy button still holds the raw line.
		expect(
			screen.getByRole("button", { name: "Copy raw line" }),
		).toBeInTheDocument();
	});

	test("facet values are escaped", () => {
		const stats = new FacetStats();
		stats.add(
			parseLine({
				line: JSON.stringify({ method: "GET", path: `/x${RLO}y`, kind: "api" }),
			}),
		);
		const { container } = render(
			<FacetPanel
				stats={stats}
				statsVersion={1}
				keys={["level"]}
				query=""
				onQueryChange={() => {}}
				requests
			/>,
		);
		expect(container.textContent).toContain("/x\\u202Ey");
		expect(container.textContent).not.toContain(RLO);
	});
});
