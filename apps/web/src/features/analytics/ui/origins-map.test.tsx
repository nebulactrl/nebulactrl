import {
	afterEach,
	beforeEach,
	describe,
	expect,
	mock,
	spyOn,
	test,
} from "bun:test";
import type { MapBounds } from "@nebulactrl/dither/backgrounds/geo";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type GeoPlace, geoPlaces, REGIONS } from "../geo/model";
import { stubResizeObserver } from "../resize-stub";
import { makeGeo } from "../test-fixtures";
import { OriginsMap, type OriginsMapProps } from "./origins-map";

const WORLD = REGIONS[0].bounds;
const EUROPE = REGIONS[1].bounds;

/** Berlin (50%) and Austin (30%) at the city level. */
const places = geoPlaces(makeGeo("city"));

let restoreSize = () => {};
beforeEach(() => {
	restoreSize = stubResizeObserver({ width: 600, height: 300 });
});
afterEach(() => restoreSize());

async function mount(props: Partial<OriginsMapProps> = {}) {
	const view = render(<OriginsMap places={places} {...props} />);
	// The box reports its size on the first microtask.
	await act(async () => {
		await Promise.resolve();
	});
	return view;
}

/**
 * Dispatches a wheel event. happy-dom's WheelEvent drops the modifier keys and
 * the pointer position, so they are put on the event itself. Returns whether
 * the page's default (scrolling, browser zoom) was left alone.
 */
function wheel(
	el: Element,
	init: {
		deltaY: number;
		ctrlKey?: boolean;
		metaKey?: boolean;
		clientX?: number;
		clientY?: number;
	},
): boolean {
	const event = new WheelEvent("wheel", {
		deltaY: init.deltaY,
		bubbles: true,
		cancelable: true,
	});
	for (const key of ["ctrlKey", "metaKey", "clientX", "clientY"] as const) {
		Object.defineProperty(event, key, {
			value: init[key] ?? (key.endsWith("Key") ? false : 0),
		});
	}
	let notPrevented = true;
	act(() => {
		notPrevented = el.dispatchEvent(event);
	});
	return notPrevented;
}

const lonSpan = (b: MapBounds) => b.east - b.west;
const latSpan = (b: MapBounds) => b.north - b.south;
const hotspots = () =>
	screen.queryAllByRole("button").filter((b) => b.hasAttribute("data-place"));
const layers = (root: HTMLElement) => root.querySelectorAll('[role="img"]');
const lastCall = <T,>(fn: { mock: { calls: T[][] } }) =>
	fn.mock.calls.at(-1)?.[0] as T;

