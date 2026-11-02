import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { act, render } from "@testing-library/react";
import { useRef } from "react";
import { type ClearBox, type ClearBoxOptions, useClearBox } from "./clear-box";

/** Rects come from `data-rect="left,top,right,bottom"`, read on every measure. */
function installRects() {
	return spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(
		function (this: HTMLElement) {
			const [left = 0, top = 0, right = 0, bottom = 0] = (
				this.dataset.rect ?? "0,0,0,0"
			)
				.split(",")
				.map(Number);
			return {
				left,
				top,
				right,
				bottom,
				width: right - left,
				height: bottom - top,
				x: left,
				y: top,
				toJSON() {},
			} as DOMRect;
		},
	);
}

function installRaf() {
	const g = globalThis as unknown as {
		requestAnimationFrame: typeof requestAnimationFrame;
		cancelAnimationFrame: typeof cancelAnimationFrame;
	};
	const real = [g.requestAnimationFrame, g.cancelAnimationFrame] as const;
	const queue = new Map<number, FrameRequestCallback>();
	let next = 1;
	g.requestAnimationFrame = (cb) => {
		queue.set(next, cb);
		return next++;
	};
	g.cancelAnimationFrame = (h) => {
		queue.delete(h);
	};
	return {
		pending: () => queue.size,
		run() {
			const cbs = [...queue.values()];
			queue.clear();
			act(() => {
				for (const cb of cbs) cb(0);
			});
		},
		restore() {
			[g.requestAnimationFrame, g.cancelAnimationFrame] = real;
		},
	};
}

function installResizeObserver() {
	const g = globalThis as unknown as { ResizeObserver: typeof ResizeObserver };
	const real = g.ResizeObserver;
	const instances: {
		callback: () => void;
		observed: Element[];
		disconnected: boolean;
	}[] = [];
	g.ResizeObserver = class {
		record = {
			callback: () => {},
			observed: [] as Element[],
			disconnected: false,
		};
		constructor(callback: () => void) {
			this.record.callback = callback;
			instances.push(this.record);
		}
		observe(el: Element) {
			this.record.observed.push(el);
		}
		unobserve() {}
		disconnect() {
			this.record.disconnected = true;
		}
	} as unknown as typeof ResizeObserver;
	return {
		instances,
		restore() {
			g.ResizeObserver = real;
		},
	};
}

let rects: ReturnType<typeof installRects>;
let raf: ReturnType<typeof installRaf>;
let ro: ReturnType<typeof installResizeObserver>;
beforeEach(() => {
	rects = installRects();
	raf = installRaf();
	ro = installResizeObserver();
});
afterEach(() => {
	rects.mockRestore();
	raf.restore();
	ro.restore();
});

interface HarnessProps {
	selectors: readonly string[];
	options?: ClearBoxOptions;
	titleRect?: string;
	subRect?: string;
	onBoxes?: (boxes: readonly ClearBox[]) => void;
}

function Harness({
	selectors,
	options,
	titleRect = "100,80,300,120",
	subRect = "100,130,260,150",
	onBoxes,
}: HarnessProps) {
	const host = useRef<HTMLDivElement>(null);
	const boxes = useClearBox(host, selectors, options);
	onBoxes?.(boxes);
	return (
		<div ref={host} data-rect="50,50,850,650" data-testid="host">
			<h1 className="title" data-rect={titleRect}>
				T
			</h1>
			<p className="sub" data-rect={subRect}>
				S
			</p>
			<i className="collapsed" data-rect="10,10,10,10" />
			<output data-testid="boxes">{JSON.stringify(boxes)}</output>
		</div>
	);
}

const boxesOf = (container: HTMLElement): ClearBox[] =>
	JSON.parse(container.querySelector("output")?.textContent ?? "[]");

