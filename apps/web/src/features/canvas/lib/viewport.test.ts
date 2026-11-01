import { describe, expect, test } from "bun:test";
import { initialViewport } from "./viewport";

const panned = { x: -120, y: 40, zoom: 0.94 };

describe("initialViewport", () => {
	test("a saved view is kept on a desktop screen", () => {
		expect(initialViewport(panned, 1440)).toEqual(panned);
		expect(initialViewport(panned, 760)).toEqual(panned);
	});

	test("a phone fits everything on first paint, whatever was saved", () => {
		expect(initialViewport(panned, 390)).toBeUndefined();
		expect(initialViewport(panned, 759)).toBeUndefined();
	});

	test("nothing saved, or the server's untouched default, fits everything", () => {
		expect(initialViewport(null, 1440)).toBeUndefined();
		expect(initialViewport({ x: 0, y: 0, zoom: 1 }, 1440)).toBeUndefined();
	});
});
