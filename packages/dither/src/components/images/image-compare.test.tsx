import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { act, fireEvent, render, waitFor } from "@testing-library/react";
import { useState } from "react";
import { stubCanvas2d } from "../canvas-stub";
import { ImageCompare, type ImageCompareProps } from "./image-compare";
import { installImageEnv, withBox, withMotion } from "./test-env";

let env: Awaited<ReturnType<typeof installImageEnv>>;
beforeEach(async () => {
	env = await installImageEnv(stubCanvas2d);
});
afterEach(() => env.restore());

let n = 0;
const src = (name: string) => `/cmp-${name}-${++n}.png`;

const frame = (view: ReturnType<typeof render>) =>
	view.container.querySelector("fieldset") as HTMLFieldSetElement;
const slider = (view: ReturnType<typeof render>) =>
	view.getByRole("slider") as HTMLElement;
const clip = (view: ReturnType<typeof render>) =>
	(frame(view).querySelector("div.absolute.inset-0") as HTMLElement).style
		.clipPath;

const compare = (props: Partial<ImageCompareProps> = {}) => (
	<ImageCompare
		src={src("img")}
		alt="Before and after"
		hint={false}
		{...props}
	/>
);

describe("ImageCompare structure", () => {
	test("is a labelled image-comparison group with an accessible slider", () => {
		const view = render(compare());
		expect(frame(view)).toHaveAttribute("aria-label", "Before and after");
		expect(frame(view)).toHaveAttribute(
			"aria-roledescription",
			"image comparison",
		);
		const handle = slider(view);
		expect(handle).toHaveAttribute(
			"aria-label",
			"Dithered and Original divider",
		);
		expect(handle).toHaveAttribute("aria-orientation", "horizontal");
		expect(handle).toHaveAttribute("aria-valuemin", "0");
		expect(handle).toHaveAttribute("aria-valuemax", "100");
		expect(handle).toHaveAttribute("aria-valuenow", "50");
		expect(handle).toHaveAttribute("aria-valuetext", "50% dithered");
		expect(handle).toHaveAttribute("tabindex", "0");
	});

	test("the default sides are the dither on the left and the untouched photo on the right", async () => {
		const view = render(compare());
		// The right side is the plain image, hidden from assistive tech.
		const img = frame(view).querySelector(":scope > img") as HTMLImageElement;
		expect(img).toHaveAttribute("alt", "");
		expect(img).toHaveAttribute("aria-hidden", "true");
		expect(img).toHaveClass("object-cover");
		expect(img).toHaveAttribute("draggable", "false");
		// The left side is a dither layer inside the clipped top.
		env.observers.intersect(frame(view), true);
		env.observers.resize(frame(view), 80, 40);
		await waitFor(() =>
			expect(frame(view).querySelector("canvas")).not.toBeNull(),
		);
		expect(
			frame(view).querySelector("div.absolute.inset-0 canvas"),
		).not.toBeNull();
	});

	test("labels name the sides, and custom labels are used in the slider name and value text", () => {
		const view = render(
			compare({ beforeLabel: "Photo", afterLabel: "Dither", defaultValue: 30 }),
		);
		expect(view.getByText("Photo")).toBeInTheDocument();
		expect(view.getByText("Dither")).toBeInTheDocument();
		expect(slider(view)).toHaveAttribute(
			"aria-label",
			"Photo and Dither divider",
		);
		expect(slider(view)).toHaveAttribute("aria-valuetext", "30% photo");
	});

	test("the corner labels are decoration; labels={false} removes them", () => {
		const view = render(compare());
		expect(view.getByText("Dithered")).toHaveAttribute("aria-hidden", "true");
		view.unmount();
		const bare = render(compare({ labels: false }));
		expect(bare.queryByText("Dithered")).toBeNull();
		expect(bare.queryByText("Original")).toBeNull();
	});

	test("a corner label fades out when the divider covers it", () => {
		const view = render(compare({ defaultValue: 10 }));
		expect(view.getByText("Dithered")).toHaveClass("opacity-0");
		expect(view.getByText("Original")).toHaveClass("opacity-100");
		view.unmount();
		const right = render(compare({ defaultValue: 90 }));
		expect(right.getByText("Original")).toHaveClass("opacity-0");
		expect(right.getByText("Dithered")).toHaveClass("opacity-100");
	});

	test("sides can be any look, or both the original", () => {
		const view = render(
			compare({
				before: { kind: "original" },
				after: { kind: "dither", preset: "gameboy" },
			}),
		);
		expect(slider(view)).toHaveAttribute(
			"aria-label",
			"Original and Dithered divider",
		);
		expect(frame(view).querySelectorAll("img")).toHaveLength(1);
	});

	test("fit=contain contains the plain side", () => {
		const view = render(compare({ fit: "contain" }));
		expect(frame(view).querySelector(":scope > img")).toHaveClass(
			"object-contain",
		);
	});

	test("the image's ratio is reserved once it is known, and ratio wins", async () => {
		const view = render(compare());
		env.observers.intersect(frame(view), true);
		await waitFor(() =>
			expect(frame(view).style.aspectRatio).toBe(`${400 / 300} / 1`),
		);
		view.unmount();
		const fixed = render(compare({ ratio: 2 }));
		expect(frame(fixed).style.aspectRatio).toBe("2 / 1");
	});

	test("an aspect class on the box is left alone", async () => {
		const view = render(compare({ className: "aspect-square" }));
		env.observers.intersect(frame(view), true);
		await act(async () => {});
		expect(frame(view).style.aspectRatio).toBe("");
	});
});

