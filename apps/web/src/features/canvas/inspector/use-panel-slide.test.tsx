import {
	afterEach,
	beforeEach,
	describe,
	expect,
	type Mock,
	spyOn,
	test,
} from "bun:test";
import { render } from "@testing-library/react";
import { useDomEventTarget } from "./dom-event-target.test-helper";
import { usePanelSlide } from "./panel-motion";

/*
 * `motion/mini` animates real elements with the Web Animations API. Under
 * happy-dom its "is this an element" check (`instanceof EventTarget`) fails,
 * because the global EventTarget is not the one DOM nodes derive from, so for
 * these tests the global is swapped for the DOM's own class. Layout is not
 * computed either: `offsetWidth` is stubbed to the panel's width.
 */
const PANEL_WIDTH = 700;

interface Recorded {
	keyframes: Record<string, unknown>;
	duration: number;
}

let recorded: Recorded[];
let restoreEventTarget: () => void;
let animateSpy: Mock<typeof Element.prototype.animate>;
const layout: Record<string, PropertyDescriptor | undefined> = {};

beforeEach(() => {
	recorded = [];
	restoreEventTarget = useDomEventTarget();
	const animate = Element.prototype.animate;
	animateSpy = spyOn(Element.prototype, "animate");
	animateSpy.mockImplementation(function (
		this: Element,
		keyframes: Keyframe[] | PropertyIndexedKeyframes | null,
		options?: number | KeyframeAnimationOptions,
	) {
		const opts = options as KeyframeAnimationOptions;
		recorded.push({
			keyframes: keyframes as Record<string, unknown>,
			duration: Number(opts.duration),
		});
		return animate.call(this, keyframes, options);
	});
	for (const name of ["offsetWidth", "offsetHeight"]) {
		layout[name] = Object.getOwnPropertyDescriptor(HTMLElement.prototype, name);
	}
	Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
		configurable: true,
		get: () => PANEL_WIDTH,
	});
	Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
		configurable: true,
		get: () => 800,
	});
});

afterEach(() => {
	restoreEventTarget();
	animateSpy.mockRestore();
	for (const [name, descriptor] of Object.entries(layout)) {
		if (descriptor)
			Object.defineProperty(HTMLElement.prototype, name, descriptor);
		else Reflect.deleteProperty(HTMLElement.prototype, name);
	}
});

function Panel({
	open,
	axis = "x",
	reduced = false,
	instant = false,
}: {
	open: boolean;
	axis?: "x" | "y";
	reduced?: boolean;
	instant?: boolean;
}) {
	const ref = usePanelSlide(open, axis, reduced);
	return (
		<div
			ref={ref}
			data-testid="panel"
			{...(instant ? { "data-instant": "" } : {})}
		/>
	);
}

const opacityOf = (r: Recorded) => r.keyframes.opacity;
const transformOf = (r: Recorded) => r.keyframes.transform;

describe("usePanelSlide", () => {
	test("a panel that mounts open slides in from past the right edge over 250ms", () => {
		render(<Panel open />);
		expect(recorded.map(opacityOf)).toContainEqual([0, 1]);
		expect(recorded.map(transformOf)).toContainEqual([
			`translateX(${PANEL_WIDTH + 24}px)`,
			"none",
		]);
		expect(recorded.every((r) => r.duration === 250)).toBe(true);
	});

	test("a phone sheet slides in from one height below, on y", () => {
		render(<Panel open axis="y" />);
		expect(recorded.map(transformOf)).toContainEqual([
			"translateY(800px)",
			"none",
		]);
	});

	test("under reduced motion it only fades, with no transform", () => {
		render(<Panel open reduced />);
		expect(recorded.map(opacityOf)).toContainEqual([0, 1]);
		expect(recorded.some((r) => "transform" in r.keyframes)).toBe(false);
		expect(recorded.every((r) => r.duration === 150)).toBe(true);
	});

	test("closing slides back out in 150ms, from where the panel rests", () => {
		const view = render(<Panel open />);
		const panel = view.getByTestId("panel");
		recorded.length = 0;
		panel.style.opacity = "1";
		panel.style.transform = "none";
		view.rerender(<Panel open={false} />);
		expect(recorded.map(transformOf)).toContainEqual([
			"translateX(0px)",
			`translateX(${PANEL_WIDTH + 24}px)`,
		]);
		expect(recorded.map(opacityOf)).toContainEqual([1, 0]);
		expect(recorded.every((r) => r.duration === 150)).toBe(true);
	});

	test("a close that interrupts the open starts where the panel is and takes a share of the time", () => {
		const view = render(<Panel open />);
		const panel = view.getByTestId("panel");
		recorded.length = 0;
		panel.style.opacity = "0.5";
		panel.style.transform = "matrix(1, 0, 0, 1, 362, 0)";
		view.rerender(<Panel open={false} />);
		expect(recorded.map(transformOf)).toContainEqual([
			"translateX(362px)",
			`translateX(${PANEL_WIDTH + 24}px)`,
		]);
		expect(recorded.map(opacityOf)).toContainEqual([0.5, 0]);
		// 150ms scaled by how much of the travel is left (0.5 of the opacity).
		expect(recorded.every((r) => r.duration === 75)).toBe(true);
	});

	test("re-rendering with the same state starts no new animation", () => {
		const view = render(<Panel open />);
		const count = recorded.length;
		view.rerender(<Panel open />);
		view.rerender(<Panel open reduced />);
		expect(recorded).toHaveLength(count);
	});

	test("opening again after a close slides back in", () => {
		const view = render(<Panel open />);
		const panel = view.getByTestId("panel");
		panel.style.opacity = "1";
		panel.style.transform = "none";
		view.rerender(<Panel open={false} />);
		recorded.length = 0;
		panel.style.opacity = "0";
		panel.style.transform = `matrix(1, 0, 0, 1, ${PANEL_WIDTH + 24}, 0)`;
		view.rerender(<Panel open />);
		expect(recorded.map(transformOf)).toContainEqual([
			`translateX(${PANEL_WIDTH + 24}px)`,
			"none",
		]);
		expect(recorded.map(opacityOf)).toContainEqual([0, 1]);
	});

	test("a keyboard-driven panel (data-instant) is simply there: no animation, no leftover styles", () => {
		const view = render(<Panel open={false} instant />);
		const panel = view.getByTestId("panel");
		// The first mount asks for an open and finds data-instant.
		expect(recorded).toHaveLength(0);
		panel.style.transform = "translateX(10px)";
		panel.style.opacity = "0.3";
		view.rerender(<Panel open instant />);
		expect(recorded).toHaveLength(0);
		expect(panel.style.transform).toBe("");
		expect(panel.style.opacity).toBe("");
	});

	test("unmounting stops the running animation and a remount slides in again", () => {
		const view = render(<Panel open />);
		view.unmount();
		recorded.length = 0;
		render(<Panel open />);
		expect(recorded.map(opacityOf)).toContainEqual([0, 1]);
	});
});
