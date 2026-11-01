import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { DitherProgressBar } from "./progress-bar";

const render = (props: Partial<Parameters<typeof DitherProgressBar>[0]>) =>
	renderToStaticMarkup(
		<DitherProgressBar
			label="Deployment"
			value={40}
			animate={false}
			{...props}
		/>,
	);

describe("DitherProgressBar activity and complete", () => {
	test("an ordinary bar has no band and no resolve layer", () => {
		const html = render({});
		expect(html).not.toContain("data-dither-band");
		expect(html).not.toContain("data-dither-resolve");
	});

	test('activity="band" draws one travelling band inside the fill, on the 1.4s keyframe', () => {
		const html = render({ activity: "band" });
		expect(html.match(/data-dither-band/g)).toHaveLength(1);
		expect(html).toContain("nc-dither-band 1.4s linear infinite");
		expect(html).not.toContain("data-dither-complete");
	});

	test("a running bar carries an idle resolve layer that is ready to fade in", () => {
		const html = render({ activity: "band", complete: false });
		expect(html).toContain("data-dither-band");
		expect(html).toContain("data-dither-resolve");
		expect(html).not.toContain("data-dither-complete");
		expect(html).toContain("opacity:0");
	});

	test("complete stops the band and resolves the fill to full density in 300ms", () => {
		const html = render({ value: 100, activity: "band", complete: true });
		expect(html).not.toContain("data-dither-band");
		expect(html).toContain("data-dither-complete");
		expect(html).toContain("opacity 300ms cubic-bezier(0.23, 1, 0.32, 1)");
	});

	test("the band is opt in: complete alone adds no band", () => {
		const html = render({ complete: false });
		expect(html).not.toContain("data-dither-band");
	});
});
