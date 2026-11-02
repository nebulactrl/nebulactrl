import { afterEach, describe, expect, mock, test } from "bun:test";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";
import { VARIABLES_HINT } from "./settings-chrome";
import { SettingsPages } from "./settings-nav";
import {
	type CategoryId,
	categoriesFor,
	stagedCounts,
} from "./settings-nav-model";

const subject = { kind: "app", sourceType: "image", oneshot: false };
const categories = categoriesFor(subject);
const NO_STAGED = stagedCounts([]);

interface HarnessProps {
	initial?: CategoryId;
	edited?: CategoryId[];
	invalid?: CategoryId[];
	staged?: Partial<Record<CategoryId, number>>;
	onSelect?: (id: CategoryId) => void;
	banner?: ReactNode;
	footer?: ReactNode;
	children?: (active: CategoryId) => ReactNode;
}

/** SettingsPages with its own selected category, as the tab holds it. */
function Harness({
	initial = "general",
	edited = [],
	invalid = [],
	staged = {},
	onSelect,
	banner,
	footer,
	children,
}: HarnessProps) {
	const [active, setActive] = useState<CategoryId>(initial);
	return (
		<SettingsPages
			serviceId="svc-1"
			subject={subject}
			categories={categories}
			active={active}
			onSelect={(id) => {
				onSelect?.(id);
				setActive(id);
			}}
			edited={new Set(edited)}
			invalid={new Set(invalid)}
			staged={{ ...NO_STAGED, ...staged }}
			banner={banner}
			footer={footer}
		>
			{children?.(active) ?? <p>page of {active}</p>}
		</SettingsPages>
	);
}

const tab = (name: string) =>
	screen.getByRole("tab", { name: new RegExp(`^${name}`) });

describe("SettingsPages categories", () => {
	test("lists every category as a tab and selects the active one", () => {
		render(<Harness initial="deploy" />);
		expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(
			categories.map((c) => c.label),
		);
		expect(tab("Deploy")).toHaveAttribute("aria-selected", "true");
		expect(tab("General")).toHaveAttribute("aria-selected", "false");
		expect(screen.getByRole("tablist")).toHaveAccessibleName(
			"Settings categories",
		);
	});

	test("the page names the active category and is labelled by its tab", () => {
		render(<Harness initial="storage" />);
		expect(
			screen.getByRole("heading", { level: 2, name: "Storage" }),
		).toBeInTheDocument();
		expect(
			screen.getByText("Volumes and files written into the container."),
		).toBeInTheDocument();
		const panel = screen.getByRole("tabpanel");
		expect(panel).toHaveAccessibleName("Storage");
		expect(within(panel).getByText("page of storage")).toBeInTheDocument();
		expect(tab("Storage")).toHaveAttribute("aria-controls", panel.id);
	});

	test("clicking a category reports it and shows its page", async () => {
		const user = userEvent.setup();
		const onSelect = mock((_id: CategoryId) => {});
		render(<Harness onSelect={onSelect} />);
		await user.click(tab("Networking"));
		expect(onSelect.mock.calls).toEqual([["networking"]]);
		expect(tab("Networking")).toHaveAttribute("aria-selected", "true");
		expect(screen.getByText("page of networking")).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { level: 2, name: "Networking" }),
		).toBeInTheDocument();
	});

	test("the banner and the footer frame the page", () => {
		render(
			<Harness
				banner={<p>Read-only banner</p>}
				footer={<p>Save bar here</p>}
			/>,
		);
		expect(screen.getByText("Read-only banner")).toBeInTheDocument();
		expect(screen.getByText("Save bar here")).toBeInTheDocument();
		expect(
			document
				.querySelector("[data-tab='settings']")
				?.getAttribute("data-service-id"),
		).toBe("svc-1");
	});
});

describe("SettingsPages markers", () => {
	test("an edited category says so in words", () => {
		render(<Harness edited={["deploy"]} />);
		expect(within(tab("Deploy")).getByText("edited")).toBeInTheDocument();
		expect(within(tab("General")).queryByText("edited")).toBeNull();
	});

	test("an invalid category says it needs a fix and wins over edited", () => {
		render(<Harness edited={["general", "deploy"]} invalid={["general"]} />);
		expect(within(tab("General")).getByText("needs fix")).toBeInTheDocument();
		expect(within(tab("General")).queryByText("edited")).toBeNull();
		expect(within(tab("Deploy")).getByText("edited")).toBeInTheDocument();
	});

	test("staged counts show as a labelled badge, singular and plural", () => {
		render(<Harness staged={{ networking: 3, deploy: 1, general: 0 }} />);
		const networking = within(tab("Networking")).getByLabelText(
			"3 staged changes",
		);
		expect(networking).toHaveTextContent("3");
		const deploy = within(tab("Deploy")).getByLabelText("1 staged change");
		expect(deploy).toHaveTextContent("1");
		expect(within(tab("General")).queryByLabelText(/staged/)).toBeNull();
	});
});

