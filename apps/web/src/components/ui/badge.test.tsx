import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { Badge } from "./badge";

const badge = () =>
	screen.getByText("label").closest("[data-slot=badge]") as HTMLElement;

describe("Badge", () => {
	test("renders its children in a span tagged as a badge", () => {
		render(<Badge>label</Badge>);
		const el = screen.getByText("label");
		expect(el.tagName).toBe("SPAN");
		expect(el).toHaveAttribute("data-slot", "badge");
	});

	test("the default variant carries no tone", () => {
		render(<Badge>label</Badge>);
		expect(badge()).not.toHaveAttribute("data-tone");
		expect(badge()).toHaveAttribute("data-variant", "default");
	});

	test.each([
		["success", "ok"],
		["warning", "warn"],
		["destructive", "bad"],
	] as const)("variant %s maps to tone %s", (variant, tone) => {
		render(<Badge variant={variant}>label</Badge>);
		expect(badge()).toHaveAttribute("data-tone", tone);
		expect(badge()).toHaveAttribute("data-variant", variant);
	});

	test.each(["accent", "outline", "solid"] as const)(
		"non-tonal variant %s has no tone",
		(variant) => {
			render(<Badge variant={variant}>label</Badge>);
			expect(badge()).not.toHaveAttribute("data-tone");
			expect(badge()).toHaveAttribute("data-variant", variant);
		},
	);

	test("variants produce distinct styling", () => {
		const cls = (["default", "accent", "outline", "solid"] as const).map(
			(variant) => {
				const { unmount } = render(<Badge variant={variant}>label</Badge>);
				const c = badge().className;
				unmount();
				return c;
			},
		);
		expect(new Set(cls).size).toBe(4);
	});

	test("shape changes the styling", () => {
		const cls = (["tag", "pill", "count", "capsule", "tab-count"] as const).map(
			(shape) => {
				const { unmount } = render(<Badge shape={shape}>label</Badge>);
				const c = badge().className;
				unmount();
				return c;
			},
		);
		expect(new Set(cls).size).toBe(5);
	});

	test("the tone prop sets data-tone and wins over the variant's tone", () => {
		render(
			<Badge tone="live" variant="destructive">
				label
			</Badge>,
		);
		expect(badge()).toHaveAttribute("data-tone", "live");
	});

	test("the tone prop does not change data-variant", () => {
		render(<Badge tone="staged">label</Badge>);
		expect(badge()).toHaveAttribute("data-variant", "default");
		expect(badge()).toHaveAttribute("data-tone", "staged");
	});

	test("no status icon unless lamp is set", () => {
		render(<Badge tone="live">label</Badge>);
		expect(badge().querySelector("svg")).toBeNull();
		expect(document.querySelector("[data-slot=status-icon]")).toBeNull();
	});

	test("lamp draws the tone's status icon before the label", () => {
		render(
			<Badge tone="failed" lamp>
				label
			</Badge>,
		);
		const icon = document.querySelector(
			"[data-slot=status-icon]",
		) as HTMLElement;
		expect(icon).toHaveAttribute("data-kind", "failed");
		const el = badge();
		expect(el.firstElementChild).toBe(icon);
		expect(el.textContent).toBe("label");
		// icon is decorative: no spoken duplicate beside the label
		expect(el.querySelector(".sr-only")).toBeNull();
	});

	test("lamp resolves the status kind from a tonal variant", () => {
		render(
			<Badge variant="warning" lamp>
				label
			</Badge>,
		);
		expect(document.querySelector("[data-slot=status-icon]")).toHaveAttribute(
			"data-kind",
			"warning",
		);
	});

	test("lamp on an info tone uses the info glyph, not a status icon", () => {
		render(
			<Badge tone="info" lamp>
				label
			</Badge>,
		);
		expect(document.querySelector("[data-slot=status-icon]")).toBeNull();
		const svg = badge().querySelector("svg") as SVGElement;
		expect(svg).toHaveAttribute("aria-hidden", "true");
	});

	test("lamp without any tone falls back to the offline kind", () => {
		render(<Badge lamp>label</Badge>);
		expect(document.querySelector("[data-slot=status-icon]")).toHaveAttribute(
			"data-kind",
			"offline",
		);
	});

	test("render prop swaps the element while keeping slot and content", () => {
		render(
			<Badge render={<a href="/x" />} tone="live">
				label
			</Badge>,
		);
		const link = screen.getByRole("link", { name: "label" });
		expect(link).toHaveAttribute("href", "/x");
		expect(link).toHaveAttribute("data-slot", "badge");
		expect(link).toHaveAttribute("data-tone", "live");
	});

	test("className and native props are merged through", () => {
		render(
			<Badge className="extra" id="b" title="hint">
				label
			</Badge>,
		);
		expect(badge()).toHaveClass("extra");
		expect(badge()).toHaveAttribute("id", "b");
		expect(badge()).toHaveAttribute("title", "hint");
	});
});
