import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { TemplateIcon, TemplatesMark } from "./template-icon";

describe("TemplateIcon", () => {
	test("a template without a brand mark gets a tile with its upper-cased initial", () => {
		const { container } = render(
			<TemplateIcon icon={undefined} name="umami" />,
		);
		const tile = container.querySelector("[data-slot=service-icon]");
		expect(tile).toHaveTextContent("U");
		expect(tile).toHaveStyle({ width: "36px", height: "36px" });
	});

	test("an icon slug the brand sprite lacks falls back to the initial", () => {
		const { container } = render(
			<TemplateIcon icon="not-a-brand" name="  ghost " size={40} />,
		);
		const tile = container.querySelector("[data-slot=service-icon]");
		expect(tile).toHaveTextContent("G");
		expect(tile).toHaveStyle({ width: "40px" });
	});

	test("a blank name shows a question mark rather than an empty tile", () => {
		const { container } = render(<TemplateIcon icon="" name="   " />);
		expect(
			container.querySelector("[data-slot=service-icon]"),
		).toHaveTextContent("?");
	});

	test("a known brand slug draws the sprite mark instead of a letter", () => {
		const { container } = render(<TemplateIcon icon="postgresql" name="Pg" />);
		const mark = container.querySelector("[data-slot=service-icon]");
		expect(mark).not.toHaveTextContent("P");
		expect(mark?.querySelector("use, svg")).not.toBeNull();
	});
});

describe("TemplatesMark", () => {
	test("is a sized decorative tile holding the layers glyph", () => {
		const { container } = render(<TemplatesMark size={30} />);
		const tile = container.querySelector("[data-slot=service-icon]");
		expect(tile).toHaveAttribute("aria-hidden", "true");
		expect(tile).toHaveStyle({ width: "30px", height: "30px" });
		expect(tile?.querySelector("svg")).toHaveAttribute("width", "17");
	});
});