describe("SettingsPages keyboard", () => {
	test("only the active tab is a tab stop", () => {
		render(<Harness initial="resources" />);
		for (const t of screen.getAllByRole("tab")) {
			expect(t).toHaveAttribute(
				"tabindex",
				t === tab("Resources") ? "0" : "-1",
			);
		}
	});

	test("arrows, Home and End move focus and wrap around", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		const last = screen.getAllByRole("tab").at(-1);
		await user.click(tab("General"));
		tab("General").focus();
		await user.keyboard("{ArrowRight}");
		expect(tab("Deploy")).toHaveFocus();
		await user.keyboard("{ArrowDown}");
		expect(tab("Networking")).toHaveFocus();
		await user.keyboard("{ArrowLeft}");
		expect(tab("Deploy")).toHaveFocus();
		await user.keyboard("{End}");
		expect(last).toHaveFocus();
		await user.keyboard("{ArrowRight}");
		expect(tab("General")).toHaveFocus();
		await user.keyboard("{ArrowUp}");
		expect(last).toHaveFocus();
		await user.keyboard("{Home}");
		expect(tab("General")).toHaveFocus();
		// Moving focus does not select; Enter does.
		expect(tab("General")).toHaveAttribute("aria-selected", "true");
		await user.keyboard("{ArrowDown}{Enter}");
		expect(tab("Deploy")).toHaveAttribute("aria-selected", "true");
	});

	test("focus becomes the roving tab stop", async () => {
		render(<Harness />);
		act(() => tab("Storage").focus());
		expect(tab("Storage")).toHaveAttribute("tabindex", "0");
		expect(tab("General")).toHaveAttribute("tabindex", "-1");
	});
});

describe("SettingsPages layout", () => {
	const original = Object.getOwnPropertyDescriptor(
		HTMLElement.prototype,
		"clientWidth",
	);
	afterEach(() => {
		if (original) {
			Object.defineProperty(HTMLElement.prototype, "clientWidth", original);
		} else {
			delete (HTMLElement.prototype as unknown as Record<string, unknown>)
				.clientWidth;
		}
	});

	test("a narrow panel shows the categories as a horizontal strip", () => {
		render(<Harness />);
		expect(document.querySelector("[data-tab='settings']")).toHaveAttribute(
			"data-layout",
			"strip",
		);
		expect(screen.getByRole("tablist")).toHaveAttribute(
			"aria-orientation",
			"horizontal",
		);
		expect(
			screen.getAllByRole("navigation", { name: "Settings" }),
		).toHaveLength(1);
	});

	test("a wide panel shows them as a vertical rail", () => {
		Object.defineProperty(HTMLElement.prototype, "clientWidth", {
			configurable: true,
			get: () => 900,
		});
		render(<Harness />);
		expect(document.querySelector("[data-tab='settings']")).toHaveAttribute(
			"data-layout",
			"rail",
		);
		expect(screen.getByRole("tablist")).toHaveAttribute(
			"aria-orientation",
			"vertical",
		);
	});

	test("in the strip Left/Right move focus, in the rail only Up/Down do", () => {
		const { unmount } = render(<Harness />);
		tab("General").focus();
		fireEvent.keyDown(tab("General"), { key: "ArrowRight" });
		expect(tab("Deploy")).toHaveFocus();
		unmount();

		Object.defineProperty(HTMLElement.prototype, "clientWidth", {
			configurable: true,
			get: () => 900,
		});
		render(<Harness />);
		tab("General").focus();
		fireEvent.keyDown(tab("General"), { key: "ArrowRight" });
		expect(tab("General")).toHaveFocus();
		fireEvent.keyDown(tab("General"), { key: "ArrowDown" });
		expect(tab("Deploy")).toHaveFocus();
	});
});

