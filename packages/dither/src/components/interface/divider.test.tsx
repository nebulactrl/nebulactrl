import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { DitherDivider } from "./divider";

const rules = (root: Element) =>
	Array.from(root.querySelectorAll<HTMLElement>("[data-slot=divider-rule]"));

const maskClass = (el: Element) => el.className.match(/nc-dm-\d+/)?.[0] ?? "";

/** The CSS rule the band-mask engine injected for a class. */
const maskRule = (name: string) =>
	Array.from(document.querySelectorAll("style[data-nc-dither-masks]"))
		.map((style) => style.textContent ?? "")
		.join("")
		.split("}")
		.find((rule) => rule.startsWith(`.${name}{`)) ?? "";

describe("DitherDivider structure", () => {
	test("a plain horizontal divider is one decorative rule hidden from assistive tech", () => {
		render(<DitherDivider data-testid="d" />);
		const divider = screen.getByTestId("d");
		expect(divider).toHaveAttribute("data-slot", "divider");
		const [rule, ...rest] = rules(divider);
		expect(rest).toEqual([]);
		expect(rule?.tagName).toBe("SPAN");
		expect(rule).toHaveAttribute("aria-hidden", "true");
		expect(screen.queryByRole("separator")).toBeNull();
	});

	test("decorative={false} makes the rule a separator with its orientation", () => {
		render(<DitherDivider decorative={false} />);
		const separator = screen.getByRole("separator");
		expect(separator.tagName).toBe("HR");
		expect(separator).toHaveAttribute("aria-orientation", "horizontal");
		expect(separator).not.toHaveAttribute("aria-hidden");
	});

	test("a vertical separator reports a vertical orientation", () => {
		render(<DitherDivider orientation="vertical" decorative={false} />);
		expect(screen.getByRole("separator")).toHaveAttribute(
			"aria-orientation",
			"vertical",
		);
	});

	test("a label splits the rule in two and shows the text between them", () => {
		render(<DitherDivider data-testid="d" label="or" />);
		const divider = screen.getByTestId("d");
		const pair = rules(divider);
		expect(pair).toHaveLength(2);
		expect(screen.getByText("or")).toBeVisible();
		const children = Array.from(divider.children);
		expect(children.indexOf(screen.getByText("or"))).toBe(1);
		// Each half fades away from the label, so the halves are different masks.
		expect(maskClass(pair[0] as Element)).not.toBe(
			maskClass(pair[1] as Element),
		);
		for (const half of pair) expect(half).toHaveClass("flex-1");
	});

	test("only one of the two halves is announced as the separator", () => {
		render(<DitherDivider label="or" decorative={false} />);
		expect(screen.getAllByRole("separator")).toHaveLength(1);
	});

	test("a vertical divider ignores its label", () => {
		render(
			<DitherDivider data-testid="d" orientation="vertical" label="never" />,
		);
		expect(screen.queryByText("never")).toBeNull();
		expect(rules(screen.getByTestId("d"))).toHaveLength(1);
	});

	test("an empty-string label still counts as a label; null does not", () => {
		const { unmount } = render(<DitherDivider data-testid="d" label="" />);
		expect(rules(screen.getByTestId("d"))).toHaveLength(2);
		unmount();
		render(<DitherDivider data-testid="d" label={null} />);
		expect(rules(screen.getByTestId("d"))).toHaveLength(1);
	});

	test("passes through props and merges className, per orientation", () => {
		const { unmount } = render(
			<DitherDivider data-testid="d" className="my-4" id="x" />,
		);
		expect(screen.getByTestId("d")).toHaveClass("w-full", "my-4");
		expect(screen.getByTestId("d")).toHaveAttribute("id", "x");
		unmount();
		render(<DitherDivider data-testid="d" orientation="vertical" />);
		expect(screen.getByTestId("d")).toHaveClass("self-stretch");
		expect(screen.getByTestId("d")).not.toHaveClass("w-full");
	});
});

