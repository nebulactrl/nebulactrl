import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { ChannelIcon } from "./channel-icon";
import type { ChannelKind } from "./model";

function tile(kind: ChannelKind, size = 36) {
	const { container } = render(<ChannelIcon kind={kind} size={size} />);
	const el = container.querySelector<HTMLElement>('[data-slot="service-icon"]');
	if (!el) throw new Error("no tile rendered");
	return el;
}

describe("ChannelIcon", () => {
	test("brand channels draw their glyph from the brand sprite", () => {
		for (const kind of ["discord", "telegram"] as const) {
			const el = tile(kind);
			const use = el.querySelector("use");
			expect(use?.getAttribute("href")).toEndWith(`#brand-${kind}`);
		}
	});

	test("slack, email and webhook use a generic glyph, not the sprite", () => {
		for (const kind of ["slack", "email", "webhook"] as const) {
			const el = tile(kind);
			expect(el.querySelector("use")).toBeNull();
			expect(el.querySelector("svg")).not.toBeNull();
		}
	});

	test("slack keeps its aubergine tile and a white glyph", () => {
		const el = tile("slack");
		expect(el.style.getPropertyValue("--t")).toBe("#4a154b");
		expect(el.style.color).toMatch(/^(#fff(fff)?|white|rgb\(255, 255, 255\))$/);
	});

	test("the tile scales its size, corner radius and glyph with `size`", () => {
		const el = tile("email", 100);
		expect(el.style.width).toBe("100px");
		expect(el.style.height).toBe("100px");
		expect(el.style.borderRadius).toBe("29px");
		const glyph = el.querySelector("svg");
		expect(glyph?.getAttribute("width")).toBe("57");
	});

	test("defaults to a 36px tile and is hidden from assistive tech", () => {
		const el = tile("webhook");
		expect(el.style.width).toBe("36px");
		expect(el).toHaveAttribute("aria-hidden", "true");
	});
});
