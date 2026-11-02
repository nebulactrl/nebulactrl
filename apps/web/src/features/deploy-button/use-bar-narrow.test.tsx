import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { useBarNarrow } from "./use-bar-narrow";

type Callback = (entries: Array<{ contentRect: { width: number } }>) => void;

/** A ResizeObserver the test drives by hand. */
class FakeObserver {
	static instances: FakeObserver[] = [];
	observed: Element[] = [];
	disconnected = false;
	constructor(readonly callback: Callback) {
		FakeObserver.instances.push(this);
	}
	observe(element: Element) {
		this.observed.push(element);
	}
	disconnect() {
		this.disconnected = true;
	}
	unobserve() {}
}

const realObserver = globalThis.ResizeObserver;
beforeEach(() => {
	FakeObserver.instances = [];
	globalThis.ResizeObserver = FakeObserver as unknown as typeof ResizeObserver;
});
afterEach(() => {
	globalThis.ResizeObserver = realObserver;
});

function Probe() {
	const ref = useRef<HTMLSpanElement>(null);
	const narrow = useBarNarrow(ref);
	return (
		<span ref={ref} data-testid="probe">
			{narrow ? "narrow" : "wide"}
		</span>
	);
}

function renderInBar(width: number, padding = "0px") {
	const bar = document.createElement("header");
	bar.style.paddingLeft = padding;
	bar.style.paddingRight = padding;
	bar.style.borderLeftWidth = "0px";
	bar.style.borderRightWidth = "0px";
	bar.getBoundingClientRect = () => ({ width }) as DOMRect;
	document.body.append(bar);
	return render(<Probe />, {
		container: bar.appendChild(document.createElement("div")),
	});
}

describe("useBarNarrow", () => {
	test("a bar under 760px of content width is narrow", () => {
		renderInBar(700);
		expect(screen.getByTestId("probe")).toHaveTextContent("narrow");
	});

	test("a bar of 760px or more is wide", () => {
		renderInBar(760);
		expect(screen.getByTestId("probe")).toHaveTextContent("wide");
	});

	test("the bar's own padding does not count as room", () => {
		// 800px wide, 20px each side: 760px of content is just enough; 30px each side is not.
		renderInBar(800, "20px");
		expect(screen.getByTestId("probe")).toHaveTextContent("wide");
	});

	test("padding that eats the room makes it narrow", () => {
		renderInBar(800, "30px");
		expect(screen.getByTestId("probe")).toHaveTextContent("narrow");
	});

	test("follows the bar as it is resized and stops watching on unmount", () => {
		const { unmount } = renderInBar(900);
		expect(screen.getByTestId("probe")).toHaveTextContent("wide");
		const observer = FakeObserver.instances[0];
		if (!observer) throw new Error("no observer");
		expect(observer.observed[0]?.tagName).toBe("HEADER");

		act(() => observer.callback([{ contentRect: { width: 500 } }]));
		expect(screen.getByTestId("probe")).toHaveTextContent("narrow");
		act(() => observer.callback([{ contentRect: { width: 1000 } }]));
		expect(screen.getByTestId("probe")).toHaveTextContent("wide");
		// An empty batch changes nothing.
		act(() => observer.callback([]));
		expect(screen.getByTestId("probe")).toHaveTextContent("wide");

		unmount();
		expect(observer.disconnected).toBe(true);
	});

	test("outside a header there is nothing to measure: wide", () => {
		render(<Probe />);
		expect(screen.getByTestId("probe")).toHaveTextContent("wide");
		expect(FakeObserver.instances).toEqual([]);
	});
});
