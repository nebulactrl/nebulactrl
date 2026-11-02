import { describe, expect, mock, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CanvasToolbar } from "./canvas-toolbar";

function setup(props: Partial<Parameters<typeof CanvasToolbar>[0]> = {}) {
	const handlers = {
		onSearch: mock(() => {}),
		onArrange: mock(() => {}),
		onGroup: mock(() => {}),
	};
	render(
		<CanvasToolbar
			empty={false}
			canGroup
			compact={false}
			{...handlers}
			{...props}
		/>,
	);
	return handlers;
}

describe("CanvasToolbar", () => {
	test("each labelled button calls its own handler", async () => {
		const user = userEvent.setup();
		const h = setup();
		await user.click(screen.getByRole("button", { name: "Search services" }));
		expect([
			h.onSearch.mock.calls.length,
			h.onArrange.mock.calls.length,
			h.onGroup.mock.calls.length,
		]).toEqual([1, 0, 0]);
		await user.click(screen.getByRole("button", { name: /Arrange/ }));
		await user.click(screen.getByRole("button", { name: /Group/ }));
		expect([
			h.onSearch.mock.calls.length,
			h.onArrange.mock.calls.length,
			h.onGroup.mock.calls.length,
		]).toEqual([1, 1, 1]);
	});

	test("shows the visible labels and the search shortcut when roomy", () => {
		setup();
		expect(
			screen.getByRole("button", { name: "Search services" }),
		).toHaveTextContent("Search⌘F");
		expect(screen.getByRole("button", { name: "Arrange" })).toHaveTextContent(
			"Arrange",
		);
		expect(screen.getByRole("button", { name: "Group" })).toHaveTextContent(
			"Group",
		);
	});

	test("an empty canvas disables Arrange but not Search", async () => {
		const user = userEvent.setup();
		const h = setup({ empty: true });
		const arrange = screen.getByRole("button", { name: "Arrange" });
		expect(arrange).toBeDisabled();
		await user.click(arrange);
		expect(h.onArrange).not.toHaveBeenCalled();
		expect(
			screen.getByRole("button", { name: "Search services" }),
		).toBeEnabled();
	});

	test("Group is disabled until a group can be formed", async () => {
		const user = userEvent.setup();
		const h = setup({ canGroup: false });
		const group = screen.getByRole("button", { name: "Group" });
		expect(group).toBeDisabled();
		await user.click(group);
		expect(h.onGroup).not.toHaveBeenCalled();
		// A hint says how to make a selection large enough.
		expect(group).toHaveAttribute(
			"title",
			expect.stringContaining("select several with Shift"),
		);
	});

	test("compact mode drops the labels but keeps accessible names", async () => {
		const user = userEvent.setup();
		const h = setup({ compact: true });
		const search = screen.getByRole("button", { name: "Search services" });
		const arrange = screen.getByRole("button", { name: "Arrange services" });
		const group = screen.getByRole("button", {
			name: "Group selected services",
		});
		for (const button of [search, arrange, group]) {
			expect(button.textContent).toBe("");
		}
		expect(screen.queryByText("⌘F")).not.toBeInTheDocument();
		await user.click(arrange);
		await user.click(group);
		await user.click(search);
		expect(h.onArrange).toHaveBeenCalledTimes(1);
		expect(h.onGroup).toHaveBeenCalledTimes(1);
		expect(h.onSearch).toHaveBeenCalledTimes(1);
	});

	test("compact mode keeps the disabled states", () => {
		setup({ compact: true, empty: true, canGroup: false });
		expect(
			screen.getByRole("button", { name: "Arrange services" }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Group selected services" }),
		).toBeDisabled();
	});
});
