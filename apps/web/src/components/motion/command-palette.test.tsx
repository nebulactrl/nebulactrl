import { describe, expect, jest, test } from "bun:test";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { type ComponentProps, useState } from "react";
import { type CommandItem, CommandPalette } from "./command-palette";

type Props = ComponentProps<typeof CommandPalette>;

function item(
	id: string,
	label: string,
	extra: Partial<CommandItem> = {},
): CommandItem {
	return { id, label, onSelect: () => {}, ...extra } as CommandItem;
}

/** Lets the animation frame that focuses the field run. */
async function nextFrame() {
	await act(async () => {
		await new Promise((resolve) => requestAnimationFrame(resolve));
	});
}

const input = () => screen.getByRole("combobox");
const labels = () =>
	screen.getAllByRole("option").map((option) => option.textContent ?? "");
const active = () =>
	screen
		.getAllByRole("option")
		.findIndex((o) => o.getAttribute("aria-selected") === "true");

function ctrlK(key = "k", init: KeyboardEventInit = {}) {
	fireEvent.keyDown(window, { key, ctrlKey: true, ...init });
}

/** An uncontrolled palette that starts open. */
function openPalette(props: Partial<Props> = {}) {
	const view = render(<CommandPalette items={[]} {...props} />);
	ctrlK();
	return view;
}

const THREE = (log: string[] = []) => [
	item("deploy", "Deploy checkout", {
		group: "Action",
		onSelect: () => log.push("deploy"),
	}),
	item("servers", "Open servers", {
		group: "Go to",
		hint: "hosts",
		onSelect: () => log.push("servers"),
	}),
	item("deployments", "Open deployments", {
		group: "Go to",
		onSelect: () => log.push("deployments"),
	}),
];

