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

	test("a runtime line shows its control and bidi characters as escapes and still marks a word after them", () => {
		view({
			terms: [{ parts: ["disk"] }],
			lines: [line(1, "ERROR \u202Edisk\u0007 full")],
		});
		expect(log().textContent).toContain("\\u202E");
		expect(log().textContent).toContain("\\u0007");
		expect(log().textContent).not.toContain("\u202E");
		expect(log().textContent).not.toContain("\u0007");
		expect(screen.getByText("\\u202E")).toHaveClass("text-ink-4");
		const marks = [...log().querySelectorAll("mark")].map((m) => m.textContent);
		expect(marks).toEqual(["disk"]);
	});

	test("a runtime line marks the word after a control at its own place and keeps tabs", () => {
		view({
			terms: [{ parts: ["full"] }],
			lines: [line(1, "a\u202Eb\tdisk full")],
		});
		expect(log().textContent).toContain("a\\u202Eb\tdisk full");
		const marks = [...log().querySelectorAll("mark")].map((m) => m.textContent);
		expect(marks).toEqual(["full"]);
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

	test("a request line is drawn as method, status, host and path, and duration", () => {
		view({
			showLevel: true,
			lines: [
				line(
					1,
					JSON.stringify({
						method: "GET",
						host: "shop.example.com",
						path: "/cart",
						status: 404,
						duration_ms: 12,
					}),
					{ stream: "requests" },
				),
			],
		});
		expect(within(log()).getByText("GET")).toBeInTheDocument();
		expect(within(log()).getByText("404")).toHaveAttribute("data-tone", "warn");
		expect(within(log()).getByText("shop.example.com")).toBeInTheDocument();
		expect(within(log()).getByText("/cart")).toBeInTheDocument();
		expect(within(log()).getByText("12 ms")).toBeInTheDocument();
		expect(log().textContent).not.toContain("{");
	});

	test("a runtime line of JSON stays text, and a request record without a path too", () => {
		view({
			lines: [
				line(1, '{"method":"GET","path":"/x","status":200}'),
				line(2, '{"method":"GET"}', { stream: "requests" }),
			],
		});
		expect(log()).toHaveTextContent(
			'{"method":"GET","path":"/x","status":200}',
		);
		expect(log()).toHaveTextContent('{"method":"GET"}');
		expect(within(log()).queryByText("200")).toBeNull();
	});

	test("without onSelect the rows are not buttons", () => {
		view({ lines: [line(1, "plain")] });
		expect(within(log()).queryByRole("button")).toBeNull();
	});

	test("with onSelect a click or Enter or Space picks the line, and the picked row is marked", () => {
		const onSelect = mock();
		const lines = [line(1, "first"), line(2, "second")];
		const { rerender } = view({ lines, onSelect });
		const rows = within(log()).getAllByRole("button");
		fireEvent.click(rows[0] as HTMLElement);
		expect(onSelect).toHaveBeenLastCalledWith(lines[0]);
		fireEvent.keyDown(rows[1] as HTMLElement, { key: "Enter" });
		expect(onSelect).toHaveBeenLastCalledWith(lines[1]);
		fireEvent.keyDown(rows[1] as HTMLElement, { key: " " });
		expect(onSelect).toHaveBeenCalledTimes(3);
		rerender(
			<LogView
				lines={lines}
				follow={false}
				onLetGo={() => undefined}
				label="Runtime log"
				empty="Nothing here."
				onSelect={onSelect}
				selected={lines[1] ?? null}
			/>,
		);
		const [one, two] = within(log()).getAllByRole("button");
		expect(one).toHaveAttribute("aria-pressed", "false");
		expect(two).toHaveAttribute("aria-pressed", "true");
	});

	test("the arrow keys move a roving tab stop through the rows", () => {
		view({
			lines: [line(1, "first"), line(2, "second"), line(3, "third")],
			onSelect: mock(),
		});
		const rows = () => within(log()).getAllByRole("button");
		expect(rows().map((row) => row.getAttribute("tabindex"))).toEqual([
			"-1",
			"-1",
			"-1",
		]);
		fireEvent.keyDown(log(), { key: "ArrowDown" });
		expect(rows()[0]).toHaveFocus();
		expect(rows()[0]).toHaveAttribute("tabindex", "0");
		fireEvent.keyDown(rows()[0] as HTMLElement, { key: "ArrowDown" });
		expect(rows()[1]).toHaveFocus();
		fireEvent.keyDown(rows()[1] as HTMLElement, { key: "End" });
		expect(rows()[2]).toHaveFocus();
		fireEvent.keyDown(rows()[2] as HTMLElement, { key: "ArrowUp" });
		expect(rows()[1]).toHaveFocus();
		fireEvent.keyDown(rows()[1] as HTMLElement, { key: "Home" });
		expect(rows()[0]).toHaveFocus();
	});

	test("moving between rows by key lets go of the tail, so following cannot scroll the focused row away", () => {
		const { onLetGo } = view({
			lines: [line(1, "first"), line(2, "second"), line(3, "third")],
			onSelect: mock(),
		});
		for (const key of ["ArrowDown", "PageDown", "End"]) {
			onLetGo.mockClear();
			fireEvent.keyDown(log(), { key });
			expect(onLetGo).toHaveBeenCalled();
		}
		// A key that is no navigation does not.
		onLetGo.mockClear();
		fireEvent.keyDown(log(), { key: "a" });
		expect(onLetGo).not.toHaveBeenCalled();
	});

	test("focusing a row lets go of the tail", () => {
		const { onLetGo } = view({
			lines: [line(1, "first"), line(2, "second")],
			onSelect: mock(),
		});
		fireEvent.focus(within(log()).getAllByRole("button")[1] as HTMLElement);
		expect(onLetGo).toHaveBeenCalledTimes(1);
	});

	test("the picked row is found by what the line says, not by object identity", () => {
		const before = [line(1, "first"), line(2, "second", { serviceId: "s1" })];
		const { rerender } = view({
			lines: before,
			onSelect: mock(),
			selected: before[1] ?? null,
		});
		// A history page reload rebuilds every line object and its id.
		const after = [line(11, "first"), line(12, "second", { serviceId: "s1" })];
		rerender(
			<LogView
				lines={after}
				follow={false}
				onLetGo={() => undefined}
				label="Runtime log"
				empty="Nothing here."
				onSelect={mock()}
				selected={before[1] ?? null}
			/>,
		);
		const [one, two] = within(log()).getAllByRole("button");
		expect(one).toHaveAttribute("aria-pressed", "false");
		expect(two).toHaveAttribute("aria-pressed", "true");
	});

	test("reports whether the list is within a row of its end each time it scrolls", () => {
		const onAtBottomChange = mock();
		view({ lines: [line(1, "x")], onAtBottomChange });
		const scrollTo = (top: number) => {
			Object.defineProperty(log(), "scrollHeight", {
				value: 1000,
				configurable: true,
			});
			Object.defineProperty(log(), "clientHeight", {
				value: 400,
				configurable: true,
			});
			log().scrollTop = top;
			fireEvent.scroll(log());
		};
		scrollTo(100);
		expect(onAtBottomChange).toHaveBeenLastCalledWith(false);
		scrollTo(590);
		expect(onAtBottomChange).toHaveBeenLastCalledWith(true);
		scrollTo(600);
		expect(onAtBottomChange).toHaveBeenLastCalledWith(true);
	});

	test("showHost off leaves the host out of request rows", () => {
		view({
			showHost: false,
			lines: [
				line(
					1,
					JSON.stringify({
						method: "GET",
						host: "shop.example.com",
						path: "/cart",
						status: 200,
					}),
					{ stream: "requests" },
				),
			],
		});
		expect(within(log()).queryByText("shop.example.com")).toBeNull();
		expect(
			within(log()).getByTitle("shop.example.com/cart"),
		).toBeInTheDocument();
	});
});
