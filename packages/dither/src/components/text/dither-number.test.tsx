import { afterEach, describe, expect, mock, spyOn, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import { installChartEnv } from "../charts/test-env";
import {
	DitherNumber,
	type DitherNumberProps,
	numberCell,
} from "./dither-number";
import { maskUrl } from "./masks";

const env = installChartEnv();

interface Played {
	el: HTMLElement;
	frames: Record<string, unknown>[];
	options: KeyframeAnimationOptions;
	cancel: ReturnType<typeof mock>;
	finish: () => Promise<void>;
}

let spies: { mockRestore: () => void }[] = [];
afterEach(() => {
	for (const s of spies) s.mockRestore();
	spies = [];
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

function recordAnimations(): Played[] {
	const played: Played[] = [];
	spies.push(
		spyOn(Element.prototype, "animate").mockImplementation(function (
			this: HTMLElement,
			frames: Keyframe[] | PropertyIndexedKeyframes | null,
			options?: number | KeyframeAnimationOptions,
		) {
			let resolve: () => void = () => {};
			const finished = new Promise<Animation>((res) => {
				resolve = () => res({} as Animation);
			});
			const cancel = mock(() => {});
			played.push({
				el: this,
				frames: frames as Record<string, unknown>[],
				options: options as KeyframeAnimationOptions,
				cancel,
				finish: async () => {
					resolve();
					await finished;
					await Promise.resolve();
				},
			});
			return { cancel, finished } as unknown as Animation;
		} as unknown as Element["animate"]),
	);
	return played;
}

const number = (props: Partial<DitherNumberProps> = {}) =>
	(<DitherNumber data-testid="n" value={0} {...props} />) as React.ReactElement;
const visual = (c: HTMLElement) =>
	c.querySelector("span[aria-hidden='true']") as HTMLElement;
const digits = (c: HTMLElement) => [
	...c.querySelectorAll<HTMLElement>("[data-digit]"),
];
const shownText = (c: HTMLElement) =>
	digits(c)
		.map((d) => d.firstElementChild?.textContent)
		.join("");
const strips = (played: Played[]) =>
	played.filter(
		(p) =>
			p.el.className.includes("whitespace-pre") &&
			"transform" in (p.frames[0] ?? {}),
	);

describe("numberCell", () => {
	test("is the font size / 30, at least 1px", () => {
		expect(numberCell(60)).toBe(2);
		expect(numberCell(90)).toBe(3);
		expect(numberCell(16)).toBe(1);
	});

	test("an unusable font size gives 2", () => {
		expect(numberCell(Number.NaN)).toBe(2);
		expect(numberCell(0)).toBe(2);
		expect(numberCell(-4)).toBe(2);
	});
});

describe("DitherNumber text", () => {
	test("the formatted value is always in the DOM as screen-reader text; the digits are decoration", () => {
		const { container } = render(number({ value: 12345 }));
		expect(container.querySelector(".sr-only")).toHaveTextContent("12,345");
		expect(visual(container)).toHaveTextContent("12,345");
		expect(digits(container)).toHaveLength(5);
	});

	test("only plain digits are animated slots; separators are literals", () => {
		const { container } = render(
			number({ value: 1234567.5, format: { maximumFractionDigits: 1 } }),
		);
		expect(digits(container).map((d) => d.textContent)).toEqual([
			"1",
			"2",
			"3",
			"4",
			"5",
			"6",
			"7",
			"5",
		]);
		expect(visual(container).textContent).toBe("1,234,567.5");
	});

	test("Intl format options: currency, percent, compact", () => {
		const usd = render(
			number({ value: 1234.5, format: { style: "currency", currency: "USD" } }),
		);
		expect(usd.container.querySelector(".sr-only")).toHaveTextContent(
			"$1,234.50",
		);
		expect(digits(usd.container)).toHaveLength(6);
		usd.unmount();
		const pct = render(number({ value: 0.256, format: { style: "percent" } }));
		expect(pct.container.querySelector(".sr-only")).toHaveTextContent("26%");
		pct.unmount();
		const compact = render(
			number({ value: 1500, format: { notation: "compact" } }),
		);
		expect(compact.container.querySelector(".sr-only")).toHaveTextContent(
			"1.5K",
		);
		expect(digits(compact.container)).toHaveLength(2);
	});

	test("locales and locale format the digits for that region", () => {
		const de = render(number({ value: 1234.5, locales: "de-DE" }));
		expect(de.container.querySelector(".sr-only")).toHaveTextContent("1.234,5");
		de.unmount();
		const fr = render(number({ value: 1234.5, locale: "en-IN" }));
		expect(fr.container.querySelector(".sr-only")).toHaveTextContent("1,234.5");
	});

	test("prefix and suffix join the text and the screen-reader copy but are not animated", () => {
		const { container } = render(
			number({
				value: 42,
				prefix: "+",
				suffix: " ms",
				affixClassName: "text-ink-3",
			}),
		);
		expect(container.querySelector(".sr-only")).toHaveTextContent("+42 ms");
		expect(digits(container)).toHaveLength(2);
		const affixes = [...visual(container).children].filter((c) =>
			c.className.includes("whitespace-pre"),
		);
		expect(affixes.map((a) => a.textContent)).toEqual(["+", " ms"]);
		expect(affixes.every((a) => a.className.includes("text-ink-3"))).toBe(true);
	});

	test("a negative value keeps its sign as a literal", () => {
		const { container } = render(number({ value: -7 }));
		expect(container.querySelector(".sr-only")).toHaveTextContent("-7");
		expect(digits(container)).toHaveLength(1);
		expect(visual(container).textContent).toBe("-7");
	});

	test("zero, and NaN", () => {
		const zero = render(number({ value: 0 }));
		expect(shownText(zero.container)).toBe("0");
		zero.unmount();
		const nan = render(number({ value: Number.NaN }));
		expect(nan.container.querySelector(".sr-only")).toHaveTextContent("NaN");
		expect(digits(nan.container)).toHaveLength(0);
	});

	test("a new value replaces the digits at once under reduced motion, with the new count of slots", () => {
		const { container, rerender } = render(number({ value: 99 }));
		rerender(number({ value: 100 }));
		expect(shownText(container)).toBe("100");
		expect(container.querySelector(".sr-only")).toHaveTextContent("100");
		rerender(number({ value: 5 }));
		expect(digits(container)).toHaveLength(1);
	});

	test("under reduced motion `from` is ignored: the value is shown at once", () => {
		const { container } = render(
			number({ value: 80, from: 10, trigger: "in-view" }),
		);
		expect(shownText(container)).toBe("80");
	});

	test("size presets, colour, className and extra props", () => {
		render(
			number({
				value: 1,
				size: "xl",
				color: "green",
				className: "mine",
				id: "x",
				"aria-live": "polite",
			}),
		);
		const el = screen.getByTestId("n");
		expect(el.className).toContain("text-8xl");
		expect(el.className).toContain("mine");
		expect(el.className).toContain("tabular-nums");
		expect(el.style.color).toBe("var(--dither-green)");
		expect(el).toHaveAttribute("id", "x");
		expect(el).toHaveAttribute("aria-live", "polite");
	});

	test("without a size the number is sized by the caller", () => {
		render(number({ value: 1 }));
		expect(screen.getByTestId("n").className).not.toMatch(
			/text-(2xl|4xl|6xl|8xl)/,
		);
	});

	test("tick pops the whole value in as one block of plain text", () => {
		const { container } = render(number({ value: 123, effect: "tick" }));
		expect(digits(container)).toHaveLength(0);
		expect(visual(container).textContent).toBe("123");
		expect(visual(container).className).toContain("inline-block");
	});
});

describe("DitherNumber roll", () => {
	test("starts at `from` and, once started, rolls the changed digit up the strip", () => {
		fullMotion();
		const played = recordAnimations();
		const { container } = render(
			number({ value: 8, from: 5, trigger: "mount" }),
		);
		expect(shownText(container)).toBe("8");
		const [roll] = strips(played);
		expect(roll?.el.textContent).toBe("5\n6\n7\n8");
		expect(roll?.frames).toEqual([
			{ transform: "translateY(0)" },
			{ transform: "translateY(-75%)" },
		]);
		expect(roll?.options).toMatchObject({
			duration: 900,
			delay: 0,
			fill: "both",
			easing: "cubic-bezier(0.23, 1, 0.32, 1)",
		});
		expect(roll?.el.style.visibility).toBe("visible");
	});

	test("a falling value rolls down: the strip runs the other way", () => {
		fullMotion();
		const played = recordAnimations();
		render(number({ value: 5, from: 8, trigger: "mount" }));
		const [roll] = strips(played);
		expect(roll?.el.textContent).toBe("5\n6\n7\n8");
		expect(roll?.frames).toEqual([
			{ transform: "translateY(-75%)" },
			{ transform: "translateY(0)" },
		]);
	});

	test("the digits wrap round 9 to 0 on the strip", () => {
		fullMotion();
		const played = recordAnimations();
		render(number({ value: 21, from: 18, trigger: "mount" }));
		expect(strips(played).map((r) => r.el.textContent)).toEqual([
			"1\n2",
			"8\n9\n0\n1",
		]);
	});

	test("only the digits that changed move, left to right, one stagger apart", () => {
		fullMotion();
		const played = recordAnimations();
		const { container } = render(
			number({ value: 199, from: 100, trigger: "mount" }),
		);
		expect(shownText(container)).toBe("199");
		const rolls = strips(played);
		// 1 stays; the tens and the units change, tens first.
		expect(rolls).toHaveLength(2);
		expect(rolls.map((r) => r.options.delay)).toEqual([0, 40]);
	});

	test("stagger and duration override the defaults", () => {
		fullMotion();
		const played = recordAnimations();
		render(
			number({
				value: 22,
				from: 11,
				trigger: "mount",
				stagger: 100,
				duration: 300,
			}),
		);
		const rolls = strips(played);
		expect(rolls.map((r) => r.options.delay)).toEqual([0, 100]);
		expect(rolls.map((r) => r.options.duration)).toEqual([300, 300]);
	});

	test("a place that moved by ten or more spins an extra turn", () => {
		fullMotion();
		const played = recordAnimations();
		render(number({ value: 57, from: 0, trigger: "mount" }));
		const lengths = strips(played).map(
			(r) => r.el.textContent?.split("\n").length,
		);
		expect(lengths).toContain(18);
	});

	test("the accent colour fades back over twice the duration; none turns it off", () => {
		fullMotion();
		const played = recordAnimations();
		render(number({ value: 8, from: 5, trigger: "mount", accent: "green" }));
		const accent = played.filter((p) => "color" in (p.frames[0] ?? {}));
		expect(accent).toHaveLength(1);
		expect(accent[0]?.frames[0]?.color).toBe("var(--dither-green)");
		expect(accent[0]?.options).toMatchObject({
			duration: 1800,
			easing: "ease-out",
		});
		spies.pop()?.mockRestore();
		const none = recordAnimations();
		render(number({ value: 8, from: 5, trigger: "mount", accent: "none" }));
		expect(none.filter((p) => "color" in (p.frames[0] ?? {}))).toHaveLength(0);
	});

	test("the window's edges are dithered out while the strip moves", () => {
		fullMotion();
		recordAnimations();
		const { container } = render(
			number({ value: 8, from: 5, trigger: "mount", cell: 2 }),
		);
		const win = digits(container)[0] as HTMLElement;
		expect(win.style.maskImage).toContain("linear-gradient(#000, #000)");
	});

	test("when the animation finishes the slot returns to a plain digit", async () => {
		fullMotion();
		const played = recordAnimations();
		const { container } = render(
			number({ value: 8, from: 5, trigger: "mount", cell: 2 }),
		);
		const roll = strips(played)[0] as Played;
		await act(async () => {
			await roll.finish();
		});
		const win = digits(container)[0] as HTMLElement;
		expect(win.getAttribute("style")).toBeNull();
		expect(roll.el.textContent).toBe("");
		expect(roll.el.style.visibility).toBe("hidden");
		expect(win.firstElementChild).toHaveTextContent("8");
		expect((win.firstElementChild as HTMLElement).style.visibility).toBe("");
	});

	test("unmounting cancels the running animation", () => {
		fullMotion();
		const played = recordAnimations();
		const view = render(number({ value: 8, from: 5, trigger: "mount" }));
		view.unmount();
		expect(played.every((p) => p.cancel.mock.calls.length > 0)).toBe(true);
	});

	test("later value changes roll too, without a `from`", () => {
		fullMotion();
		const played = recordAnimations();
		const { rerender } = render(
			number({ value: 3, from: 3, trigger: "mount" }),
		);
		expect(strips(played)).toHaveLength(0);
		rerender(number({ value: 6, from: 3, trigger: "mount" }));
		expect(strips(played)[0]?.el.textContent).toBe("3\n4\n5\n6");
	});

	test("equal from and value shows the number at once with nothing to play", () => {
		fullMotion();
		const played = recordAnimations();
		const { container } = render(
			number({ value: 42, from: 42, trigger: "mount" }),
		);
		expect(shownText(container)).toBe("42");
		expect(played).toHaveLength(0);
	});
});

describe("DitherNumber trigger", () => {
	test("in-view holds the digits at `from` until the number scrolls into view", () => {
		fullMotion();
		const played = recordAnimations();
		const { container } = render(number({ value: 80, from: 10 }));
		expect(shownText(container)).toBe("10");
		expect(container.querySelector(".sr-only")).toHaveTextContent("80");
		expect(played).toHaveLength(0);
		act(() => env.intersect(true));
		expect(shownText(container)).toBe("80");
		expect(strips(played).length).toBeGreaterThan(0);
	});

	test("mount starts without waiting", () => {
		fullMotion();
		const played = recordAnimations();
		render(number({ value: 80, from: 10, trigger: "mount" }));
		expect(strips(played).length).toBeGreaterThan(0);
	});
});

describe("DitherNumber resolve", () => {
	test("swaps the digit cell by cell through complementary Bayer masks", () => {
		fullMotion();
		const played = recordAnimations();
		const { container } = render(
			number({
				value: 8,
				from: 5,
				trigger: "mount",
				effect: "resolve",
				cell: 2,
			}),
		);
		const masks = played.filter((p) => "maskImage" in (p.frames[0] ?? {}));
		expect(masks).toHaveLength(2);
		const [next, old] = masks as [Played, Played];
		expect(next.frames).toHaveLength(17);
		expect(next.frames[0]?.maskImage).toBe(maskUrl(0, 2, 4));
		expect(next.frames[16]?.maskImage).toBe(maskUrl(16, 2, 4));
		// The outgoing digit uses the complement: its first step shows everything, its last nothing.
		expect(old.frames[0]?.maskImage).not.toBe(old.frames[16]?.maskImage);
		expect(next.options).toMatchObject({
			duration: 560,
			delay: 0,
			fill: "both",
			easing: "linear",
		});
		expect(old.el.textContent).toBe("5");
		expect(old.el.style.visibility).toBe("visible");
		expect(shownText(container)).toBe("8");
	});

	test("the dither size follows the matrix", () => {
		fullMotion();
		const played = recordAnimations();
		render(
			number({
				value: 8,
				from: 5,
				trigger: "mount",
				effect: "resolve",
				cell: 2,
				matrix: "bayer8",
			}),
		);
		const first = played.find((p) => "maskImage" in (p.frames[0] ?? {}));
		expect(first?.frames[1]?.maskImage).toBe(maskUrl(1, 2, 8));
	});

	test("accent colours the incoming digit", () => {
		fullMotion();
		const played = recordAnimations();
		render(
			number({
				value: 8,
				from: 5,
				trigger: "mount",
				effect: "resolve",
				accent: "blue",
			}),
		);
		const accent = played.find((p) => "color" in (p.frames[0] ?? {}));
		expect(accent?.frames[0]?.color).toBe("var(--dither-blue)");
		expect(accent?.options.duration).toBe(1120);
	});
});
