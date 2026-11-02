import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { type EdgeFade, useEdgeFade } from "./tab-strip";

/** The strip's measurements, which happy-dom does not lay out. */
const metrics = { scrollLeft: 0, clientWidth: 300, scrollWidth: 300 };

class FakeResizeObserver {
	static instances: FakeResizeObserver[] = [];
	observed: Element[] = [];
	disconnected = false;
	constructor(readonly callback: () => void) {
		FakeResizeObserver.instances.push(this);
	}
	observe(element: Element) {
		this.observed.push(element);
	}
	unobserve() {}
	disconnect() {
		this.disconnected = true;
	}
}

let savedObserver: typeof ResizeObserver;

beforeEach(() => {
	metrics.scrollLeft = 0;
	metrics.clientWidth = 300;
	metrics.scrollWidth = 300;
	FakeResizeObserver.instances = [];
	savedObserver = globalThis.ResizeObserver;
	globalThis.ResizeObserver =
		FakeResizeObserver as unknown as typeof ResizeObserver;
});

afterEach(() => {
	globalThis.ResizeObserver = savedObserver;
});

function Strip({ onFade }: { onFade?: (fade: EdgeFade) => void }) {
	const ref = useRef<HTMLDivElement>(null);
	const fade = useEdgeFade(ref);
	onFade?.(fade);
	return (
		<div
			ref={(element) => {
				if (!element) return;
				for (const key of Object.keys(metrics) as (keyof typeof metrics)[]) {
					Object.defineProperty(element, key, {
						configurable: true,
						get: () => metrics[key],
					});
				}
				ref.current = element;
			}}
			role="tablist"
			data-start={fade.start}
			data-end={fade.end}
		>
			<button type="button" role="tab">
				One
			</button>
			<button type="button" role="tab">
				Two
			</button>
		</div>
	);
}

const strip = () => screen.getByRole("tablist");

describe("useEdgeFade", () => {
	test("a strip whose tabs all fit fades at neither end", () => {
		render(<Strip />);
		expect(strip()).toHaveAttribute("data-start", "false");
		expect(strip()).toHaveAttribute("data-end", "false");
	});

	test("a strip wider than its box fades the end it continues past", () => {
		metrics.scrollWidth = 500;
		render(<Strip />);
		expect(strip()).toHaveAttribute("data-start", "false");
		expect(strip()).toHaveAttribute("data-end", "true");
	});

	test("scrolling moves the fade to the start and then clears the end", () => {
		metrics.scrollWidth = 500;
		render(<Strip />);
		metrics.scrollLeft = 100;
		fireEvent.scroll(strip());
		expect(strip()).toHaveAttribute("data-start", "true");
		expect(strip()).toHaveAttribute("data-end", "true");
		metrics.scrollLeft = 200;
		fireEvent.scroll(strip());
		expect(strip()).toHaveAttribute("data-start", "true");
		expect(strip()).toHaveAttribute("data-end", "false");
	});

	test("a scroll that changes nothing keeps the same fade object", () => {
		metrics.scrollWidth = 500;
		const seen: EdgeFade[] = [];
		render(<Strip onFade={(fade) => seen.push(fade)} />);
		const settled = seen[seen.length - 1];
		metrics.scrollLeft = 0.5;
		fireEvent.scroll(strip());
		fireEvent.scroll(strip());
		expect(seen[seen.length - 1]).toBe(settled as EdgeFade);
	});

	test("a resize re-measures: tabs that stopped fitting fade the end", () => {
		render(<Strip />);
		expect(strip()).toHaveAttribute("data-end", "false");
		metrics.scrollWidth = 420;
		const observer = FakeResizeObserver.instances[0] as FakeResizeObserver;
		act(() => observer.callback());
		expect(strip()).toHaveAttribute("data-end", "true");
	});

	test("it watches the strip and every tab in it, since tabs come and go", () => {
		render(<Strip />);
		const observer = FakeResizeObserver.instances[0] as FakeResizeObserver;
		expect(observer.observed).toEqual([strip(), ...screen.getAllByRole("tab")]);
	});

	test("unmounting disconnects the observer and stops listening to scroll", () => {
		metrics.scrollWidth = 500;
		let last: EdgeFade | undefined;
		const view = render(<Strip onFade={(fade) => (last = fade)} />);
		const element = strip();
		const observer = FakeResizeObserver.instances[0] as FakeResizeObserver;
		view.unmount();
		expect(observer.disconnected).toBe(true);
		metrics.scrollLeft = 100;
		expect(() => fireEvent.scroll(element)).not.toThrow();
		expect(last).toEqual({ start: false, end: true });
	});
});
