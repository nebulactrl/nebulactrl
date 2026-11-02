import { describe, expect, test } from "bun:test";
import { act, render, renderHook } from "@testing-library/react";
import { type DitherFill, ditherFillId } from "./pattern";

// pattern.ts keeps a page-wide registry and one hidden <svg role="img" aria-label="patterns"> host at module
// level, and bun shares modules across test files, so each test gets its own
// copy of the module.
let copy = 0;
async function freshPattern(): Promise<typeof import("./pattern")> {
	copy += 1;
	return import(`./pattern?copy=${copy}`);
}

const half = { tone: 0.5, color: "var(--dither-ink)", cell: 3 };
const full = { tone: 1, color: "red" };

const flush = () => act(async () => {});

describe("useDitherFillUrl", () => {
	test("returns url(#id) for the fill's id and mounts a hidden host with its pattern", async () => {
		const { useDitherFillUrl } = await freshPattern();
		const { result } = renderHook(() => useDitherFillUrl(half));
		expect(result.current).toBe(`url(#${ditherFillId(half)})`);

		await flush();
		const host = document.body.querySelector("svg[aria-hidden='true']");
		expect(host).not.toBeNull();
		expect(host?.getAttribute("width")).toBe("0");
		expect(host?.querySelector(`pattern#${ditherFillId(half)}`)).not.toBeNull();
	});

	test("equal fills share one id and one pattern", async () => {
		const { useDitherFillUrl } = await freshPattern();
		const a = renderHook(() => useDitherFillUrl({ ...half }));
		const b = renderHook(() => useDitherFillUrl({ ...half }));
		expect(a.result.current).toBe(b.result.current);
		await flush();
		expect(document.body.querySelectorAll("pattern")).toHaveLength(1);
	});

	test("different fills register separately", async () => {
		const { useDitherFillUrl } = await freshPattern();
		const a = renderHook(() => useDitherFillUrl(half));
		const b = renderHook(() => useDitherFillUrl(full));
		expect(a.result.current).not.toBe(b.result.current);
		await flush();
		expect(document.body.querySelectorAll("pattern")).toHaveLength(2);
	});

	test("a solid fill renders a rect and a sparse one a path in the host", async () => {
		const { useDitherFillUrl } = await freshPattern();
		renderHook(() => useDitherFillUrl(full));
		renderHook(() => useDitherFillUrl(half));
		await flush();
		const solid = document.getElementById(ditherFillId(full));
		const sparse = document.getElementById(ditherFillId(half));
		expect(solid?.querySelector("rect")).not.toBeNull();
		expect(sparse?.querySelector("path")).not.toBeNull();
	});
});

describe("useDitherFillUrls", () => {
	test("maps every fill to its url and registers them all", async () => {
		const { useDitherFillUrls } = await freshPattern();
		const fills = [half, full];
		const { result } = renderHook(() => useDitherFillUrls(fills));
		expect(result.current).toEqual([
			`url(#${ditherFillId(half)})`,
			`url(#${ditherFillId(full)})`,
		]);
		await flush();
		expect(document.body.querySelectorAll("pattern")).toHaveLength(2);
	});

	test("keeps the same array while the fills are equal by value", async () => {
		const { useDitherFillUrls } = await freshPattern();
		const { result, rerender } = renderHook(
			({ fills }) => useDitherFillUrls(fills),
			{ initialProps: { fills: [half] as DitherFill[] } },
		);
		const first = result.current;
		rerender({ fills: [{ ...half }] });
		expect(result.current).toBe(first);
		rerender({ fills: [half, full] });
		expect(result.current).toHaveLength(2);
	});
});

describe("DitherDefs", () => {
	test("renders registered patterns in place of the self-mounted host", async () => {
		const { DitherDefs, useDitherFillUrl } = await freshPattern();
		// biome-ignore lint/correctness/noComponentHookFactories: the component closes over the per-test module copy
		function User() {
			return <i data-url={useDitherFillUrl(half)} />;
		}
		const { container } = render(
			<DitherDefs>
				<User />
			</DitherDefs>,
		);
		await flush();
		// Only the provider's own svg exists: the fallback host was removed.
		const svgs = document.body.querySelectorAll("svg");
		expect(svgs).toHaveLength(1);
		expect(container.contains(svgs[0] ?? null)).toBe(true);
		expect(
			container.querySelector(`defs pattern#${ditherFillId(half)}`),
		).not.toBeNull();
		expect(container.querySelector("i")?.getAttribute("data-url")).toBe(
			`url(#${ditherFillId(half)})`,
		);
	});

	test("patterns registered after mount show up in the provider", async () => {
		const { DitherDefs, useDitherFillUrl } = await freshPattern();
		const { container } = render(<DitherDefs />);
		expect(container.querySelectorAll("pattern")).toHaveLength(0);
		renderHook(() => useDitherFillUrl(full));
		await flush();
		expect(
			container.querySelector(`pattern#${ditherFillId(full)} rect`),
		).not.toBeNull();
	});

	test("restores the self-mounted host when the provider unmounts", async () => {
		const { DitherDefs, useDitherFillUrl } = await freshPattern();
		renderHook(() => useDitherFillUrl(half));
		const { unmount } = render(<DitherDefs />);
		await flush();
		expect(document.body.querySelectorAll("svg")).toHaveLength(1);
		unmount();
		await flush();
		const host = document.body.querySelector("svg[aria-hidden='true']");
		expect(host).not.toBeNull();
		expect(host?.querySelector(`pattern#${ditherFillId(half)}`)).not.toBeNull();
	});
});

