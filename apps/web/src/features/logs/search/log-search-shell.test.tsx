import { afterEach, beforeEach, describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import type { BufferLine } from "./buffer";
import { FacetStats } from "./facets";
import { LogSearchShell, NoMatches } from "./log-search-shell";
import { useLogSearch } from "./use-log-search";

const lines: BufferLine[] = [
	{ id: 1, line: "INFO server started", serviceName: "api" },
	{ id: 2, line: "ERROR disk full", serviceName: "api" },
	{ id: 3, line: "ERROR timeout", serviceName: "web" },
];

function Harness({
	data = lines,
	footer,
}: {
	data?: BufferLine[];
	footer?: string;
}) {
	const search = useLogSearch({ lines: data });
	return (
		<LogSearchShell
			search={search}
			facetKeys={["level", "service"]}
			label="Search logs"
			toolbar={<button type="button">Pause</button>}
			{...(footer === undefined ? {} : { footer })}
		>
			<ul aria-label="window">
				{search.visible.map((l) => (
					<li key={l.id}>{l.line}</li>
				))}
			</ul>
		</LogSearchShell>
	);
}

const countsOf = (method: string) => ({
	stats: {
		facets: new FacetStats().facets,
		attrs: new Map([
			["method", { count: 3, numeric: 0, values: new Map([[method, 3]]) }],
		]),
	},
	version: 1,
});

function Scoped({
	onCaretChange,
}: {
	onCaretChange(caret: number | null): void;
}) {
	const search = useLogSearch({ lines });
	return (
		<LogSearchShell
			search={search}
			facetKeys={["level"]}
			label="Search logs"
			facetSource={countsOf("PUT")}
			suggestSource={countsOf("PATCH")}
			onCaretChange={onCaretChange}
		>
			<ul aria-label="window" />
		</LogSearchShell>
	);
}

function Served() {
	const search = useLogSearch({ lines });
	return (
		<LogSearchShell
			search={search}
			facetKeys={["level", "service"]}
			label="Search logs"
			placeholder="Find lines"
			beforeBody={<figure>chart</figure>}
			facetSource={{
				stats: {
					facets: {
						level: new Map([["fatal", 41]]),
						service: new Map(),
						stream: new Map(),
						replica: new Map(),
						status: new Map(),
						deployment: new Map(),
					},
					attrs: new Map([
						["method", { count: 9, numeric: 0, values: new Map([["PUT", 9]]) }],
					]),
				},
				version: 1,
			}}
		>
			<ul aria-label="window" />
		</LogSearchShell>
	);
}

const realWidth = Object.getOwnPropertyDescriptor(
	HTMLElement.prototype,
	"clientWidth",
);
function setWidth(width: number) {
	Object.defineProperty(HTMLElement.prototype, "clientWidth", {
		configurable: true,
		get: () => width,
	});
}
// The search asks who is signed in to key its recent queries; nothing here
// depends on the answer, so it never comes.
beforeEach(() => {
	stubApi({
		"GET /me": () => new Promise<Response>(() => {}),
		"GET /teams": () => new Promise<Response>(() => {}),
	});
});
afterEach(() => {
	if (realWidth)
		Object.defineProperty(HTMLElement.prototype, "clientWidth", realWidth);
	else Reflect.deleteProperty(HTMLElement.prototype, "clientWidth");
	localStorage.clear();
});

const items = () =>
	within(screen.getByRole("list", { name: "window" }))
		.getAllByRole("listitem")
		.map((li) => li.textContent);

describe("LogSearchShell", () => {
	test("shows the box, the toolbar, the window and the line count with its scope note", async () => {
		await renderWithProviders(<Harness />);
		expect(
			screen.getByRole("combobox", { name: "Search logs" }),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Pause" })).toBeInTheDocument();
		expect(items()).toHaveLength(3);
		expect(screen.getByText(/3 lines/, { selector: "p" })).toHaveTextContent(
			"Searching the last 3 lines. Older lines are not kept.",
		);
	});

	test("a single line is counted in the singular", async () => {
		await renderWithProviders(<Harness data={[lines[0] as BufferLine]} />);
		expect(screen.getByText(/1 line/, { selector: "p" })).toHaveTextContent(
			"1 line · Searching the only line.",
		);
	});

	test("while searching the footer says how many lines match", async () => {
		await renderWithProviders(<Harness />, { path: "/?q=error" });
		expect(
			screen.getByText(/2 of 3 lines/, { selector: "p" }),
		).toBeInTheDocument();
		expect(items()).toEqual(["ERROR disk full", "ERROR timeout"]);
	});

	test("the first problem in the query is shown under the box", async () => {
		await renderWithProviders(<Harness />, { path: "/?q=status:>" });
		const box = screen.getByRole("combobox", { name: "Search logs" });
		await waitFor(() => expect(box).toHaveAttribute("aria-invalid", "true"));
		expect(box.getAttribute("aria-describedby")).toBeTruthy();
	});

	test("a custom footer replaces the count", async () => {
		await renderWithProviders(
			<Harness footer="Showing 500 lines from history." />,
		);
		expect(
			screen.getByText("Showing 500 lines from history."),
		).toBeInTheDocument();
		expect(screen.queryByText(/Searching the last/)).not.toBeInTheDocument();
	});

	test("typing narrows the window and announces the result politely", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await user.type(
			screen.getByRole("combobox", { name: "Search logs" }),
			"disk",
		);
		await waitFor(() => expect(items()).toEqual(["ERROR disk full"]));
		await waitFor(
			() =>
				expect(screen.getByRole("status")).toHaveTextContent(
					"1 of 3 lines match",
				),
			{ timeout: 3000 },
		);
	});

	test("a narrow view offers the facets in a popover", async () => {
		const user = userEvent.setup();
		setWidth(600);
		await renderWithProviders(<Harness />);
		await user.click(screen.getByRole("button", { name: "Facets" }));
		const include = await screen.findByRole("button", {
			name: "Include level:error",
		});
		await user.click(include);
		await waitFor(() =>
			expect(items()).toEqual(["ERROR disk full", "ERROR timeout"]),
		);
		expect(screen.getByRole("combobox", { name: "Search logs" })).toHaveValue(
			"level:error",
		);
	});

	test("a wide view docks the facet panel and has no Facets button", async () => {
		const user = userEvent.setup();
		setWidth(1200);
		await renderWithProviders(<Harness />);
		expect(
			screen.queryByRole("button", { name: "Facets" }),
		).not.toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: "Exclude service:web" }),
		);
		await waitFor(() =>
			expect(items()).toEqual(["INFO server started", "ERROR disk full"]),
		);
	});

	test("server counts replace the buffer's in the facets and the suggestions; the placeholder and a row above the body are the caller's", async () => {
		const user = userEvent.setup();
		setWidth(1200);
		await renderWithProviders(<Served />);
		const box = screen.getByRole("combobox", { name: "Search logs" });
		expect(box).toHaveAttribute("placeholder", "Find lines");
		expect(screen.getByRole("figure")).toHaveTextContent("chart");
		const level = screen.getByRole("region", { name: "Level" });
		expect(within(level).getByText("fatal")).toBeInTheDocument();
		expect(within(level).getByText("41")).toBeInTheDocument();
		expect(within(level).queryByText("error")).toBeNull();
		await user.type(box, "@method:");
		expect(
			await screen.findByRole("option", { name: /PUT/ }),
		).toBeInTheDocument();
	});

	test("the suggestions count over their own source and the caret is reported", async () => {
		const user = userEvent.setup();
		setWidth(1200);
		const onCaretChange = mock();
		await renderWithProviders(<Scoped onCaretChange={onCaretChange} />);
		const box = screen.getByRole("combobox", { name: "Search logs" });
		await user.type(box, "@method:");
		expect(
			await screen.findByRole("option", { name: /PATCH/ }),
		).toBeInTheDocument();
		expect(screen.queryByRole("option", { name: /PUT/ })).toBeNull();
		expect(onCaretChange).toHaveBeenLastCalledWith(8);
	});
});

describe("NoMatches", () => {
	test("names the query and where it looked, and offers to clear", async () => {
		const user = userEvent.setup();
		const onClear = mock();
		await renderWithProviders(
			<NoMatches query="  boom  " where=" in this range" onClear={onClear} />,
		);
		expect(screen.getByText("boom")).toBeInTheDocument();
		expect(screen.getByText(/No lines match/)).toHaveTextContent(
			"No lines match boom in this range.",
		);
		await user.click(screen.getByRole("button", { name: "Clear search" }));
		expect(onClear).toHaveBeenCalledTimes(1);
	});
});
