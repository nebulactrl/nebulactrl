import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { createRef } from "react";
import { CanvasFloor } from "./canvas-floor";

describe("CanvasFloor", () => {
	test("is decorative: hidden from assistive tech", () => {
		const { container } = render(<CanvasFloor production={false} />);
		const floor = container.firstElementChild;
		expect(floor).toHaveAttribute("aria-hidden", "true");
		expect(floor).not.toHaveAttribute("data-env");
	});

	test("marks production so the floor can wear its tint", () => {
		const { container } = render(<CanvasFloor production />);
		expect(container.firstElementChild).toHaveAttribute(
			"data-env",
			"production",
		);
	});

	test("hands the lattice layer to the caller's ref", () => {
		const ref = createRef<HTMLDivElement>();
		const { container } = render(
			<CanvasFloor production={false} latticeRef={ref} />,
		);
		expect(ref.current).toBeInstanceOf(HTMLDivElement);
		expect(container.firstElementChild?.contains(ref.current)).toBe(true);
	});
});
