import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RangeControl } from "./range-control";

const RANGES = ["5m", "1h", "24h"] as const;

describe("RangeControl", () => {
	test("lists every range as a button with the current one pressed", () => {
		render(
			<RangeControl
				ranges={RANGES}
				value="1h"
				onChange={() => {}}
				live={false}
			/>,
		);
		const group = screen.getByRole("group", { name: "Time range" });
		expect(group).toBeInTheDocument();
		expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual([
			"5m",
			"1h",
			"24h",
		]);
		expect(screen.getByRole("button", { name: "1h" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "5m" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	test("picking another range reports it", async () => {
		const picked: string[] = [];
		render(
			<RangeControl
				ranges={RANGES}
				value="1h"
				onChange={(v) => picked.push(v)}
				live={false}
			/>,
		);
		await userEvent.setup().click(screen.getByRole("button", { name: "24h" }));
		expect(picked).toEqual(["24h"]);
	});

	test("clicking the pressed range does not report an empty choice", async () => {
		const picked: string[] = [];
		render(
			<RangeControl
				ranges={RANGES}
				value="1h"
				onChange={(v) => picked.push(v)}
				live={false}
			/>,
		);
		await userEvent.setup().click(screen.getByRole("button", { name: "1h" }));
		expect(picked).toEqual([]);
		expect(screen.getByRole("button", { name: "1h" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	test("the group takes a custom accessible name", () => {
		render(
			<RangeControl
				ranges={RANGES}
				value="1h"
				onChange={() => {}}
				live={false}
				aria-label="Traffic range"
			/>,
		);
		expect(
			screen.getByRole("group", { name: "Traffic range" }),
		).toBeInTheDocument();
	});

	test("shows the Live cue only when the range is live", () => {
		const { rerender } = render(
			<RangeControl ranges={RANGES} value="5m" onChange={() => {}} live />,
		);
		expect(screen.getByRole("status")).toHaveTextContent("Live");
		rerender(
			<RangeControl
				ranges={RANGES}
				value="1h"
				onChange={() => {}}
				live={false}
			/>,
		);
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	test("a delayed feed says so beside the range", () => {
		render(
			<RangeControl
				ranges={RANGES}
				value="5m"
				onChange={() => {}}
				live
				delayed
			/>,
		);
		expect(screen.getByRole("status")).toHaveTextContent("Live · delayed");
	});
});
