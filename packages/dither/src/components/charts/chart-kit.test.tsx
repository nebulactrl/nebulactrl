import { afterEach, describe, expect, mock, spyOn, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef } from "react";
import { DIMMED_OPACITY } from "./cartesian-math";
import {
	Bloom,
	ChartLive,
	dimStyle,
	Entrance,
	type EntrancePlan,
	Grow,
	Sweep,
	useChartUid,
	useEntrance,
	useLegendState,
	useSpotlight,
} from "./chart-kit";
import { installChartEnv } from "./test-env";

const env = installChartEnv();

interface FakeAnimation {
	cancel: ReturnType<typeof mock>;
	onfinish: (() => void) | null;
}
interface AnimateCall {
	frames: Keyframe[];
	options: KeyframeAnimationOptions;
	animation: FakeAnimation;
}

let spies: { mockRestore: () => void }[] = [];
afterEach(() => {
	for (const s of spies) s.mockRestore();
	spies = [];
});

function fullMotion() {
	spies.push(
		spyOn(window, "matchMedia").mockImplementation(
			(q: string) =>
				({
					matches: false,
					media: q,
					addEventListener: () => {},
					removeEventListener: () => {},
				}) as unknown as MediaQueryList,
		),
	);
}

/** Replace Element.animate with a recorder; returns the calls it saw. */
function recordAnimations(): AnimateCall[] {
	const calls: AnimateCall[] = [];
	spies.push(
		spyOn(Element.prototype, "animate").mockImplementation(((
			frames: Keyframe[],
			options: KeyframeAnimationOptions,
		) => {
			const animation: FakeAnimation = {
				cancel: mock(() => {}),
				onfinish: null,
			};
			calls.push({ frames, options, animation });
			return animation as unknown as Animation;
		}) as unknown as Element["animate"]),
	);
	return calls;
}

const armed: EntrancePlan = { armed: true, go: false };
const playing: EntrancePlan = { armed: true, go: true };
const off: EntrancePlan = { armed: false, go: false };

function EntranceProbe({ animate = true }: { animate?: boolean }) {
	const ref = useRef<HTMLDivElement>(null);
	const plan = useEntrance(ref, animate);
	return (
		<div
			ref={ref}
			data-testid="probe"
			data-armed={plan.armed}
			data-go={plan.go}
		/>
	);
}

function Spotlight() {
	const s = useSpotlight();
	return (
		<div>
			<span data-testid="focus">{String(s.focus)}</span>
			<span data-testid="locked">{String(s.locked)}</span>
			<button type="button" onClick={() => s.hover("a")}>
				hover a
			</button>
			<button type="button" onClick={() => s.lock("b")}>
				lock b
			</button>
			<button type="button" onClick={() => s.hover(null)}>
				unhover
			</button>
			<button type="button" onClick={s.clear}>
				clear
			</button>
		</div>
	);
}

function Legend({ action }: { action?: "spotlight" | "toggle" }) {
	const state = useLegendState(3, action);
	return (
		<div>
			<span data-testid="hidden">{[...state.hidden].sort().join(",")}</span>
			<span data-testid="mode">
				{"onToggle" in state.legendProps ? "toggle" : "spotlight"}
			</span>
			<span data-testid="opacity-a">{state.opacityOf("a")}</span>
			<span data-testid="opacity-b">{state.opacityOf("b")}</span>
			{"onToggle" in state.legendProps ? (
				["a", "b", "c"].map((l) => (
					<button
						key={l}
						type="button"
						onClick={() => state.legendProps.onToggle?.(l)}
					>
						toggle {l}
					</button>
				))
			) : (
				<>
					<button
						type="button"
						onClick={() => state.legendProps.onHover?.("a")}
					>
						hover a
					</button>
					<button type="button" onClick={state.clear}>
						clear
					</button>
				</>
			)}
		</div>
	);
}

function Uid() {
	return <i data-testid="uid">{useChartUid()}</i>;
}

