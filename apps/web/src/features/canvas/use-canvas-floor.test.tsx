import { expect, test } from "bun:test";
import { act, render } from "@testing-library/react";
import { ReactFlow, ReactFlowProvider, useReactFlow } from "@xyflow/react";
import { type RefObject, useRef } from "react";
import { useCanvasFloor } from "./use-canvas-floor";

let flow: ReturnType<typeof useReactFlow> | undefined;
let lattice: RefObject<HTMLDivElement | null> | undefined;

function Floor({ mount = true }: { mount?: boolean }) {
	const ref = useRef<HTMLDivElement>(null);
	lattice = ref;
	flow = useReactFlow();
	useCanvasFloor(ref);
	return mount ? <div ref={ref} data-testid="lattice" /> : null;
}

function mount(ui = <Floor />) {
	// The pane's zoom behaviour (which moves the viewport) exists only inside <ReactFlow>.
	return render(
		<ReactFlowProvider>
			<div style={{ width: 800, height: 600 }}>
				<ReactFlow nodes={[]} edges={[]} />
			</div>
			{ui}
		</ReactFlowProvider>,
	);
}

test("the lattice is placed at the opening viewport, before anything moves", () => {
	const view = mount();
	const el = view.getByTestId("lattice");
	// Identity viewport: 24px pitch, offset = the 48px bleed mod the pitch (0).
	expect(el.style.transform).toBe("translate3d(0px, 0px, 0)");
	expect(el.style.backgroundSize).toBe("24px 24px");
	expect(el.dataset.pitch).toBe("24px");
});

test("a pan moves the layer by the offset modulo the pitch and leaves the dot size alone", async () => {
	const view = mount();
	const el = view.getByTestId("lattice");
	await act(async () => {
		await flow?.setViewport({ x: 10, y: -5, zoom: 1 });
	});
	// (10 + 48) mod 24 = 10 ; (-5 + 48) mod 24 = 19
	expect(el.style.transform).toBe("translate3d(10px, 19px, 0)");
	expect(el.style.backgroundSize).toBe("24px 24px");
});

test("a zoom changes the dot pitch", async () => {
	const view = mount();
	const el = view.getByTestId("lattice");
	await act(async () => {
		await flow?.setViewport({ x: 0, y: 0, zoom: 2 });
	});
	expect(el.dataset.pitch).toBe("48px");
	expect(el.style.backgroundSize).toBe("48px 48px");
	// The lattice never gets tighter than 12px, however far out.
	await act(async () => {
		await flow?.setViewport({ x: 0, y: 0, zoom: 0.2 });
	});
	expect(el.dataset.pitch).toBe("12px");
	expect(el.style.backgroundSize).toBe("12px 12px");
});

test("a viewport change before the lattice element exists is ignored", async () => {
	mount(<Floor mount={false} />);
	expect(lattice?.current).toBeNull();
	await act(async () => {
		await flow?.setViewport({ x: 3, y: 3, zoom: 1.5 });
	});
	expect(lattice?.current).toBeNull();
});
