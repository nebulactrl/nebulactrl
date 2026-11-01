import { describe, expect, test } from "bun:test";
import type { DitherPalette } from "../../lib/tokens";
import { MIN_COLUMNS } from "./grid";
import {
	DITHER_IMAGE_PRESETS,
	IMAGE_PRESETS,
	paletteEntries,
	resolveColor,
	resolveLook,
	resolveRgba,
	sortByLuminance,
} from "./look";

const PALETTE: DitherPalette = {
	ink: "#ffffff",
	paper: "#101010",
	accent: "#808080",
	muted: "#a0a0a0",
	status: {
		live: "#00ff00",
		building: "#0000ff",
		deploying: "#00ffff",
		failed: "#ff0000",
		warning: "#ffff00",
		info: "#0000ff",
		stopped: "#777777",
	},
	series: ["#111111", "#222222", "#333333", "#444444", "#555555", "#666666"],
	named: {
		blue: "#2040ff",
		purple: "#8040ff",
		green: "#20c040",
		pink: "#ff60a0",
		orange: "#ff9020",
		red: "#ff2020",
		grey: "#808080",
	},
};

describe("palette entries", () => {
	test("named recipes and explicit lists", () => {
		expect(paletteEntries("mono")).toEqual(["paper", "ink"]);
		expect(paletteEntries("tritone")).toEqual(["paper", "accent", "ink"]);
		expect(paletteEntries(["paper", "green", "pink"])).toEqual([
			"paper",
			"green",
			"pink",
		]);
	});

	test("names, keywords and transparent resolve without a DOM", () => {
		expect(resolveColor("blue", PALETTE)).toEqual([0x20, 0x40, 0xff]);
		expect(resolveColor("ink", PALETTE)).toEqual([255, 255, 255]);
		expect(resolveColor("paper", PALETTE)).toEqual([16, 16, 16]);
		expect(resolveColor("accent", PALETTE)).toEqual([128, 128, 128]);
		expect(resolveRgba("transparent", PALETTE)[3]).toBe(0);
		expect(resolveRgba("#ff0000", PALETTE)).toEqual([255, 0, 0, 1]);
	});

	test("sorting is darkest first and stable", () => {
		const sorted = sortByLuminance([
			[255, 255, 255],
			[0, 0, 0],
			[128, 128, 128],
			[128, 128, 128],
		] as const);
		expect(sorted.map((c) => c[0])).toEqual([0, 128, 128, 255]);
	});
});

describe("presets", () => {
	test("there are five, and each says method, cell and contrast", () => {
		expect([...DITHER_IMAGE_PRESETS]).toEqual([
			"ink",
			"duotone",
			"newsprint",
			"gameboy",
			"riso",
		]);
		expect(IMAGE_PRESETS.duotone).toMatchObject({
			method: "blue-noise",
			cell: 2,
			contrast: 1.1,
		});
		expect(IMAGE_PRESETS.gameboy).toMatchObject({ method: "bayer4", cell: 3 });
		expect(IMAGE_PRESETS.newsprint).toMatchObject({ method: "bayer8" });
		expect(IMAGE_PRESETS.ink.method).toBe("atkinson");
		expect(IMAGE_PRESETS.riso.palette("ink")).toHaveLength(4);
		expect(IMAGE_PRESETS.gameboy.palette("ink")).toHaveLength(4);
		expect(IMAGE_PRESETS.newsprint.palette("ink")).toHaveLength(2);
		expect(IMAGE_PRESETS.duotone.palette("green")).toHaveLength(3);
	});
});

describe("resolveLook", () => {
	test("defaults to the duotone preset", () => {
		const look = resolveLook({}, PALETTE);
		expect(look.raster.algorithm).toBe("blue-noise");
		expect(look.raster.contrast).toBe(1.1);
		expect(look.raster.sharpen).toBe(0.3);
		expect(look.raster.normalize).toBe(true);
		expect(look.raster.palette).toHaveLength(3);
		expect(look.cell).toBe(2);
		expect(look.minColumns).toBe(MIN_COLUMNS);
		expect(look.fit).toBe("cover");
	});

	test("props override the preset; an explicit cell turns off the column floor", () => {
		const look = resolveLook(
			{
				preset: "newsprint",
				method: "sierra-lite",
				cell: 5,
				contrast: 2,
				sharpen: 0,
				fit: "contain",
			},
			PALETTE,
		);
		expect(look.raster.algorithm).toBe("sierra-lite");
		expect(look.raster.contrast).toBe(2);
		expect(look.raster.sharpen).toBe(0);
		expect(look.cell).toBe(5);
		expect(look.minColumns).toBe(0);
		expect(look.fit).toBe("contain");
	});

	test("method wins over algorithm; bayer methods carry their size", () => {
		expect(
			resolveLook({ method: "bayer8", algorithm: "atkinson" }, PALETTE).raster,
		).toMatchObject({ algorithm: "bayer", bayerSize: 8 });
		expect(
			resolveLook({ algorithm: "bayer", bayerSize: 2 }, PALETTE).raster,
		).toMatchObject({ algorithm: "bayer", bayerSize: 2 });
		expect(resolveLook({ preset: "newsprint" }, PALETTE).raster).toMatchObject({
			algorithm: "bayer",
			bayerSize: 8,
		});
	});

	test("a palette replaces the preset's, sorted dark to light unless told not to", () => {
		const palette = ["ink", "paper", "blue"] as const;
		expect(
			resolveLook({ palette }, PALETTE).raster.palette.map((c) => c[0]),
		).toEqual([16, 0x20, 255]);
		expect(
			resolveLook({ palette, sortPalette: false }, PALETTE).raster.palette.map(
				(c) => c[0],
			),
		).toEqual([255, 16, 0x20]);
	});

	test("transparent entries become palette alpha and sort first", () => {
		const raster = resolveLook(
			{ palette: ["paper", "transparent", "blue"] },
			PALETTE,
		).raster;
		expect(raster.paletteAlpha).toEqual([0, 1, 1]);
		expect(resolveLook({ palette: "mono" }, PALETTE).raster.paletteAlpha).toBe(
			undefined,
		);
	});

	test("the ink colour reaches the ink preset", () => {
		const raster = resolveLook(
			{ preset: "ink", color: "pink" },
			{ ...PALETTE, paper: "#101010" },
		).raster;
		// paper and pink: two colours. Without a DOM `--dither-paper` is mid grey.
		expect(raster.palette).toHaveLength(2);
		expect(raster.palette.map((c) => c[0])).toContain(0xff);
	});

	test("colour mode and named palette recipes", () => {
		const raster = resolveLook(
			{ palette: "tritone", mode: "color" },
			PALETTE,
		).raster;
		expect(raster.mode).toBe("color");
		expect(raster.palette).toHaveLength(3);
	});
});