describe("useEntrance", () => {
	test("armed while motion is allowed, and plays from the first time it is seen", () => {
		fullMotion();
		render(<EntranceProbe />);
		const probe = screen.getByTestId("probe");
		expect(probe).toHaveAttribute("data-armed", "true");
		expect(probe).toHaveAttribute("data-go", "false");
		env.intersect(true);
		expect(probe).toHaveAttribute("data-go", "true");
		// Seen once is seen for good: scrolling away does not rearm it.
		env.intersect(false);
		expect(probe).toHaveAttribute("data-go", "true");
	});

	test("animate={false} never arms", () => {
		fullMotion();
		render(<EntranceProbe animate={false} />);
		env.intersect(true);
		const probe = screen.getByTestId("probe");
		expect(probe).toHaveAttribute("data-armed", "false");
		expect(probe).toHaveAttribute("data-go", "false");
	});

	test("reduced motion (the suite's default) never arms", () => {
		render(<EntranceProbe />);
		env.intersect(true);
		expect(screen.getByTestId("probe")).toHaveAttribute("data-armed", "false");
	});
});

describe("Entrance", () => {
	const wrap = (
		plan: EntrancePlan,
		props: Partial<Parameters<typeof Entrance>[0]> = {},
	) =>
		render(
			<svg>
				<title>t</title>
				<Entrance plan={plan} kind="fade" {...props}>
					<circle data-testid="child" r="1" />
				</Entrance>
			</svg>,
		);

	test("hides its children while armed and not yet seen", () => {
		const { container } = wrap(armed);
		expect(container.querySelector("g")?.style.opacity).toBe("0");
		expect(screen.getByTestId("child")).toBeInTheDocument();
	});

	test("shows plain children when not armed or already playing", () => {
		const idle = wrap(off);
		expect(idle.container.querySelector("g")?.style.opacity).toBe("");
		idle.unmount();
		const go = wrap(playing);
		expect(go.container.querySelector("g")?.style.opacity).toBe("");
	});

	test("does not animate until the plan goes", () => {
		const calls = recordAnimations();
		wrap(armed);
		expect(calls).toHaveLength(0);
	});

	test("fade steps through the dither levels with the given delay and duration", () => {
		const calls = recordAnimations();
		wrap(playing, { kind: "fade", delay: 120, duration: 500 });
		expect(calls).toHaveLength(1);
		expect(calls[0]?.frames).toEqual([{ opacity: 0 }, { opacity: 1 }]);
		expect(calls[0]?.options).toMatchObject({
			duration: 500,
			delay: 120,
			easing: "steps(6, end)",
			fill: "backwards",
		});
	});

	test("wipe reveals left to right with a clip path", () => {
		const calls = recordAnimations();
		wrap(playing, { kind: "wipe" });
		const [from, to] = calls[0]?.frames ?? [];
		expect(from?.clipPath).toBe("inset(-30% 100% -30% -2%)");
		expect(to?.clipPath).toBe("inset(-30% -2% -30% -2%)");
		expect(calls[0]?.options.duration).toBe(700);
		expect(calls[0]?.options.easing).toContain("cubic-bezier");
	});

	test("rise scales up from the baseline", () => {
		const calls = recordAnimations();
		const { container } = wrap(playing, { kind: "rise", originY: 180 });
		expect(calls[0]?.frames).toEqual([
			{ transform: "scaleY(0)" },
			{ transform: "scaleY(1)" },
		]);
		expect(container.querySelector("g")?.style.transformOrigin).toBe(
			"0px 180px",
		);
	});

	test("rise without an origin keeps the default transform origin", () => {
		const { container } = wrap(off, { kind: "rise" });
		expect(container.querySelector("g")?.style.transformOrigin).toBe("");
	});

	test("cancels the animation on unmount", () => {
		const calls = recordAnimations();
		const view = wrap(playing);
		view.unmount();
		expect(calls[0]?.animation.cancel).toHaveBeenCalledTimes(1);
	});
});

