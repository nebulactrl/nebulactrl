import { expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { type CanvasUi, CanvasUiContext, useCanvasUi } from "./canvas-context";

function Probe() {
	const ui = useCanvasUi();
	return (
		<div>
			<span>
				match:{ui.matchIds === null ? "none" : [...ui.matchIds].join(",")}
			</span>
			<span>tick:{ui.tick}</span>
			<span>renaming:{ui.renamingGroupId ?? "nobody"}</span>
			<button type="button" onClick={() => ui.toggleGroup("g")}>
				toggle
			</button>
		</div>
	);
}

let grabbed: CanvasUi | undefined;

function Grab() {
	grabbed = useCanvasUi();
	return null;
}

test("outside a canvas the defaults filter nothing and every action is a harmless no-op", () => {
	render(<Grab />);
	const ui = grabbed;
	expect(ui?.matchIds).toBeNull();
	expect(ui?.tick).toBe(0);
	expect(ui?.renamingGroupId).toBeNull();
	expect(() => {
		ui?.toggleGroup("g");
		ui?.renameGroup("g", "name");
		ui?.ungroup("g");
		ui?.finishRenameRequest();
	}).not.toThrow();
});

test("a canvas provides its own state to the cards below it", () => {
	const toggled: string[] = [];
	render(
		<CanvasUiContext.Provider
			value={{
				matchIds: new Set(["a", "b"]),
				tick: 4,
				toggleGroup: (id) => toggled.push(id),
				renameGroup: () => {},
				ungroup: () => {},
				renamingGroupId: "g-9",
				finishRenameRequest: () => {},
			}}
		>
			<Probe />
		</CanvasUiContext.Provider>,
	);
	expect(screen.getByText("match:a,b")).toBeInTheDocument();
	expect(screen.getByText("tick:4")).toBeInTheDocument();
	expect(screen.getByText("renaming:g-9")).toBeInTheDocument();
	screen.getByRole("button", { name: "toggle" }).click();
	expect(toggled).toEqual(["g"]);
});
