import { afterAll, beforeAll, describe, expect, mock, test } from "bun:test";
import { fireEvent, render, screen } from "@testing-library/react";
import { ShellActionsProvider, useShellActions } from "./shell-actions";
import { renderAt, shellApi } from "./shell-test-kit";

beforeAll(() => {
	mock.module("virtual:docs-search", () => ({
		default: { pages: [], rows: [] },
	}));
});
afterAll(() => {
	mock.module("virtual:docs-search", () => ({
		default: { pages: [], rows: [] },
	}));
});

function Buttons() {
	const actions = useShellActions();
	return (
		<>
			<output aria-label="palette-open">{String(actions.paletteOpen)}</output>
			<button type="button" onClick={actions.openPalette}>
				open palette
			</button>
			<button type="button" onClick={() => actions.setPaletteOpen(false)}>
				close palette
			</button>
			<button type="button" onClick={actions.openNewProject}>
				new project
			</button>
		</>
	);
}

describe("ShellActionsProvider", () => {
	test("openPalette shows the palette; Escape and setPaletteOpen(false) close it", async () => {
		shellApi();
		await renderAt(
			<ShellActionsProvider>
				<Buttons />
			</ShellActionsProvider>,
		);
		expect(
			screen.queryByRole("dialog", { name: "Command palette" }),
		).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "open palette" }));
		expect(screen.getByLabelText("palette-open")).toHaveTextContent("true");
		expect(
			screen.getByRole("dialog", { name: "Command palette" }),
		).toBeInTheDocument();
		fireEvent.keyDown(window, { key: "Escape" });
		expect(screen.getByLabelText("palette-open")).toHaveTextContent("false");
		fireEvent.click(screen.getByRole("button", { name: "open palette" }));
		fireEvent.click(screen.getByRole("button", { name: "close palette" }));
		expect(
			screen.queryByRole("dialog", { name: "Command palette" }),
		).toBeNull();
	});

	test("Ctrl+K opens the palette through the provider", async () => {
		shellApi();
		await renderAt(
			<ShellActionsProvider>
				<Buttons />
			</ShellActionsProvider>,
		);
		fireEvent.keyDown(window, { key: "k", ctrlKey: true });
		expect(screen.getByLabelText("palette-open")).toHaveTextContent("true");
	});

	test("openNewProject shows the new project dialog", async () => {
		shellApi();
		await renderAt(
			<ShellActionsProvider>
				<Buttons />
			</ShellActionsProvider>,
		);
		expect(screen.queryByRole("dialog")).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "new project" }));
		expect(
			await screen.findByRole("dialog", { name: /new project/i }),
		).toBeInTheDocument();
	});

	test("useShellActions outside the provider says what is missing", () => {
		const real = console.error;
		console.error = () => {};
		try {
			expect(() => render(<Buttons />)).toThrow(
				"useShellActions must be used inside ShellActionsProvider",
			);
		} finally {
			console.error = real;
		}
	});
});
