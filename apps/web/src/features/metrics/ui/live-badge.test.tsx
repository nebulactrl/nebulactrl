import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { LiveBadge } from "./live-badge";

describe("LiveBadge", () => {
	test("says Live next to a live range", () => {
		render(<LiveBadge live />);
		expect(screen.getByRole("status")).toHaveTextContent("Live");
		expect(screen.getByRole("status")).not.toHaveTextContent("delayed");
	});

	test("says Live · delayed when the feed lags", () => {
		render(<LiveBadge live delayed />);
		expect(screen.getByRole("status")).toHaveTextContent("Live · delayed");
	});

	test("renders nothing for a range that is not live, even when delayed", () => {
		const { container } = render(<LiveBadge live={false} delayed />);
		expect(container).toBeEmptyDOMElement();
	});

	test("accepts a class name", () => {
		render(<LiveBadge live className="ml-3" />);
		expect(screen.getByRole("status")).toHaveClass("ml-3");
	});
});
