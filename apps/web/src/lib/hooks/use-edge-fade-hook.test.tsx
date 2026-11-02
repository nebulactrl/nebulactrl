import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { useEdgeFade } from "./use-edge-fade";

class FakeResizeObserver {
	static instances: FakeResizeObserver[] = [];
	observed: Element[] = [];
	disconnected = false;
	callback: () => void;
	constructor(callback: () => void) {
		this.callback = callback;
		FakeResizeObserver.instances.push(this);
	}
	observe(element: Element) {
		this.observed.push(element);
	}
	disconnect() {
		this.disconnected = true;
	}
	unobserve() {}
}

const realResizeObserver = globalThis.ResizeObserver;
const proto = HTMLElement.prototype;
const originals = new Map<string, PropertyDescriptor | undefined>();

/** A strip's geometry comes from data attributes, since happy-dom does no layout. */
function stubGeometry() {
	for (const [prop, attr] of [
		["scrollLeft", "data-sl"],
		["clientWidth", "data-cw"],
		["scrollWidth", "data-sw"],
	] as const) {
		originals.set(prop, Object.getOwnPropertyDescriptor(proto, prop));
		Object.defineProperty(proto, prop, {
			configurable: true,
			get(this: HTMLElement) {
				return Number(this.getAttribute(attr) ?? 0);
			},
		});
	}
}

beforeEach(() => {
	FakeResizeObserver.instances = [];
	globalThis.ResizeObserver =
		FakeResizeObserver as unknown as typeof ResizeObserver;
	stubGeometry();
});

afterEach(() => {
	globalThis.ResizeObserver = realResizeObserver;
	for (const [prop, descriptor] of originals) {
		if (descriptor) Object.defineProperty(proto, prop, descriptor);
		else Reflect.deleteProperty(proto, prop);
	}
	originals.clear();
});

function Strip({ scrollLeft = 0, width = 300, content = 500 }) {
	const ref = useRef<HTMLDivElement>(null);
	const fade = useEdgeFade(ref);
	return (
		<div
			ref={ref}
			data-testid="strip"
			data-sl={scrollLeft}
			data-cw={width}
			data-sw={content}
			data-start={String(fade.start)}
			data-end={String(fade.end)}
		>
			<span>one</span>
			<span>two</span>
		</div>
	);
}

const strip = () => screen.getByTestId("strip");

describe("useEdgeFade", () => {
	test("a strip with more behind its end fades the end", () => {
		render(<Strip />);
		expect(strip()).toHaveAttribute("data-start", "false");
		expect(strip()).toHaveAttribute("data-end", "true");
	});

	test("a strip that fits fades nowhere", () => {
		render(<Strip width={500} content={500} />);
		expect(strip()).toHaveAttribute("data-start", "false");
		expect(strip()).toHaveAttribute("data-end", "false");
	});

	test("scrolling updates which ends fade", () => {
		render(<Strip />);
		act(() => {
			strip().setAttribute("data-sl", "100");
			strip().dispatchEvent(new Event("scroll"));
		});
		expect(strip()).toHaveAttribute("data-start", "true");
		expect(strip()).toHaveAttribute("data-end", "true");
		act(() => {
			strip().setAttribute("data-sl", "200");
			strip().dispatchEvent(new Event("scroll"));
		});
		expect(strip()).toHaveAttribute("data-start", "true");
		expect(strip()).toHaveAttribute("data-end", "false");
	});

	test("a resize or a change of items re-measures", () => {
		render(<Strip />);
		const [observer] = FakeResizeObserver.instances;
		expect(observer?.observed).toEqual([
			strip(),
			...Array.from(strip().children),
		]);
		act(() => {
			strip().setAttribute("data-sw", "300");
			observer?.callback();
		});
		expect(strip()).toHaveAttribute("data-end", "false");
	});

	test("unmounting stops listening and observing", () => {
		const { unmount } = render(<Strip />);
		const element = strip();
		const [observer] = FakeResizeObserver.instances;
		unmount();
		expect(observer?.disconnected).toBe(true);
		// A scroll after unmount must not throw or update anything.
		element.dispatchEvent(new Event("scroll"));
	});
});
