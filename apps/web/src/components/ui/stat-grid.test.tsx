import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { StatGrid } from "./stat-grid";

describe("StatGrid", () => {
	test("renders every child as a direct grid item", () => {
		render(
			<StatGrid>
				<div>CPU</div>
				<div>Memory</div>
				<div>Disk</div>
			</StatGrid>,
		);
		const grid = screen.getByText("CPU").parentElement as HTMLElement;
		expect(grid).toHaveAttribute("data-slot", "stat-grid");
		expect(Array.from(grid.children).map((c) => c.textContent)).toEqual([
			"CPU",
			"Memory",
			"Disk",
		]);
	});

	test("renders an empty grid without children content", () => {
		const { container } = render(<StatGrid>{null}</StatGrid>);
		const grid = container.firstElementChild as HTMLElement;
		expect(grid).toHaveAttribute("data-slot", "stat-grid");
		expect(grid).toBeEmptyDOMElement();
	});

	test("appends className to the base layout classes", () => {
		const { container } = render(
			<StatGrid className="mb-4">
				<span>x</span>
			</StatGrid>,
		);
		const grid = container.firstElementChild as HTMLElement;
		expect(grid).toHaveClass("mb-4");
		expect(grid).toHaveClass("grid");
	});
});