describe("OriginsMap drawing", () => {
	test("draws nothing until the box has a size, then one hotspot per located place", async () => {
		restoreSize();
		restoreSize = () => {};
		render(<OriginsMap places={places} />);
		expect(hotspots()).toHaveLength(0);
		expect(document.querySelectorAll('[role="img"]')).toHaveLength(0);
	});

	test("names every place and its count and share on its hotspot", async () => {
		await mount();
		expect(
			screen.getByRole("button", { name: "Berlin, DE: 500 requests, 50%" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Austin, US: 300 requests, 30%" }),
		).toBeInTheDocument();
	});

	test("names the busiest places beside their markers, hidden from assistive tech", async () => {
		const { container } = await mount();
		const chips = Array.from(
			container.querySelectorAll<HTMLElement>("span.surface-pop"),
		);
		expect(chips.map((c) => c.textContent)).toEqual(
			expect.arrayContaining(["Berlin, DE", "Austin, US"]),
		);
		for (const chip of chips) {
			expect(chip.closest('[aria-hidden="true"]')).not.toBeNull();
		}
	});

	test("shows a place's name and numbers in a tooltip on focus", async () => {
		const user = userEvent.setup();
		await mount();
		await user.hover(screen.getByRole("button", { name: /^Berlin/ }));
		expect(await screen.findByText("500 · 50%")).toBeInTheDocument();
		expect(screen.getAllByText("Berlin, DE").length).toBeGreaterThan(0);
	});

	test("marks at most 24 places", async () => {
		const many: GeoPlace[] = Array.from({ length: 30 }, (_, i) => ({
			key: `p${i}`,
			country: "DE",
			at: [40 + (i % 6), 5 + i] as const,
			label: `Place ${i}`,
			requests: 100 - i,
			share: 0.01,
			weight: 0.5,
		}));
		await mount({ places: many });
		expect(hotspots()).toHaveLength(24);
		expect(
			screen.getByRole("button", { name: /^Place 0:/ }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /^Place 24:/ }),
		).not.toBeInTheDocument();
	});

	test("a region leaves out places that fall outside it", async () => {
		await mount({ bounds: EUROPE });
		expect(hotspots().map((b) => b.dataset.place)).toEqual([
			expect.stringContaining("Berlin"),
		]);
	});

	test("with no places the map is still drawn, with nothing to point at", async () => {
		const { container } = await mount({ places: [] });
		expect(hotspots()).toHaveLength(0);
		expect(layers(container)).toHaveLength(1);
	});

	test("lights the places named in highlight, two rings each, and only those", async () => {
		const berlin = places[0] as GeoPlace;
		const { container, rerender } = await mount();
		const rings = () =>
			container.querySelectorAll('span[aria-hidden][style*="clip-path"]');
		expect(rings()).toHaveLength(0);
		rerender(<OriginsMap places={places} highlight={[berlin.key]} />);
		expect(rings()).toHaveLength(2);
		rerender(<OriginsMap places={places} highlight={["nowhere"]} />);
		expect(rings()).toHaveLength(0);
		rerender(
			<OriginsMap places={places} highlight={places.map((p) => p.key)} />,
		);
		expect(rings()).toHaveLength(4);
	});

	test("merges the caller's style over the box and takes the region's shape unless filled", async () => {
		const { container, rerender } = await mount({ style: { color: "red" } });
		const box = container.firstElementChild as HTMLElement;
		expect(box.style.aspectRatio).not.toBe("");
		expect(box.style.color).toBe("red");
		rerender(<OriginsMap places={places} fill style={{ color: "red" }} />);
		expect(box.style.aspectRatio).toBe("");
		expect(box.style.color).toBe("red");
	});
});

describe("OriginsMap changing region", () => {
	test("a new region glides: the old drawing stays until the move ends, then it is replaced", async () => {
		const { container, rerender } = await mount();
		expect(layers(container)).toHaveLength(1);
		rerender(<OriginsMap places={places} bounds={EUROPE} />);
		expect(layers(container)).toHaveLength(2);
		// The leaving drawing is hidden from assistive tech; the new one is not.
		expect(
			container.querySelectorAll('[aria-hidden="true"] > [role="img"]'),
		).toHaveLength(1);
		await waitFor(() => expect(layers(container)).toHaveLength(1));
		expect(hotspots()).toHaveLength(1);
	});

	test("without glide the new region replaces the old at once", async () => {
		const { container, rerender } = await mount({ glide: false });
		rerender(<OriginsMap places={places} glide={false} bounds={EUROPE} />);
		expect(layers(container)).toHaveLength(1);
		expect(hotspots()).toHaveLength(1);
	});

	test("rerendering the same bounds does not start a move", async () => {
		const { container, rerender } = await mount({ bounds: EUROPE });
		rerender(<OriginsMap places={places} bounds={{ ...EUROPE }} />);
		expect(layers(container)).toHaveLength(1);
	});

	test("a box of another size gets a grid of its own size", async () => {
		const wide = await mount();
		const grid = (root: HTMLElement) =>
			root.querySelector<HTMLElement>('[role="img"]')
				?.parentElement as HTMLElement;
		const wideWidth = grid(wide.container).style.width;
		wide.unmount();
		restoreSize();
		restoreSize = stubResizeObserver({ width: 300, height: 150 });
		const narrow = await mount();
		// Whole dots of the 3px pitch, never more than the box.
		expect(
			Number.parseInt(grid(narrow.container).style.width, 10),
		).toBeLessThanOrEqual(300);
		expect(grid(narrow.container).style.width).not.toBe(wideWidth);
	});
});

describe("OriginsMap as a picture", () => {
	test("not interactive: no application role, help text or controls", async () => {
		await mount({ interactive: false, controls: true });
		expect(screen.queryByRole("application")).not.toBeInTheDocument();
		expect(screen.queryByText(/Interactive map/)).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Zoom in" }),
		).not.toBeInTheDocument();
	});

	test("interactive: a focusable named map described by how to use it", async () => {
		await mount();
		const map = screen.getByRole("application", {
			name: "Map of where requests come from",
		});
		expect(map).toHaveAttribute("tabindex", "0");
		expect(map).toHaveAttribute("aria-roledescription", "interactive map");
		const help = document.getElementById(
			map.getAttribute("aria-describedby") ?? "",
		);
		expect(help).toHaveTextContent(
			/Drag to pan\. Hold (Control|Command) and scroll, or pinch, to zoom\./,
		);
		expect(help).toHaveTextContent("0 fits the map to the data");
		expect(map).not.toHaveAttribute("aria-hidden");
	});

	test("without the controls prop there are no buttons beyond the hotspots", async () => {
		await mount();
		expect(
			screen.queryByRole("button", { name: "Zoom in" }),
		).not.toBeInTheDocument();
	});
});