describe("Sweep", () => {
	const sweep = (plan: EntrancePlan) =>
		render(
			<svg>
				<title>t</title>
				<Sweep plan={plan} cx={50} cy={50} radius={40}>
					<path data-testid="slice" d="M0 0" />
				</Sweep>
			</svg>,
		);

	test("not armed: children drawn plainly with no mask", () => {
		const { container } = sweep(off);
		expect(container.querySelector("mask")).toBeNull();
		expect(screen.getByTestId("slice")).toBeInTheDocument();
	});

	test("armed: a clockwise mask from 12 o'clock hides the slices until seen", () => {
		const { container } = sweep(armed);
		const ring = container.querySelector("mask circle");
		expect(ring).toHaveAttribute("r", "20");
		expect(ring).toHaveAttribute("stroke-width", "42");
		expect(ring).toHaveAttribute("transform", "rotate(-90 50 50)");
		const maskId = container.querySelector("mask")?.id;
		const group = container.querySelector("g[mask]") as SVGGElement;
		expect(group.getAttribute("mask")).toBe(`url(#${maskId})`);
		expect(group.style.opacity).toBe("0");
	});

	test("going draws the circumference in, then drops the mask so glows are not clipped", () => {
		const calls = recordAnimations();
		const { container } = sweep(playing);
		expect(calls).toHaveLength(1);
		const circumference = 2 * Math.PI * 20;
		expect(calls[0]?.frames).toEqual([
			{ strokeDashoffset: circumference },
			{ strokeDashoffset: 0 },
		]);
		expect(calls[0]?.options.duration).toBe(900);
		expect(container.querySelector("mask")).not.toBeNull();
		act(() => calls[0]?.animation.onfinish?.());
		expect(container.querySelector("mask")).toBeNull();
		expect(screen.getByTestId("slice")).toBeInTheDocument();
	});
});

describe("Grow", () => {
	test("grows from the centre, hidden until seen", () => {
		const calls = recordAnimations();
		const idle = render(
			<svg>
				<title>t</title>
				<Grow plan={armed} cx={30} cy={40}>
					<circle r="1" />
				</Grow>
			</svg>,
		);
		const g = idle.container.querySelector("g") as SVGGElement;
		expect(g.style.transformOrigin).toBe("30px 40px");
		expect(g.style.opacity).toBe("0");
		expect(calls).toHaveLength(0);
		idle.unmount();
		const view = render(
			<svg>
				<title>t</title>
				<Grow plan={playing} cx={30} cy={40} delay={90} duration={400}>
					<circle r="1" />
				</Grow>
			</svg>,
		);
		expect(calls).toHaveLength(1);
		expect(calls[0]?.frames).toEqual([
			{ transform: "scale(0)", opacity: 0 },
			{ transform: "scale(1)", opacity: 1 },
		]);
		expect(calls[0]?.options).toMatchObject({ delay: 90, duration: 400 });
		expect(view.container.querySelector("g")?.style.opacity).toBe("");
		view.unmount();
		expect(calls[0]?.animation.cancel).toHaveBeenCalled();
	});
});

describe("Bloom", () => {
	const bloom = (level: "off" | "low" | "high" | undefined) =>
		render(
			<svg>
				<title>t</title>
				<Bloom bloom={level} box={{ width: 200, height: 100 }}>
					<path d="M0 0" />
				</Bloom>
			</svg>,
		);

	test("off and unset render nothing", () => {
		expect(bloom("off").container.querySelector("g")).toBeNull();
		expect(bloom(undefined).container.querySelector("g")).toBeNull();
	});

	test("low and high blur by their radius over a region padded around the chart", () => {
		const low = bloom("low");
		expect(low.container.querySelector("feGaussianBlur")).toHaveAttribute(
			"stdDeviation",
			"5",
		);
		const filter = low.container.querySelector("filter");
		expect(filter).toHaveAttribute("x", "-20");
		expect(filter).toHaveAttribute("width", "240");
		expect(filter).toHaveAttribute("height", "140");
		low.unmount();
		const high = bloom("high");
		expect(high.container.querySelector("feGaussianBlur")).toHaveAttribute(
			"stdDeviation",
			"9",
		);
		expect(high.container.querySelector("filter")).toHaveAttribute("x", "-36");
	});

	test("the glow is decorative, never takes pointer events and is scaled by --dither-bloom", () => {
		const { container } = bloom("low");
		const g = container.querySelector("g") as SVGGElement;
		expect(g).toHaveAttribute("aria-hidden", "true");
		expect(g).toHaveAttribute("pointer-events", "none");
		expect(g.style.opacity).toContain("var(--dither-bloom");
		const id = container.querySelector("filter")?.id;
		expect(g.getAttribute("filter")).toBe(`url(#${id})`);
	});
});

