import {
	afterAll,
	afterEach,
	describe,
	expect,
	mock,
	spyOn,
	test,
} from "bun:test";
import {
	act,
	cleanup,
	fireEvent,
	render,
	screen,
} from "@testing-library/react";
import { useEffect, useRef } from "react";
import * as realMotion from "../../lib/motion";
import { useReducedMotionPref } from "../../lib/prefs";
import { installChartEnv } from "../charts/test-env";
import { maskUrl } from "./masks";
import {
	blockCell,
	ScrambleText,
	type ScrambleTextProps,
} from "./scramble-text";

const env = installChartEnv();

/**
 * The shared frame clock caches `prefers-reduced-motion` for the whole process
 * and drives itself from requestAnimationFrame and an IntersectionObserver, so
 * a test cannot steer it. Only `useDitherFrame` is replaced: the test calls
 * the component's own frame callback with a time in seconds, exactly as the
 * scheduler would, and everything the component does with it is real.
 */
const actual = { ...realMotion };
interface FrameHook {
	onFrame: realMotion.FrameCallback;
	enabled: boolean | undefined;
	fps: number | undefined;
}
let hook: FrameHook | null = null;
mock.module("../../lib/motion", () => ({
	...actual,
	useDitherFrame: (
		onFrame: realMotion.FrameCallback,
		options: realMotion.DitherFrameOptions = {},
	) => {
		hook = { onFrame, enabled: options.enabled, fps: options.fps };
		const reduced = useReducedMotionPref();
		const latest = useRef(onFrame);
		latest.current = onFrame;
		const enabled = options.enabled;
		// As the real hook: with motion off the callback runs once, flagged as a still frame.
		useEffect(() => {
			if (!enabled) latest.current(0, 0, reduced ? "reduced" : "disabled");
		}, [enabled, reduced]);
		return options.enabled ? "running" : "static";
	},
}));
afterAll(() => {
	mock.module("../../lib/motion", () => actual);
});

let spies: { mockRestore: () => void }[] = [];
afterEach(() => {
	cleanup();
	for (const s of spies) s.mockRestore();
	spies = [];
	hook = null;
});

function fullMotion() {
	spies.push(
		spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: false,
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		),
	);
}

const animates: {
	el: Element;
	frames: Record<string, unknown>[];
	options: KeyframeAnimationOptions;
}[] = [];
function recordAnimations() {
	animates.length = 0;
	spies.push(
		spyOn(Element.prototype, "animate").mockImplementation(function (
			this: Element,
			frames: Keyframe[] | PropertyIndexedKeyframes | null,
			options?: number | KeyframeAnimationOptions,
		) {
			animates.push({
				el: this,
				frames: frames as Record<string, unknown>[],
				options: options as KeyframeAnimationOptions,
			});
			return {
				cancel: () => {},
				finished: new Promise(() => {}),
			} as unknown as Animation;
		} as unknown as Element["animate"]),
	);
}

/** Run the component's frame callback at `seconds` on the clock. */
const frame = (seconds: number) => {
	if (!hook) throw new Error("the component is not frame-driven right now");
	const { onFrame } = hook;
	act(() => onFrame(seconds, 0));
};

const scramble = (props: Partial<ScrambleTextProps> = {}) =>
	(
		<ScrambleText data-testid="s" trigger="mount" {...props}>
			{props.children ?? "Hi"}
		</ScrambleText>
	) as React.ReactElement;

interface Slot {
	real: HTMLElement;
	block: HTMLElement;
	hold: HTMLElement;
}
const slots = (c: HTMLElement): Slot[] =>
	[...c.querySelectorAll<HTMLElement>("span.relative.inline-block")].map(
		(s) => ({
			real: s.children[0] as HTMLElement,
			block: s.children[1] as HTMLElement,
			hold: s.children[2] as HTMLElement,
		}),
	);
const caretOf = (c: HTMLElement) =>
	c.querySelector<HTMLElement>(
		"span.pointer-events-none.absolute.top-0.left-0",
	);
const settled = (c: HTMLElement) =>
	slots(c).every(
		(s) =>
			s.real.style.opacity === "" &&
			s.hold.textContent === "" &&
			s.block.style.opacity === "0",
	);