describe("CommandPalette opening", () => {
	test("renders nothing while closed", () => {
		render(<CommandPalette items={THREE()} />);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("Ctrl+K opens it and Ctrl+K again closes it", () => {
		render(<CommandPalette items={THREE()} />);
		ctrlK();
		expect(
			screen.getByRole("dialog", { name: "Command palette" }),
		).toBeInTheDocument();
		ctrlK();
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("Cmd+K opens it too, and other keys do not", () => {
		render(<CommandPalette items={THREE()} />);
		fireEvent.keyDown(window, { key: "j", ctrlKey: true });
		fireEvent.keyDown(window, { key: "k" });
		expect(screen.queryByRole("dialog")).toBeNull();
		fireEvent.keyDown(window, { key: "K", metaKey: true });
		expect(screen.getByRole("dialog")).toBeInTheDocument();
	});

	test("the shortcut key is configurable", () => {
		render(<CommandPalette items={THREE()} shortcut="p" />);
		ctrlK("k");
		expect(screen.queryByRole("dialog")).toBeNull();
		ctrlK("p");
		expect(screen.getByRole("dialog")).toBeInTheDocument();
		expect(screen.getByText("toggle").previousElementSibling).toHaveTextContent(
			"p",
		);
	});

	test("Escape closes it", () => {
		openPalette({ items: THREE() });
		fireEvent.keyDown(window, { key: "Escape" });
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("Escape while closed does nothing and does not report a change", () => {
		const onOpenChange = jest.fn();
		render(<CommandPalette items={THREE()} onOpenChange={onOpenChange} />);
		fireEvent.keyDown(window, { key: "Escape" });
		expect(onOpenChange).not.toHaveBeenCalled();
	});

	test("onBeforeOpen runs before a closed palette opens, and onOpenChange reports it", () => {
		const order: string[] = [];
		render(
			<CommandPalette
				items={THREE()}
				onBeforeOpen={() => order.push("before")}
				onOpenChange={(open) => order.push(`change:${open}`)}
			/>,
		);
		ctrlK();
		expect(order).toEqual(["before", "change:true"]);
		ctrlK();
		expect(order).toEqual(["before", "change:true", "change:false"]);
	});

	test("controlled: the host decides, the palette only asks", () => {
		const onOpenChange = jest.fn();
		const { rerender } = render(
			<CommandPalette
				items={THREE()}
				open={false}
				onOpenChange={onOpenChange}
			/>,
		);
		ctrlK();
		expect(onOpenChange).toHaveBeenCalledWith(true);
		expect(screen.queryByRole("dialog")).toBeNull();
		rerender(
			<CommandPalette items={THREE()} open onOpenChange={onOpenChange} />,
		);
		expect(screen.getByRole("dialog")).toBeInTheDocument();
		fireEvent.keyDown(window, { key: "Escape" });
		expect(onOpenChange).toHaveBeenLastCalledWith(false);
		expect(screen.getByRole("dialog")).toBeInTheDocument();
	});

	test("clicking the scrim closes it", () => {
		openPalette({ items: THREE() });
		fireEvent.click(
			screen.getByRole("button", { name: "Close command palette" }),
		);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("page scrolling is locked while open and restored after", () => {
		document.body.style.overflow = "auto";
		openPalette({ items: THREE() });
		expect(document.body.style.overflow).toBe("hidden");
		expect(document.documentElement.style.overflow).toBe("hidden");
		ctrlK();
		expect(document.body.style.overflow).toBe("auto");
		expect(document.documentElement.style.overflow).toBe("");
		document.body.style.overflow = "";
	});

	test("focuses the search field when it opens", async () => {
		openPalette({ items: THREE() });
		await nextFrame();
		expect(input()).toHaveFocus();
	});

	test("the field carries the placeholder and combobox semantics", () => {
		openPalette({ items: THREE(), placeholder: "Find anything…" });
		expect(input()).toHaveAttribute("placeholder", "Find anything…");
		expect(input()).toHaveAccessibleName("Search commands");
		expect(input()).toHaveAttribute("aria-autocomplete", "list");
		const listbox = screen.getByRole("listbox", { name: "Commands" });
		expect(input().getAttribute("aria-controls")).toBe(listbox.id);
	});

	test("reopening starts again with an empty query", () => {
		openPalette({ items: THREE() });
		fireEvent.change(input(), { target: { value: "serv" } });
		expect(screen.getAllByRole("option")).toHaveLength(1);
		ctrlK();
		ctrlK();
		expect(input()).toHaveValue("");
		expect(screen.getAllByRole("option")).toHaveLength(3);
	});
});

const Icon = (props: { className?: string }) => (
	<svg data-testid="icon" {...props} />
);

describe("CommandPalette rows and search", () => {
	test("lists the items with their group and hint", () => {
		openPalette({ items: THREE() });
		const rows = screen.getAllByRole("option");
		expect(rows).toHaveLength(3);
		expect(
			within(rows[1] as HTMLElement).getByText("hosts"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByText("Go to"),
		).toBeInTheDocument();
		expect(
			within(rows[0] as HTMLElement).getByText("Action"),
		).toBeInTheDocument();
	});

	test("shows a shortcut keycap and a badge when a row has them", () => {
		openPalette({
			items: [item("a", "Deploy", { shortcut: "D", badge: <span>new</span> })],
		});
		const row = screen.getByRole("option");
		expect(within(row).getByText("D")).toBeInTheDocument();
		expect(within(row).getByText("new")).toBeInTheDocument();
	});

	test("typing filters fuzzily, best match first, and draws the matched letters", () => {
		openPalette({ items: THREE() });
		fireEvent.change(input(), { target: { value: "depl" } });
		const rows = screen.getAllByRole("option");
		expect(
			rows.map((row) => row.querySelector("span.truncate")?.textContent),
		).toEqual(["Deploy checkout", "Open deployments"]);
		const emphasised = [
			...(rows[0] as HTMLElement).querySelectorAll(".text-accent-ink"),
		].map((el) => el.textContent);
		expect(emphasised.join("")).toBe("Depl");
	});

	test("rows are found through their keywords and hints", () => {
		openPalette({
			items: [
				item("servers", "Open servers", { keywords: ["machines"] }),
				item("pages", "Open pages", { hint: "uptime" }),
			],
		});
		fireEvent.change(input(), { target: { value: "machines" } });
		expect(labels()).toEqual([expect.stringContaining("Open servers")]);
		fireEvent.change(input(), { target: { value: "uptime" } });
		expect(labels()).toEqual([expect.stringContaining("Open pages")]);
	});

	test("a query nothing matches says so, quoting it", () => {
		openPalette({ items: THREE() });
		fireEvent.change(input(), { target: { value: "zzzqq" } });
		expect(screen.getByText(/Nothing matches/)).toHaveTextContent(
			"Nothing matches “zzzqq”.",
		);
		expect(screen.queryByRole("option")).toBeNull();
		expect(input()).not.toHaveAttribute("aria-activedescendant");
	});

	test("an empty palette shows the empty message", () => {
		openPalette({ items: [], emptyMessage: "Nothing to jump to yet." });
		expect(screen.getByText("Nothing to jump to yet.")).toBeInTheDocument();
	});

	test("the empty prompt uses initialItems; typing searches all items", () => {
		const items = THREE();
		openPalette({ items, initialItems: [items[1] as CommandItem] });
		expect(labels()).toEqual([expect.stringContaining("Open servers")]);
		fireEvent.change(input(), { target: { value: "deploy" } });
		expect(screen.getAllByRole("option").length).toBeGreaterThan(1);
	});

	test("draws ten rows and counts the rest", () => {
		const many = Array.from({ length: 13 }, (_, i) =>
			item(`r${i}`, `Row ${i}`),
		);
		openPalette({ items: many });
		expect(screen.getAllByRole("option")).toHaveLength(10);
		expect(
			screen.getByText("3 more · keep typing to narrow"),
		).toBeInTheDocument();
	});

	test("rows that bring an icon reserve its column; plain rows do not", () => {
		openPalette({
			items: [item("a", "With", { icon: Icon as never }), item("b", "Without")],
		});
		expect(screen.getAllByTestId("icon")).toHaveLength(1);
		const without = screen.getAllByRole("option")[1] as HTMLElement;
		expect(without.querySelector("span.size-4")).not.toBeNull();
	});

	test("recent ids break ties between equally good matches", () => {
		const items = [item("a", "Open alpha"), item("b", "Open beta")];
		openPalette({ items, recent: ["b"] });
		fireEvent.change(input(), { target: { value: "open" } });
		expect(labels()[0]).toContain("Open beta");
	});
});

describe("CommandPalette keyboard", () => {
	test("the first row is active and Enter runs it and closes the palette", () => {
		const log: string[] = [];
		openPalette({ items: THREE(log) });
		expect(active()).toBe(0);
		fireEvent.keyDown(input(), { key: "Enter" });
		expect(log).toEqual(["deploy"]);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("arrow keys move the highlight, clamped at both ends, and Enter runs that row", () => {
		const log: string[] = [];
		openPalette({ items: THREE(log) });
		fireEvent.keyDown(input(), { key: "ArrowDown" });
		expect(active()).toBe(1);
		expect(input().getAttribute("aria-activedescendant")).toBe(
			screen.getAllByRole("option")[1]?.id ?? "",
		);
		fireEvent.keyDown(input(), { key: "ArrowDown" });
		fireEvent.keyDown(input(), { key: "ArrowDown" });
		expect(active()).toBe(2);
		fireEvent.keyDown(input(), { key: "ArrowUp" });
		fireEvent.keyDown(input(), { key: "ArrowUp" });
		fireEvent.keyDown(input(), { key: "ArrowUp" });
		expect(active()).toBe(0);
		fireEvent.keyDown(input(), { key: "ArrowDown" });
		fireEvent.keyDown(input(), { key: "Enter" });
		expect(log).toEqual(["servers"]);
	});

	test("typing returns the highlight to the first match", () => {
		openPalette({ items: THREE() });
		fireEvent.keyDown(input(), { key: "ArrowDown" });
		fireEvent.change(input(), { target: { value: "open" } });
		expect(active()).toBe(0);
	});

	test("Enter with no results does nothing", () => {
		const onOpenChange = jest.fn();
		render(<CommandPalette items={THREE()} onOpenChange={onOpenChange} />);
		ctrlK();
		fireEvent.change(input(), { target: { value: "zzzqq" } });
		onOpenChange.mockClear();
		fireEvent.keyDown(input(), { key: "Enter" });
		expect(onOpenChange).not.toHaveBeenCalled();
	});

	test("keys typed while an IME is composing are ignored", () => {
		const log: string[] = [];
		openPalette({ items: THREE(log) });
		fireEvent.keyDown(input(), { key: "Enter", isComposing: true });
		expect(log).toEqual([]);
		expect(screen.getByRole("dialog")).toBeInTheDocument();
	});

	test("hovering a row highlights it and clicking runs it", () => {
		const log: string[] = [];
		openPalette({ items: THREE(log) });
		const rows = screen.getAllByRole("option");
		fireEvent.mouseEnter(rows[2] as HTMLElement);
		expect(active()).toBe(2);
		fireEvent.click(rows[2] as HTMLElement);
		expect(log).toEqual(["deployments"]);
		expect(screen.queryByRole("dialog")).toBeNull();
	});
});

function withLevel(log: string[] = []) {
	return [
		item("plain", "Plain row", { onSelect: () => log.push("plain") }),
		{
			id: "checkout",
			label: "checkout",
			group: "Service",
			next: {
				id: "checkout-level",
				title: "checkout",
				placeholder: "Do something to checkout…",
				items: () => [
					item("restart", "Restart", { onSelect: () => log.push("restart") }),
					item("logs", "Open logs", { onSelect: () => log.push("logs") }),
				],
			},
		} as CommandItem,
		item("both", "Both", {
			onSelect: () => log.push("both"),
			next: {
				id: "both-level",
				title: "both",
				items: () => [item("inner", "Inner")],
			},
		}),
	];
}

/** A palette whose item list the page can swap out from under an open level. */
function Host() {
	const [items, setItems] = useState(withLevel());
	return (
		<>
			<button
				type="button"
				onClick={() => setItems([item("plain", "Plain row")])}
			>
				drop
			</button>
			<CommandPalette items={items} open onOpenChange={() => {}} />
		</>
	);
}

describe("CommandPalette levels", () => {
	test("Enter on a row that only opens a level descends into it", () => {
		const onLevelChange = jest.fn();
		openPalette({ items: withLevel(), onLevelChange });
		fireEvent.keyDown(input(), { key: "ArrowDown" });
		fireEvent.keyDown(input(), { key: "Enter" });
		expect(
			screen.getByRole("button", { name: "Back to checkout" }),
		).toBeInTheDocument();
		expect(input()).toHaveAttribute("placeholder", "Do something to checkout…");
		expect(input()).toHaveAccessibleName("Search checkout commands");
		expect(
			screen.getByRole("listbox", { name: "checkout" }),
		).toBeInTheDocument();
		expect(labels().map((l) => l.replace(/Service$/, ""))).toEqual([
			"Restart",
			"Open logs",
		]);
		expect(onLevelChange).toHaveBeenLastCalledWith(["checkout-level"]);
		expect(screen.getByText("back")).toBeInTheDocument();
	});

	test("a row that runs and opens a level runs on Enter; → opens the level", () => {
		const log: string[] = [];
		openPalette({ items: withLevel(log) });
		fireEvent.change(input(), { target: { value: "both" } });
		expect(screen.getByText("more")).toBeInTheDocument();
		fireEvent.keyDown(input(), { key: "ArrowRight" });
		expect(
			screen.getByRole("button", { name: "Back to both" }),
		).toBeInTheDocument();
		expect(log).toEqual([]);
	});

	test("→ with the caret short of the end moves the caret instead", () => {
		openPalette({ items: withLevel() });
		fireEvent.change(input(), { target: { value: "both" } });
		(input() as HTMLInputElement).setSelectionRange(1, 1);
		fireEvent.keyDown(input(), { key: "ArrowRight" });
		expect(screen.queryByRole("button", { name: "Back to both" })).toBeNull();
	});

	test("→ on a row with no level does nothing", () => {
		openPalette({ items: withLevel() });
		fireEvent.keyDown(input(), { key: "ArrowRight" });
		expect(screen.queryByRole("button", { name: /Back to/ })).toBeNull();
	});

	test("the row's chevron opens its level without running the row", () => {
		const log: string[] = [];
		openPalette({ items: withLevel(log) });
		fireEvent.click(screen.getByRole("button", { name: "Open both commands" }));
		expect(
			screen.getByRole("button", { name: "Back to both" }),
		).toBeInTheDocument();
		expect(log).toEqual([]);
	});

	test("Backspace on an empty prompt goes back; with text it edits the text", () => {
		openPalette({ items: withLevel() });
		fireEvent.click(
			screen.getByRole("button", { name: "Open checkout commands" }),
		);
		fireEvent.change(input(), { target: { value: "res" } });
		fireEvent.keyDown(input(), { key: "Backspace" });
		expect(
			screen.getByRole("button", { name: "Back to checkout" }),
		).toBeInTheDocument();
		fireEvent.change(input(), { target: { value: "" } });
		fireEvent.keyDown(input(), { key: "Backspace" });
		expect(screen.queryByRole("button", { name: /Back to/ })).toBeNull();
		expect(labels()).toHaveLength(3);
	});

	test("clicking the breadcrumb goes back to that level", () => {
		const onLevelChange = jest.fn();
		openPalette({ items: withLevel(), onLevelChange });
		fireEvent.click(
			screen.getByRole("button", { name: "Open checkout commands" }),
		);
		fireEvent.click(screen.getByRole("button", { name: "Back to checkout" }));
		expect(
			screen.getByRole("button", { name: "Back to checkout" }),
		).toBeInTheDocument();
		expect(onLevelChange).toHaveBeenLastCalledWith(["checkout-level"]);
	});

	test("choosing a row inside a level runs it and closes", () => {
		const log: string[] = [];
		openPalette({ items: withLevel(log) });
		fireEvent.click(
			screen.getByRole("button", { name: "Open checkout commands" }),
		);
		fireEvent.click(screen.getByRole("option", { name: /Open logs/ }));
		expect(log).toEqual(["logs"]);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("typing inside a level searches only that level", () => {
		openPalette({ items: withLevel() });
		fireEvent.click(
			screen.getByRole("button", { name: "Open checkout commands" }),
		);
		fireEvent.change(input(), { target: { value: "plain" } });
		expect(screen.queryByRole("option")).toBeNull();
		fireEvent.change(input(), { target: { value: "rest" } });
		expect(labels()).toEqual([expect.stringContaining("Restart")]);
	});

	test("onLevelChange reports [] once the palette closes", () => {
		const onLevelChange = jest.fn();
		openPalette({ items: withLevel(), onLevelChange });
		fireEvent.click(
			screen.getByRole("button", { name: "Open checkout commands" }),
		);
		ctrlK();
		expect(onLevelChange).toHaveBeenLastCalledWith([]);
	});

	test("a level whose row has left the list ends the path there", () => {
		render(<Host />);
		fireEvent.click(
			screen.getByRole("button", { name: "Open checkout commands" }),
		);
		expect(
			screen.getByRole("button", { name: "Back to checkout" }),
		).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "drop" }));
		expect(screen.queryByRole("button", { name: /Back to/ })).toBeNull();
		expect(labels()).toEqual([expect.stringContaining("Plain row")]);
	});
});

describe("CommandPalette queryItems", () => {
	const docs = (query: string): CommandItem[] => [
		item("docs:a", `Docs for ${query}`, { group: "Docs" }),
		item("docs:pinned", "Pinned doc", { group: "Docs", pin: true }),
		item("deploy", "Duplicate of a command"),
	];

	test("are not asked for on the empty prompt", () => {
		const queryItems = jest.fn(docs);
		openPalette({ items: THREE(), queryItems });
		expect(queryItems).not.toHaveBeenCalled();
	});

	test("pinned rows lead, matched commands follow, the rest trail, duplicates dropped", () => {
		openPalette({ items: THREE(), queryItems: docs });
		fireEvent.change(input(), { target: { value: "deploy" } });
		const rows = screen
			.getAllByRole("option")
			.map((row) => row.querySelector("span.truncate")?.textContent);
		expect(rows[0]).toBe("Pinned doc");
		expect(rows).toContain("Deploy checkout");
		expect(rows.at(-1)).toBe("Docs for deploy");
		// "deploy" is a real command id, so the query row with that id is not drawn twice.
		expect(
			rows.filter((label) => label === "Duplicate of a command"),
		).toHaveLength(0);
	});

	test("are not mixed into a nested level", () => {
		const queryItems = jest.fn(docs);
		openPalette({
			items: [
				{
					id: "svc",
					label: "svc",
					next: {
						id: "svc-level",
						title: "svc",
						items: () => [item("restart", "Restart")],
					},
				} as CommandItem,
			],
			queryItems,
		});
		fireEvent.click(screen.getByRole("button", { name: "Open svc commands" }));
		fireEvent.change(input(), { target: { value: "rest" } });
		expect(queryItems).not.toHaveBeenCalled();
		expect(labels()).toEqual([expect.stringContaining("Restart")]);
	});
});

describe("CommandPalette touch", () => {
	test("a touch device gets no key hints and a 16px field", () => {
		const real = Object.getOwnPropertyDescriptor(
			Navigator.prototype,
			"maxTouchPoints",
		);
		Object.defineProperty(Navigator.prototype, "maxTouchPoints", {
			configurable: true,
			get: () => 5,
		});
		try {
			openPalette({ items: THREE() });
			expect(screen.queryByText("navigate")).toBeNull();
			expect(screen.queryByText("esc")).toBeNull();
			expect(input()).toHaveClass("text-base");
		} finally {
			if (real)
				Object.defineProperty(Navigator.prototype, "maxTouchPoints", real);
			else
				delete (Navigator.prototype as unknown as Record<string, unknown>)
					.maxTouchPoints;
		}
	});

	test("a pointer device sees the key hints", () => {
		openPalette({ items: THREE() });
		expect(screen.getByText("navigate")).toBeInTheDocument();
		expect(screen.getByText("esc")).toBeInTheDocument();
		expect(screen.getByText("open")).toBeInTheDocument();
	});
});
