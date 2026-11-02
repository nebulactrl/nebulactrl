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
import { act, fireEvent, render } from "@testing-library/react";
import { useRef } from "react";
import {
	INDICATOR_SECONDS,
	PENDING_MS,
	useNavIndicator,
} from "./nav-indicator";
import { stubMedia } from "./shell-test-kit";

// motion/mini cannot find an element in happy-dom, so slides are recorded.
// mock.module rewrites the module's namespace in place, so a bare namespace
// would later show the mock; the copy keeps the real exports to restore.
const realMini = { ...(await import("motion/mini")) };
interface Call {
	el: Element;
	keyframes: { transform: string[] };
	options: Record<string, unknown>;
	controls: { stop: ReturnType<typeof jest.fn> };
}
let calls: Call[] = [];

beforeAll(() => {
	mock.module("motion/mini", () => ({
		...realMini,
		animate: (
			el: Element,
			keyframes: { transform: string[] },
			options: Record<string, unknown>,
		) => {
			const controls = { stop: jest.fn() };
			calls.push({ el, keyframes, options, controls });
			return controls;
		},
	}));
});
afterAll(() => {
	mock.module("motion/mini", () => realMini);
});

const realRect = Element.prototype.getBoundingClientRect;
beforeEach(() => {
	// Layout: a row is where its data attributes put it; the list sits at the origin.
	Element.prototype.getBoundingClientRect = function (this: Element) {
		const el = this as HTMLElement;
		const top = Number(el.dataset.top ?? 0);
		return {
			left: 0,
			top,
			width: 100,
			height: 32,
			right: 100,
			bottom: top + 32,
			x: 0,
			y: top,
			toJSON() {},
		} as DOMRect;
	};
});
afterEach(() => {
	Element.prototype.getBoundingClientRect = realRect;
});

let media: ReturnType<typeof stubMedia>;
beforeEach(() => {
	calls = [];
	media = stubMedia({ "(prefers-reduced-motion: reduce)": false });
});
afterEach(() => {
	media.restore();
	jest.useRealTimers();
	// The input modality is module state: a pointer press puts it back to "not keyboard".
	fireEvent.pointerDown(document.body);
});

function Nav({
	active,
	layoutKey = "a",
}: {
	active?: string | undefined;
	layoutKey?: string;
}) {
	const container = useRef<HTMLDivElement>(null);
	const indicator = useRef<HTMLSpanElement>(null);
	const nav = useNavIndicator(container, indicator, layoutKey);
	const rows: Array<[string, number]> = [
		["home", 0],
		["servers", 40],
		["pages", 80],
	];
	return (
		<div ref={container} data-testid="list" onPointerDownCapture={nav.press}>
			<span ref={indicator} data-testid="indicator" />
			{rows.map(([name, top]) => (
				<a
					key={name}
					href={`/${name}`}
					data-testid={name}
					data-top={top}
					{...(active === name ? { "data-status": "active" } : {})}
					{...(name === "pages" ? { "data-nav-external": "" } : {})}
				>
					{name}
				</a>
			))}
		</div>
	);
}

const row = (name: string) =>
	document.querySelector(`[data-testid="${name}"]`) as HTMLElement;
const indicator = () =>
	document.querySelector('[data-testid="indicator"]') as HTMLElement;

describe("useNavIndicator", () => {
	test("the routed row is the current one and the plate sits behind it at its size", () => {
		render(<Nav active="servers" />);
		expect(row("servers")).toHaveAttribute("data-current");
		expect(row("home")).not.toHaveAttribute("data-current");
		expect(indicator().style.transform).toBe("translate(0px, 40px)");
		expect(indicator().style.width).toBe("100px");
		expect(indicator().style.height).toBe("32px");
		expect(indicator().style.opacity).toBe("1");
		// The first placement is a reveal, not a slide.
		expect(calls).toEqual([]);
	});

	test("with no routed row the plate is hidden and nothing is current", () => {
		render(<Nav active={undefined} />);
		expect(indicator().style.opacity).toBe("0");
		expect(document.querySelector("[data-current]")).toBeNull();
	});

	test("a new route slides the plate to the new row on transform alone", () => {
		const view = render(<Nav active="home" />);
		view.rerender(<Nav active="servers" />);
		expect(calls).toHaveLength(1);
		expect(calls[0]?.el).toBe(indicator());
		expect(calls[0]?.keyframes.transform).toEqual([
			"translate(0px, 0px)",
			"translate(0px, 40px)",
		]);
		expect(calls[0]?.options.duration).toBe(INDICATOR_SECONDS);
		expect(row("servers")).toHaveAttribute("data-current");
		expect(row("home")).not.toHaveAttribute("data-current");
	});

	test("a slide still running is stopped before the next one starts", () => {
		const view = render(<Nav active="home" />);
		view.rerender(<Nav active="servers" />);
		const first = calls[0];
		view.rerender(<Nav active="home" />);
		expect(first?.controls.stop).toHaveBeenCalledTimes(1);
		expect(calls).toHaveLength(2);
	});

	test("a keyboard user's plate jumps instead of sliding", () => {
		const view = render(<Nav active="home" />);
		fireEvent.keyDown(document.body, { key: "ArrowDown" });
		view.rerender(<Nav active="servers" />);
		expect(calls).toEqual([]);
		expect(indicator().style.transform).toBe("translate(0px, 40px)");
	});

	test("reduced motion jumps too", () => {
		const view = render(<Nav active="home" />);
		media.set("(prefers-reduced-motion: reduce)", true);
		view.rerender(<Nav active="servers" />);
		expect(calls).toEqual([]);
		expect(indicator().style.transform).toBe("translate(0px, 40px)");
	});

	test("when the rows change shape (the rail collapses) the plate jumps", () => {
		const view = render(<Nav active="servers" layoutKey="wide" />);
		row("servers").dataset.top = "50";
		view.rerender(<Nav active="servers" layoutKey="rail" />);
		expect(calls).toEqual([]);
		expect(indicator().style.transform).toBe("translate(0px, 50px)");
	});

	test("a row that has not moved leaves the plate alone", () => {
		const view = render(<Nav active="servers" />);
		indicator().style.transform = "marker";
		view.rerender(<Nav active="servers" />);
		expect(indicator().style.transform).toBe("marker");
	});

	test("the plate goes away when the route leaves the list", () => {
		const view = render(<Nav active="servers" />);
		view.rerender(<Nav active={undefined} />);
		expect(indicator().style.opacity).toBe("0");
		expect(document.querySelector("[data-current]")).toBeNull();
	});
});