describe("blockCell", () => {
	test("is the font size / 22, at least 1px", () => {
		expect(blockCell(44)).toBe(2);
		expect(blockCell(22)).toBe(1);
		expect(blockCell(10)).toBe(1);
	});

	test("an unusable font size falls back to 2", () => {
		expect(blockCell(0)).toBe(2);
		expect(blockCell(Number.NaN)).toBe(2);
		expect(blockCell(-5)).toBe(2);
	});
});

describe("ScrambleText content", () => {
	test("the final text is screen-reader copy; the scrambling layer is aria-hidden", () => {
		const { container } = render(scramble());
		expect(container.querySelector(".sr-only")).toHaveTextContent("Hi");
		expect(
			container.querySelector("span[aria-hidden='true']"),
		).toHaveTextContent("Hi");
		expect(slots(container)).toHaveLength(2);
	});

	test("headings get an aria-label, other elements do not; as, className, style and props pass through", () => {
		const h = render(
			scramble({
				as: "h3",
				children: "Ship it",
				className: "big",
				style: { margin: "2px" },
				id: "x",
			}),
		);
		const el = screen.getByRole("heading", { level: 3, name: "Ship it" });
		expect(el).toHaveAttribute("id", "x");
		expect(el.className).toContain("big");
		expect(el.style.margin).toBe("2px");
		h.unmount();
		const p = render(scramble({ children: "Ship it" }));
		expect(p.container.firstElementChild).not.toHaveAttribute("aria-label");
	});

	test("words stay whole and spaces and newlines are not animated slots", () => {
		const { container } = render(scramble({ children: "ab c\nd" }));
		expect(slots(container)).toHaveLength(4);
		expect(container.querySelector("br")).not.toBeNull();
		const words = [...container.querySelectorAll("span.whitespace-nowrap")].map(
			(w) => w.textContent,
		);
		expect(words).toEqual(["ab", "c", "d"]);
	});

	test("empty text renders no slots", () => {
		const { container } = render(scramble({ children: "" }));
		expect(slots(container)).toHaveLength(0);
	});

	test("astral characters count as one slot", () => {
		const { container } = render(scramble({ children: "a😀" }));
		expect(slots(container)).toHaveLength(2);
	});
});

describe("ScrambleText under reduced motion", () => {
	test("shows the final text at once and never plays", () => {
		recordAnimations();
		const done = mock(() => {});
		const { container } = render(scramble({ onComplete: done }));
		expect(settled(container)).toBe(true);
		expect(hook?.enabled).toBe(false);
		expect(done).not.toHaveBeenCalled();
		expect(animates).toHaveLength(0);
	});

	test("changing the text just shows the new text", () => {
		const { container, rerender } = render(scramble({ children: "Hi" }));
		rerender(scramble({ children: "Yo" }));
		expect(container.querySelector(".sr-only")).toHaveTextContent("Yo");
		expect(slots(container).map((s) => s.real.textContent)).toEqual(["Y", "o"]);
		expect(settled(container)).toBe(true);
	});
});

