import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { PopNumber } from "./pop-number";

describe("PopNumber", () => {
	test("shows the formatted text", () => {
		render(<PopNumber text="12.3%" />);
		expect(screen.getByText("12.3%")).toBeInTheDocument();
	});

	test("a changed value is the new text in place, never animation only", () => {
		const { rerender } = render(<PopNumber text="212 MiB" />);
		rerender(<PopNumber text="220 MiB" />);
		expect(screen.getByText("220 MiB")).toBeInTheDocument();
		expect(screen.queryByText("212 MiB")).not.toBeInTheDocument();
	});

	test("merges the caller's class name with the tabular base", () => {
		render(<PopNumber text="1" className="text-lg" />);
		const el = screen.getByText("1");
		expect(el).toHaveClass("text-lg");
		expect(el).toHaveClass("tabular-nums");
	});
});
