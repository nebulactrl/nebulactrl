import { afterEach, beforeEach, spyOn } from "bun:test";
import { act } from "@testing-library/react";

/**
 * Test-only environment for the chart components. happy-dom lays nothing out,
 * so a chart never learns its width (ResizeObserver never fires) and is never
 * "seen" (IntersectionObserver never fires). `installChartEnv` registers
 * hooks that stub both observers, give every element a fixed bounding box and
 * hand back the controls a test needs.
 */

export interface ChartEnv {
	/** The width every observed element reports, in px. */
	width: number;
	/** Fire the intersection observers: the chart scrolls in (or out). */
	intersect: (isIntersecting: boolean) => void;
	/** Change the width every resize observer reports and notify them. */
	resize: (width: number) => void;
}

interface ResizeWatch {
	cb: ResizeObserverCallback;
	el: Element;
	self: ResizeObserver;
}

interface IntersectWatch {
	cb: IntersectionObserverCallback;
	el: Element;
	self: IntersectionObserver;
}

const boxOf = (width: number) => ({
	x: 0,
	y: 0,
	left: 0,
	top: 0,
	width,
	height: 200,
	right: width,
	bottom: 200,
});

export function installChartEnv(initial: { width?: number } = {}): ChartEnv {
	const env: ChartEnv = {
		width: initial.width ?? 480,
		intersect: () => {},
		resize: () => {},
	};
	const realResize = globalThis.ResizeObserver;
	const realIntersection = globalThis.IntersectionObserver;
	let boxSpy: { mockRestore: () => void } | undefined;

	beforeEach(() => {
		env.width = initial.width ?? 480;
		const resizers = new Set<ResizeWatch>();
		const watchers = new Set<IntersectWatch>();
		const notify = (r: ResizeWatch) =>
			r.cb(
				[
					{
						target: r.el,
						contentRect: boxOf(env.width),
					} as unknown as ResizeObserverEntry,
				],
				r.self,
			);

		class FakeResizeObserver {
			private mine: ResizeWatch[] = [];
			private readonly cb: ResizeObserverCallback;
			constructor(cb: ResizeObserverCallback) {
				this.cb = cb;
			}
			observe(el: Element) {
				const entry = {
					cb: this.cb,
					el,
					self: this as unknown as ResizeObserver,
				};
				this.mine.push(entry);
				resizers.add(entry);
				notify(entry);
			}
			unobserve() {}
			disconnect() {
				for (const entry of this.mine) resizers.delete(entry);
				this.mine = [];
			}
		}
		class FakeIntersectionObserver {
			private mine: IntersectWatch[] = [];
			private readonly cb: IntersectionObserverCallback;
			constructor(cb: IntersectionObserverCallback) {
				this.cb = cb;
			}
			observe(el: Element) {
				const entry = {
					cb: this.cb,
					el,
					self: this as unknown as IntersectionObserver,
				};
				this.mine.push(entry);
				watchers.add(entry);
			}
			unobserve() {}
			disconnect() {
				for (const entry of this.mine) watchers.delete(entry);
				this.mine = [];
			}
			takeRecords() {
				return [];
			}
		}
		globalThis.ResizeObserver =
			FakeResizeObserver as unknown as typeof ResizeObserver;
		globalThis.IntersectionObserver =
			FakeIntersectionObserver as unknown as typeof IntersectionObserver;

		env.intersect = (isIntersecting) => {
			act(() => {
				for (const w of watchers)
					w.cb(
						[{ target: w.el, isIntersecting } as IntersectionObserverEntry],
						w.self,
					);
			});
		};
		env.resize = (width) => {
			env.width = width;
			act(() => {
				for (const r of resizers) notify(r);
			});
		};
		boxSpy = spyOn(
			Element.prototype,
			"getBoundingClientRect",
		).mockImplementation(() => boxOf(env.width) as DOMRect);
	});

	afterEach(() => {
		boxSpy?.mockRestore();
		globalThis.ResizeObserver = realResize;
		globalThis.IntersectionObserver = realIntersection;
	});

	return env;
}