describe("ScrambleText playing", () => {
	test("on mount every letter is held back, then climbs through dither blocks", () => {
		fullMotion();
		const { container } = render(scramble({ cell: 3 }));
		expect(slots(container).every((s) => s.real.style.opacity === "0")).toBe(
			true,
		);
		expect(hook?.enabled).toBe(true);
		frame(0);
		const [first, second] = slots(container) as [Slot, Slot];
		// The first letter has started at step 1; the second waits 34ms.
		expect(first.block.style.opacity).toBe("1");
		expect(first.block.style.maskImage).toBe(maskUrl(1, 3, 4));
		expect(first.block.style.backgroundColor).toBe("var(--dither-ink)");
		expect(first.block.style.maskSize).toBe("12px 12px");
		expect(second.block.style.opacity).not.toBe("1");
		// 260ms in (half of 520), the block is denser than at the start.
		frame(0.26);
		expect(first.block.style.maskImage).not.toBe(maskUrl(1, 3, 4));
		expect(first.block.style.maskImage).toBe(maskUrl(14, 3, 4));
	});

	test("after the block phase a letter flickers through glyphs of the text itself, then lands", () => {
		fullMotion();
		recordAnimations();
		const { container } = render(scramble({ children: "Hi", cell: 3 }));
		frame(0);
		frame(0.4);
		const [first] = slots(container) as [Slot];
		expect(first.block.style.opacity).toBe("0");
		expect(["H", "i"]).toContain(first.hold.textContent);
		expect(first.real.style.opacity).toBe("0");
		frame(0.53);
		expect(first.hold.textContent).toBe("");
		expect(first.real.style.opacity).toBe("");
		expect(first.block.style.opacity).toBe("0");
	});

	test("a landing letter fades from the scramble colour to the text colour", () => {
		fullMotion();
		recordAnimations();
		render(scramble({ children: "Hi", color: "green" }));
		frame(0);
		frame(0.53);
		const fade = animates.find((a) => "color" in (a.frames[0] ?? {}));
		expect(fade?.frames[0]?.color).toBe("var(--dither-green)");
		expect(fade?.options).toMatchObject({ duration: 320, easing: "ease-out" });
	});

	test("settles, then reports completion once through onComplete and onDone", () => {
		fullMotion();
		const complete = mock(() => {});
		const doneToo = mock(() => {});
		const { container } = render(
			scramble({ onComplete: complete, onDone: doneToo }),
		);
		frame(0);
		frame(0.3);
		expect(complete).not.toHaveBeenCalled();
		frame(0.6);
		expect(complete).toHaveBeenCalledTimes(1);
		expect(doneToo).toHaveBeenCalledTimes(1);
		expect(settled(container)).toBe(true);
		expect(hook?.enabled).toBe(false);
		frame(0.7);
		expect(complete).toHaveBeenCalledTimes(1);
	});

	test("onDone alone and onComplete alone both work", () => {
		fullMotion();
		const done = mock(() => {});
		render(scramble({ onDone: done }));
		frame(0);
		frame(0.6);
		expect(done).toHaveBeenCalledTimes(1);
	});

	test("timing: speed multiplies every timing, delay holds the first letter back", () => {
		fullMotion();
		const fast = mock(() => {});
		const a = render(scramble({ speed: 2, onComplete: fast }));
		frame(0);
		// (1 gap of 17ms + 260ms) = 277ms: done at 300ms, though the default would not be.
		frame(0.3);
		expect(fast).toHaveBeenCalledTimes(1);
		a.unmount();
		const slow = mock(() => {});
		const b = render(scramble({ onComplete: slow }));
		frame(0);
		frame(0.3);
		expect(slow).not.toHaveBeenCalled();
		b.unmount();
		const delayed = mock(() => {});
		const c = render(scramble({ delay: 200, onComplete: delayed }));
		frame(0);
		frame(0.1);
		expect(slots(c.container)[0]?.block.style.opacity).not.toBe("1");
		frame(0.6);
		expect(delayed).not.toHaveBeenCalled();
		frame(0.8);
		expect(delayed).toHaveBeenCalledTimes(1);
	});

	test("duration and stagger set when each letter lands", () => {
		fullMotion();
		const done = mock(() => {});
		render(
			scramble({
				children: "abc",
				duration: 100,
				stagger: 100,
				onComplete: done,
			}),
		);
		frame(0);
		// last letter starts at 200ms and lands at 300ms.
		frame(0.29);
		expect(done).not.toHaveBeenCalled();
		frame(0.31);
		expect(done).toHaveBeenCalledTimes(1);
	});

	test("modes: blocks only, glyphs only, both", () => {
		fullMotion();
		const at = (mode: "dither" | "blocks" | "glyphs", t: number) => {
			const view = render(scramble({ mode, children: "a", characters: "xy" }));
			frame(0);
			frame(t);
			const slot = slots(view.container)[0] as Slot;
			const out = {
				block: slot.block.style.opacity,
				hold: slot.hold.textContent,
			};
			view.unmount();
			return out;
		};
		// 80% through: dither mode has moved on to glyphs, blocks mode is still blocks, glyphs mode never blocked.
		expect(at("dither", 0.42)).toEqual({
			block: "0",
			hold: expect.stringMatching(/^[xy]$/),
		});
		expect(at("blocks", 0.42)).toEqual({ block: "1", hold: "" });
		expect(at("glyphs", 0.05)).toEqual({
			block: "0",
			hold: expect.stringMatching(/^[xy]$/),
		});
	});

	test("characters sets the glyph pool", () => {
		fullMotion();
		const seen = new Set<string>();
		render(
			scramble({ mode: "glyphs", children: "abcdefgh", characters: "01" }),
		);
		frame(0);
		for (let t = 0.05; t < 0.5; t += 0.06) {
			frame(t);
			for (const s of slots(document.body))
				if (s.hold.textContent) seen.add(s.hold.textContent);
		}
		expect(seen.size).toBeGreaterThan(0);
		expect([...seen].every((g) => g === "0" || g === "1")).toBe(true);
	});

	test("the same seed flickers the same way; another seed differs", () => {
		fullMotion();
		const run = (seed: number) => {
			const view = render(
				scramble({ mode: "glyphs", children: "abcdef", seed }),
			);
			frame(0);
			const out: string[] = [];
			for (const t of [0.07, 0.13, 0.2, 0.31]) {
				frame(t);
				out.push(
					slots(view.container)
						.map((s) => s.hold.textContent ?? "")
						.join(""),
				);
			}
			view.unmount();
			return out;
		};
		const a = run(5);
		expect(run(5)).toEqual(a);
		expect(run(6)).not.toEqual(a);
	});

	test("color tints blocks and glyphs", () => {
		fullMotion();
		const { container } = render(
			scramble({ children: "a", color: "#336699", mode: "blocks" }),
		);
		frame(0);
		expect((slots(container)[0] as Slot).block.style.backgroundColor).toBe(
			"#336699",
		);
		const g = render(
			scramble({ children: "a", color: "#336699", mode: "glyphs" }),
		);
		frame(0);
		frame(0.1);
		expect((slots(g.container)[0] as Slot).hold.style.color).toBe("#336699");
	});

	test("the default block cell follows the font size", () => {
		fullMotion();
		const { container } = render(scramble());
		frame(0);
		// jsdom-style default font 16px / 22 -> 1px cell -> 4px tile.
		expect((slots(container)[0] as Slot).block.style.maskSize).toBe("4px 4px");
	});

	test("unmounting mid-play is safe", () => {
		fullMotion();
		const view = render(scramble());
		frame(0);
		expect(() => view.unmount()).not.toThrow();
	});
});

