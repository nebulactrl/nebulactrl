import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { ChartCard } from "./chart-card";

describe("ChartCard", () => {
	test("titles the section with a heading and shows its content", () => {
		render(
			<ChartCard title="CPU">
				<p>plot</p>
			</ChartCard>,
		);
		expect(
			screen.getByRole("heading", { name: "CPU", level: 3 }),
		).toBeInTheDocument();
		expect(screen.getByText("plot")).toBeInTheDocument();
	});

	test("shows the subtitle and actions beside the title when given", () => {
		render(
			<ChartCard
				title="Memory"
				subtitle="Working set in MiB"
				actions={<button type="button">MiB</button>}
			>
				<p>plot</p>
			</ChartCard>,
		);
		expect(screen.getByText("Working set in MiB")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "MiB" })).toBeInTheDocument();
	});

	test("leaves out the subtitle and the actions area when absent", () => {
		const { container } = render(
			<ChartCard title="Disk">
				<p>plot</p>
			</ChartCard>,
		);
		const header = container.querySelector("header");
		expect(header?.querySelector("p")).toBeNull();
		expect(header?.children).toHaveLength(1);
	});

	test("dimming is opt-in and keeps the content in the page", () => {
		const { rerender } = render(
			<ChartCard title="CPU">
				<p>plot</p>
			</ChartCard>,
		);
		const section = () => screen.getByText("plot").closest("section");
		expect(section()).not.toHaveClass("opacity-60");
		rerender(
			<ChartCard title="CPU" dimmed>
				<p>plot</p>
			</ChartCard>,
		);
		expect(section()).toHaveClass("opacity-60");
		expect(screen.getByText("plot")).toBeVisible();
	});

	test("accepts a class name for layout", () => {
		render(
			<ChartCard title="CPU" className="col-span-2">
				<p>plot</p>
			</ChartCard>,
		);
		expect(screen.getByText("plot").closest("section")).toHaveClass(
			"col-span-2",
		);
	});
});
