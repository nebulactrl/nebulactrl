import { expect, test } from "bun:test";
import {
	markTier,
	NEBULA_MARK_DITHER_MIN,
	NebulaMark,
} from "@nebulactrl/dither/images/nebula-mark-g";
import { renderToStaticMarkup } from "react-dom/server";

test("the threshold is 24 px", () => {
	expect(NEBULA_MARK_DITHER_MIN).toBe(24);
});

test("below 24 px the mark is the plain solid gradient: no shadow, no bevel", () => {
	for (const size of [16, 20, 23]) {
		const html = renderToStaticMarkup(<NebulaMark size={size} />);
		expect(html).not.toContain("crispEdges");
		expect(html).not.toContain("<mask");
		expect(html).toContain("linearGradient");
	}
});

test("from 24 px it draws the shadow and the bevel, hidden in forced colours", () => {
	for (const size of [24, 28, 59, 128]) {
		const html = renderToStaticMarkup(<NebulaMark size={size} />);
		expect(html).toContain("crispEdges");
		expect(html.match(/<mask/g)?.length).toBe(2);
		expect(html.match(/forced-colors:hidden/g)?.length).toBe(2);
		expect(html).not.toContain("<filter");
		expect(html).not.toContain("animate");
	}
});

test("it is decorative unless labelled", () => {
	expect(renderToStaticMarkup(<NebulaMark />)).toContain('aria-hidden="true"');
	expect(renderToStaticMarkup(<NebulaMark label="NebulaCtrl" />)).toContain(
		'aria-label="NebulaCtrl"',
	);
});

test("the nearest generated grid is used", () => {
	expect(markTier(24).size).toBe(24);
	expect(markTier(59).size).toBe(64);
	expect(markTier(200).size).toBe(128);
});

test("two marks never share ids", () => {
	const html = renderToStaticMarkup(
		<>
			<NebulaMark />
			<NebulaMark />
		</>,
	);
	const ids = [...html.matchAll(/ id="([^"]+)"/g)].map((m) => m[1]);
	expect(new Set(ids).size).toBe(ids.length);
});
