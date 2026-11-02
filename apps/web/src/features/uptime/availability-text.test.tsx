import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { AvailabilityText } from "./availability-text";

describe("AvailabilityText", () => {
	test("shows a figure floored to two decimals, never rounded up to 100%", () => {
		render(<AvailabilityText percent={99.999} />);
		expect(screen.getByText("99.99%")).toBeInTheDocument();
	});

	test("shows 0% as a figure when every check failed", () => {
		render(<AvailabilityText percent={0} />);
		expect(screen.getByText("0%")).toBeInTheDocument();
	});

	test("with nothing to judge it is a hidden dash plus 'No data' for screen readers, never 0%", () => {
		const { container } = render(<AvailabilityText percent={null} />);
		const dash = screen.getByText("—");
		expect(dash).toHaveAttribute("aria-hidden", "true");
		const words = screen.getByText("No data");
		expect(words).toHaveClass("sr-only");
		expect(container).not.toHaveTextContent("0%");
	});
});
