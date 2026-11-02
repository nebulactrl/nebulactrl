import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { DrainIcon } from "./drain-icon";
import { DRAIN_KINDS } from "./model";

describe("DrainIcon", () => {
	test("is decorative: hidden from assistive tech", () => {
		const { container } = render(<DrainIcon kind="loki" />);
		expect(container.firstElementChild).toHaveAttribute("aria-hidden", "true");
	});

	test("scales the tile and its glyph from the size", () => {
		const { container } = render(<DrainIcon kind="syslog" size={24} />);
		const tile = container.firstElementChild as HTMLElement;
		expect(tile.style.width).toBe("24px");
		expect(tile.style.height).toBe("24px");
		expect(tile.style.borderRadius).toBe("7px");
		const glyph = tile.querySelector("svg");
		expect(glyph).toHaveAttribute("width", "14");
	});

	test("defaults to a 36px tile and takes extra classes", () => {
		const { container } = render(<DrainIcon kind="http" className="extra" />);
		const tile = container.firstElementChild as HTMLElement;
		expect(tile.style.width).toBe("36px");
		expect(tile).toHaveClass("extra");
	});

	test("gives every kind its own colour and glyph", () => {
		const tiles = DRAIN_KINDS.map(({ kind }) => {
			const { container, unmount } = render(<DrainIcon kind={kind} />);
			const tile = container.firstElementChild as HTMLElement;
			const out = [
				tile.style.getPropertyValue("--t"),
				tile.querySelector("svg")?.getAttribute("class") ?? "",
			];
			unmount();
			return out;
		});
		expect(new Set(tiles.map((t) => t[0])).size).toBe(DRAIN_KINDS.length);
		expect(new Set(tiles.map((t) => t[1])).size).toBe(DRAIN_KINDS.length);
	});
});
