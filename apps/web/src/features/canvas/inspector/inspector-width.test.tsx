import { afterEach, describe, expect, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import { publishInspectorWidth, useInspectorWidthPx } from "./geometry";

function Reader({ label }: { label: string }) {
	return <output aria-label={label}>{useInspectorWidthPx()}</output>;
}

let renders = 0;

/** Counts its own renders. */
function Counting() {
	renders += 1;
	return <output>{useInspectorWidthPx()}</output>;
}

afterEach(() => {
	act(() => publishInspectorWidth(0));
});

describe("the published inspector width", () => {
	test("reads 0 while the inspector is closed", () => {
		render(<Reader label="width" />);
		expect(screen.getByLabelText("width")).toHaveTextContent("0");
	});

	test("every reader follows the width the inspector publishes", () => {
		render(
			<>
				<Reader label="canvas" />
				<Reader label="dock" />
			</>,
		);
		act(() => publishInspectorWidth(720));
		expect(screen.getByLabelText("canvas")).toHaveTextContent("720");
		expect(screen.getByLabelText("dock")).toHaveTextContent("720");
		act(() => publishInspectorWidth(0));
		expect(screen.getByLabelText("canvas")).toHaveTextContent("0");
	});

	test("a reader that mounts later starts from the current width", () => {
		act(() => publishInspectorWidth(640));
		render(<Reader label="late" />);
		expect(screen.getByLabelText("late")).toHaveTextContent("640");
	});

	test("an unchanged width notifies nobody", () => {
		renders = 0;
		render(<Counting />);
		act(() => publishInspectorWidth(500));
		const after = renders;
		act(() => publishInspectorWidth(500));
		expect(renders).toBe(after);
	});

	test("a reader that unmounted stops listening", () => {
		const view = render(<Reader label="gone" />);
		view.unmount();
		expect(() => act(() => publishInspectorWidth(900))).not.toThrow();
	});
});
