import {
	afterAll,
	afterEach,
	beforeAll,
	beforeEach,
	describe,
	expect,
	jest,
	mock,
	test,
} from "bun:test";
import { durations } from "@nebulactrl/tokens";
import { act, render, renderHook } from "@testing-library/react";
import { useRef } from "react";
import type * as RailModule from "./rail-motion";
import { clipFromRight, railDuration } from "./rail-motion";
import { stubMedia } from "./shell-test-kit";
import { useSidebar } from "./use-sidebar";

// motion/mini cannot find an element in happy-dom ("No valid elements"), so
// the animations are recorded instead of run: what matters here is what the
// rail asks them to do.
// mock.module rewrites the module's namespace in place, so a bare namespace
// would later show the mock; the copy keeps the real exports to restore.
const realMini = { ...(await import("motion/mini")) };

interface Call {
	el: Element;
	keyframes: Record<string, unknown>;
	options: Record<string, unknown>;
	controls: { stop: ReturnType<typeof jest.fn>; finish(): void };
}
let calls: Call[] = [];

beforeAll(() => {
	mock.module("motion/mini", () => ({
		...realMini,
		animate: (
			el: Element,
			keyframes: Record<string, unknown>,
			options: Record<string, unknown>,
		) => {
			const done: Array<() => void> = [];
			const controls = {
				stop: jest.fn(),
				finish: () => {
					for (const fn of done) fn();
				},
				// biome-ignore lint/suspicious/noThenProperty: motion's playback controls are thenable and the hook awaits them
				then: (fn: () => void) => {
					done.push(fn);
				},
			};
			calls.push({ el, keyframes, options, controls });
			return controls;
		},
	}));
});
afterAll(() => {
	mock.module("motion/mini", () => realMini);
});

let rail: typeof RailModule;
let media: ReturnType<typeof stubMedia>;
const realWidth = Object.getOwnPropertyDescriptor(
	HTMLElement.prototype,
	"offsetWidth",
);
const realLeft = Object.getOwnPropertyDescriptor(
	HTMLElement.prototype,
	"offsetLeft",
);

beforeAll(async () => {
	// The query string gives this file its own copy, loaded after the mock above.
	const specifier = "./rail-motion.ts?hooks";
	rail = (await import(specifier)) as typeof RailModule;
});
beforeEach(() => {
	// Layout: an element reports the size in its data attributes.
	Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
		configurable: true,
		get() {
			return Number((this as HTMLElement).dataset.w ?? 0);
		},
	});
	Object.defineProperty(HTMLElement.prototype, "offsetLeft", {
		configurable: true,
		get() {
			return Number((this as HTMLElement).dataset.left ?? 0);
		},
	});
});
afterEach(() => {
	if (realWidth)
		Object.defineProperty(HTMLElement.prototype, "offsetWidth", realWidth);
	else Reflect.deleteProperty(HTMLElement.prototype, "offsetWidth");
	if (realLeft)
		Object.defineProperty(HTMLElement.prototype, "offsetLeft", realLeft);
	else Reflect.deleteProperty(HTMLElement.prototype, "offsetLeft");
});

beforeEach(() => {
	calls = [];
	// Full motion unless a test asks for the reduced kind.
	media = stubMedia({ "(prefers-reduced-motion: reduce)": false });
	window.localStorage.clear();
});
afterEach(() => {
	media.restore();
});

const WIDE = 220;
const RAIL = 64;

function Frame({
	collapsed,
	narrow = false,
}: {
	collapsed: boolean;
	narrow?: boolean;
}) {
	const aside = useRef<HTMLElement>(null);
	const column = useRef<HTMLDivElement>(null);
	rail.useRailReveal(aside, collapsed, narrow);
	rail.useColumnSlide(column, collapsed, narrow);
	const width = collapsed ? RAIL : WIDE;
	return (
		<div data-testid="host">
			<aside ref={aside} id="primary-navigation" data-w={width}>
				<canvas />
				<button type="button" id="inner">
					Servers
				</button>
			</aside>
			<div ref={column} data-testid="column" data-left={width} />
		</div>
	);
}

/** Sets the sidebar store's `animate` flag: true after a pointer toggle, false after a key. */
function setAnimates(animate: boolean) {
	const { result, unmount } = renderHook(() => useSidebar());
	act(() => result.current.toggle(!animate));
	act(() => result.current.toggle(!animate));
	unmount();
}

const aside = () => document.querySelector("aside") as HTMLElement;
const column = () =>
	document.querySelector('[data-testid="column"]') as HTMLElement;

describe("useColumnSlide", () => {
	test("the column slides from where its left edge was to where it is now", () => {
		setAnimates(true);
		const view = render(<Frame collapsed={false} />);
		view.rerender(<Frame collapsed />);
		const slide = calls.find((c) => c.el === column());
		// The rail shrank by 156px, so the column starts 156px off its resting place and arrives at none.
		expect(slide?.keyframes).toEqual({
			transform: ["translateX(156px)", "none"],
		});
		expect(slide?.options.duration).toBe(railDuration(true, false));
	});

	test("expanding slides it in from the other side", () => {
		setAnimates(true);
		const view = render(<Frame collapsed />);
		view.rerender(<Frame collapsed={false} />);
		const slide = calls.find((c) => c.el === column());
		expect(slide?.keyframes).toEqual({
			transform: ["translateX(-156px)", "none"],
		});
		expect(slide?.options.duration).toBe(railDuration(false, false));
	});

	test("a keyboard toggle moves it with no animation", () => {
		setAnimates(false);
		const view = render(<Frame collapsed={false} />);
		view.rerender(<Frame collapsed />);
		expect(calls).toEqual([]);
	});

	test("reduced motion does not slide the column", () => {
		setAnimates(true);
		media.set("(prefers-reduced-motion: reduce)", true);
		const view = render(<Frame collapsed={false} />);
		view.rerender(<Frame collapsed />);
		expect(calls.find((c) => c.el === column())).toBeUndefined();
	});

	test("a change of anything but collapse (a rerender) animates nothing", () => {
		setAnimates(true);
		const view = render(<Frame collapsed={false} />);
		view.rerender(<Frame collapsed={false} />);
		expect(calls).toEqual([]);
	});

	test("on a narrow screen there is nothing to slide", () => {
		setAnimates(true);
		const view = render(<Frame collapsed={false} narrow />);
		view.rerender(<Frame collapsed narrow />);
		expect(calls).toEqual([]);
	});

	test("a second toggle stops the slide still running", () => {
		setAnimates(true);
		const view = render(<Frame collapsed={false} />);
		view.rerender(<Frame collapsed />);
		const first = calls.find((c) => c.el === column());
		view.rerender(<Frame collapsed={false} />);
		expect(first?.controls.stop).toHaveBeenCalledTimes(1);
	});
});