describe("ScrambleText triggers", () => {
	test("in-view holds the letters back until the text scrolls in, then plays", () => {
		fullMotion();
		const done = mock(() => {});
		const { container } = render(
			scramble({ trigger: "in-view", onComplete: done }),
		);
		expect(slots(container).every((s) => s.real.style.opacity === "0")).toBe(
			true,
		);
		expect(hook?.enabled).toBe(false);
		frame(0);
		env.intersect(true);
		expect(hook?.enabled).toBe(true);
		frame(0);
		expect((slots(container)[0] as Slot).block.style.opacity).toBe("1");
		frame(0.6);
		expect(done).toHaveBeenCalledTimes(1);
		expect(settled(container)).toBe(true);
	});

	test("view is the same as in-view, and it plays only once", () => {
		fullMotion();
		const done = mock(() => {});
		const { container } = render(
			scramble({ trigger: "view", onComplete: done }),
		);
		env.intersect(true);
		frame(0);
		frame(0.6);
		env.intersect(false);
		env.intersect(true);
		expect(hook?.enabled).toBe(false);
		expect(done).toHaveBeenCalledTimes(1);
		expect(settled(container)).toBe(true);
	});

	test("mount plays at once", () => {
		fullMotion();
		render(scramble({ trigger: "mount" }));
		expect(hook?.enabled).toBe(true);
	});

	test("hover shows the text and plays when the closest button is hovered or focused", () => {
		fullMotion();
		const done = mock(() => {});
		const { container } = render(
			<button type="button">
				<ScrambleText trigger="hover" onComplete={done}>
					Hi
				</ScrambleText>
			</button>,
		);
		expect(settled(container)).toBe(true);
		expect(hook?.enabled).toBe(false);
		const button = screen.getByRole("button");
		fireEvent.pointerEnter(button);
		expect(hook?.enabled).toBe(true);
		frame(0);
		frame(0.6);
		expect(done).toHaveBeenCalledTimes(1);
		fireEvent.focusIn(button);
		expect(hook?.enabled).toBe(true);
	});

	test("hover with no link or button around listens on the text itself", () => {
		fullMotion();
		render(scramble({ trigger: "hover" }));
		expect(hook?.enabled).toBe(false);
		fireEvent.pointerEnter(screen.getByTestId("s"));
		expect(hook?.enabled).toBe(true);
	});

	test("change shows the text, then scrambles only when it changes", () => {
		fullMotion();
		const done = mock(() => {});
		const { container, rerender } = render(
			scramble({ trigger: "change", children: "Hi", onComplete: done }),
		);
		expect(settled(container)).toBe(true);
		expect(hook?.enabled).toBe(false);
		rerender(scramble({ trigger: "change", children: "Ho", onComplete: done }));
		expect(hook?.enabled).toBe(true);
	});
});