describe("OriginsMap zoom controls and keys", () => {
	test("at the whole world only Zoom in is available", async () => {
		await mount({ controls: true });
		expect(screen.getByRole("button", { name: "Zoom out" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Zoom in" })).toBeEnabled();
		expect(screen.getByRole("button", { name: "Fit to data" })).toBeDisabled();
	});

	test("Zoom in reports narrower bounds once the move rests, then enables Zoom out and Fit", async () => {
		const user = userEvent.setup();
		const onViewChange = mock((_b: MapBounds) => {});
		await mount({ controls: true, onViewChange });
		expect(onViewChange).not.toHaveBeenCalled();
		await user.click(screen.getByRole("button", { name: "Zoom in" }));
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		const next = lastCall<MapBounds>(onViewChange);
		// One step is x1.5 about the centre.
		expect(lonSpan(next)).toBeCloseTo(360 / 1.5, 5);
		// The grid is a whole number of dots, so its latitude span is the world's to within a row.
		expect(latSpan(next)).toBeCloseTo(146 / 1.5, 0);
		expect(screen.getByRole("button", { name: "Zoom out" })).toBeEnabled();
		expect(screen.getByRole("button", { name: "Fit to data" })).toBeEnabled();
	});

	test("Zoom out after zooming in brings the world back", async () => {
		const user = userEvent.setup();
		const onViewChange = mock((_b: MapBounds) => {});
		await mount({ controls: true, onViewChange });
		await user.click(screen.getByRole("button", { name: "Zoom in" }));
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		await user.click(screen.getByRole("button", { name: "Zoom out" }));
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(2));
		expect(lonSpan(lastCall<MapBounds>(onViewChange))).toBeCloseTo(360, 5);
	});

	test("Fit to data goes back to the requested bounds and calls onFit", async () => {
		const user = userEvent.setup();
		const onFit = mock(() => {});
		const onViewChange = mock((_b: MapBounds) => {});
		await mount({ controls: true, onFit, onViewChange });
		await user.click(screen.getByRole("button", { name: "Zoom in" }));
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		await user.click(screen.getByRole("button", { name: "Fit to data" }));
		expect(onFit).toHaveBeenCalledTimes(1);
		expect(screen.getByRole("button", { name: "Fit to data" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Zoom out" })).toBeDisabled();
	});

	test("+ and - keys zoom, and a zoom in and out in one breath nets to nothing", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		const { container } = await mount({ onViewChange });
		const map = screen.getByRole("application");
		fireEvent.keyDown(map, { key: "+" });
		const layer = container.querySelector<HTMLElement>('[role="img"]')
			?.parentElement as HTMLElement;
		expect(layer.style.transform).toContain("scale(1.5, 1.5)");
		fireEvent.keyDown(map, { key: "-" });
		// Back at rest: the layer is released and nothing is reported.
		await waitFor(() => expect(layer.style.transform).toBe(""));
		expect(onViewChange).not.toHaveBeenCalled();
	});

	test("= zooms in like + does", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		await mount({ onViewChange });
		fireEvent.keyDown(screen.getByRole("application"), { key: "=" });
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		expect(lonSpan(lastCall<MapBounds>(onViewChange))).toBeCloseTo(240, 5);
	});

	test("arrow keys pan a zoomed map a fifth of its width, the way the camera moves", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		await mount({ onViewChange });
		const map = screen.getByRole("application");
		fireEvent.keyDown(map, { key: "+" });
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		const zoomed = lastCall<MapBounds>(onViewChange);
		fireEvent.keyDown(map, { key: "ArrowLeft" });
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(2));
		const left = lastCall<MapBounds>(onViewChange);
		expect(lonSpan(left)).toBeCloseTo(lonSpan(zoomed), 5);
		expect(left.west).toBeCloseTo(zoomed.west - lonSpan(zoomed) * 0.2, 5);
		fireEvent.keyDown(map, { key: "ArrowRight" });
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(3));
		expect(lastCall<MapBounds>(onViewChange).west).toBeCloseTo(zoomed.west, 5);
		fireEvent.keyDown(map, { key: "ArrowUp" });
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(4));
		expect(lastCall<MapBounds>(onViewChange).north).toBeGreaterThan(
			zoomed.north - 1e-9,
		);
		fireEvent.keyDown(map, { key: "ArrowDown" });
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(5));
		expect(latSpan(lastCall<MapBounds>(onViewChange))).toBeCloseTo(
			latSpan(zoomed),
			5,
		);
	});

	test("0 asks the parent for its fitted view", async () => {
		const onFit = mock(() => {});
		await mount({ onFit });
		fireEvent.keyDown(screen.getByRole("application"), { key: "0" });
		expect(onFit).toHaveBeenCalledTimes(1);
	});

	test("0 on a zoomed map without onFit returns to the requested bounds", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		await mount({ controls: true, onViewChange });
		const map = screen.getByRole("application");
		fireEvent.keyDown(map, { key: "+" });
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		await act(async () => {});
		expect(screen.getByRole("button", { name: "Fit to data" })).toBeEnabled();
		fireEvent.keyDown(map, { key: "0" });
		await act(async () => {});
		expect(screen.getByRole("button", { name: "Fit to data" })).toBeDisabled();
		// Nothing is reported for the return: the parent's bounds are simply shown again.
		expect(onViewChange).toHaveBeenCalledTimes(1);
	});

	test("other keys and modified keys are left to the page", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		const onFit = mock(() => {});
		await mount({ onViewChange, onFit });
		const map = screen.getByRole("application");
		// fireEvent returns false when the default was prevented.
		expect(fireEvent.keyDown(map, { key: "a" })).toBe(true);
		expect(fireEvent.keyDown(map, { key: "+", ctrlKey: true })).toBe(true);
		expect(fireEvent.keyDown(map, { key: "0", metaKey: true })).toBe(true);
		expect(fireEvent.keyDown(map, { key: "ArrowLeft", altKey: true })).toBe(
			true,
		);
		expect(onFit).not.toHaveBeenCalled();
		expect(fireEvent.keyDown(map, { key: "+" })).toBe(false);
	});

	test("a picture ignores keys", async () => {
		const onFit = mock(() => {});
		const { container } = await mount({ interactive: false, onFit });
		fireEvent.keyDown(container.firstElementChild as HTMLElement, { key: "0" });
		expect(onFit).not.toHaveBeenCalled();
	});
});

