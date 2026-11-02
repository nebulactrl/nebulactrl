import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { act, render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef } from "react";
import { installLayoutEnv, type LayoutEnv } from "./layout-env.testkit";
import { type TabGlideOptions, useTabGlide } from "./tab-glide";

const BOXES: Record<string, Record<string, number>> = {
	One: { offsetLeft: 10, offsetTop: 4, offsetWidth: 50, offsetHeight: 30 },
	Two: { offsetLeft: 70, offsetTop: 4, offsetWidth: 80, offsetHeight: 30 },
};

const BAR: TabGlideOptions = {
	active: "[data-active]",
	items: "button",
	attributes: ["data-active"],
	thumb: false,
	scrolls: false,
	enabled: true,
};

function Strip({
	active,
	options,
}: {
	active: string | null;
	options: Partial<TabGlideOptions>;
}) {
	const list = useRef<HTMLDivElement>(null);
	const indicator = useRef<HTMLSpanElement>(null);
	useTabGlide(list, indicator, { ...BAR, ...options });
	return (
		<div ref={list} data-testid="list">
			<span ref={indicator} data-testid="ind" />
			{["One", "Two"].map((name) => (
				<button
					type="button"
					key={name}
					{...(name === active ? { "data-active": "" } : {})}
				>
					{name}
				</button>
			))}
		</div>
	);
}