describe("legend state", () => {
	test("hover wins over the lock, unhover falls back to the lock, clear lets go of both", async () => {
		const user = userEvent.setup();
		render(<Spotlight />);
		expect(screen.getByTestId("focus")).toHaveTextContent("null");
		await user.click(screen.getByText("lock b"));
		expect(screen.getByTestId("focus")).toHaveTextContent("b");
		await user.click(screen.getByText("hover a"));
		expect(screen.getByTestId("focus")).toHaveTextContent("a");
		expect(screen.getByTestId("locked")).toHaveTextContent("b");
		await user.click(screen.getByText("unhover"));
		expect(screen.getByTestId("focus")).toHaveTextContent("b");
		await user.click(screen.getByText("clear"));
		expect(screen.getByTestId("focus")).toHaveTextContent("null");
		expect(screen.getByTestId("locked")).toHaveTextContent("null");
	});

	test("spotlight mode dims every series but the focused one", async () => {
		const user = userEvent.setup();
		render(<Legend />);
		expect(screen.getByTestId("mode")).toHaveTextContent("spotlight");
		expect(screen.getByTestId("opacity-b")).toHaveTextContent("1");
		await user.click(screen.getByText("hover a"));
		expect(screen.getByTestId("opacity-a")).toHaveTextContent("1");
		expect(screen.getByTestId("opacity-b")).toHaveTextContent(
			String(DIMMED_OPACITY),
		);
		await user.click(screen.getByText("clear"));
		expect(screen.getByTestId("opacity-b")).toHaveTextContent("1");
	});

	test("toggle mode hides and shows series but always keeps one visible", async () => {
		const user = userEvent.setup();
		render(<Legend action="toggle" />);
		expect(screen.getByTestId("mode")).toHaveTextContent("toggle");
		await user.click(screen.getByText("toggle a"));
		await user.click(screen.getByText("toggle b"));
		expect(screen.getByTestId("hidden")).toHaveTextContent("a,b");
		// Hiding the third would leave nothing: refused.
		await user.click(screen.getByText("toggle c"));
		expect(screen.getByTestId("hidden")).toHaveTextContent("a,b");
		await user.click(screen.getByText("toggle a"));
		expect(screen.getByTestId("hidden")).toHaveTextContent("b");
	});

	test("dimStyle pairs an opacity with the shared transition", () => {
		expect(dimStyle(0.4)).toEqual({
			opacity: 0.4,
			transition: "opacity 150ms ease-out",
		});
	});
});

describe("ChartLive and useChartUid", () => {
	test("key help carries the id and a polite, atomic live region carries the text", () => {
		const { container } = render(<ChartLive id="x-keys" text="Mon: A 1" />);
		expect(document.getElementById("x-keys")).toHaveTextContent(
			"Use the arrow keys to move through the data, Home and End to jump to the ends, Escape to clear.",
		);
		const live = container.querySelector("[aria-live]");
		expect(live).toHaveAttribute("aria-live", "polite");
		expect(live).toHaveAttribute("aria-atomic", "true");
		expect(live).toHaveTextContent("Mon: A 1");
	});

	test("uids are CSS-id safe and differ per chart", () => {
		render(
			<>
				<Uid />
				<Uid />
			</>,
		);
		const [a, b] = screen.getAllByTestId("uid").map((e) => e.textContent ?? "");
		expect(a).toMatch(/^nc-[a-zA-Z0-9_-]+$/);
		expect(b).toMatch(/^nc-[a-zA-Z0-9_-]+$/);
		expect(a).not.toBe(b);
	});
});
