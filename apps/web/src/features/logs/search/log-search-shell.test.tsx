import { afterEach, describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import type { BufferLine } from "./buffer";
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
