import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { BRAND_ICONS } from "#/assets/brand-icons";
import { ProviderIcon } from "./provider-icon";

function tile(container: HTMLElement): HTMLElement {
	const el = container.querySelector<HTMLElement>('[data-slot="service-icon"]');
	if (!el) throw new Error("no tile");
	return el;
}

describe("ProviderIcon", () => {
	test("sizes the tile and glyph from the size prop", () => {
		const { container } = render(<ProviderIcon provider="gitlab" size={40} />);
		const el = tile(container);
		expect(el.style.width).toBe("40px");
		expect(el.style.height).toBe("40px");
		expect(el.style.borderRadius).toBe("12px");
		const svg = container.querySelector("svg");
		expect(svg?.getAttribute("width")).toBe("23");
	});

	test("uses the provider's brand sprite and colour", () => {
		const { container } = render(<ProviderIcon provider="github" />);
		const use = container.querySelector("use");
		expect(use?.getAttribute("href")).toEndWith("#brand-github");
		expect(tile(container).style.color).not.toBe("");
		expect(BRAND_ICONS.github.glyph).toBeDefined();
	});

	test("an unknown provider falls back to the Gitea mark", () => {
		const { container } = render(<ProviderIcon provider="sourcehut" />);
		expect(container.querySelector("use")?.getAttribute("href")).toEndWith(
			"#brand-gitea",
		);
	});

	test("is decorative and merges a custom class", () => {
		const { container } = render(
			<ProviderIcon provider="forgejo" className="extra" />,
		);
		const el = tile(container);
		expect(el).toHaveAttribute("aria-hidden", "true");
		expect(el.className).toContain("extra");
		expect(el.querySelector("use")?.getAttribute("href")).toEndWith(
			"#brand-forgejo",
		);
	});
});
