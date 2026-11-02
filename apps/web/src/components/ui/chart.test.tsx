import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { Line, LineChart } from "recharts";
import { type ChartConfig, ChartContainer } from "./chart";

// Scoped to the body: other components leave their own <style> in the head
// (Base UI's scroll area does), which is not the chart's.
const styleText = () => document.body.querySelector("style")?.innerHTML ?? "";

describe("ChartContainer", () => {
	test("renders a chart slot whose data-chart id comes from the id prop", () => {
		const { container } = render(
			<ChartContainer id="cpu" config={{}}>
				<LineChart data={[]} />
			</ChartContainer>,
		);
		const el = container.firstElementChild as HTMLElement;
		expect(el).toHaveAttribute("data-slot", "chart");
		expect(el).toHaveAttribute("data-chart", "chart-cpu");
	});

	test("generates a stable id without colons when none is given", () => {
		const { container } = render(
			<ChartContainer config={{}}>
				<LineChart data={[]} />
			</ChartContainer>,
		);
		const id = (container.firstElementChild as HTMLElement).getAttribute(
			"data-chart",
		);
		expect(id).toMatch(/^chart-[^:]+$/);
		expect(id).not.toBe("chart-");
	});

	test("two charts get different ids", () => {
		const { container } = render(
			<>
				<ChartContainer config={{}}>
					<LineChart data={[]} />
				</ChartContainer>
				<ChartContainer config={{}}>
					<LineChart data={[]} />
				</ChartContainer>
			</>,
		);
		const ids = Array.from(container.querySelectorAll("[data-slot=chart]")).map(
			(e) => e.getAttribute("data-chart"),
		);
		expect(new Set(ids).size).toBe(2);
	});

	test("emits no <style> when no series has a colour", () => {
		render(
			<ChartContainer config={{ cpu: { label: "CPU" } }}>
				<LineChart data={[]} />
			</ChartContainer>,
		);
		expect(document.body.querySelector("style")).toBeNull();
	});

	test("a plain colour becomes a --color-<key> variable for both themes", () => {
		const config: ChartConfig = {
			cpu: { label: "CPU", color: "var(--chart-1)" },
			mem: { color: "var(--chart-2)" },
		};
		render(
			<ChartContainer id="x" config={config}>
				<LineChart data={[]} />
			</ChartContainer>,
		);
		const css = styleText();
		expect(css).toContain("[data-chart=chart-x]");
		expect(css).toContain("--color-cpu: var(--chart-1);");
		expect(css).toContain("--color-mem: var(--chart-2);");
		// light block has no prefix, dark block is scoped to .dark
		expect(css).toMatch(/^\s*\n? ?\[data-chart=chart-x\]/);
		expect(css).toContain(".dark [data-chart=chart-x]");
	});

	test("a theme colour yields a different value per theme", () => {
		render(
			<ChartContainer
				id="t"
				config={{
					net: { theme: { light: "#111111", dark: "#eeeeee" } },
				}}
			>
				<LineChart data={[]} />
			</ChartContainer>,
		);
		const css = styleText();
		const [light, dark] = css.split(".dark [data-chart=chart-t]");
		expect(light).toContain("--color-net: #111111;");
		expect(light).not.toContain("#eeeeee");
		expect(dark).toContain("--color-net: #eeeeee;");
	});

	test("series without a colour are left out of the style block", () => {
		render(
			<ChartContainer
				id="m"
				config={{ a: { color: "red" }, b: { label: "B" } }}
			>
				<LineChart data={[]} />
			</ChartContainer>,
		);
		const css = styleText();
		expect(css).toContain("--color-a: red;");
		expect(css).not.toContain("--color-b");
	});

	test("className and props are forwarded to the wrapper", () => {
		const { container } = render(
			<ChartContainer config={{}} className="h-40" data-testid="c">
				<LineChart data={[]} />
			</ChartContainer>,
		);
		const el = container.firstElementChild as HTMLElement;
		expect(el).toHaveClass("h-40");
		expect(el).toHaveAttribute("data-testid", "c");
	});

	test("renders the chart inside Recharts' responsive container", () => {
		const { container } = render(
			<ChartContainer config={{}}>
				<LineChart data={[{ v: 1 }, { v: 2 }]}>
					<Line dataKey="v" isAnimationActive={false} />
				</LineChart>
			</ChartContainer>,
		);
		expect(
			container.querySelector(".recharts-responsive-container"),
		).not.toBeNull();
	});
});
