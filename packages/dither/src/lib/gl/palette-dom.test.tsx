import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { cleanup, render, renderHook, waitFor } from "@testing-library/react";
import { useRef } from "react";

// palette.ts caches its probe canvas at module level and bun shares modules
// across test files, so each test imports a private copy.
let copy = 0;
async function freshPalette(): Promise<typeof import("./palette")> {
	copy += 1;
	return import(`./palette?copy=${copy}`);
}

afterEach(() => {
	// Unmount before resetting <html>, so the theme observers do not fire into
	// a mounted hook outside act().
	cleanup();
	const root = document.documentElement;
	root.removeAttribute("style");
	root.removeAttribute("class");
	root.removeAttribute("data-theme");
});

function host(vars: Record<string, string> = {}) {
	const el = document.createElement("div");
	for (const [name, value] of Object.entries(vars))
		el.style.setProperty(name, value);
	document.body.appendChild(el);
	return el;
}

describe("resolvePaletteColors", () => {
	test("resolves tokens, names and literals in the host's context", async () => {
		const { resolvePaletteColors } = await freshPalette();
		const el = host({ "--dither-blue": "#0000ff", "--chart-1": "#ff0000" });
		const colors = resolvePaletteColors(el, [
			"blue",
			"--chart-1",
			"#00ff00",
			"rgb(0, 0, 0)",
		]);
		expect(colors).toEqual([
			[0, 0, 1, 1],
			[1, 0, 0, 1],
			[0, 1, 0, 1],
			[0, 0, 0, 1],
		]);
	});

	test("a wrapper override beats the page, and a missing token uses its fallback", async () => {
		const { resolvePaletteColors } = await freshPalette();
		document.documentElement.style.setProperty("--dither-green", "#00ff00");
		const overridden = host({ "--dither-green": "#0000ff" });
		const plain = host();
		expect(resolvePaletteColors(overridden, ["green"])[0]).toEqual([
			0, 0, 1, 1,
		]);
		expect(resolvePaletteColors(plain, ["green"])[0]).toEqual([0, 1, 0, 1]);
		// `grey` is not defined anywhere: the built-in fallback #7a818d applies.
		const [r, g, b] = resolvePaletteColors(plain, ["grey"])[0] ?? [];
		expect([r, g, b].map((c) => Math.round((c ?? 0) * 255))).toEqual([
			0x7a, 0x81, 0x8d,
		]);
	});

	test("leaves no probe element behind", async () => {
		const { resolvePaletteColors } = await freshPalette();
		const el = host();
		resolvePaletteColors(el, ["#fff", "#000"]);
		expect(el.children).toHaveLength(0);
	});

	test("colours the parser cannot read are read back through a canvas", async () => {
		const painted: string[] = [];
		const ctx = {
			clearRect() {},
			fillRect() {},
			set fillStyle(v: string) {
				painted.push(v);
			},
			getImageData: () => ({
				data: new Uint8ClampedArray([51, 102, 204, 255]),
			}),
		};
		const getContext = spyOn(
			HTMLCanvasElement.prototype,
			"getContext",
		).mockImplementation((() => ctx) as never);
		try {
			const { resolvePaletteColors } = await freshPalette();
			const [c] = resolvePaletteColors(host(), ["hsl(120 100% 25%)"]);
			expect(c?.[0]).toBeCloseTo(0.2, 6);
			expect(c?.[1]).toBeCloseTo(0.4, 6);
			expect(c?.[2]).toBeCloseTo(0.8, 6);
			expect(c?.[3]).toBe(1);
			// Reset to black first so an unparsable colour cannot keep the last one.
			expect(painted[0]).toBe("#000");
			expect(painted).toContain("hsl(120 100% 25%)");
			// The probe canvas is made once, however many colours are read.
			const { resolvePaletteColors: again } = await freshPalette();
			again(host(), ["hsl(120 100% 25%)", "hsl(240 100% 25%)"]);
			expect(getContext).toHaveBeenCalledTimes(2);
		} finally {
			getContext.mockRestore();
		}
	});

	test("without a 2d context an unparsable colour is opaque black", async () => {
		const getContext = spyOn(
			HTMLCanvasElement.prototype,
			"getContext",
		).mockImplementation((() => null) as never);
		try {
			const { resolvePaletteColors } = await freshPalette();
			expect(resolvePaletteColors(host(), ["hsl(120 100% 25%)"])).toEqual([
				[0, 0, 0, 1],
			]);
		} finally {
			getContext.mockRestore();
		}
	});
});