describe("DitherDivider drawing", () => {
	test("a horizontal rule is as thick as the variant and as wide as its box", () => {
		render(<DitherDivider data-testid="d" />);
		const [rule] = rules(screen.getByTestId("d"));
		expect(rule?.style.height).toBe("4px");
		expect(rule?.style.width).toBe("");
		expect(rule).toHaveClass("w-full");
	});

	test("a vertical rule sets a width and stretches", () => {
		render(<DitherDivider data-testid="d" orientation="vertical" />);
		const [rule] = rules(screen.getByTestId("d"));
		expect(rule?.style.width).toBe("4px");
		expect(rule?.style.height).toBe("");
		expect(rule).toHaveClass("self-stretch");
	});

	test("dots is one cell thin", () => {
		render(<DitherDivider data-testid="d" variant="dots" />);
		expect(rules(screen.getByTestId("d"))[0]?.style.height).toBe("2px");
	});

	test("thickness counts cells", () => {
		render(<DitherDivider data-testid="d" thickness={3} cell={2} />);
		expect(rules(screen.getByTestId("d"))[0]?.style.height).toBe("6px");
	});

	test("the fade bands run along the rule, so the mask is lengthwise in both orientations", () => {
		const { unmount } = render(<DitherDivider data-testid="h" />);
		const horizontal = maskRule(
			maskClass(rules(screen.getByTestId("h"))[0] as Element),
		);
		unmount();
		render(<DitherDivider data-testid="v" orientation="vertical" />);
		const vertical = maskRule(
			maskClass(rules(screen.getByTestId("v"))[0] as Element),
		);
		expect(horizontal).toContain("mask-image:url(");
		expect(vertical).toContain("mask-image:url(");
		const decode = (css: string) =>
			decodeURIComponent(
				css.match(/url\("data:image\/svg\+xml,([^"]+)"/)?.[1] ?? "",
			);
		// x-axis bands: rects are laid out by x and span the full height.
		expect(decode(horizontal)).toContain('height="100%"');
		expect(decode(vertical)).toContain('width="100%"');
		expect(decode(horizontal)).not.toBe(decode(vertical));
	});

	test("variants and tuning produce different masks; identical props share one", () => {
		render(
			<>
				<DitherDivider data-testid="a" />
				<DitherDivider data-testid="b" />
				<DitherDivider data-testid="solid" variant="solid" />
				<DitherDivider data-testid="dots" variant="dots" />
				<DitherDivider data-testid="strong" strength={1} />
				<DitherDivider data-testid="matrix" matrix="bayer8" fade={0.2} />
			</>,
		);
		const mask = (id: string) =>
			maskClass(rules(screen.getByTestId(id))[0] as Element);
		expect(mask("a")).toBe(mask("b"));
		const others = ["solid", "dots", "strong", "matrix"].map(mask);
		expect(new Set([mask("a"), ...others]).size).toBe(5);
	});

	test("without a colour the rule uses the muted ink; with one it sets --nc-rule", () => {
		const { unmount } = render(<DitherDivider data-testid="d" />);
		const plain = rules(screen.getByTestId("d"))[0] as HTMLElement;
		expect(plain).toHaveClass("bg-ink-4/70");
		expect(plain.style.getPropertyValue("--nc-rule")).toBe("");
		unmount();
		render(<DitherDivider data-testid="d" color="blue" label="x" />);
		for (const rule of rules(screen.getByTestId("d"))) {
			expect(rule.style.getPropertyValue("--nc-rule")).toBe(
				"var(--dither-blue)",
			);
			expect(rule).toHaveClass("bg-(--nc-rule)");
		}
	});

	test("forced colours turn the rule into solid text colour", () => {
		render(<DitherDivider data-testid="d" />);
		expect(rules(screen.getByTestId("d"))[0]).toHaveClass(
			"forced-colors:bg-[CanvasText]",
		);
	});
});
