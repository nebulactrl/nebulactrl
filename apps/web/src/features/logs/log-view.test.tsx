import { afterEach, beforeEach, describe, expect, mock, test } from "bun:test";
import { fireEvent, render, screen, within } from "@testing-library/react";
import type { LogLine } from "#/api/sse";
import { LogView } from "./log-view";
import { stubLayout } from "./test-layout";

let restoreLayout: () => void;
beforeEach(() => {
	restoreLayout = stubLayout();
});
afterEach(() => restoreLayout());

const line = (
	id: number,
	text: string,
	extra: Partial<LogLine> = {},
): LogLine => ({
	id,
	ts: "2026-10-20T14:02:11.000Z",
	line: text,
	...extra,
});

function view(props: Partial<Parameters<typeof LogView>[0]> = {}) {
	const onLetGo = mock();
	const utils = render(
		<LogView
			lines={[]}
			follow={false}
			onLetGo={onLetGo}
			label="Runtime log"
			empty="Nothing here."
			{...props}
		/>,
	);
	return { onLetGo, ...utils };
}

const log = () => screen.getByRole("log", { name: "Runtime log" });

describe("LogView", () => {
	test("with no lines it shows the empty state", () => {
		view({ empty: <span>No lines in this range.</span> });
		expect(log()).toHaveTextContent("No lines in this range.");
	});

	test("draws the lines as text, whole and in order", () => {
		view({ lines: [line(1, "first line"), line(2, "second line")] });
		expect(log()).toHaveTextContent("first line");
		expect(log()).toHaveTextContent("second line");
		expect(screen.queryByText("Nothing here.")).not.toBeInTheDocument();
		expect(log().textContent?.indexOf("first line")).toBeLessThan(
			log().textContent?.indexOf("second line") ?? 0,
		);
	});

	test("strips colour escapes from a line", () => {
		view({ lines: [line(1, "\u001b[31mred alert\u001b[0m")] });
		expect(log()).toHaveTextContent("red alert");
		expect(log().textContent).not.toContain("\u001b");
	});

	test("each row starts with the time of the line", () => {
		view({ lines: [line(1, "hello", { ts: "2026-10-20T14:02:11.000Z" })] });
		expect(log().textContent).toMatch(/\d{2}:\d{2}:\d{2}/);
	});

	test("a line without a readable time has no time", () => {
		view({
			lines: [
				line(1, "hello", { ts: "garbage" }),
				line(2, "again", { ts: "" }),
			],
		});
		expect(log().textContent).not.toMatch(/\d{2}:\d{2}:\d{2}/);
	});

	test("showDate puts the day before the time", () => {
		view({
			showDate: true,
			lines: [line(1, "hello", { ts: "2026-10-20T14:02:11.000Z" })],
		});
		expect(log().textContent).toMatch(/\d{1,2} Oct \d{2}:\d{2}:\d{2}/);
	});

	test("showLevel names the level a line states and leaves other lines blank", () => {
		view({
			showLevel: true,
			lines: [line(1, "ERROR disk full"), line(2, "just words")],
		});
		const rows = within(log()).getAllByText(/disk full|just words/);
		expect(rows).toHaveLength(2);
		expect(screen.getAllByText("ERROR").length).toBeGreaterThan(0);
		expect(screen.queryByText("WARN")).not.toBeInTheDocument();
	});

	test("showService names the service, with a fallback", () => {
		view({
			showService: true,
			serviceIcon: (l) => <i data-testid="icon">{l.id}</i>,
			lines: [line(1, "a", { serviceName: "api" }), line(2, "b")],
		});
		expect(screen.getByText("api")).toBeInTheDocument();
		expect(screen.getByText("service")).toBeInTheDocument();
		expect(screen.getAllByTestId("icon")).toHaveLength(2);
	});

	test("showStream tags the rows that have a stream", () => {
		view({
			showStream: true,
			lines: [line(1, "compiling", { stream: "build" }), line(2, "plain")],
		});
		expect(screen.getByText("build")).toBeInTheDocument();
	});

	test("marks the search terms inside a line", () => {
		view({
			terms: [{ parts: ["disk"] }],
			lines: [line(1, "ERROR disk full")],
		});
		const marks = [...log().querySelectorAll("mark")].map((m) => m.textContent);
		expect(marks).toEqual(["disk"]);
	});

	test("is a keyboard-reachable log region", () => {
		view({ lines: [line(1, "x")] });
		expect(log()).toHaveAttribute("tabindex", "0");
	});

	test("scrolling up by hand lets go of the tail; scrolling down does not", () => {
		const { onLetGo } = view({ lines: [line(1, "x")] });
		fireEvent.wheel(log(), { deltaY: 40 });
		expect(onLetGo).not.toHaveBeenCalled();
		fireEvent.wheel(log(), { deltaY: -40 });
		expect(onLetGo).toHaveBeenCalledTimes(1);
	});

	test("touch moves and the keys that go up let go; other keys do not", () => {
		const { onLetGo } = view({ lines: [line(1, "x")] });
		fireEvent.keyDown(log(), { key: "ArrowDown" });
		fireEvent.keyDown(log(), { key: "Enter" });
		expect(onLetGo).not.toHaveBeenCalled();
		for (const key of ["ArrowUp", "PageUp", "Home"]) {
			fireEvent.keyDown(log(), { key });
		}
		expect(onLetGo).toHaveBeenCalledTimes(3);
		fireEvent.touchMove(log());
		expect(onLetGo).toHaveBeenCalledTimes(4);
	});

	test("pressing the scrollbar (the region itself) lets go, pressing a line does not", () => {
		const { onLetGo } = view({ lines: [line(1, "a line")] });
		fireEvent.pointerDown(screen.getByText("a line"));
		expect(onLetGo).not.toHaveBeenCalled();
		fireEvent.pointerDown(log());
		expect(onLetGo).toHaveBeenCalledTimes(1);
	});
});
