import { describe, expect, test } from "bun:test";
import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import { renderToStaticMarkup } from "react-dom/server";

/**
 * The reveal contract between the kit and the site: every fill edge is
 * `percent * --dither-grow`, and `animate={false}` leaves the variable to the
 * host's stylesheet (the site holds it at 0 until a bar scrolls into view).
 */
describe("dither bar reveal contract", () => {
	test("animate={false} renders at the final value and sets no grow variable", () => {
		const html = renderToStaticMarkup(
			<DitherProgressBar label="Image" value={40} animate={false} />,
		);
		expect(html).not.toContain("--dither-grow:");
		expect(html).toContain("calc(40% * var(--dither-grow, 1))");
		expect(html).toContain("data-dither-edge");
	});

	test("animate (the default) mounts empty and owns the variable", () => {
		const html = renderToStaticMarkup(
			<DitherProgressBar label="Image" value={40} />,
		);
		expect(html).toContain("--dither-grow:0");
	});

	test("stacked parts share the grow variable and an empty part stays mounted, hidden", () => {
		const html = renderToStaticMarkup(
			<DitherProgressBar
				label="Capabilities"
				animate={false}
				max={10}
				parts={[
					{ label: "Yes", value: 6, color: "green" },
					{ label: "No", value: 0, color: "grey" },
				]}
			/>,
		);
		expect(html).toContain("calc(60% * var(--dither-grow, 1))");
		expect(html.match(/data-dither-edge/g)).toHaveLength(2);
		expect(html).toContain("visibility:hidden");
	});

	test("step blocks fill one after another", () => {
		const html = renderToStaticMarkup(
			<DitherProgressBar
				label="Steps"
				animate={false}
				value={50}
				segments={4}
			/>,
		);
		expect(html).toContain("--dither-fill-delay, 0ms) + 24ms");
	});
});
