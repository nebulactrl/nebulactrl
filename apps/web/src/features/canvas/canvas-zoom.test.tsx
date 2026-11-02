import { describe, expect, mock, test } from "bun:test";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ReactFlow, ReactFlowProvider } from "@xyflow/react";
import { CanvasZoomToolbar } from "./canvas-zoom";

function setup(mapOpen = false) {
	const handlers = {
		onFit: mock(() => {}),
		onToggleMap: mock(() => {}),
		onShortcuts: mock(() => {}),
	};
	render(
		<div style={{ width: 800, height: 600 }}>
			<ReactFlowProvider>
				<ReactFlow nodes={[]} edges={[]}>
					<CanvasZoomToolbar mapOpen={mapOpen} {...handlers} />
				</ReactFlow>
			</ReactFlowProvider>
		</div>,
	);
	return handlers;
}

/** The percent the zoom button prints now. */
function percent(): number {
	const button = screen.getByRole("button", { name: /^Zoom \d+%, reset/ });
	return Number.parseInt(button.textContent ?? "", 10);
}

describe("CanvasZoomToolbar", () => {
	test("prints the camera's zoom as a percent, 100% at rest", () => {
		setup();
		expect(percent()).toBe(100);
	});

	test("zoom in raises the camera, reset returns it to 100%, zoom out lowers it", async () => {
		const user = userEvent.setup();
		setup();
		await user.click(screen.getByRole("button", { name: "Zoom in" }));
		await waitFor(() => expect(percent()).toBeGreaterThan(100));

		await user.click(screen.getByRole("button", { name: /reset to 100%/ }));
		await waitFor(() => expect(percent()).toBe(100));

		await user.click(screen.getByRole("button", { name: "Zoom out" }));
		await waitFor(() => expect(percent()).toBeLessThan(100));
	});

	test("Fit, the minimap toggle and the shortcuts key call the canvas's handlers", async () => {
		const user = userEvent.setup();
		const h = setup();
		await user.click(screen.getByRole("button", { name: "Fit all services" }));
		expect(h.onFit).toHaveBeenCalledTimes(1);
		await user.click(screen.getByRole("button", { name: "Show minimap" }));
		expect(h.onToggleMap).toHaveBeenCalledTimes(1);
		await user.click(
			screen.getByRole("button", { name: "Keyboard shortcuts" }),
		);
		expect(h.onShortcuts).toHaveBeenCalledTimes(1);
		expect(h.onFit).toHaveBeenCalledTimes(1);
	});

	test("the minimap toggle reflects whether the map is open", () => {
		setup(true);
		expect(
			screen.getByRole("button", { name: "Hide minimap" }),
		).toHaveAttribute("aria-pressed", "true");
	});
});