function Controlled() {
	const [value, setValue] = useState(40);
	return compare({ value, onValueChange: setValue });
}

describe("ImageCompare divider position", () => {
	test("starts at defaultValue and clips the left side to it", () => {
		const view = render(compare({ defaultValue: 30 }));
		expect(slider(view)).toHaveAttribute("aria-valuenow", "30");
		expect(clip(view)).toBe("inset(0 70% 0 0)");
		expect((slider(view).parentElement as HTMLElement).style.left).toBe("30%");
	});

	test("an out-of-range default is clamped", () => {
		expect(
			slider(render(compare({ defaultValue: 250 }))).getAttribute(
				"aria-valuenow",
			),
		).toBe("100");
	});

	test("a controlled value drives the position and ignores the internal one", () => {
		const changes: number[] = [];
		const view = render(
			compare({ value: 20, onValueChange: (v) => changes.push(v) }),
		);
		fireEvent.keyDown(slider(view), { key: "ArrowRight" });
		expect(changes).toEqual([21]);
		// The parent has not accepted it: the divider stays.
		expect(slider(view)).toHaveAttribute("aria-valuenow", "20");
	});

	test("a controlled parent that accepts the change moves the divider", () => {
		const view = render(<Controlled />);
		fireEvent.keyDown(slider(view), { key: "End" });
		expect(slider(view)).toHaveAttribute("aria-valuenow", "100");
		expect(clip(view)).toBe("inset(0 0% 0 0)");
	});
});

