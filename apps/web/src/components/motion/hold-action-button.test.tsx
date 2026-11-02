import {
	afterEach,
	beforeEach,
	describe,
	expect,
	jest,
	spyOn,
	test,
} from "bun:test";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { createRef } from "react";
import { HoldActionButton } from "./hold-action-button";

beforeEach(() => {
	jest.useFakeTimers();
});

afterEach(() => {
	jest.useRealTimers();
});

function advance(ms: number) {
	act(() => {
		jest.advanceTimersByTime(ms);
	});
}

function press(button: HTMLElement, init: PointerEventInit = {}) {
	fireEvent.pointerDown(button, { button: 0, pointerId: 1, ...init });
}

function release(button: HTMLElement) {
	fireEvent.pointerUp(button, { button: 0, pointerId: 1 });
}

function setup(props: Partial<Parameters<typeof HoldActionButton>[0]> = {}) {
	const onHoldComplete = jest.fn();
	const view = render(
		<HoldActionButton
			holdDuration={1000}
			onHoldComplete={onHoldComplete}
			{...props}
		>
			Delete project
		</HoldActionButton>,
	);
	const button = screen.getByRole("button", { name: "Delete project" });
	return { ...view, button, onHoldComplete };
}

describe("HoldActionButton", () => {
	test("is named by its label and idle until pressed", () => {
		const { button, onHoldComplete } = setup();
		expect(button).not.toHaveAttribute("data-pressed");
		expect(button).toHaveAttribute("type", "button");
		expect(screen.getByText("Keep holding")).toHaveAttribute(
			"aria-hidden",
			"true",
		);
		expect(screen.getByText("Done")).toHaveAttribute("aria-hidden", "true");
		expect(onHoldComplete).not.toHaveBeenCalled();
	});

	test("holding for the full duration completes once", () => {
		const { button, onHoldComplete } = setup();
		press(button);
		expect(button).toHaveAttribute("data-pressed", "true");
		expect(screen.getByText("Keep holding")).toHaveAttribute(
			"aria-hidden",
			"false",
		);

		advance(999);
		expect(onHoldComplete).not.toHaveBeenCalled();
		advance(1);
		expect(onHoldComplete).toHaveBeenCalledTimes(1);
		// The complete label takes over from the holding one.
		expect(screen.getByText("Done")).toHaveAttribute("aria-hidden", "false");
		expect(screen.getByText("Keep holding")).toHaveAttribute(
			"aria-hidden",
			"true",
		);

		advance(5000);
		expect(onHoldComplete).toHaveBeenCalledTimes(1);
	});

	test("releasing early cancels: nothing completes and the button is idle again", () => {
		const { button, onHoldComplete } = setup();
		press(button);
		advance(600);
		release(button);
		expect(button).not.toHaveAttribute("data-pressed");
		advance(5000);
		expect(onHoldComplete).not.toHaveBeenCalled();
		expect(screen.getByText("Keep holding")).toHaveAttribute(
			"aria-hidden",
			"true",
		);
	});

	test("a completed hold stays complete when the pointer is released", () => {
		const { button, onHoldComplete } = setup();
		press(button);
		advance(1000);
		release(button);
		expect(screen.getByText("Done")).toHaveAttribute("aria-hidden", "false");
		// Pressing again while complete does not restart or fire a second time.
		press(button);
		advance(5000);
		expect(onHoldComplete).toHaveBeenCalledTimes(1);
	});

	test("uses the default 2 second hold", () => {
		const onHoldComplete = jest.fn();
		render(
			<HoldActionButton onHoldComplete={onHoldComplete}>Wipe</HoldActionButton>,
		);
		press(screen.getByRole("button", { name: "Wipe" }));
		advance(1999);
		expect(onHoldComplete).not.toHaveBeenCalled();
		advance(1);
		expect(onHoldComplete).toHaveBeenCalledTimes(1);
	});

	test("a secondary mouse button does not start a hold", () => {
		const { button, onHoldComplete } = setup();
		fireEvent.pointerDown(button, { button: 2, pointerId: 1 });
		expect(button).not.toHaveAttribute("data-pressed");
		advance(5000);
		expect(onHoldComplete).not.toHaveBeenCalled();
	});

	test("a disabled button never holds", () => {
		const { button, onHoldComplete } = setup({ disabled: true });
		expect(button).toBeDisabled();
		press(button);
		fireEvent.keyDown(button, { key: "Enter" });
		advance(5000);
		expect(onHoldComplete).not.toHaveBeenCalled();
	});

	test("pressing Enter or Space holds, and letting go cancels", () => {
		const { button, onHoldComplete } = setup();
		fireEvent.keyDown(button, { key: "Enter" });
		expect(button).toHaveAttribute("data-pressed", "true");
		advance(500);
		fireEvent.keyUp(button, { key: "Enter" });
		expect(button).not.toHaveAttribute("data-pressed");
		advance(5000);
		expect(onHoldComplete).not.toHaveBeenCalled();

		fireEvent.keyDown(button, { key: " " });
		advance(1000);
		expect(onHoldComplete).toHaveBeenCalledTimes(1);
	});

	test("key auto-repeat does not restart the hold timer", () => {
		const { button, onHoldComplete } = setup();
		fireEvent.keyDown(button, { key: "Enter" });
		advance(600);
		fireEvent.keyDown(button, { key: "Enter", repeat: true });
		advance(400);
		expect(onHoldComplete).toHaveBeenCalledTimes(1);
	});

	test("other keys do nothing", () => {
		const { button, onHoldComplete } = setup();
		fireEvent.keyDown(button, { key: "a" });
		fireEvent.keyUp(button, { key: "a" });
		expect(button).not.toHaveAttribute("data-pressed");
		advance(5000);
		expect(onHoldComplete).not.toHaveBeenCalled();
	});

	test("sliding the pointer off the button abandons the hold", () => {
		const { button, onHoldComplete } = setup();
		press(button);
		// No layout in happy-dom: the button is a 0x0 box at the origin.
		fireEvent.pointerMove(button, { clientX: 0, clientY: 0, pointerId: 1 });
		expect(button).toHaveAttribute("data-pressed", "true");
		fireEvent.pointerMove(button, { clientX: 50, clientY: 0, pointerId: 1 });
		expect(button).not.toHaveAttribute("data-pressed");
		advance(5000);
		expect(onHoldComplete).not.toHaveBeenCalled();
	});

	test("moving the pointer while not holding does nothing", () => {
		const { button } = setup();
		fireEvent.pointerMove(button, { clientX: 50, clientY: 50, pointerId: 1 });
		expect(button).not.toHaveAttribute("data-pressed");
	});

	test("a touch leaving the button does not cancel the hold", () => {
		const { button, onHoldComplete } = setup();
		press(button, { pointerType: "touch" });
		fireEvent.pointerLeave(button, { pointerType: "touch", pointerId: 1 });
		expect(button).toHaveAttribute("data-pressed", "true");
		advance(1000);
		expect(onHoldComplete).toHaveBeenCalledTimes(1);
	});

	test("a mouse leaving the button cancels the hold", () => {
		const { button, onHoldComplete } = setup();
		press(button, { pointerType: "mouse" });
		fireEvent.pointerLeave(button, { pointerType: "mouse", pointerId: 1 });
		expect(button).not.toHaveAttribute("data-pressed");
		advance(5000);
		expect(onHoldComplete).not.toHaveBeenCalled();
	});

	test("a cancelled pointer abandons the hold", () => {
		const { button, onHoldComplete } = setup();
		press(button);
		fireEvent.pointerCancel(button, { pointerId: 1 });
		expect(button).not.toHaveAttribute("data-pressed");
		advance(5000);
		expect(onHoldComplete).not.toHaveBeenCalled();
	});

	test("unmounting mid-hold clears the timer so the action never fires", () => {
		const { button, onHoldComplete, unmount } = setup();
		press(button);
		unmount();
		advance(5000);
		expect(onHoldComplete).not.toHaveBeenCalled();
	});

	test("a refused press shakes the button instead of starting", () => {
		const animate = spyOn(Element.prototype, "animate");
		try {
			const { button, onHoldComplete } = setup({ refused: true });
			press(button);
			expect(animate).toHaveBeenCalledTimes(1);
			expect(animate.mock.calls[0]?.[1]).toEqual({ duration: 340 });
			expect(button).not.toHaveAttribute("data-pressed");
			advance(5000);
			expect(onHoldComplete).not.toHaveBeenCalled();
		} finally {
			animate.mockRestore();
		}
	});

	test("a disabled refused button neither shakes nor holds", () => {
		const animate = spyOn(Element.prototype, "animate");
		try {
			const { button } = setup({ refused: true, disabled: true });
			press(button);
			expect(animate).not.toHaveBeenCalled();
		} finally {
			animate.mockRestore();
		}
	});

	test("a failed action re-arms: enabling the button again lets the next hold run", () => {
		const onHoldComplete = jest.fn();
		const ui = (disabled: boolean) => (
			<HoldActionButton
				holdDuration={1000}
				onHoldComplete={onHoldComplete}
				disabled={disabled}
			>
				Delete project
			</HoldActionButton>
		);
		const { rerender } = render(ui(false));
		const button = screen.getByRole("button", { name: "Delete project" });
		press(button);
		advance(1000);
		expect(onHoldComplete).toHaveBeenCalledTimes(1);
		// The caller disables it while the action runs, then enables it on failure.
		rerender(ui(true));
		rerender(ui(false));
		expect(screen.getByText("Done")).toHaveAttribute("aria-hidden", "true");

		press(screen.getByRole("button", { name: "Delete project" }));
		advance(1000);
		expect(onHoldComplete).toHaveBeenCalledTimes(2);
	});

	test("custom labels, ref and classes reach the DOM", () => {
		const ref = createRef<HTMLButtonElement>();
		render(
			<HoldActionButton
				ref={ref}
				holdingLabel="Hold on"
				completeLabel="Gone"
				className="custom-btn"
				type="horizontal"
			>
				{<span>Erase</span>}
			</HoldActionButton>,
		);
		expect(screen.getByText("Hold on")).toBeInTheDocument();
		expect(screen.getByText("Gone")).toBeInTheDocument();
		expect(ref.current).toBeInstanceOf(HTMLButtonElement);
		expect(ref.current).toHaveClass("custom-btn");
		// A non-string child gives no aria-label to override its own text.
		expect(ref.current).not.toHaveAttribute("aria-label");
	});

	test("the context menu is suppressed so a long press does not open it", () => {
		const { button } = setup();
		const notCancelled = fireEvent.contextMenu(button);
		expect(notCancelled).toBe(false);
	});
});
