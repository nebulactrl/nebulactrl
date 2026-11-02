import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import {
	type NodeProps,
	Position,
	ReactFlow,
	ReactFlowProvider,
} from "@xyflow/react";
import { JackHandle, type JackHandleProps } from "./jack-handle";
import { JACK_RADIUS } from "./lib/cable";

/** Renders the jack from the props the test put in the node's data. */
function JackNode({ data }: NodeProps) {
	return <JackHandle {...(data as unknown as JackHandleProps)} />;
}

function show(props: JackHandleProps) {
	return render(
		<div style={{ width: 400, height: 300 }}>
			<ReactFlowProvider>
				<ReactFlow
					nodes={[
						{
							id: "n",
							type: "jack",
							position: { x: 0, y: 0 },
							data: props as unknown as Record<string, unknown>,
						},
					]}
					edges={[]}
					nodeTypes={{ jack: JackNode }}
				/>
			</ReactFlowProvider>
		</div>,
	);
}

const base = {
	type: "source",
	position: Position.Left,
	plugged: false,
	label: "Left jack",
} as const;

describe("JackHandle", () => {
	test("is a handle named by its label, the size of the jack's diameter", () => {
		show({ ...base, id: "left" });
		const jack = screen.getByLabelText("Left jack");
		expect(jack).toHaveAttribute("data-handleid", "left");
		expect(jack.style.width).toBe(`${JACK_RADIUS * 2}px`);
		expect(jack.style.height).toBe(`${JACK_RADIUS * 2}px`);
		expect(jack).toHaveClass("jack");
	});

	test("an empty jack is a bore: no plug face inside", () => {
		show(base);
		expect(screen.getByLabelText("Left jack").querySelector("span")).toBeNull();
	});

	test("a plugged jack draws the plug's dark eye and a metal face", () => {
		show({ ...base, plugged: true });
		const jack = screen.getByLabelText("Left jack");
		expect(jack.querySelector("span[aria-hidden='true']")).not.toBeNull();
		expect(jack.style.background).toContain("#eef0f5");
	});

	test("an unplugged jack is drawn as a bore, not a plug", () => {
		show(base);
		expect(screen.getByLabelText("Left jack").style.background).not.toContain(
			"#eef0f5",
		);
	});

	test("a side jack sits at the given offset from the card's top", () => {
		show({ ...base, top: 26 });
		expect(screen.getByLabelText("Left jack").style.top).toBe("26px");
	});

	test("top and bottom jacks ignore the offset: they stay centred on their edge", () => {
		show({ ...base, position: Position.Bottom, top: 26 });
		expect(screen.getByLabelText("Left jack").style.top).toBe("");
	});

	test("connectable by default, and not when asked", () => {
		const { unmount } = show(base);
		expect(screen.getByLabelText("Left jack")).toHaveClass("connectable");
		unmount();
		show({ ...base, connectable: false });
		expect(screen.getByLabelText("Left jack")).not.toHaveClass("connectable");
	});
});