describe("ImageCompare keyboard", () => {
	const press = (
		view: ReturnType<typeof render>,
		key: string,
		shiftKey = false,
	) => fireEvent.keyDown(slider(view), { key, shiftKey });
	const now = (view: ReturnType<typeof render>) =>
		slider(view).getAttribute("aria-valuenow");

	test("arrows move 1%, shift moves 10%", () => {
		const view = render(compare());
		press(view, "ArrowRight");
		expect(now(view)).toBe("51");
		press(view, "ArrowLeft");
		press(view, "ArrowLeft");
		expect(now(view)).toBe("49");
		press(view, "ArrowUp", true);
		expect(now(view)).toBe("59");
		press(view, "ArrowDown", true);
		expect(now(view)).toBe("49");
	});

	test("page keys move 10%, home and end go to the ends", () => {
		const view = render(compare());
		press(view, "PageUp");
		expect(now(view)).toBe("60");
		press(view, "PageDown");
		press(view, "PageDown");
		expect(now(view)).toBe("40");
		press(view, "Home");
		expect(now(view)).toBe("0");
		press(view, "End");
		expect(now(view)).toBe("100");
	});

	test("the divider never leaves 0..100", () => {
		const view = render(compare({ defaultValue: 99 }));
		press(view, "PageUp");
		expect(now(view)).toBe("100");
		press(view, "Home");
		press(view, "ArrowLeft");
		expect(now(view)).toBe("0");
	});

	test("handled keys are consumed so the page does not scroll or the parent react", () => {
		let reached = false;
		const view = render(
			// biome-ignore lint/a11y/noStaticElementInteractions: a stand-in parent that listens for keys
			<div onKeyDown={() => (reached = true)}>{compare()}</div>,
		);
		const handled = fireEvent.keyDown(slider(view), {
			key: "ArrowRight",
			cancelable: true,
		});
		expect(handled).toBe(false);
		expect(reached).toBe(false);
	});

	test("other keys do nothing and are left alone", () => {
		const view = render(compare());
		const notPrevented = fireEvent.keyDown(slider(view), {
			key: "a",
			cancelable: true,
		});
		expect(notPrevented).toBe(true);
		expect(now(view)).toBe("50");
	});

	test("positions are reported with one decimal", () => {
		const changes: number[] = [];
		const view = render(
			compare({ defaultValue: 33.333, onValueChange: (v) => changes.push(v) }),
		);
		press(view, "ArrowRight");
		expect(changes).toEqual([34.3]);
	});
});

describe("ImageCompare pointer", () => {
	function prepare(props: Partial<ImageCompareProps> = {}) {
		const changes: number[] = [];
		const view = render(
			compare({ onValueChange: (v) => changes.push(v), ...props }),
		);
		const box = frame(view);
		withBox(box, { left: 100, width: 200, height: 100 });
		const captured: number[] = [];
		const released: number[] = [];
		box.setPointerCapture = (id: number) => captured.push(id);
		box.releasePointerCapture = (id: number) => released.push(id);
		return { view, box, changes, captured, released };
	}

	test("pressing jumps the divider to the pointer and captures it", () => {
		const { view, box, changes, captured } = prepare();
		fireEvent.pointerDown(box, { clientX: 150, button: 0, pointerId: 4 });
		expect(changes).toEqual([25]);
		expect(captured).toEqual([4]);
		expect(slider(view)).toHaveAttribute("aria-valuenow", "25");
	});

	test("only the primary button grabs the divider", () => {
		const { box, changes, captured } = prepare();
		fireEvent.pointerDown(box, { clientX: 150, button: 2, pointerId: 1 });
		expect(changes).toEqual([]);
		expect(captured).toEqual([]);
	});

	test("dragging follows the pointer exactly, clamped to the box", () => {
		const { box, changes } = prepare();
		fireEvent.pointerDown(box, { clientX: 100, button: 0, pointerId: 1 });
		fireEvent.pointerMove(box, { clientX: 200 });
		fireEvent.pointerMove(box, { clientX: 1000 });
		fireEvent.pointerMove(box, { clientX: -50 });
		expect(changes).toEqual([0, 50, 100, 0]);
	});

	test("moving without pressing does nothing; after release it stops following", () => {
		const { box, changes, released } = prepare();
		fireEvent.pointerMove(box, { clientX: 200 });
		expect(changes).toEqual([]);
		fireEvent.pointerDown(box, { clientX: 200, button: 0, pointerId: 9 });
		fireEvent.pointerUp(box, { pointerId: 9 });
		expect(released).toEqual([9]);
		fireEvent.pointerMove(box, { clientX: 250 });
		expect(changes).toEqual([50]);
	});

	test("a cancelled pointer stops the drag", () => {
		const { box, changes } = prepare();
		fireEvent.pointerDown(box, { clientX: 150, button: 0, pointerId: 1 });
		fireEvent.pointerCancel(box);
		fireEvent.pointerMove(box, { clientX: 250 });
		expect(changes).toEqual([25]);
	});

	test("a box with no width ignores the pointer", () => {
		const { box, changes } = prepare();
		withBox(box, { width: 0, height: 0 });
		fireEvent.pointerDown(box, { clientX: 150, button: 0, pointerId: 1 });
		expect(changes).toEqual([]);
	});

	test("a click glides the divider; a drag does not; keys do not", () => {
		const restore = withMotion();
		try {
			const { view, box } = prepare();
			const handleBox = () => slider(view).parentElement as HTMLElement;
			fireEvent.pointerDown(box, { clientX: 150, button: 0, pointerId: 1 });
			expect(handleBox().style.transition).toContain("left 200ms");
			fireEvent.pointerMove(box, { clientX: 160 });
			expect(handleBox().style.transition).toBe("");
			fireEvent.pointerUp(box, { pointerId: 1 });
			expect(handleBox().style.transition).toContain("left 200ms");
			fireEvent.keyDown(slider(view), { key: "ArrowRight" });
			expect(handleBox().style.transition).toBe("");
		} finally {
			restore();
		}
	});

	test("under reduced motion there is never a glide", () => {
		const { view, box } = prepare();
		fireEvent.pointerDown(box, { clientX: 150, button: 0, pointerId: 1 });
		expect((slider(view).parentElement as HTMLElement).style.transition).toBe(
			"",
		);
	});
});

