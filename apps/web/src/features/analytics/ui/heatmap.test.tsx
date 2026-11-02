import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { ActiveHours } from "./heatmap";

describe("ActiveHours", () => {
	test("reads out the busiest hour and names the weekday and UTC hour of it", () => {
		const grid: number[][] = Array.from({ length: 7 }, () => Array(24).fill(0));
		(grid[2] as number[])[13] = 1234;
		(grid[5] as number[])[3] = 10;
		render(<ActiveHours grid={grid} />);
		expect(screen.getByText("busiest hour 1,234 requests")).toBeInTheDocument();
		const group = screen.getByRole("group", {
			name: /Requests by weekday and UTC hour/,
		});
		expect(group).toHaveAccessibleName(/1,?244 requests in total/);
		expect(group).toHaveAccessibleName(/busiest Tue 13 with 1,?234/);
	});

	test("tolerates a short or sparse grid: missing rows and cells count as zero", () => {
		render(<ActiveHours grid={[[3], null]} />);
		expect(screen.getByText("busiest hour 3 requests")).toBeInTheDocument();
		expect(
			screen.getByRole("group", { name: /3 requests in total/ }),
		).toBeInTheDocument();
	});

	test("an empty grid says the busiest hour has 0 requests", () => {
		render(<ActiveHours grid={[]} />);
		expect(screen.getByText("busiest hour 0 requests")).toBeInTheDocument();
	});
});