describe("OriginsMap wheel", () => {
	test("Ctrl and the wheel zoom about the pointer and keep the page from scrolling", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		await mount({ onViewChange });
		const map = screen.getByRole("application");
		expect(
			wheel(map, { deltaY: -100, ctrlKey: true, clientX: 300, clientY: 150 }),
		).toBe(false);
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		const next = lastCall<MapBounds>(onViewChange);
		// Scrolling up zooms in.
		expect(lonSpan(next)).toBeLessThan(360);
		// About the centre, so the middle of the world stays the middle.
		expect(next.west + lonSpan(next) / 2).toBeCloseTo(0, 3);
	});

	test("scrolling down with the modifier zooms out only as far as the world", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		const { container } = await mount({ onViewChange });
		const layer = container.querySelector<HTMLElement>('[role="img"]')
			?.parentElement as HTMLElement;
		wheel(screen.getByRole("application"), { deltaY: 200, metaKey: true });
		await waitFor(() => expect(layer.style.transform).toBe(""));
		expect(onViewChange).not.toHaveBeenCalled();
	});

	test("a pinch (small Ctrl wheel deltas) zooms too", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		await mount({ onViewChange });
		wheel(screen.getByRole("application"), { deltaY: -20, ctrlKey: true });
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		expect(lonSpan(lastCall<MapBounds>(onViewChange))).toBeLessThan(360);
	});

	test("the wheel without the modifier scrolls the page and hints once per session", async () => {
		window.sessionStorage.removeItem("nebula:map-zoom-hint");
		const onViewChange = mock((_b: MapBounds) => {});
		const first = await mount({ onViewChange });
		const hintBox = (root: HTMLElement) =>
			Array.from(root.querySelectorAll<HTMLElement>(".surface-pop")).find(
				(el) => el.textContent?.includes("to zoom"),
			)?.parentElement as HTMLElement;
		const hint = () => hintBox(first.container);
		expect(hint().className).toContain("opacity-0");
		expect(wheel(screen.getByRole("application"), { deltaY: 100 })).toBe(true);
		expect(hint().className).toContain("opacity-100");
		expect(first.container).toHaveTextContent(/Hold (⌘|Ctrl) to zoom/);
		expect(window.sessionStorage.getItem("nebula:map-zoom-hint")).toBe("1");
		expect(onViewChange).not.toHaveBeenCalled();
		first.unmount();
		// The next map in the same session does not nag again.
		const second = await mount();
		wheel(screen.getByRole("application"), { deltaY: 100 });
		const again = hintBox(second.container);
		expect(again.className).toContain("opacity-0");
	});
});

