import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { MiniStat } from "./mini-stat";

describe("MiniStat", () => {
	test("shows the label, the formatted number and the hint", () => {
		render(<MiniStat label="CPU now" text="41.5%" hint="of one core = 100%" />);
		expect(screen.getByText("CPU now")).toBeInTheDocument();
		expect(screen.getByText("of one core = 100%")).toBeInTheDocument();
		expect(screen.getByText(/41\.5/)).toBeInTheDocument();
	});

	test("leaves the hint line out when there is none", () => {
		const { container } = render(<MiniStat label="Replicas" text="3" />);
		expect(container.firstElementChild?.children).toHaveLength(2);
	});

	test("the default tone has no warning mark", () => {
		const { container } = render(
			<MiniStat label="Memory now" text="212 MiB" />,
		);
		expect(container.firstElementChild).not.toHaveAttribute("data-tone");
		expect(container.querySelector("svg")).toBeNull();
	});

	test("the warning tone marks the tile with data-tone and the warning icon", () => {
		const { container } = render(
			<MiniStat label="Memory now" text="480 MiB" tone="warning" />,
		);
		expect(container.firstElementChild).toHaveAttribute("data-tone", "warning");
		expect(container.querySelector("svg")).not.toBeNull();
		expect(screen.getByText("Memory now")).toBeInTheDocument();
	});

	test("text that is not a single number is shown as written", () => {
		render(<MiniStat label="Uptime" text="3h 12m" />);
		expect(screen.getByText("3h 12m")).toBeInTheDocument();
	});
});
