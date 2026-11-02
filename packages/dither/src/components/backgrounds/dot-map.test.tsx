import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import type { DitherDotMapProps } from "./dot-map";
import { DitherDotMap } from "./dot-map";
import {
	installProbe,
	lastShader,
	lastUniforms,
	resetProbe,
	restoreProbe,
} from "./shader-probe";

// What `DitherDotMap` hands the shader and draws around it, through a
// recording stand-in for the shader.
installProbe();
afterAll(restoreProbe);
beforeEach(resetProbe);

const PAPER_FREE = "transparent";
const INK = "--dither-ink";

/** A map of lat -60..60 and lon -120..120: two units wide per unit tall, easy sums. */
const BOUNDS = { north: 60, south: -60, west: -120, east: 120 } as const;

function mount(props: DitherDotMapProps = {}) {
	const view = render(<DitherDotMap {...props} />);
	const host = view.container.firstElementChild as HTMLElement;
	return { ...view, host };
}

describe("DitherDotMap frame", () => {
	test("defaults: a labelled image as wide as its parent with the world's aspect ratio", () => {
		const { host } = mount();
		expect(host).toHaveAttribute("role", "img");
		expect(host).toHaveAttribute("aria-label", "World map");
		expect(host).toHaveClass("relative", "w-full", "forced-colors:hidden");
		// 360 degrees of longitude over 78N..56S.
		expect(host.style.aspectRatio).toBe(`${360 / 134} / 1`);
	});

	test("the aspect ratio follows custom bounds", () => {
		const { host } = mount({ bounds: BOUNDS });
		expect(host.style.aspectRatio).toBe("2 / 1");
	});

	test("className merges and a caller's style can override the aspect ratio", () => {
		const { host } = mount({
			className: "max-h-full w-1/2",
			style: { aspectRatio: "1", opacity: 0.5 },
		});
		expect(host).toHaveClass("max-h-full", "w-1/2", "relative");
		expect(host).not.toHaveClass("w-full");
		expect(host.style.aspectRatio).toBe("1 / 1");
		expect(host.style.opacity).toBe("0.5");
	});

	test("the title names the map", () => {
		const { host } = mount({ title: "Offices" });
		expect(host).toHaveAttribute("aria-label", "Offices");
	});

	test("labelled markers are read out after the title", () => {
		const { host } = mount({
			bounds: BOUNDS,
			markers: [
				{ at: [0, 0], label: "Null Island" },
				{ at: [30, -60], label: "North west" },
				{ at: [10, 10] },
			],
		});
		expect(host).toHaveAttribute(
			"aria-label",
			"World map, Null Island, North west",
		);
	});

	test("a marker outside the bounds is neither read out nor drawn", () => {
		const { host } = mount({
			bounds: BOUNDS,
			markers: [
				{ at: [0, 0], label: "Inside" },
				{ at: [80, 0], label: "Too far north" },
				{ at: [-70, 0], label: "Too far south" },
			],
		});
		expect(host).toHaveAttribute("aria-label", "World map, Inside");
		expect(screen.queryByText("Too far north")).toBeNull();
		expect(screen.queryByText("Too far south")).toBeNull();
	});

	test("labels sit beside their dots, positioned as a share of the map", () => {
		mount({
			bounds: BOUNDS,
			markers: [
				{ at: [0, 0], label: "Centre" },
				{ at: [30, -60], label: "Upper left quarter" },
			],
		});
		const centre = screen.getByText("Centre");
		expect(centre.tagName).toBe("SPAN");
		expect(centre).toHaveAttribute("aria-hidden", "true");
		expect(centre).toHaveClass("pointer-events-none", "absolute");
		expect(centre.style.left).toBe("50%");
		expect(centre.style.top).toBe("50%");
		const corner = screen.getByText("Upper left quarter");
		expect(corner.style.left).toBe("25%");
		expect(corner.style.top).toBe("25%");
	});

	test("`labels={false}` draws no labels but keeps them in the accessible name", () => {
		const { host } = mount({
			labels: false,
			bounds: BOUNDS,
			markers: [{ at: [0, 0], label: "Centre" }],
		});
		expect(screen.queryByText("Centre")).toBeNull();
		expect(host.querySelectorAll("span")).toHaveLength(0);
		expect(host).toHaveAttribute("aria-label", "World map, Centre");
	});

	test("the shader fills the frame", () => {
		const { host } = mount();
		expect(lastShader().className).toBe("absolute inset-0");
		expect(host.querySelector("[data-slot=probe-shader]")).toHaveClass(
			"absolute",
			"inset-0",
		);
	});
});

