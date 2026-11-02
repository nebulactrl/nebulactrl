import { describe, expect, test } from "bun:test";
import { render, waitFor } from "@testing-library/react";
import { DitherBackdrop } from "./backdrop";

describe("DitherBackdrop", () => {
	test("is decoration: hidden from assistive tech and ignores the pointer", () => {
		const { container } = render(<DitherBackdrop />);
		const host = container.querySelector("[data-slot=dither-shader]");
		expect(host).toHaveAttribute("aria-hidden", "true");
		expect(host).toHaveClass("pointer-events-none", "-z-10");
	});

	test("merges a custom className onto the shader host", () => {
		const { container } = render(<DitherBackdrop className="opacity-100" />);
		expect(container.querySelector("[data-slot=dither-shader]")).toHaveClass(
			"opacity-100",
		);
	});

	test("without WebGL it settles on the accent wash fallback", async () => {
		const { container } = render(<DitherBackdrop animate={false} />);
		await waitFor(() =>
			expect(
				container.querySelector("[data-slot=dither-shader]"),
			).toHaveAttribute("data-state", "fallback"),
		);
		const wash = container.querySelector(
			"[data-slot=dither-shader] > div.absolute > div[aria-hidden=true]",
		);
		expect(wash).not.toBeNull();
		expect(wash?.className).toContain("--color-accent-wash");
	});
});