describe("ImageCompare hint", () => {
	let animate: ReturnType<typeof spyOn>;
	const cancelled: number[] = [];
	let calls: Array<{
		el: Element;
		frames: Keyframe[];
		timing: KeyframeAnimationOptions;
	}>;

	beforeEach(() => {
		calls = [];
		cancelled.length = 0;
		animate = spyOn(Element.prototype, "animate").mockImplementation(function (
			this: Element,
			frames: Keyframe[],
			timing: KeyframeAnimationOptions,
		) {
			calls.push({ el: this, frames, timing });
			const id = calls.length;
			return {
				cancel: () => cancelled.push(id),
				finished: Promise.resolve(),
			} as unknown as Animation;
		} as never);
	});
	afterEach(() => animate.mockRestore());

	test("nudges once when it first comes into view: left, right, home", async () => {
		const restore = withMotion();
		try {
			const view = render(<ImageCompare src={src("hint")} alt="x" />);
			expect(calls).toHaveLength(0);
			env.observers.intersect(frame(view), true);
			expect(calls).toHaveLength(2);
			const [top, handle] = calls;
			expect(top?.frames.map((f) => f.clipPath)).toEqual([
				"inset(0 50% 0 0)",
				"inset(0 59% 0 0)",
				"inset(0 41% 0 0)",
				"inset(0 50% 0 0)",
			]);
			expect(handle?.frames.map((f) => f.left)).toEqual([
				"50%",
				"41%",
				"59%",
				"50%",
			]);
			expect(top?.timing).toMatchObject({
				duration: 1300,
				easing: "ease-in-out",
			});
			await act(async () => {});
		} finally {
			restore();
		}
	});

	test("hint={false} and intro={false} are silent; hint wins over intro", async () => {
		const restore = withMotion();
		try {
			const quiet = render(
				<ImageCompare src={src("q1")} alt="x" hint={false} />,
			);
			env.observers.intersect(frame(quiet), true);
			quiet.unmount();
			const intro = render(
				<ImageCompare src={src("q2")} alt="x" intro={false} />,
			);
			env.observers.intersect(frame(intro), true);
			intro.unmount();
			expect(calls).toHaveLength(0);
			const wins = render(
				<ImageCompare src={src("q3")} alt="x" hint intro={false} />,
			);
			env.observers.intersect(frame(wins), true);
			expect(calls).toHaveLength(2);
			await act(async () => {});
		} finally {
			restore();
		}
	});

	test("no nudge under reduced motion", async () => {
		const view = render(<ImageCompare src={src("rm")} alt="x" />);
		env.observers.intersect(frame(view), true);
		expect(calls).toHaveLength(0);
		await act(async () => {});
	});

	test("the first interaction cancels a running nudge", async () => {
		const restore = withMotion();
		try {
			const view = render(<ImageCompare src={src("cancel")} alt="x" />);
			env.observers.intersect(frame(view), true);
			expect(cancelled).toEqual([]);
			fireEvent.keyDown(slider(view), { key: "ArrowRight" });
			expect(cancelled.sort()).toEqual([1, 2]);
			await act(async () => {});
		} finally {
			restore();
		}
	});
});
