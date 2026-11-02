import { afterEach, describe, expect, mock, spyOn, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { installChartEnv } from "../charts/test-env";
import { DissolveText, type DissolveTextProps } from "./dissolve-text";
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

/** Full motion: the suite runs under reduced motion by default. */
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

/** Record every Element.animate call; each animation settles when its `finish` is called. */
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

const dissolve = (props: Partial<DissolveTextProps> = {}) =>
	(
		<DissolveText data-testid="d" trigger="mount" {...props}>
			{props.children ?? "Hi there"}
		</DissolveText>
	) as React.ReactElement;

const units = (c: HTMLElement) => [
	...c.querySelectorAll<HTMLElement>("[data-unit]"),
];
const maskAnimations = (played: Played[]) =>
	played.filter((p) => "maskImage" in (p.frames[0] ?? {}));

describe("DissolveText structure", () => {
	test("the real text is screen-reader copy and the masked units are aria-hidden decoration", () => {
		const { container } = render(dissolve());
		expect(container.querySelector(".sr-only")).toHaveTextContent("Hi there");
		const decoration = container.querySelector("span[aria-hidden='true']");
		expect(decoration?.textContent).toBe("Hi there");
		expect(units(container).map((u) => u.textContent)).toEqual([
			"H",
			"i",
			"t",
			"h",
			"e",
			"r",
			"e",
		]);
	});

	test("by=char keeps each word whole so lines wrap at spaces", () => {
		const { container } = render(dissolve());
		const words = [
			...container.querySelectorAll(
				"span[aria-hidden='true'] > span.whitespace-nowrap",
			),
		];
		expect(words.map((w) => w.textContent)).toEqual(["Hi", "there"]);
	});

	test("by=word and by=line animate whole words or lines", () => {
		const word = render(dissolve({ by: "word" }));
		expect(units(word.container).map((u) => u.textContent)).toEqual([
			"Hi",
			"there",
		]);
		word.unmount();
		const line = render(dissolve({ by: "line", children: "one\ntwo" }));
		expect(units(line.container).map((u) => u.textContent)).toEqual([
			"one",
			"two",
		]);
		expect(line.container.querySelector("br")).not.toBeNull();
		expect(units(line.container)[0]?.className).toContain("block");
	});

	test("headings get an aria-label with the text, other elements do not", () => {
		const h = render(dissolve({ as: "h2", children: "Ship it" }));
		expect(
			screen.getByRole("heading", { level: 2, name: "Ship it" }),
		).toBeInTheDocument();
		h.unmount();
		const p = render(dissolve({ children: "Ship it" }));
		expect(p.container.firstElementChild).not.toHaveAttribute("aria-label");
	});

	test("color sets the text colour; className and style are kept", () => {
		render(
			dissolve({ color: "blue", className: "x", style: { margin: "1px" } }),
		);
		const el = screen.getByTestId("d");
		expect(el.style.color).toBe("var(--dither-blue)");
		expect(el.style.margin).toBe("1px");
		expect(el.className).toContain("x");
		expect(el).toHaveAttribute("data-dissolve");
	});

	test("a noscript block in the server markup shows the text when scripts are off", () => {
		const html = renderToStaticMarkup(<DissolveText>Hi</DissolveText>);
		expect(html).toContain("<noscript>");
		expect(html).toContain(
			"[data-dissolve] [data-unit]{-webkit-mask-image:none!important;mask-image:none!important}",
		);
	});

	test("empty text renders no units", () => {
		const { container } = render(dissolve({ children: "" }));
		expect(units(container)).toHaveLength(0);
	});
});

describe("DissolveText under reduced motion", () => {
	test("the text is shown at once, with no mask and no animation", () => {
		const played = recordAnimations();
		const { container } = render(dissolve());
		expect(played).toHaveLength(0);
		for (const u of units(container)) {
			expect(u.style.opacity).toBe("");
			expect(u.style.maskImage).toBe("");
		}
		expect(screen.getByTestId("d")).not.toHaveAttribute("aria-hidden");
	});

	test("show=false hides the units and the element from assistive tech", () => {
		const { container, rerender } = render(dissolve({ show: false }));
		expect(units(container).every((u) => u.style.opacity === "0")).toBe(true);
		expect(screen.getByTestId("d")).toHaveAttribute("aria-hidden", "true");
		rerender(dissolve({ show: true }));
		expect(units(container).every((u) => u.style.opacity === "")).toBe(true);
		expect(screen.getByTestId("d")).not.toHaveAttribute("aria-hidden");
	});
});

describe("DissolveText playing", () => {
	test("units start empty (a fully masked-out step) before anything plays", () => {
		fullMotion();
		recordAnimations();
		const { container } = render(dissolve({ trigger: "in-view", cell: 3 }));
		expect(units(container)[0]?.style.maskImage).toBe(
			maskUrl(0, 3, 4, "square"),
		);
		expect(units(container)[0]?.style.maskSize).toBe("12px 12px");
		expect(screen.getByTestId("d")).toHaveAttribute("aria-hidden", "true");
	});

	test("mount plays at once: one mask animation per unit, stepping 0..16 through the Bayer levels", () => {
		fullMotion();
		const played = recordAnimations();
		render(dissolve({ cell: 3 }));
		const masks = maskAnimations(played);
		expect(masks).toHaveLength(7);
		const first = masks[0] as Played;
		expect(first.frames).toHaveLength(17);
		expect(first.frames[0]?.maskImage).toBe(maskUrl(0, 3, 4, "square"));
		expect(first.frames[16]?.maskImage).toBe(maskUrl(16, 3, 4, "square"));
		expect(first.frames.map((f) => f.offset)).toEqual(
			Array.from({ length: 17 }, (_, k) => k / 16),
		);
		expect(first.options).toMatchObject({
			duration: 600,
			fill: "both",
			easing: "linear",
		});
	});

	test("in-view waits for the element to scroll in, then plays", () => {
		fullMotion();
		const played = recordAnimations();
		render(dissolve({ trigger: "in-view" }));
		expect(played).toHaveLength(0);
		act(() => env.intersect(true));
		expect(maskAnimations(played)).toHaveLength(7);
		expect(screen.getByTestId("d")).not.toHaveAttribute("aria-hidden");
	});

	test("units arrive in order, 28ms apart per character, 90ms per word", () => {
		fullMotion();
		const chars = recordAnimations();
		const a = render(dissolve({ children: "abcd" }));
		expect(maskAnimations(chars).map((p) => p.options.delay)).toEqual([
			0, 28, 56, 84,
		]);
		a.unmount();
		spies.pop()?.mockRestore();
		const words = recordAnimations();
		render(dissolve({ children: "one two three", by: "word" }));
		expect(maskAnimations(words).map((p) => p.options.delay)).toEqual([
			0, 90, 180,
		]);
	});

	test("direction end reverses the arrival order; center starts in the middle", () => {
		fullMotion();
		const end = recordAnimations();
		const a = render(dissolve({ children: "abc", direction: "end" }));
		expect(maskAnimations(end).map((p) => p.options.delay)).toEqual([
			56, 28, 0,
		]);
		a.unmount();
		spies.pop()?.mockRestore();
		const center = recordAnimations();
		render(dissolve({ children: "abc", direction: "center" }));
		expect(maskAnimations(center).map((p) => p.options.delay)).toEqual([
			28, 0, 56,
		]);
	});

	test("random order is a fixed shuffle: every slot used once, the same for the same seed", () => {
		fullMotion();
		const delays = (seed: number) => {
			const played = recordAnimations();
			const view = render(
				dissolve({ children: "abcdef", direction: "random", seed }),
			);
			const out = maskAnimations(played).map((p) => Number(p.options.delay));
			view.unmount();
			spies.pop()?.mockRestore();
			return out;
		};
		const one = delays(7);
		expect([...one].sort((x, y) => x - y)).toEqual([0, 28, 56, 84, 112, 140]);
		expect(delays(7)).toEqual(one);
		expect(delays(8)).not.toEqual(one);
	});

	test("duration, stagger, delay and speed shape the timing", () => {
		fullMotion();
		const played = recordAnimations();
		render(
			dissolve({
				children: "ab",
				duration: 400,
				stagger: 100,
				delay: 50,
				speed: 2,
			}),
		);
		const masks = maskAnimations(played);
		expect(masks.map((p) => p.options.duration)).toEqual([200, 200]);
		expect(masks.map((p) => p.options.delay)).toEqual([25, 75]);
	});

	test("fills end at their own density: gradient level, dotted, hatched", () => {
		fullMotion();
		const steps = (props: Partial<DissolveTextProps>) => {
			const played = recordAnimations();
			const view = render(dissolve({ children: "a", ...props }));
			const n = maskAnimations(played)[0]?.frames.length;
			view.unmount();
			spies.pop()?.mockRestore();
			return n;
		};
		expect(steps({ fill: "solid" })).toBe(17);
		expect(steps({ fill: "gradient" })).toBe(11);
		expect(steps({ fill: "gradient", level: 0.5 })).toBe(9);
		expect(steps({ fill: "gradient", level: 5 })).toBe(17);
		expect(steps({ fill: "dotted" })).toBe(6);
		expect(steps({ fill: "hatched" })).toBe(9);
	});

	test("dotted and hatched fills use their shapes in the mask", () => {
		fullMotion();
		const played = recordAnimations();
		render(dissolve({ children: "a", fill: "dotted", cell: 2 }));
		expect(maskAnimations(played)[0]?.frames[3]?.maskImage).toBe(
			maskUrl(3, 2, 4, "dot"),
		);
		expect(units(document.body)[0]?.style.maskImage).toBe(
			maskUrl(0, 2, 4, "dot"),
		);
	});

	test("matrix picks the Bayer size and so the tile size", () => {
		fullMotion();
		const played = recordAnimations();
		render(dissolve({ children: "a", matrix: "bayer8", cell: 2 }));
		expect(maskAnimations(played)[0]?.frames[1]?.maskImage).toBe(
			maskUrl(1, 2, 8, "square"),
		);
		expect(units(document.body)[0]?.style.maskSize).toBe("16px 16px");
	});

	test("an accent colour is worn while a unit arrives and fades back over twice the duration", () => {
		fullMotion();
		const played = recordAnimations();
		render(dissolve({ children: "ab", accent: "green", duration: 300 }));
		const accents = played.filter((p) => "color" in (p.frames[0] ?? {}));
		expect(accents).toHaveLength(2);
		expect(accents[0]?.frames[0]?.color).toBe("var(--dither-green)");
		expect(accents[0]?.options).toMatchObject({
			duration: 600,
			easing: "ease-out",
		});
		expect(accents.map((p) => p.options.delay)).toEqual([0, 28]);
	});

	test("no accent by default", () => {
		fullMotion();
		const played = recordAnimations();
		render(dissolve({ children: "ab" }));
		expect(played.filter((p) => "color" in (p.frames[0] ?? {}))).toHaveLength(
			0,
		);
	});

	test("every unit finishing settles the masks and calls onDone('in') once", async () => {
		fullMotion();
		const played = recordAnimations();
		const done = mock((_d: "in" | "out") => {});
		const { container } = render(dissolve({ children: "ab", onDone: done }));
		const masks = maskAnimations(played);
		await act(async () => {
			await masks[0]?.finish();
		});
		expect(done).not.toHaveBeenCalled();
		await act(async () => {
			await masks[1]?.finish();
		});
		expect(done).toHaveBeenCalledTimes(1);
		expect(done).toHaveBeenCalledWith("in");
		// A solid dissolve ends with no mask at all.
		for (const u of units(container)) expect(u.style.maskImage).toBe("none");
	});

	test("a gradient dissolve keeps its final mask once settled", async () => {
		fullMotion();
		const played = recordAnimations();
		const { container } = render(
			dissolve({ children: "a", fill: "gradient", cell: 3 }),
		);
		await act(async () => {
			await maskAnimations(played)[0]?.finish();
		});
		expect(units(container)[0]?.style.maskImage).toBe(
			maskUrl(10, 3, 4, "square"),
		);
	});

	test("show=false after showing dissolves out, level 16 down to 0, and says so", async () => {
		fullMotion();
		const played = recordAnimations();
		const done = mock((_d: "in" | "out") => {});
		const { rerender } = render(
			dissolve({ children: "a", onDone: done, cell: 3 }),
		);
		const before = played.length;
		rerender(dissolve({ children: "a", onDone: done, cell: 3, show: false }));
		const out = maskAnimations(played.slice(before));
		expect(out).toHaveLength(1);
		expect(out[0]?.frames[0]?.maskImage).toBe(maskUrl(16, 3, 4, "square"));
		expect(out[0]?.frames.at(-1)?.maskImage).toBe(maskUrl(0, 3, 4, "square"));
		expect(screen.getByTestId("d")).toHaveAttribute("aria-hidden", "true");
		await act(async () => {
			await out[0]?.finish();
		});
		expect(done).toHaveBeenLastCalledWith("out");
		expect(units(document.body)[0]?.style.maskImage).toBe(
			maskUrl(0, 3, 4, "square"),
		);
	});

	test("hidden from the start plays nothing until it is shown", () => {
		fullMotion();
		const played = recordAnimations();
		const { rerender } = render(dissolve({ children: "a", show: false }));
		expect(played).toHaveLength(0);
		rerender(dissolve({ children: "a", show: true }));
		expect(maskAnimations(played)).toHaveLength(1);
	});

	test("visible is the same switch as show, and show wins", () => {
		fullMotion();
		const hidden = recordAnimations();
		render(dissolve({ children: "a", visible: false }));
		expect(hidden).toHaveLength(0);
		const shown = recordAnimations();
		render(dissolve({ children: "a", visible: false, show: true }));
		expect(maskAnimations(shown)).toHaveLength(1);
	});

	test("a new replayKey cancels the running animation and plays again", () => {
		fullMotion();
		const played = recordAnimations();
		const { rerender } = render(dissolve({ children: "a", replayKey: 1 }));
		const first = maskAnimations(played)[0] as Played;
		rerender(dissolve({ children: "a", replayKey: 2 }));
		expect(first.cancel).toHaveBeenCalled();
		expect(maskAnimations(played)).toHaveLength(2);
	});

	test("changing the text plays the new units", () => {
		fullMotion();
		const played = recordAnimations();
		const { rerender } = render(dissolve({ children: "ab" }));
		rerender(dissolve({ children: "abc" }));
		expect(maskAnimations(played).slice(2)).toHaveLength(3);
	});

	test("unmounting cancels what is running", () => {
		fullMotion();
		const played = recordAnimations();
		const view = render(dissolve({ children: "ab" }));
		view.unmount();
		for (const p of played) expect(p.cancel).toHaveBeenCalled();
	});

	test("a rejected animation does not break the component", async () => {
		fullMotion();
		spies.push(
			spyOn(Element.prototype, "animate").mockImplementation((() => ({
				cancel: () => {},
				finished: Promise.reject(new Error("canceled")),
			})) as unknown as Element["animate"]),
		);
		render(dissolve({ children: "a" }));
		await act(async () => {
			await Promise.resolve();
		});
		expect(screen.getByTestId("d")).toHaveTextContent("a");
	});
});