describe("useClearBox", () => {
	test("measures each selector into a padded box relative to the host", () => {
		const { container } = render(<Harness selectors={[".title", ".sub"]} />);
		// Defaults: pad 16, radius 12, feather 24; host top-left is (50, 50).
		expect(boxesOf(container)).toEqual([
			{ x: 34, y: 14, w: 232, h: 72, radius: 12, feather: 24 },
			{ x: 34, y: 64, w: 192, h: 52, radius: 12, feather: 24 },
		]);
	});

	test("pad, radius and feather options shape the boxes", () => {
		const { container } = render(
			<Harness
				selectors={[".title"]}
				options={{ pad: 0, radius: 4, feather: 0 }}
			/>,
		);
		expect(boxesOf(container)).toEqual([
			{ x: 50, y: 30, w: 200, h: 40, radius: 4, feather: 0 },
		]);
	});

	test("skips selectors that match nothing or measure zero", () => {
		const { container } = render(
			<Harness selectors={[".missing", ".collapsed", ".sub"]} />,
		);
		expect(boxesOf(container)).toHaveLength(1);
		expect(boxesOf(container)[0]).toMatchObject({ x: 34, y: 64 });
	});

	test("no selectors means no boxes, and the empty list keeps its identity", () => {
		const seen: (readonly ClearBox[])[] = [];
		const { rerender } = render(
			<Harness selectors={[]} onBoxes={(b) => seen.push(b)} />,
		);
		expect(seen.every((b) => b.length === 0)).toBe(true);
		rerender(
			<Harness selectors={[".missing"]} onBoxes={(b) => seen.push(b)} />,
		);
		expect(new Set(seen).size).toBe(1);
	});

	test("only the first four selectors are measured", () => {
		const six = [".title", ".sub", ".title", ".sub", ".title", ".sub"];
		const { container } = render(<Harness selectors={six} />);
		expect(boxesOf(container)).toHaveLength(4);
	});

	test("observes the host and every measured element for size changes", () => {
		const { container, getByTestId } = render(
			<Harness selectors={[".title", ".missing"]} />,
		);
		const [observer] = ro.instances;
		expect(observer?.observed[0]).toBe(getByTestId("host"));
		expect(observer?.observed).toContain(
			container.querySelector(".title") as Element,
		);
		expect(observer?.observed).toHaveLength(2);
	});

	test("re-measures when a ResizeObserver fires, replacing stale boxes", () => {
		const { container, rerender } = render(<Harness selectors={[".title"]} />);
		expect(boxesOf(container)[0]?.w).toBe(232);

		// Same selectors, new geometry: nothing re-runs until something notifies.
		rerender(<Harness selectors={[".title"]} titleRect="100,80,200,120" />);
		expect(boxesOf(container)[0]?.w).toBe(232);

		act(() => ro.instances[0]?.callback());
		expect(boxesOf(container)[0]?.w).toBe(132);
	});

	test("keeps the same array while no box changed", () => {
		const seen: (readonly ClearBox[])[] = [];
		const { rerender } = render(
			<Harness selectors={[".title"]} onBoxes={(b) => seen.push(b)} />,
		);
		const first = seen[seen.length - 1];
		rerender(<Harness selectors={[".title"]} onBoxes={(b) => seen.push(b)} />);
		act(() => ro.instances[0]?.callback());
		expect(seen[seen.length - 1]).toBe(first);
	});

	test("window resizes are coalesced into one measure per frame", () => {
		const measured: number[] = [];
		const { container, rerender } = render(
			<Harness
				selectors={[".title"]}
				onBoxes={(b) => measured.push(b.length)}
			/>,
		);
		rerender(
			<Harness
				selectors={[".title"]}
				titleRect="100,80,200,120"
				onBoxes={(b) => measured.push(b.length)}
			/>,
		);
		act(() => {
			window.dispatchEvent(new Event("resize"));
			window.dispatchEvent(new Event("resize"));
		});
		expect(raf.pending()).toBe(1);
		// Nothing is measured until the frame runs.
		expect(boxesOf(container)[0]?.w).toBe(232);
		raf.run();
		expect(boxesOf(container)[0]?.w).toBe(132);
		expect(raf.pending()).toBe(0);
	});

	test("animationend and transitionend on the host re-measure (a transform moves a box)", () => {
		const { container, rerender, getByTestId } = render(
			<Harness selectors={[".title"]} />,
		);
		rerender(<Harness selectors={[".title"]} titleRect="150,80,350,120" />);
		act(() => {
			getByTestId("host").dispatchEvent(new Event("animationend"));
		});
		raf.run();
		expect(boxesOf(container)[0]?.x).toBe(84);

		rerender(<Harness selectors={[".title"]} titleRect="170,80,370,120" />);
		act(() => {
			getByTestId("host").dispatchEvent(new Event("transitionend"));
		});
		raf.run();
		expect(boxesOf(container)[0]?.x).toBe(104);
	});

	test("re-measures once fonts have loaded", async () => {
		const doc = document as unknown as {
			fonts?: { ready: Promise<void> } | undefined;
		};
		const real = doc.fonts;
		let loaded!: () => void;
		doc.fonts = {
			ready: new Promise<void>((resolve) => {
				loaded = resolve;
			}),
		};
		try {
			const { container, rerender } = render(
				<Harness selectors={[".title"]} />,
			);
			rerender(<Harness selectors={[".title"]} titleRect="100,80,220,120" />);
			expect(boxesOf(container)[0]?.w).toBe(232);
			await act(async () => {
				loaded();
				await Promise.resolve();
			});
			expect(boxesOf(container)[0]?.w).toBe(152);
		} finally {
			doc.fonts = real;
		}
	});

	test("a font load after unmount measures nothing", async () => {
		const doc = document as unknown as {
			fonts?: { ready: Promise<void> } | undefined;
		};
		const real = doc.fonts;
		let loaded!: () => void;
		doc.fonts = {
			ready: new Promise<void>((resolve) => {
				loaded = resolve;
			}),
		};
		try {
			const { unmount } = render(<Harness selectors={[".title"]} />);
			const calls = rects.mock.calls.length;
			unmount();
			await act(async () => {
				loaded();
				await Promise.resolve();
			});
			expect(rects.mock.calls.length).toBe(calls);
		} finally {
			doc.fonts = real;
		}
	});

	test("changing the selectors re-measures", () => {
		const { container, rerender } = render(<Harness selectors={[".title"]} />);
		expect(boxesOf(container)).toHaveLength(1);
		rerender(<Harness selectors={[".title", ".sub"]} />);
		expect(boxesOf(container)).toHaveLength(2);
	});

	test("unmount disconnects the observer and stops listening and cancels a pending frame", () => {
		const { unmount } = render(<Harness selectors={[".title"]} />);
		act(() => {
			window.dispatchEvent(new Event("resize"));
		});
		expect(raf.pending()).toBe(1);

		unmount();
		expect(ro.instances[0]?.disconnected).toBe(true);
		expect(raf.pending()).toBe(0);
		act(() => {
			window.dispatchEvent(new Event("resize"));
		});
		expect(raf.pending()).toBe(0);
	});

	test("without a host element nothing is measured", () => {
		// biome-ignore lint/correctness/noComponentHookFactories: the component closes over the per-test module copy
		function Detached() {
			const boxes = useClearBox({ current: null }, [".title"]);
			return <output>{boxes.length}</output>;
		}
		const { container } = render(<Detached />);
		expect(container.querySelector("output")).toHaveTextContent("0");
		expect(ro.instances).toHaveLength(0);
	});

	test("works without ResizeObserver", () => {
		(globalThis as { ResizeObserver?: unknown }).ResizeObserver = undefined;
		const { container } = render(<Harness selectors={[".title"]} />);
		expect(boxesOf(container)).toHaveLength(1);
	});
});