/** A nav whose second link can be removed while a press on it is pending. */
function Switchable({ show }: { show: boolean }) {
	const container = useRef<HTMLDivElement>(null);
	const plate = useRef<HTMLSpanElement>(null);
	const nav = useNavIndicator(container, plate, "a");
	return (
		<div ref={container} onPointerDownCapture={nav.press}>
			<span ref={plate} />
			<a href="/home" data-status="active">
				home
			</a>
			{show ? (
				<a href="/gone" data-testid="gone">
					gone
				</a>
			) : null}
		</div>
	);
}

describe("useNavIndicator press", () => {
	test("a press highlights the row before the route has changed", () => {
		render(<Nav active="home" />);
		fireEvent.pointerDown(row("servers"), { button: 0 });
		expect(row("servers")).toHaveAttribute("data-current");
		expect(row("home")).not.toHaveAttribute("data-current");
		expect(calls).toHaveLength(1);
		expect(calls[0]?.keyframes.transform?.[1]).toBe("translate(0px, 40px)");
	});

	test("the route catching up keeps the highlight where it is", () => {
		const view = render(<Nav active="home" />);
		fireEvent.pointerDown(row("servers"), { button: 0 });
		view.rerender(<Nav active="servers" />);
		expect(row("servers")).toHaveAttribute("data-current");
		expect(calls).toHaveLength(1);
	});

	test("a navigation that never arrives hands the highlight back", () => {
		jest.useFakeTimers();
		render(<Nav active="home" />);
		fireEvent.pointerDown(row("servers"), { button: 0 });
		act(() => {
			jest.advanceTimersByTime(PENDING_MS - 1);
		});
		expect(row("servers")).toHaveAttribute("data-current");
		act(() => {
			jest.advanceTimersByTime(1);
		});
		expect(row("home")).toHaveAttribute("data-current");
		expect(row("servers")).not.toHaveAttribute("data-current");
	});

	test("pressing the same row twice does not start a second slide", () => {
		render(<Nav active="home" />);
		fireEvent.pointerDown(row("servers"), { button: 0 });
		fireEvent.pointerDown(row("servers"), { button: 0 });
		expect(calls).toHaveLength(1);
	});

	test("modified and non-primary presses are ignored", () => {
		render(<Nav active="home" />);
		for (const init of [
			{ button: 1 },
			{ button: 0, metaKey: true },
			{ button: 0, ctrlKey: true },
			{ button: 0, shiftKey: true },
			{ button: 0, altKey: true },
		]) {
			fireEvent.pointerDown(row("servers"), init);
		}
		expect(row("servers")).not.toHaveAttribute("data-current");
	});

	test("a press off any row, or on a link that leaves the app, is ignored", () => {
		render(<Nav active="home" />);
		fireEvent.pointerDown(
			document.querySelector('[data-testid="list"]') as HTMLElement,
			{ button: 0 },
		);
		fireEvent.pointerDown(row("pages"), { button: 0 });
		expect(row("pages")).not.toHaveAttribute("data-current");
		expect(row("home")).toHaveAttribute("data-current");
	});

	test("a pending row that is removed does not stay highlighted", () => {
		const view = render(<Switchable show />);
		fireEvent.pointerDown(
			document.querySelector('[data-testid="gone"]') as HTMLElement,
			{ button: 0 },
		);
		view.rerender(<Switchable show={false} />);
		expect(document.querySelector('a[href="/home"]')).toHaveAttribute(
			"data-current",
		);
	});
});