describe("useRailReveal", () => {
	test("expanding sweeps the sidebar open from the left edge with a clip-path", () => {
		setAnimates(true);
		const view = render(<Frame collapsed />);
		view.rerender(<Frame collapsed={false} />);
		const reveal = calls.find((c) => c.el === aside());
		expect(reveal?.keyframes).toEqual({
			clipPath: [clipFromRight(156), clipFromRight(0)],
		});
		expect(reveal?.options.duration).toBe(railDuration(false, false));
	});

	test("the clip-path is removed once the sweep is done", () => {
		setAnimates(true);
		const view = render(<Frame collapsed />);
		view.rerender(<Frame collapsed={false} />);
		aside().style.clipPath = clipFromRight(0);
		calls.find((c) => c.el === aside())?.controls.finish();
		expect(aside().style.clipPath).toBe("");
	});

	test("collapsing sweeps a photograph of the old sidebar shut over the rail", () => {
		setAnimates(true);
		const view = render(<Frame collapsed={false} />);
		rail.stageRailGhost(aside());
		view.rerender(<Frame collapsed />);
		const host = document.querySelector('[data-testid="host"]') as HTMLElement;
		const ghost = host.querySelectorAll("aside")[1] as HTMLElement;
		expect(ghost).toBeDefined();
		expect(ghost).toHaveAttribute("aria-hidden", "true");
		expect(ghost).toHaveAttribute("inert");
		expect(ghost).not.toHaveAttribute("id");
		expect(ghost.querySelector("[id]")).toBeNull();
		// It keeps the width the sidebar had when it was photographed.
		expect(ghost.style.width).toBe(`${WIDE}px`);
		const sweep = calls.find((c) => c.el === ghost);
		expect(sweep?.keyframes).toEqual({
			clipPath: [clipFromRight(0), clipFromRight(156)],
			opacity: [1, 0],
		});
		sweep?.controls.finish();
		expect(host.querySelectorAll("aside")).toHaveLength(1);
	});

	test("collapsing with no photograph staged just changes", () => {
		setAnimates(true);
		const view = render(<Frame collapsed={false} />);
		view.rerender(<Frame collapsed />);
		expect(calls.find((c) => c.el === aside())).toBeUndefined();
		expect(document.querySelectorAll("aside")).toHaveLength(1);
	});

	test("a staged photograph is dropped when the next one is staged", () => {
		render(<Frame collapsed={false} />);
		rail.stageRailGhost(aside());
		rail.stageRailGhost(null);
		const view = render(<Frame collapsed={false} />);
		view.rerender(<Frame collapsed />);
		expect(document.querySelectorAll("aside").length).toBe(2);
	});

	test("a keyboard toggle skips the sweep", () => {
		setAnimates(false);
		const view = render(<Frame collapsed />);
		view.rerender(<Frame collapsed={false} />);
		expect(calls).toEqual([]);
	});

	test("reduced motion keeps only a short fade", () => {
		setAnimates(true);
		media.set("(prefers-reduced-motion: reduce)", true);
		const view = render(<Frame collapsed />);
		view.rerender(<Frame collapsed={false} />);
		const fade = calls.find((c) => c.el === aside());
		expect(fade?.keyframes).toEqual({ opacity: [0.4, 1] });
		expect(fade?.options.duration).toBe(durations.quick / 1000);
	});

	test("a toggle during a toggle stops the running sweep and clears what it left", () => {
		setAnimates(true);
		const view = render(<Frame collapsed />);
		view.rerender(<Frame collapsed={false} />);
		const first = calls.find((c) => c.el === aside());
		aside().style.opacity = "0.5";
		view.rerender(<Frame collapsed />);
		expect(first?.controls.stop).toHaveBeenCalledTimes(1);
		expect(aside().style.opacity).toBe("");
	});

	test("on a narrow screen the drawer is not swept", () => {
		setAnimates(true);
		const view = render(<Frame collapsed narrow />);
		view.rerender(<Frame collapsed={false} narrow />);
		expect(calls).toEqual([]);
	});
});

describe("stageRailGhost", () => {
	test("keeps the pixels of a canvas in the photograph", () => {
		const draws: unknown[] = [];
		const realGetContext = HTMLCanvasElement.prototype.getContext;
		HTMLCanvasElement.prototype.getContext = (() => ({
			drawImage: (...args: unknown[]) => draws.push(args),
		})) as unknown as typeof realGetContext;
		try {
			render(<Frame collapsed={false} />);
			rail.stageRailGhost(aside());
			expect(draws).toHaveLength(1);
		} finally {
			HTMLCanvasElement.prototype.getContext = realGetContext;
		}
	});
});
