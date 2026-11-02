import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import type { Delta } from "../delta";
import { KpiTile } from "./kpi-tile";

const up: Delta = { ratio: 0.12, direction: "up", tone: "good", text: "▲ 12%" };

describe("KpiTile", () => {
	test("shows the label and the number", () => {
		render(<KpiTile label="Requests" text="12.4k" />);
		expect(screen.getByText("Requests")).toBeInTheDocument();
		expect(screen.getByText("12.4k")).toBeInTheDocument();
	});

	test("shows the delta as text, titled with what it compares against", () => {
		render(
			<KpiTile
				label="Requests"
				text="12.4k"
				delta={up}
				deltaLabel="vs previous 24h"
			/>,
		);
		// The arrow is an icon beside the percent.
		const chip = screen.getByText("12%");
		expect(chip).toHaveAttribute("title", "vs previous 24h");
		expect(chip).toHaveAttribute("data-tone", "ok");
		// With no hint, the comparison label is the line under the number.
		expect(
			screen.getByText("vs previous 24h", { selector: "p, span, div" }),
		).toBeInTheDocument();
	});

	test("a hint wins over the delta label for the line under the number", () => {
		render(
			<KpiTile
				label="Visitors"
				text="900"
				hint="unique, counted per UTC day"
				delta={up}
				deltaLabel="vs previous 24h"
			/>,
		);
		expect(screen.getByText("unique, counted per UTC day")).toBeInTheDocument();
		expect(
			screen.queryByText("vs previous 24h", { selector: "p, div, span" }),
		).not.toBeInTheDocument();
	});

	test("a decline is toned bad", () => {
		render(
			<KpiTile
				label="Requests"
				text="9"
				delta={{ ratio: -0.3, direction: "down", tone: "bad", text: "▼ 30%" }}
			/>,
		);
		expect(screen.getByText("30%")).toHaveAttribute("data-tone", "bad");
	});

	test("without a delta there is no chip", () => {
		render(
			<KpiTile label="p95 latency" text="–" deltaLabel="vs previous 1h" />,
		);
		expect(screen.queryByText(/%/)).not.toBeInTheDocument();
		expect(screen.queryByText("vs previous 1h")).not.toBeInTheDocument();
	});
});
