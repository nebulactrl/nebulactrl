import { expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { CoolifyMark } from "./coolify-mark";

test("the mark is a decorative tile sized by the prop, with a rounded corner and a scaled icon", () => {
	const { container } = render(<CoolifyMark size={40} />);
	const tile = container.querySelector(
		"[data-slot=service-icon]",
	) as HTMLElement;
	expect(tile).toHaveAttribute("aria-hidden", "true");
	expect(tile.style.width).toBe("40px");
	expect(tile.style.height).toBe("40px");
	expect(tile.style.borderRadius).toBe("12px");
	const icon = tile.querySelector("svg") as SVGElement;
	expect(icon.getAttribute("width")).toBe("23");
	expect(icon.getAttribute("height")).toBe("23");
});

test("defaults to 22px", () => {
	const { container } = render(<CoolifyMark />);
	const tile = container.querySelector(
		"[data-slot=service-icon]",
	) as HTMLElement;
	expect(tile.style.width).toBe("22px");
	expect(tile.style.borderRadius).toBe("6px");
});