describe("SettingsPages search", () => {
	const field = () => screen.getByRole("combobox", { name: "Search settings" });

	test("matches fields by label and keyword, with where they live", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.type(field(), "memory");
		const options = screen.getAllByRole("option");
		expect(options[0]).toHaveTextContent("Memory");
		expect(options[0]).toHaveTextContent("Resources · Limits");
		expect(field()).toHaveAttribute("aria-expanded", "true");
		// While searching the category list gives way to the results.
		expect(screen.queryByRole("tablist")).toBeNull();

		await user.clear(field());
		await user.type(field(), "ram");
		expect(screen.getAllByRole("option")[0]).toHaveTextContent("Memory");
	});

	test("says so when nothing matches", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.type(field(), "zzzqqq");
		expect(screen.getByRole("status")).toHaveTextContent(
			"No settings match “zzzqqq”.",
		);
		expect(screen.queryByRole("option")).toBeNull();
	});

	test("an environment-variable query points at the Variables tab", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.type(field(), "secret");
		expect(screen.getByText(VARIABLES_HINT)).toBeInTheDocument();
	});

	test("the clear button and Escape empty the field and bring the tabs back", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.type(field(), "memory");
		await user.click(screen.getByRole("button", { name: "Clear search" }));
		expect(field()).toHaveValue("");
		expect(screen.getByRole("tablist")).toBeInTheDocument();

		await user.type(field(), "port");
		await user.keyboard("{Escape}");
		expect(field()).toHaveValue("");
		expect(screen.queryByRole("button", { name: "Clear search" })).toBeNull();
		expect(field()).toHaveFocus();
		// With nothing to clear, Escape lets go of the field.
		await user.keyboard("{Escape}");
		expect(field()).not.toHaveFocus();
	});

	test("the arrows choose a result and wrap", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.type(field(), "port");
		const options = screen.getAllByRole("option");
		expect(options.length).toBeGreaterThan(1);
		expect(options[0]).toHaveAttribute("aria-selected", "true");
		expect(field()).toHaveAttribute("aria-activedescendant", options[0]?.id);
		await user.keyboard("{ArrowDown}");
		expect(options[1]).toHaveAttribute("aria-selected", "true");
		expect(options[0]).toHaveAttribute("aria-selected", "false");
		expect(field()).toHaveAttribute("aria-activedescendant", options[1]?.id);
		await user.keyboard("{ArrowUp}{ArrowUp}");
		expect(options.at(-1)).toHaveAttribute("aria-selected", "true");
	});

	test("Enter jumps to the chosen result: its category, then the control is focused", async () => {
		const user = userEvent.setup();
		const onSelect = mock((_id: CategoryId) => {});
		render(
			<Harness onSelect={onSelect}>
				{(active) => (
					<>
						{active === "networking" ? (
							<input id="set-port" aria-label="Port field" />
						) : null}
						{active === "resources" ? (
							<input id="set-memory" aria-label="Memory field" />
						) : null}
					</>
				)}
			</Harness>,
		);
		await user.type(field(), "memory");
		await user.keyboard("{Enter}");

		expect(onSelect.mock.calls).toEqual([["resources"]]);
		expect(tab("Resources")).toHaveAttribute("aria-selected", "true");
		expect(screen.getByLabelText("Memory field")).toHaveFocus();
		// The search starts over once a result is picked.
		expect(field()).toHaveValue("");
		expect(screen.getByRole("tablist")).toBeInTheDocument();
	});

	test("clicking a result does the same and keeps focus off the search field", async () => {
		const user = userEvent.setup();
		const onSelect = mock((_id: CategoryId) => {});
		render(
			<Harness onSelect={onSelect}>
				{(active) =>
					active === "networking" ? (
						<input id="set-port" aria-label="Port field" />
					) : null
				}
			</Harness>,
		);
		await user.type(field(), "listen");
		const result = screen.getByRole("option", { name: /^Port/ });
		await user.click(result);
		expect(onSelect.mock.calls).toEqual([["networking"]]);
		expect(screen.getByLabelText("Port field")).toHaveFocus();
	});

	test("a field inside a closed group is opened before it is focused", async () => {
		const user = userEvent.setup();
		render(
			<Harness>
				{(active) =>
					active === "networking" ? (
						<details data-testid="more">
							<summary>More options</summary>
							<input id="set-port" aria-label="Port field" />
						</details>
					) : null
				}
			</Harness>,
		);
		await user.type(field(), "listen");
		await user.keyboard("{Enter}");
		expect(screen.getByTestId("more")).toHaveProperty("open", true);
		expect(screen.getByLabelText("Port field")).toHaveFocus();
	});

	test("a result on a block with a data-anchor focuses the block's first control", async () => {
		const user = userEvent.setup();
		render(
			<Harness>
				{(active) =>
					active === "general" ? (
						<div data-anchor="set-name">
							<button type="button">First control</button>
						</div>
					) : null
				}
			</Harness>,
		);
		await user.type(field(), "rename");
		await user.keyboard("{Enter}");
		expect(screen.getByRole("button", { name: "First control" })).toHaveFocus();
	});

	test("a result whose field is not on the page leaves focus alone", async () => {
		const user = userEvent.setup();
		render(<Harness>{() => <p>nothing here</p>}</Harness>);
		await user.type(field(), "memory");
		await user.keyboard("{Enter}");
		expect(tab("Resources")).toHaveAttribute("aria-selected", "true");
		expect(field()).toHaveFocus();
	});
});
