import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { act, fireEvent, render } from "@testing-library/react";
import { DitherGlobe } from "./globe";
import {
	frame,
	installProbe,
	lastShader,
	lastUniforms,
	motion,
	resetProbe,
	restoreProbe,
	shaderCalls,
} from "./shader-probe";

// The globe owns its rotation: it registers a frame callback and feeds the
// shader a longitude, tilt and clock. The shader and the frame loop are
// replaced by recorders so a test can play frames and gestures by hand.
installProbe();
afterAll(restoreProbe);
beforeEach(resetProbe);

const PAPER = "--dither-paper";
const INK = "--dither-ink";

function mount(ui: React.ReactElement) {
	const view = render(ui);
	const host = view.container.firstElementChild as HTMLElement;
	return { ...view, host };
}

/** Plays one frame of the shared loop. */
function play(time: number, delta: number, still?: "reduced" | "disabled") {
	act(() => frame.run?.(time, delta, still));
}

function pointer(
	host: HTMLElement,
	type: string,
	init: { x?: number; y?: number; t: number },
) {
	const event = new PointerEvent(type, {
		bubbles: true,
		clientX: init.x ?? 0,
		clientY: init.y ?? 0,
		pointerId: 1,
	});
	// React reads a falsy timeStamp as "now", so times are offset from 1000 ms.
	Object.defineProperty(event, "timeStamp", { value: 1000 + init.t });
	act(() => {
		host.dispatchEvent(event);
	});
}

const lon = () => lastUniforms().gbLon as number;
const tilt = () => lastUniforms().gbTilt as number;

