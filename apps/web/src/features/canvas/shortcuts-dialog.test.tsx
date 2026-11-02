import { describe, expect, test } from "bun:test";
import { render, screen, waitFor, within } from "@testing-library/react";
import { MANUAL_DASH, STAGED_DASH } from "./lib/cable";
import { ShortcutsDialog } from "./shortcuts-dialog";

function open() {
	render(<ShortcutsDialog open onOpenChange={() => {}} />);
}

describe("ShortcutsDialog", () => {
	test("renders nothing while closed", () => {
		render(<ShortcutsDialog open={false} onOpenChange={() => {}} />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	test("is a titled dialog that mentions Ctrl standing in for ⌘", () => {
		open();
		const dialog = screen.getByRole("dialog", { name: "Keyboard shortcuts" });
		expect(dialog).toHaveTextContent("Ctrl works in place of ⌘");
	});

	test("groups the shortcuts as Canvas, Inspector tabs, Anywhere and Cables", () => {
		open();
		for (const name of ["Canvas", "Inspector tabs", "Anywhere", "Cables"]) {
			expect(screen.getByLabelText(name)).toBeInTheDocument();
		}
	});

	test("lists the canvas search shortcut with its keys", () => {
		open();
		const canvas = screen.getByLabelText("Canvas");
		const row = within(canvas).getByText("Search services").closest("li");
		expect(row).toHaveTextContent("⌘F");
	});

	test("inspector tab shortcuts are chords: 'g' then the tab's letter", () => {
		open();
		const tabs = screen.getByLabelText("Inspector tabs");
		expect(tabs).toHaveTextContent(/second key within a second/);
		const row = within(tabs).getByText("Deployments").closest("li");
		expect(row).toHaveTextContent("gthend");
		// Eight tabs, every one a two-key chord.
		const items = [...tabs.querySelectorAll("li")];
		expect(items).toHaveLength(8);
		for (const item of items) expect(item).toHaveTextContent("then");
	});

	test("a non-chord shortcut never says 'then'", () => {
		open();
		const canvas = screen.getByLabelText("Canvas");
		expect(
			within(canvas).getByText("Search services").closest("li"),
		).not.toHaveTextContent("then");
		const anywhere = screen.getByLabelText("Anywhere");
		expect(
			within(anywhere).getByText("Open the command palette").closest("li"),
		).toHaveTextContent("⌘K");
	});

	test("the cables legend draws each line the way the canvas does", () => {
		open();
		const cables = screen.getByLabelText("Cables");
		const lines = [...cables.querySelectorAll("svg line")];
		expect(lines.map((l) => l.getAttribute("stroke-dasharray"))).toEqual([
			null,
			STAGED_DASH,
			MANUAL_DASH,
		]);
		expect(
			[...cables.querySelectorAll("li")].map((li) => li.textContent),
		).toEqual([
			"A variable references the other service",
			"A staged reference: live after the next deploy",
			"Linked by hand: no variable references it",
		]);
	});

	test("follows the host's open flag: closing it removes the dialog", async () => {
		const { rerender } = render(
			<ShortcutsDialog open onOpenChange={() => {}} />,
		);
		expect(screen.getByRole("dialog")).toBeInTheDocument();
		rerender(<ShortcutsDialog open={false} onOpenChange={() => {}} />);
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});
});