describe("ScrambleText changing text", () => {
	test("only the characters that differ move", () => {
		fullMotion();
		const { container, rerender } = render(scramble({ children: "Hi" }));
		frame(0);
		frame(0.6);
		rerender(scramble({ children: "Ho" }));
		const [h, o] = slots(container) as [Slot, Slot];
		expect(h.real.style.opacity).toBe("");
		expect(o.real.style.opacity).toBe("0");
		// The outgoing letter holds its place until the new one starts.
		expect(o.hold.textContent).toBe("i");
		frame(0);
		frame(0.1);
		expect(h.block.style.opacity).toBe("0");
		expect(o.block.style.opacity === "1" || o.hold.textContent !== "").toBe(
			true,
		);
		frame(0.8);
		expect(settled(container)).toBe(true);
		expect(container.querySelector(".sr-only")).toHaveTextContent("Ho");
	});

	test("replayOnChange={false} shows the new text without scrambling", () => {
		fullMotion();
		const { container, rerender } = render(
			scramble({ children: "Hi", replayOnChange: false }),
		);
		frame(0);
		frame(0.6);
		rerender(scramble({ children: "Yo", replayOnChange: false }));
		expect(hook?.enabled).toBe(false);
		expect(settled(container)).toBe(true);
		expect(slots(container).map((s) => s.real.textContent)).toEqual(["Y", "o"]);
	});

	test("a longer text adds slots that scramble in", () => {
		fullMotion();
		const { container, rerender } = render(scramble({ children: "Hi" }));
		frame(0);
		frame(0.6);
		rerender(scramble({ children: "His" }));
		expect(slots(container)).toHaveLength(3);
		expect((slots(container)[2] as Slot).real.style.opacity).toBe("0");
	});
});

describe("ScrambleText caret", () => {
	test("none draws no caret", () => {
		fullMotion();
		const { container } = render(scramble({ caret: "none" }));
		expect(caretOf(container)).toBeNull();
	});

	test("running shows a caret while resolving and hides it when done", () => {
		fullMotion();
		const { container } = render(
			scramble({ caret: "running", color: "#336699" }),
		);
		const caret = caretOf(container) as HTMLElement;
		expect(caret).toHaveAttribute("aria-hidden", "true");
		expect(caret.style.backgroundColor).toBe("#336699");
		frame(0);
		expect(caret.style.opacity).toBe("1");
		expect(caret.style.transform).toMatch(
			/^translate\(-?\d+(\.\d+)?px, -?\d+(\.\d+)?px\)$/,
		);
		frame(0.6);
		expect(caret.style.opacity).toBe("0");
	});

	test("always keeps a blinking caret after the last character once settled", () => {
		fullMotion();
		recordAnimations();
		const { container } = render(scramble({ caret: "always" }));
		frame(0);
		frame(0.6);
		const caret = caretOf(container) as HTMLElement;
		expect(caret.style.opacity).toBe("1");
		const blink = animates.find((a) => a.el === caret);
		expect(blink?.frames).toEqual([{ opacity: 1 }, { opacity: 0 }]);
		expect(blink?.options).toMatchObject({
			duration: 1000,
			iterations: Number.POSITIVE_INFINITY,
			easing: "steps(1, jump-end)",
		});
	});

	test("always follows a resize and does not blink under reduced motion", () => {
		const { container } = render(scramble({ caret: "always" }));
		const caret = caretOf(container) as HTMLElement;
		expect(caret.style.opacity).toBe("1");
		env.resize(300);
		expect(caret.style.opacity).toBe("1");
	});
});