describe("DitherGlobe", () => {
	test("defaults: a draggable image with a still shader and the designed uniforms", () => {
		const { host } = mount(<DitherGlobe />);
		expect(host).toHaveAttribute("role", "img");
		expect(host).toHaveAttribute("tabindex", "0");
		expect(host).toHaveAttribute(
			"aria-label",
			"Globe. Drag it or use the arrow keys to turn it.",
		);
		expect(host).not.toHaveAttribute("aria-hidden");
		expect(host).toHaveClass(
			"relative",
			"isolate",
			"cursor-grab",
			"select-none",
			"touch-pan-y",
		);
		const call = lastShader();
		expect(call.palette).toEqual([PAPER, INK, "orange"]);
		expect(call.cell).toBe(2);
		expect(call.levels).toBe(9);
		expect(call.matrix).toBe("bayer4");
		expect(call.animate).toBe(false);
		expect(call.className).toBe("absolute inset-0");
		expect(call.colorField).toBeUndefined();
		expect(Object.keys(call.textures ?? {})).toEqual(["gbLand"]);
		expect(lastUniforms()).toEqual({
			gbLon: 10,
			gbTilt: 18,
			gbTime: 0,
			gbLight: 300,
			gbOcean: 0.18,
			gbAtmo: 1,
			gbColoured: 0,
		});
	});

	test("not draggable: a decorative box with no keyboard or pointer handling", () => {
		const { host } = mount(
			<DitherGlobe
				draggable={false}
				className="size-40"
				style={{ opacity: 0.5 }}
			/>,
		);
		expect(host).toHaveAttribute("aria-hidden", "true");
		expect(host).not.toHaveAttribute("role");
		expect(host).not.toHaveAttribute("tabindex");
		expect(host).not.toHaveAttribute("aria-label");
		expect(host).not.toHaveClass("cursor-grab");
		expect(host).toHaveClass("size-40");
		expect(host.style.opacity).toBe("0.5");
		fireEvent.keyDown(host, { key: "ArrowRight" });
		expect(lon()).toBe(10);
	});

	test("a custom accessible name replaces the default", () => {
		const { host } = mount(<DitherGlobe aria-label="Offices" />);
		expect(host).toHaveAttribute("aria-label", "Offices");
	});

	test("palette: background, land colour, accent; levels follow", () => {
		mount(
			<DitherGlobe
				background="black"
				color="gray"
				accent="--hot"
				levels="full"
				cell={3}
			/>,
		);
		expect(lastShader().palette).toEqual(["black", "gray", "--hot"]);
		expect(lastShader().levels).toBe("full");
		expect(lastShader().cell).toBe(3);
	});

	test("light, ocean and atmosphere reach the shader; ocean is clamped", () => {
		mount(
			<DitherGlobe
				light={90}
				ocean={0.5}
				atmosphere={false}
				start={-40}
				tilt={30}
			/>,
		);
		expect(lastUniforms()).toMatchObject({
			gbLight: 90,
			gbOcean: 0.5,
			gbAtmo: 0,
			gbLon: -40,
			gbTilt: 30,
		});
		mount(<DitherGlobe ocean={4} />);
		expect(lastUniforms().gbOcean).toBe(1);
		mount(<DitherGlobe ocean={-1} />);
		expect(lastUniforms().gbOcean).toBe(0);
	});

	test("`stillTime` is the first clock reading and the loop's still moment", () => {
		mount(<DitherGlobe stillTime={3} />);
		expect(lastUniforms().gbTime).toBe(3);
		expect(frame.options?.stillTime).toBe(3);
	});

	test("the frame loop gets fps, the animate switch and the host as its visibility target", () => {
		const { host } = mount(<DitherGlobe fps={15} animate={false} />);
		expect(frame.options).toMatchObject({ fps: 15, enabled: false });
		expect(frame.options?.target?.current).toBe(host);
		mount(<DitherGlobe />);
		expect(frame.options?.enabled).toBe(true);
		expect(frame.options?.fps).toBeUndefined();
	});

	test("markers and arcs are compiled into the field, at most 24 and 8", () => {
		const markers = Array.from({ length: 30 }, (_, i) => ({
			at: [i, i] as const,
		}));
		const arcs = Array.from({ length: 10 }, (_, i) => ({
			from: [i, 0] as const,
			to: [0, i + 10] as const,
		}));
		mount(<DitherGlobe />);
		const bare = lastShader().field;
		expect(bare.match(/gbMarker\(p,/g)?.length ?? 0).toBe(0);
		mount(
			<DitherGlobe markers={markers.slice(0, 3)} arcs={arcs.slice(0, 2)} />,
		);
		const some = lastShader().field;
		expect(some.match(/v = gbMarker\(p,/g)).toHaveLength(3);
		expect(some.match(/float gbArc\d+\(/g)).toHaveLength(2);
		mount(<DitherGlobe markers={markers} arcs={arcs} />);
		expect(lastShader().field.match(/v = gbMarker\(p,/g)).toHaveLength(24);
		expect(lastShader().field.match(/float gbArc\d+\(/g)).toHaveLength(8);
	});

	test("per-item colours extend the palette, switch on the colour field and use fewer levels", () => {
		mount(
			<DitherGlobe
				accent="orange"
				markers={[
					{ at: [0, 0], color: "blue" },
					{ at: [1, 1] },
					{ at: [2, 2], color: "orange" },
				]}
				arcs={[{ from: [0, 0], to: [10, 10], color: "green" }]}
			/>,
		);
		const call = lastShader();
		expect(call.palette).toEqual([PAPER, INK, "orange", "blue", "green"]);
		expect(call.levels).toBe(5);
		expect(call.colorField).toContain("vec3 colorField(");
		expect(lastUniforms().gbColoured).toBe(1);
		// Ramp slots: land 0, accent 1/3, blue 2/3, green 1 (the last arc).
		expect(call.field).toContain("gbMk = 0.666667;");
		expect(call.field.match(/gbMk = 0\.333333;/g)).toHaveLength(2);
		expect(call.field).toContain("gbAk = 1.0;");
	});

	test("markers without colours of their own keep the plain palette", () => {
		mount(<DitherGlobe markers={[{ at: [0, 0] }]} />);
		expect(lastShader().palette).toEqual([PAPER, INK, "orange"]);
		expect(lastUniforms().gbColoured).toBe(0);
		expect(lastShader().colorField).toBeUndefined();
	});
});

describe("DitherGlobe motion", () => {
	test("each frame sets the marker clock and turns the globe against the spin", () => {
		mount(<DitherGlobe speed={10} />);
		play(2, 0.5);
		expect(lastUniforms().gbTime).toBe(2);
		expect(lon()).toBeCloseTo(10 - 10 * 0.5, 10);
		play(2.5, 0.5);
		expect(lon()).toBeCloseTo(10 - 10, 10);
	});

	test("a negative speed spins the other way", () => {
		mount(<DitherGlobe speed={-20} />);
		play(1, 0.25);
		expect(lon()).toBeCloseTo(10 + 5, 10);
	});

	test("a still frame sets the clock but does not turn the globe", () => {
		mount(<DitherGlobe speed={10} />);
		play(4, 0, "reduced");
		expect(lastUniforms().gbTime).toBe(4);
		expect(lon()).toBe(10);
		play(4, 0.5, "disabled");
		expect(lon()).toBe(10);
	});

	test("no rotation at all with speed 0", () => {
		mount(<DitherGlobe speed={0} />);
		play(1, 1);
		expect(lon()).toBe(10);
		expect(tilt()).toBe(18);
	});
});

describe("DitherGlobe keyboard", () => {
	test.each([
		["ArrowRight", 20, 18],
		["ArrowLeft", 0, 18],
		["ArrowUp", 10, 10],
		["ArrowDown", 10, 26],
	])(
		"%s turns the globe to lon %d, tilt %d",
		(key, expectedLon, expectedTilt) => {
			const { host } = mount(<DitherGlobe />);
			const notPrevented = fireEvent.keyDown(host, { key });
			expect(notPrevented).toBe(false);
			expect(lon()).toBe(expectedLon);
			expect(tilt()).toBe(expectedTilt);
		},
	);

	test("other keys are left alone", () => {
		const { host } = mount(<DitherGlobe />);
		const notPrevented = fireEvent.keyDown(host, { key: "a" });
		expect(notPrevented).toBe(true);
		expect(lon()).toBe(10);
	});

	test("keys add up and the tilt stops at the end of its range", () => {
		const { host } = mount(<DitherGlobe tilt={70} />);
		fireEvent.keyDown(host, { key: "ArrowRight" });
		fireEvent.keyDown(host, { key: "ArrowRight" });
		expect(lon()).toBe(30);
		fireEvent.keyDown(host, { key: "ArrowDown" });
		fireEvent.keyDown(host, { key: "ArrowDown" });
		expect(tilt()).toBe(80);
		for (let i = 0; i < 30; i++) fireEvent.keyDown(host, { key: "ArrowUp" });
		expect(tilt()).toBe(-60);
	});
});

describe("DitherGlobe dragging", () => {
	test("dragging right brings western land into view: longitude drops by 0.3 degrees per pixel", () => {
		const { host } = mount(<DitherGlobe speed={0} />);
		pointer(host, "pointerdown", { x: 100, y: 100, t: 0 });
		pointer(host, "pointermove", { x: 200, y: 100, t: 50 });
		expect(lon()).toBeCloseTo(10 - 100 * 0.3, 10);
		expect(tilt()).toBe(18);
	});

	test("dragging down leans the pole towards the viewer; the tilt is limited", () => {
		const { host } = mount(<DitherGlobe speed={0} />);
		pointer(host, "pointerdown", { x: 0, y: 0, t: 0 });
		pointer(host, "pointermove", { x: 0, y: 50, t: 50 });
		expect(tilt()).toBeCloseTo(18 + 15, 10);
		pointer(host, "pointermove", { x: 0, y: 5000, t: 100 });
		expect(tilt()).toBe(80);
		pointer(host, "pointermove", { x: 0, y: -50000, t: 150 });
		expect(tilt()).toBe(-60);
	});

	test("the drag scale follows the globe's size: a 200px globe turns 180 degrees across its disc", () => {
		const { host } = mount(<DitherGlobe speed={0} />);
		host.getBoundingClientRect = () => ({ width: 200, height: 300 }) as DOMRect;
		pointer(host, "pointerdown", { x: 0, y: 0, t: 0 });
		pointer(host, "pointermove", { x: 84, y: 0, t: 50 });
		// The globe's radius is 42% of the shorter side: 84px for 200px.
		expect(lon()).toBeCloseTo(10 - 180 / Math.PI, 8);
	});

	test("moving without pressing does nothing", () => {
		const { host } = mount(<DitherGlobe speed={0} />);
		pointer(host, "pointermove", { x: 400, y: 400, t: 10 });
		expect(lon()).toBe(10);
	});

	test("the pointer is captured while pressed and released afterwards", () => {
		const { host } = mount(<DitherGlobe />);
		const calls: string[] = [];
		host.setPointerCapture = (id) => calls.push(`set ${id}`);
		host.hasPointerCapture = () => true;
		host.releasePointerCapture = (id) => calls.push(`release ${id}`);
		pointer(host, "pointerdown", { t: 0 });
		pointer(host, "pointerup", { t: 10 });
		expect(calls).toEqual(["set 1", "release 1"]);
	});

	test("it does not release a pointer it no longer holds", () => {
		const { host } = mount(<DitherGlobe />);
		const calls: string[] = [];
		host.hasPointerCapture = () => false;
		host.releasePointerCapture = () => calls.push("release");
		pointer(host, "pointerdown", { t: 0 });
		pointer(host, "pointerup", { t: 10 });
		expect(calls).toEqual([]);
	});

	test("the idle spin holds while the globe is held", () => {
		const { host } = mount(<DitherGlobe speed={10} />);
		pointer(host, "pointerdown", { t: 0 });
		play(1, 0.5);
		expect(lon()).toBe(10);
		pointer(host, "pointerup", { t: 10 });
		play(1.5, 0.5);
		// The spin eases back in rather than jumping to full speed.
		expect(lon()).toBeLessThan(10);
		expect(lon()).toBeGreaterThan(10 - 10 * 0.5);
	});

	test("a flick coasts after release; holding still before letting go does not", () => {
		const { host } = mount(<DitherGlobe speed={0} />);
		pointer(host, "pointerdown", { x: 0, y: 0, t: 0 });
		pointer(host, "pointermove", { x: -20, y: 0, t: 20 });
		pointer(host, "pointerup", { x: -20, y: 0, t: 30 });
		const released = lon();
		play(1, 0.1);
		// Dragged left, so the longitude rises; it keeps rising while coasting.
		expect(lon()).toBeGreaterThan(released);
		const coasted = lon();
		play(1.1, 0.1);
		expect(lon()).toBeGreaterThan(coasted);
	});

	test("letting go after a pause leaves the globe where it is", () => {
		const { host } = mount(<DitherGlobe speed={0} />);
		pointer(host, "pointerdown", { x: 0, y: 0, t: 0 });
		pointer(host, "pointermove", { x: -20, y: 0, t: 20 });
		pointer(host, "pointerup", { x: -20, y: 0, t: 300 });
		const released = lon();
		play(1, 0.1);
		expect(lon()).toBe(released);
	});

	test("cancelling a drag releases it like lifting", () => {
		const { host } = mount(<DitherGlobe speed={10} />);
		pointer(host, "pointerdown", { t: 0 });
		pointer(host, "pointercancel", { t: 10 });
		play(1, 0.5);
		expect(lon()).toBeLessThan(10);
	});

	test("catching a coasting globe stops it", () => {
		const { host } = mount(<DitherGlobe speed={0} />);
		pointer(host, "pointerdown", { x: 0, y: 0, t: 0 });
		pointer(host, "pointermove", { x: -20, y: 0, t: 20 });
		pointer(host, "pointerup", { x: -20, y: 0, t: 30 });
		pointer(host, "pointerdown", { x: 0, y: 0, t: 40 });
		pointer(host, "pointerup", { x: 0, y: 0, t: 45 });
		const caught = lon();
		play(1, 0.2);
		expect(lon()).toBe(caught);
	});

	test("a flick does not coast when the globe is not animating", () => {
		const { host } = mount(<DitherGlobe speed={0} animate={false} />);
		pointer(host, "pointerdown", { x: 0, y: 0, t: 0 });
		pointer(host, "pointermove", { x: -20, y: 0, t: 20 });
		pointer(host, "pointerup", { x: -20, y: 0, t: 30 });
		const released = lon();
		play(1, 0.1);
		expect(lon()).toBe(released);
	});

	test("a flick does not coast under reduced motion", () => {
		motion.reduced = true;
		const { host } = mount(<DitherGlobe speed={0} />);
		pointer(host, "pointerdown", { x: 0, y: 0, t: 0 });
		pointer(host, "pointermove", { x: -20, y: 0, t: 20 });
		pointer(host, "pointerup", { x: -20, y: 0, t: 30 });
		const released = lon();
		play(1, 0.1);
		expect(lon()).toBe(released);
	});

	test("each interaction re-renders the shader with new rotation only", () => {
		const { host } = mount(<DitherGlobe speed={0} />);
		const before = shaderCalls.length;
		const field = lastShader().field;
		pointer(host, "pointerdown", { x: 0, y: 0, t: 0 });
		pointer(host, "pointermove", { x: 10, y: 0, t: 10 });
		expect(shaderCalls.length).toBeGreaterThan(before);
		expect(lastShader().field).toBe(field);
	});
});