describe("useTabGlide", () => {
	let env: LayoutEnv;
	beforeEach(() => {
		env = installLayoutEnv();
		env.layout = (el) => BOXES[el.textContent ?? ""];
	});
	afterEach(() => env.restore());

	const ind = () =>
		document.querySelector<HTMLElement>("[data-testid=ind]") as HTMLElement;
	const list = () =>
		document.querySelector<HTMLElement>("[data-testid=list]") as HTMLElement;

	it("draws a 2px bar under the active item: its width and left offset", async () => {
		render(<Strip active="One" options={{}} />);
		await env.flush();
		expect(ind().style.width).toBe("50px");
		expect(ind().style.height).toBe("2px");
		expect(ind().style.transform).toBe("translateX(10px)");
		expect(ind().style.opacity).toBe("1");
	});

	it("draws a thumb behind the active item when thumb is set: size and both offsets", async () => {
		render(<Strip active="Two" options={{ thumb: true }} />);
		await env.flush();
		expect(ind().style.width).toBe("80px");
		expect(ind().style.height).toBe("30px");
		expect(ind().style.transform).toBe("translate(70px, 4px)");
	});

	it("places first without motion, then glides on a later change", async () => {
		const { rerender } = render(<Strip active="One" options={{}} />);
		await env.flush();
		expect(ind().style.transition).toBe("none");
		rerender(<Strip active="Two" options={{}} />);
		await env.flush();
		expect(ind().style.transform).toBe("translateX(70px)");
		expect(ind().style.transition).toBe("");
	});

	it("does not glide after keyboard input", async () => {
		const user = userEvent.setup();
		const { rerender } = render(<Strip active="One" options={{}} />);
		await env.flush();
		await user.keyboard("{ArrowRight}");
		rerender(<Strip active="Two" options={{}} />);
		await env.flush();
		expect(ind().style.transition).toBe("none");
	});

	it("glides again after a pointer press follows keyboard use", async () => {
		const user = userEvent.setup();
		const { rerender } = render(<Strip active="One" options={{}} />);
		await env.flush();
		await user.keyboard("{ArrowRight}");
		await user.click(document.body);
		rerender(<Strip active="Two" options={{}} />);
		await env.flush();
		expect(ind().style.transition).toBe("");
	});

	it("does not glide under reduced motion", async () => {
		env.reducedMotion = true;
		const { rerender } = render(<Strip active="One" options={{}} />);
		await env.flush();
		rerender(<Strip active="Two" options={{}} />);
		await env.flush();
		expect(ind().style.transition).toBe("none");
	});

	it("hides the indicator when no item is active", async () => {
		const { rerender } = render(<Strip active="One" options={{}} />);
		await env.flush();
		expect(ind().style.opacity).toBe("1");
		rerender(<Strip active={null} options={{}} />);
		await env.flush();
		expect(ind().style.opacity).toBe("0");
	});

	it("does nothing when disabled", async () => {
		render(<Strip active="One" options={{ enabled: false }} />);
		await env.flush();
		expect(ind().style.opacity).toBe("");
		expect(ind().style.width).toBe("");
		expect(env.resizeObservers()).toHaveLength(0);
	});

	it("watches the list and every item for resizes and re-measures on resize", async () => {
		render(<Strip active="One" options={{}} />);
		await env.flush();
		const watched = env.resizeObservers().flatMap((o) => [...o.observed]);
		expect(watched).toContain(list());
		expect(watched.map((e) => e.textContent)).toEqual(
			expect.arrayContaining(["One", "Two"]),
		);
		BOXES.One = { ...BOXES.One, offsetWidth: 90 } as Record<string, number>;
		try {
			for (const ro of env.resizeObservers()) ro.fire();
			await env.flush();
			expect(ind().style.width).toBe("90px");
		} finally {
			BOXES.One = {
				offsetLeft: 10,
				offsetTop: 4,
				offsetWidth: 50,
				offsetHeight: 30,
			};
		}
	});

	it("re-measures on scroll of the list", async () => {
		render(<Strip active="One" options={{}} />);
		await env.flush();
		BOXES.One = { ...BOXES.One, offsetWidth: 61 } as Record<string, number>;
		try {
			act(() => {
				list().dispatchEvent(new Event("scroll"));
			});
			await env.flush();
			expect(ind().style.width).toBe("61px");
		} finally {
			BOXES.One = {
				offsetLeft: 10,
				offsetTop: 4,
				offsetWidth: 50,
				offsetHeight: 30,
			};
		}
	});

	it("re-measures when a watched attribute changes", async () => {
		const { rerender } = render(<Strip active="One" options={{}} />);
		await env.flush();
		rerender(<Strip active="Two" options={{}} />);
		// The attribute mutation itself (not a rerender-triggered effect) queues a frame.
		await act(async () => {
			await Promise.resolve();
			await Promise.resolve();
		});
		expect(env.pendingFrames()).toBeGreaterThan(0);
	});

	it("scrolls the active item into view once, not on every re-measure", async () => {
		env.layout = (el) => {
			if (el === list())
				return { rectLeft: 0, rectWidth: 100, clientWidth: 100 };
			if (el.textContent === "Two")
				return { ...BOXES.Two, rectLeft: 300, rectWidth: 80 };
			return BOXES[el.textContent ?? ""];
		};
		render(<Strip active="Two" options={{ scrolls: true }} />);
		await env.flush();
		const scrolls = () => env.scrollCalls.filter((c) => c.el === list());
		// right edge 380 + 32 margin - viewport right 100
		expect(scrolls().map((c) => c.options)).toEqual([{ left: 312 }]);
		act(() => {
			list().dispatchEvent(new Event("scroll"));
		});
		await env.flush();
		expect(scrolls()).toHaveLength(1);
	});

	it("scrolls again when the active item changes", async () => {
		env.layout = (el) => {
			if (el === list())
				return { rectLeft: 0, rectWidth: 100, clientWidth: 100 };
			if (el.textContent === "One")
				return { ...BOXES.One, rectLeft: 300, rectWidth: 50 };
			if (el.textContent === "Two")
				return { ...BOXES.Two, rectLeft: 500, rectWidth: 80 };
			return undefined;
		};
		const { rerender } = render(
			<Strip active="One" options={{ scrolls: true }} />,
		);
		await env.flush();
		rerender(<Strip active="Two" options={{ scrolls: true }} />);
		await env.flush();
		expect(env.scrollCalls.filter((c) => c.el === list())).toHaveLength(2);
	});

	it("leaves scroll alone when scrolls is off or the item is visible", async () => {
		env.layout = (el) =>
			el === list()
				? { rectLeft: 0, rectWidth: 500, clientWidth: 500 }
				: {
						...(BOXES[el.textContent ?? ""] ?? {}),
						rectLeft: 100,
						rectWidth: 50,
					};
		const { unmount } = render(
			<Strip active="One" options={{ scrolls: true }} />,
		);
		await env.flush();
		expect(env.scrollCalls).toHaveLength(0);
		unmount();
		env.layout = (el) =>
			el === list()
				? { rectLeft: 0, rectWidth: 100, clientWidth: 100 }
				: { rectLeft: 900, rectWidth: 50 };
		render(<Strip active="One" options={{ scrolls: false }} />);
		await env.flush();
		expect(env.scrollCalls).toHaveLength(0);
	});

	it("disconnects its observers, cancels its frame and stops listening on unmount", async () => {
		const { unmount } = render(<Strip active="One" options={{}} />);
		const observers = env.resizeObservers();
		const node = list();
		unmount();
		expect(observers.every((o) => o.disconnected)).toBe(true);
		expect(env.pendingFrames()).toBe(0);
		node.dispatchEvent(new Event("scroll"));
		expect(env.pendingFrames()).toBe(0);
	});
});