describe("OriginsMap pointer", () => {
	let clock = 0;
	let frames: FrameRequestCallback[] = [];
	const realRaf = globalThis.requestAnimationFrame;
	const realCaf = globalThis.cancelAnimationFrame;

	/** A clock and animation frames the test steps by hand. */
	beforeEach(() => {
		clock = 1_000;
		frames = [];
		spyOn(performance, "now").mockImplementation(() => clock);
		globalThis.requestAnimationFrame = (cb) => frames.push(cb);
		globalThis.cancelAnimationFrame = (id) => {
			frames[id - 1] = () => {};
		};
	});
	afterEach(() => {
		globalThis.requestAnimationFrame = realRaf;
		globalThis.cancelAnimationFrame = realCaf;
		mock.restore();
	});

	async function frame(ms = 16) {
		await act(async () => {
			clock += ms;
			const due = frames;
			frames = [];
			for (const cb of due) cb(clock);
		});
	}

	const pointer = (
		type: "pointerDown" | "pointerMove" | "pointerUp",
		el: Element,
		init: Record<string, unknown>,
	) =>
		fireEvent[type](el, {
			pointerId: 1,
			pointerType: "mouse",
			button: 0,
			isPrimary: true,
			...init,
		});

	test("a slow drag pans the map and reports where it rests", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		const { container } = await mount({ onViewChange });
		const map = screen.getByRole("application");
		const layer = container.querySelector<HTMLElement>('[role="img"]')
			?.parentElement as HTMLElement;
		pointer("pointerDown", map, { clientX: 100, clientY: 100 });
		pointer("pointerMove", map, { clientX: 140, clientY: 100 });
		clock += 500;
		pointer("pointerMove", map, { clientX: 180, clientY: 100 });
		expect(layer.style.transform).toBe("translate(80px, 0px) scale(1, 1)");
		expect(map).toHaveAttribute("data-dragging");
		expect(map).toHaveAttribute("data-gesture");
		clock += 500;
		pointer("pointerUp", map, { clientX: 180, clientY: 100 });
		expect(map).not.toHaveAttribute("data-dragging");
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		const next = lastCall<MapBounds>(onViewChange);
		expect(lonSpan(next)).toBeCloseTo(360, 5);
		// 80 of 600 px right: the window moved 48° west (wrapped into -180..180).
		const shift = ((next.west - WORLD.west + 540) % 360) - 180;
		expect(shift).toBeCloseTo(-48, 3);
		expect(map).not.toHaveAttribute("data-gesture");
	});

	test("the map does not follow a hand that moved less than a click", async () => {
		const { container } = await mount();
		const map = screen.getByRole("application");
		const layer = container.querySelector<HTMLElement>('[role="img"]')
			?.parentElement as HTMLElement;
		pointer("pointerDown", map, { clientX: 100, clientY: 100 });
		pointer("pointerMove", map, { clientX: 102, clientY: 101 });
		expect(layer.style.transform).toBe("");
		expect(map).not.toHaveAttribute("data-dragging");
	});

	test("a drag's last lift does not click what is under it, a plain click does", async () => {
		const onClick = mock(() => {});
		const view = render(
			// biome-ignore lint/a11y/useKeyWithClickEvents: a spy around the map
			// biome-ignore lint/a11y/noStaticElementInteractions: a spy around the map, not UI
			<div onClick={onClick}>
				<OriginsMap places={places} />
			</div>,
		);
		await act(async () => {
			await Promise.resolve();
		});
		const map = screen.getByRole("application");
		const spot = hotspots()[0] as HTMLElement;
		pointer("pointerDown", map, { clientX: 100, clientY: 100 });
		pointer("pointerMove", map, { clientX: 150, clientY: 100 });
		clock += 500;
		pointer("pointerUp", map, { clientX: 150, clientY: 100 });
		fireEvent.click(spot);
		expect(onClick).not.toHaveBeenCalled();
		await act(async () => {
			await new Promise((r) => setTimeout(r, 0));
		});
		fireEvent.click(spot);
		expect(onClick).toHaveBeenCalledTimes(1);
		view.unmount();
	});

	test("the right mouse button and a second mouse pointer do not drag", async () => {
		const { container } = await mount();
		const map = screen.getByRole("application");
		const layer = container.querySelector<HTMLElement>('[role="img"]')
			?.parentElement as HTMLElement;
		pointer("pointerDown", map, { clientX: 100, clientY: 100, button: 2 });
		pointer("pointerMove", map, { clientX: 200, clientY: 100 });
		expect(layer.style.transform).toBe("");
		pointer("pointerDown", map, {
			pointerId: 2,
			isPrimary: false,
			clientX: 100,
			clientY: 100,
		});
		pointer("pointerMove", map, {
			pointerId: 2,
			isPrimary: false,
			clientX: 200,
			clientY: 100,
		});
		expect(layer.style.transform).toBe("");
	});

	test("a picture cannot be dragged", async () => {
		const { container } = await mount({ interactive: false });
		const box = container.firstElementChild as HTMLElement;
		const layer = container.querySelector<HTMLElement>('[role="img"]')
			?.parentElement as HTMLElement;
		pointer("pointerDown", box, { clientX: 100, clientY: 100 });
		pointer("pointerMove", box, { clientX: 200, clientY: 100 });
		expect(layer.style.transform).toBe("");
	});

	test("a flick glides on after the hand lets go and stops at the edge of the map", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		const { container } = await mount({ onViewChange });
		const map = screen.getByRole("application");
		const layer = container.querySelector<HTMLElement>('[role="img"]')
			?.parentElement as HTMLElement;
		pointer("pointerDown", map, { clientX: 100, clientY: 100 });
		clock += 10;
		pointer("pointerMove", map, { clientX: 110, clientY: 100 });
		clock += 10;
		pointer("pointerMove", map, { clientX: 130, clientY: 100 });
		clock += 10;
		pointer("pointerMove", map, { clientX: 150, clientY: 100 });
		clock += 5;
		pointer("pointerUp", map, { clientX: 150, clientY: 100 });
		const dragged = Number(
			/translate\((-?[\d.]+)px/.exec(layer.style.transform)?.[1],
		);
		expect(dragged).toBe(50);
		// Frame by frame the layer travels beyond where the hand left it...
		await frame();
		const afterOne = Number(
			/translate\((-?[\d.]+)px/.exec(layer.style.transform)?.[1],
		);
		expect(afterOne).toBeGreaterThan(dragged);
		// ...until it reaches the edge: half the box (300px) of the grid stays covered.
		for (let i = 0; i < 60 && frames.length > 0; i += 1) await frame();
		expect(frames).toHaveLength(0);
		expect(layer.style.transform).toBe("translate(300px, 0px) scale(1, 1)");
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		const next = lastCall<MapBounds>(onViewChange);
		// 300 of 600 px right: half a turn west.
		expect(((next.west - WORLD.west + 540) % 360) - 180).toBeCloseTo(-180, 3);
	});

	test("a pull past the edge is held back while the hand is down and settles back on release", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		const { container } = await mount({ onViewChange });
		const map = screen.getByRole("application");
		const layer = container.querySelector<HTMLElement>('[role="img"]')
			?.parentElement as HTMLElement;
		pointer("pointerDown", map, { clientX: 0, clientY: 100 });
		pointer("pointerMove", map, { clientX: 100, clientY: 100 });
		clock += 500;
		pointer("pointerMove", map, { clientX: 450, clientY: 100 });
		clock += 500;
		const held = Number(
			/translate\((-?[\d.]+)px/.exec(layer.style.transform)?.[1],
		);
		// The raw pull is 450px; the edge is 300px; the rubber band gives some of the rest.
		expect(held).toBeGreaterThan(300);
		expect(held).toBeLessThan(450);
		pointer("pointerUp", map, { clientX: 450, clientY: 100 });
		for (let i = 0; i < 40 && frames.length > 0; i += 1) await frame(50);
		expect(layer.style.transform).toBe("translate(300px, 0px) scale(1, 1)");
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
	});

	test("a cancelled pointer ends the drag without a glide", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		const { container } = await mount({ onViewChange });
		const map = screen.getByRole("application");
		const layer = container.querySelector<HTMLElement>('[role="img"]')
			?.parentElement as HTMLElement;
		pointer("pointerDown", map, { clientX: 100, clientY: 100 });
		clock += 10;
		pointer("pointerMove", map, { clientX: 110, clientY: 100 });
		clock += 10;
		pointer("pointerMove", map, { clientX: 150, clientY: 100 });
		fireEvent.pointerCancel(map, { pointerId: 1, pointerType: "mouse" });
		expect(frames).toHaveLength(0);
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		expect(layer.style.transform).not.toBe("translate(300px, 0px) scale(1, 1)");
	});

	test("one finger scrolls the page on a map at rest but pans once it is zoomed in", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		const { container } = await mount({ onViewChange });
		const map = screen.getByRole("application");
		const layer = () =>
			container.querySelector<HTMLElement>('[role="img"]')
				?.parentElement as HTMLElement;
		const touch = (
			type: "pointerDown" | "pointerMove" | "pointerUp",
			x: number,
		) =>
			pointer(type, map, {
				pointerType: "touch",
				pointerId: 7,
				clientX: x,
				clientY: 100,
			});
		expect(map.className).toContain("touch-pan-y");
		touch("pointerDown", 100);
		touch("pointerMove", 200);
		expect(layer().style.transform).toBe("");
		touch("pointerUp", 200);

		fireEvent.keyDown(map, { key: "+" });
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		expect(map.className).toContain("touch-none");
		touch("pointerDown", 100);
		touch("pointerMove", 120);
		clock += 500;
		touch("pointerMove", 160);
		expect(layer().style.transform).toBe("translate(60px, 0px) scale(1, 1)");
		clock += 500;
		touch("pointerUp", 160);
	});

	test("two fingers pinch the map in", async () => {
		const onViewChange = mock((_b: MapBounds) => {});
		const { container } = await mount({ onViewChange });
		const map = screen.getByRole("application");
		const layer = container.querySelector<HTMLElement>('[role="img"]')
			?.parentElement as HTMLElement;
		const finger = (
			type: "pointerDown" | "pointerMove" | "pointerUp",
			id: number,
			x: number,
		) =>
			pointer(type, map, {
				pointerType: "touch",
				pointerId: id,
				isPrimary: id === 1,
				clientX: x,
				clientY: 150,
			});
		finger("pointerDown", 1, 250);
		finger("pointerDown", 2, 350);
		finger("pointerMove", 2, 450);
		// The fingers moved from 100px apart to 200px: twice the zoom.
		expect(layer.style.transform).toContain("scale(2, 2)");
		finger("pointerUp", 2, 450);
		finger("pointerUp", 1, 250);
		await waitFor(() => expect(onViewChange).toHaveBeenCalledTimes(1));
		expect(lonSpan(lastCall<MapBounds>(onViewChange))).toBeCloseTo(180, 3);
	});

	test("a third finger is left to the page", async () => {
		const { container } = await mount();
		const map = screen.getByRole("application");
		const layer = container.querySelector<HTMLElement>('[role="img"]')
			?.parentElement as HTMLElement;
		const finger = (
			type: "pointerDown" | "pointerMove",
			id: number,
			x: number,
		) =>
			pointer(type, map, {
				pointerType: "touch",
				pointerId: id,
				isPrimary: id === 1,
				clientX: x,
				clientY: 150,
			});
		finger("pointerDown", 1, 250);
		finger("pointerDown", 2, 350);
		finger("pointerDown", 3, 100);
		const before = layer.style.transform;
		finger("pointerMove", 3, 500);
		expect(layer.style.transform).toBe(before);
	});
});
