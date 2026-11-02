import {
	afterAll,
	beforeAll,
	describe,
	expect,
	jest,
	mock,
	test,
} from "bun:test";
import { fireEvent, screen, within } from "@testing-library/react";
import { useMemo, useState } from "react";
import {
	CommandPalette,
	MOD_KEY,
	PALETTE_LABEL,
	PALETTE_PROMPT,
} from "./command-palette";
import { type PaletteCommand, useRegisterCommands } from "./palette-registry";
import { renderAt } from "./shell-test-kit";

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

function Register({
	commands,
	source = "test",
}: {
	commands: PaletteCommand[];
	source?: string;
}) {
	const stable = useMemo(() => commands, [commands]);
	useRegisterCommands(source, stable);
	return null;
}

function cmd(
	id: string,
	label: string,
	extra: Partial<PaletteCommand> = {},
): PaletteCommand {
	return { id, label, run: () => {}, ...extra };
}

const input = () => screen.getByRole("combobox");
const rowLabels = () =>
	screen
		.getAllByRole("option")
		.map((row) => row.querySelector("span.truncate")?.textContent);

async function mount(
	commands: PaletteCommand[],
	props: { open?: boolean; onOpenChange?: (open: boolean) => void } = {},
) {
	return renderAt(
		<>
			<Register commands={commands} />
			<CommandPalette
				open={props.open ?? true}
				onOpenChange={props.onOpenChange ?? (() => {})}
			/>
		</>,
	);
}

/** Registers three commands and reopens the palette from a button. */
function Host() {
	const [open, setOpen] = useState(true);
	const commands = useMemo(
		() => [cmd("a", "Alpha"), cmd("b", "Beta"), cmd("c", "Gamma")],
		[],
	);
	useRegisterCommands("host", commands);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				reopen
			</button>
			<CommandPalette open={open} onOpenChange={setOpen} />
		</>
	);
}

describe("shell CommandPalette", () => {
	test("names the prompt and the chord", () => {
		expect(PALETTE_PROMPT).toBe(`${PALETTE_LABEL}…`);
		expect(["⌘", "Ctrl"]).toContain(MOD_KEY);
	});

	test("shows the registered commands under the shell's prompt", async () => {
		await mount([
			cmd("a", "Open servers", { group: "Go to", hint: "hosts" }),
			cmd("b", "Add server"),
		]);
		expect(input()).toHaveAttribute("placeholder", PALETTE_PROMPT);
		expect(rowLabels()).toEqual(["Open servers", "Add server"]);
		const first = screen.getAllByRole("option")[0] as HTMLElement;
		expect(within(first).getByText("Go to")).toBeInTheDocument();
		expect(within(first).getByText("hosts")).toBeInTheDocument();
		// A command with no group is an Action.
		expect(
			within(screen.getAllByRole("option")[1] as HTMLElement).getByText(
				"Action",
			),
		).toBeInTheDocument();
	});

	test("says there is nothing to jump to when nothing is registered", async () => {
		await mount([]);
		expect(screen.getByText("Nothing to jump to yet.")).toBeInTheDocument();
	});

	test("the empty prompt leaves out disabled and search-only rows, but typing finds them", async () => {
		await mount([
			cmd("ok", "Deploy checkout"),
			cmd("off", "Deploy nothing staged", { disabled: true }),
			cmd("rare", "Drain agent1", { searchOnly: true }),
		]);
		expect(rowLabels()).toEqual(["Deploy checkout"]);
		fireEvent.change(input(), { target: { value: "drain" } });
		expect(rowLabels()).toEqual(["Drain agent1"]);
		fireEvent.change(input(), { target: { value: "nothing staged" } });
		expect(rowLabels()).toEqual(["Deploy nothing staged"]);
	});

	test("Enter runs the command and closes the palette", async () => {
		const run = jest.fn();
		const onOpenChange = jest.fn();
		await mount([cmd("a", "Open servers", { run })], { onOpenChange });
		fireEvent.keyDown(input(), { key: "Enter" });
		expect(run).toHaveBeenCalledTimes(1);
		expect(onOpenChange).toHaveBeenLastCalledWith(false);
	});

	test("keywords find a command without being drawn", async () => {
		await mount([
			cmd("a", "Open servers", { keywords: ["machines"] }),
			cmd("b", "Open pages"),
		]);
		fireEvent.change(input(), { target: { value: "machines" } });
		expect(rowLabels()).toEqual(["Open servers"]);
		expect(screen.queryByText("machines")).toBeNull();
	});

	test("the shortcut chip is drawn on the row", async () => {
		await mount([cmd("a", "Open servers", { shortcut: "G S" })]);
		expect(screen.getByText("G S")).toBeInTheDocument();
	});

	test("a row that was used comes first as Recent the next time the palette opens", async () => {
		await renderAt(<Host />);
		expect(rowLabels()).toEqual(["Alpha", "Beta", "Gamma"]);
		fireEvent.click(screen.getByRole("option", { name: /Gamma/ }));
		expect(screen.queryByRole("dialog")).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "reopen" }));
		expect(rowLabels()).toEqual(["Gamma", "Alpha", "Beta"]);
		const first = screen.getAllByRole("option")[0] as HTMLElement;
		expect(within(first).getByText("Recent")).toBeInTheDocument();
	});

	test("stays closed when not open", async () => {
		await mount([cmd("a", "Alpha")], { open: false });
		expect(screen.queryByRole("dialog")).toBeNull();
	});
});