describe("DitherPatternDefs", () => {
	test("renders one pattern per fill: rect for solid, path for partial, empty for none", async () => {
		const { DitherPatternDefs } = await freshPattern();
		const fills = [full, half, { tone: 0, color: "blue" }];
		const { container } = render(
			<svg role="img" aria-label="patterns">
				<DitherPatternDefs fills={fills} />
			</svg>,
		);
		const patterns = container.querySelectorAll("defs > pattern");
		expect(patterns).toHaveLength(3);
		expect(patterns[0]?.id).toBe(ditherFillId(full));
		expect(patterns[0]?.querySelector("rect")?.getAttribute("width")).toBe(
			"12",
		);
		expect(patterns[1]?.querySelector("path")?.getAttribute("transform")).toBe(
			"scale(3)",
		);
		expect(patterns[2]?.children).toHaveLength(0);
		for (const p of patterns) {
			expect(p.getAttribute("patternUnits")).toBe("userSpaceOnUse");
			expect(p.getAttribute("width")).toBe("12");
		}
	});

	test("dots are anti-aliased, squares crisp", async () => {
		const { DitherPatternDefs } = await freshPattern();
		const { container } = render(
			<svg role="img" aria-label="patterns">
				<DitherPatternDefs
					fills={[
						{ tone: 0.5, color: "red", shape: "dot" },
						{ tone: 1, color: "red", shape: "dot" },
						{ tone: 0.5, color: "red" },
					]}
				/>
			</svg>,
		);
		const [dot, solidDot, square] = container.querySelectorAll("pattern");
		expect(dot?.getAttribute("shape-rendering")).toBeNull();
		// Tone 1 as dots is still circles, never a filled rect.
		expect(solidDot?.querySelector("rect")).toBeNull();
		expect(solidDot?.querySelector("path")).not.toBeNull();
		expect(square?.getAttribute("shape-rendering")).toBe("crispEdges");
	});
});

describe("DitherPattern", () => {
	test("is a pattern with the given id sized n × cell", async () => {
		const { DitherPattern } = await freshPattern();
		const { container } = render(
			<svg role="img" aria-label="patterns">
				<DitherPattern id="half" level={0.5} cell={2} matrix="bayer2" />
			</svg>,
		);
		const pattern = container.querySelector("pattern");
		expect(pattern?.id).toBe("half");
		expect(pattern?.getAttribute("width")).toBe("4");
		expect(pattern?.getAttribute("height")).toBe("4");
		expect(pattern?.getAttribute("shape-rendering")).toBe("crispEdges");
		expect(pattern?.querySelector("path")?.getAttribute("transform")).toBe(
			"scale(2)",
		);
	});

	test("resolves colour names to their tokens and fills solid at level 1", async () => {
		const { DitherPattern } = await freshPattern();
		const { container } = render(
			<svg role="img" aria-label="patterns">
				<DitherPattern id="solid" level={1} color="blue" />
			</svg>,
		);
		const rect = container.querySelector("pattern > rect");
		expect(rect).not.toBeNull();
		expect((rect as SVGElement).style.fill).toBe("var(--dither-blue)");
	});

	test("level 0 leaves only the background, drawn behind the ink", async () => {
		const { DitherPattern } = await freshPattern();
		const { container } = render(
			<svg role="img" aria-label="patterns">
				<DitherPattern id="empty" level={0} background="paper" />
				<DitherPattern id="both" level={0.5} background="paper" />
			</svg>,
		);
		const [empty, both] = container.querySelectorAll("pattern");
		expect(empty?.children).toHaveLength(1);
		expect((empty?.firstElementChild as SVGElement | null)?.style.fill).toBe(
			"var(--dither-paper)",
		);
		expect(both?.children).toHaveLength(2);
		expect(both?.firstElementChild?.tagName).toBe("rect");
		expect(both?.lastElementChild?.tagName).toBe("path");
	});

	test("a transparent pattern without background or ink is empty; dots skip crispEdges", async () => {
		const { DitherPattern } = await freshPattern();
		const { container } = render(
			<svg role="img" aria-label="patterns">
				<DitherPattern id="none" level={0} />
				<DitherPattern id="dots" level={0.5} shape="dot" />
			</svg>,
		);
		const [none, dots] = container.querySelectorAll("pattern");
		expect(none?.children).toHaveLength(0);
		expect(dots?.getAttribute("shape-rendering")).toBeNull();
		expect(dots?.querySelector("path")).not.toBeNull();
	});
});
