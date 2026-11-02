import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { MenuCaret } from "./menu-caret";

describe("MenuCaret", () => {
	test("is decoration: hidden from assistive tech, with the caller's class added", () => {
		const { container } = render(<MenuCaret className="extra" />);
		const icon = container.querySelector("svg");
		expect(icon).toHaveAttribute("aria-hidden", "true");
		expect(icon).toHaveClass("extra", "shrink-0");
	});
});