describe("useResolvedPalette", () => {
	async function mount(palette?: readonly string[]) {
		const { useResolvedPalette } = await freshPalette();
		// biome-ignore lint/correctness/noComponentHookFactories: the component closes over the per-test module copy
		function Probe({ entries }: { entries?: readonly string[] | undefined }) {
			const ref = useRef<HTMLDivElement>(null);
			const colors = useResolvedPalette(ref, entries);
			return (
				<div
					ref={ref}
					data-testid="host"
					style={{ "--chart-1": "#ff0000" } as React.CSSProperties}
				>
					<output>{JSON.stringify(colors)}</output>
				</div>
			);
		}
		const view = render(<Probe entries={palette} />);
		const read = () =>
			JSON.parse(view.container.querySelector("output")?.textContent ?? "[]");
		return { view, read, Probe };
	}

	test("resolves the given entries after mount", async () => {
		const { read } = await mount(["#000000", "--chart-1"]);
		expect(read()).toEqual([
			[0, 0, 0, 1],
			[1, 0, 0, 1],
		]);
	});

	test("defaults to paper and ink, falling back to the built-in theme", async () => {
		const { read } = await mount(undefined);
		const [paper, ink] = read();
		expect(paper.slice(0, 3).map((c: number) => Math.round(c * 255))).toEqual([
			0x12, 0x14, 0x17,
		]);
		expect(ink.slice(0, 3).map((c: number) => Math.round(c * 255))).toEqual([
			0x6d, 0x6b, 0xff,
		]);
	});

	test("an empty palette is the default palette", async () => {
		const { read } = await mount([]);
		expect(read()).toHaveLength(2);
		expect(read()[1]?.[2]).toBe(1);
	});

	test("re-resolves when the entries change", async () => {
		const { view, read, Probe } = await mount(["#000000", "#ffffff"]);
		expect(read()).toEqual([
			[0, 0, 0, 1],
			[1, 1, 1, 1],
		]);
		view.rerender(<Probe entries={["#ff0000", "#00ff00", "#0000ff"]} />);
		await waitFor(() => expect(read()).toHaveLength(3));
		expect(read()[2]).toEqual([0, 0, 1, 1]);
	});

	test("follows a theme switch on <html>", async () => {
		const { read } = await mount(["--dither-ink"]);
		const before = read()[0];
		document.documentElement.style.setProperty("--dither-ink", "#112233");
		await waitFor(() => expect(read()[0]).not.toEqual(before));
		expect(
			read()[0]
				.slice(0, 3)
				.map((c: number) => Math.round(c * 255)),
		).toEqual([0x11, 0x22, 0x33]);
	});

	test("stops observing the theme after unmount", async () => {
		const { view } = await mount(["--dither-ink"]);
		view.unmount();
		const resolved = spyOn(window, "getComputedStyle");
		try {
			document.documentElement.style.setProperty("--dither-ink", "#112233");
			await Promise.resolve();
			await Promise.resolve();
			expect(resolved).not.toHaveBeenCalled();
		} finally {
			resolved.mockRestore();
		}
	});
});

describe("useDitherColors", () => {
	test("is null before the host exists", async () => {
		const { useDitherColors } = await freshPalette();
		const { result } = renderHook(() => useDitherColors({ current: null }));
		expect(result.current).toBeNull();
	});

	test("returns ink then paper by default and the named colours when asked", async () => {
		const { useDitherColors } = await freshPalette();
		// biome-ignore lint/correctness/noComponentHookFactories: the component closes over the per-test module copy
		function Probe({ names }: { names?: readonly string[] }) {
			const ref = useRef<HTMLDivElement>(null);
			const colors = useDitherColors(ref, names);
			return (
				<div
					ref={ref}
					style={
						{
							"--dither-ink": "#102030",
							"--dither-paper": "#fafafa",
							"--dither-red": "#ff0000",
						} as React.CSSProperties
					}
				>
					<output>{JSON.stringify(colors)}</output>
				</div>
			);
		}
		const view = render(<Probe />);
		const read = () =>
			JSON.parse(view.container.querySelector("output")?.textContent ?? "null");
		const rounded = (c: number[]) =>
			c.slice(0, 3).map((v) => Math.round(v * 255));
		expect(rounded(read()[0])).toEqual([0x10, 0x20, 0x30]);
		expect(rounded(read()[1])).toEqual([0xfa, 0xfa, 0xfa]);

		view.rerender(<Probe names={["red"]} />);
		await waitFor(() => expect(read()).toHaveLength(1));
		expect(read()[0]).toEqual([1, 0, 0, 1]);
	});

	test("follows a theme switch and stops after unmount", async () => {
		const { useDitherColors } = await freshPalette();
		// biome-ignore lint/correctness/noComponentHookFactories: the component closes over the per-test module copy
		function Probe() {
			const ref = useRef<HTMLDivElement>(null);
			const colors = useDitherColors(ref, ["--dither-ink"]);
			return (
				<div ref={ref}>
					<output>{JSON.stringify(colors)}</output>
				</div>
			);
		}
		const view = render(<Probe />);
		const read = () =>
			JSON.parse(view.container.querySelector("output")?.textContent ?? "null");
		document.documentElement.style.setProperty("--dither-ink", "#112233");
		await waitFor(() =>
			expect(
				read()[0]
					.slice(0, 3)
					.map((c: number) => Math.round(c * 255)),
			).toEqual([0x11, 0x22, 0x33]),
		);
		view.unmount();
		const resolved = spyOn(window, "getComputedStyle");
		try {
			document.documentElement.style.setProperty("--dither-ink", "#445566");
			await Promise.resolve();
			await Promise.resolve();
			expect(resolved).not.toHaveBeenCalled();
		} finally {
			resolved.mockRestore();
		}
	});
});