describe("DitherDotMap shader", () => {
	test("defaults: grey land on a clear background, one-pixel cells, nine tone steps", () => {
		mount();
		const call = lastShader();
		expect(call.palette).toEqual([PAPER_FREE, "grey", INK]);
		expect(call.cell).toBe(1);
		expect(call.levels).toBe(9);
		expect(call.matrix).toBe("bayer4");
		expect(call.staticTime).toBe(1);
		expect(call.colorField).toBeUndefined();
		expect(Object.keys(call.textures ?? {})).toEqual(["dmLand"]);
		expect(call["aria-hidden"]).toBe(true);
		expect(lastUniforms()).toEqual({
			dmCols: 160,
			dmAspect: 360 / 134,
			dmBounds: [-180, 180, -56, 78],
			dmSpan: [360, 134],
			dmPitchDeg: 360 / 160,
			dmHalo: 14,
			dmOcean: 0,
			dmRound: 0,
			dmColoured: 0,
		});
	});

	test("colours: land, accent and background build the ramp", () => {
		mount({ color: "blue", accent: "red", background: "white" });
		expect(lastShader().palette).toEqual(["white", "blue", "red"]);
	});

	test("bounds feed the shader as west, east, south, north and as spans", () => {
		mount({ bounds: BOUNDS, columns: 120 });
		expect(lastUniforms()).toMatchObject({
			dmBounds: [-120, 120, -60, 60],
			dmSpan: [240, 120],
			dmAspect: 2,
			dmCols: 120,
			dmPitchDeg: 2,
		});
	});

	test("columns are rounded and never below 20", () => {
		mount({ columns: 5 });
		expect(lastUniforms().dmCols).toBe(20);
		mount({ columns: 100.6 });
		expect(lastUniforms().dmCols).toBe(101);
	});

	test('`dots="round"` and its alias `shape` choose round dots, and `shape` wins', () => {
		mount({ dots: "round" });
		expect(lastUniforms().dmRound).toBe(1);
		mount({ shape: "round" });
		expect(lastUniforms().dmRound).toBe(1);
		mount({ dots: "round", shape: "square" });
		expect(lastUniforms().dmRound).toBe(0);
		mount({ dots: "square", shape: "round" });
		expect(lastUniforms().dmRound).toBe(1);
	});

	test("halo has a floor of 0 and ocean is clamped to 0..1", () => {
		mount({ halo: -5, ocean: 3 });
		expect(lastUniforms()).toMatchObject({ dmHalo: 0, dmOcean: 1 });
		mount({ halo: 0, ocean: -1 });
		expect(lastUniforms()).toMatchObject({ dmHalo: 0, dmOcean: 0 });
		mount({ halo: 20, ocean: 0.25 });
		expect(lastUniforms()).toMatchObject({ dmHalo: 20, dmOcean: 0.25 });
	});

	test("engine props pass through", () => {
		const fallback = <b>no gl</b>;
		mount({
			cell: 3,
			levels: 4,
			fps: 8,
			animate: false,
			speed: 2,
			stillTime: 5,
			n: 8,
			fallback,
		});
		expect(lastShader()).toMatchObject({
			cell: 3,
			levels: 4,
			fps: 8,
			animate: false,
			speed: 2,
			staticTime: 5,
			matrix: "bayer8",
			fallback,
		});
		mount({ matrix: "blue-noise", n: 2 });
		expect(lastShader().matrix).toBe("blue-noise");
	});

	test("markers and arcs are compiled into the field, at most 24 and 8", () => {
		const markers = Array.from({ length: 30 }, (_, i) => ({
			at: [i, i] as const,
		}));
		const arcs = Array.from({ length: 10 }, (_, i) => ({
			from: [i, 0] as const,
			to: [0, i + 10] as const,
		}));
		mount();
		expect(lastShader().field.match(/m = dmMarker\(/g)).toBeNull();
		mount({ markers: markers.slice(0, 2), arcs: arcs.slice(0, 3) });
		expect(lastShader().field.match(/m = dmMarker\(/g)).toHaveLength(2);
		const arcFns = lastShader().field.match(/float dmArc\d+\(/g);
		expect(arcFns).toHaveLength(3);
		mount({ markers, arcs });
		expect(lastShader().field.match(/m = dmMarker\(/g)).toHaveLength(24);
		expect(lastShader().field.match(/float dmArc\d+\(/g)).toHaveLength(8);
	});

	test("marker weight scales its core, reach and brightness", () => {
		mount({ markers: [{ at: [0, 0], weight: 1 }] });
		expect(lastShader().field).toContain("vec3(2.2, 1.0, 1.0)");
		mount({ markers: [{ at: [0, 0], weight: 0 }] });
		expect(lastShader().field).toContain("vec3(1.0, 0.45, 0.6)");
		mount({ markers: [{ at: [0, 0] }] });
		expect(lastShader().field).toContain("vec3(1.0, 1.0, 1.0)");
	});

	test("per-item colours extend the palette and switch to the colour field with five levels", () => {
		mount({
			accent: "red",
			markers: [{ at: [0, 0], color: "blue" }, { at: [1, 1] }],
			arcs: [{ from: [0, 0], to: [10, 10], color: "green" }],
		});
		const call = lastShader();
		expect(call.palette).toEqual([PAPER_FREE, "grey", "red", "blue", "green"]);
		expect(call.levels).toBe(5);
		expect(call.colorField).toContain("vec3 colorField(");
		expect(lastUniforms().dmColoured).toBe(1);
		// Land 0, accent 1/3, blue 2/3, green 1.
		expect(call.field).toContain("dmMk = 0.666667;");
		expect(call.field).toContain("dmMk = 0.333333;");
	});
});
