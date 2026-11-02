import { describe, expect, mock, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ProcessOption } from "./model";
import { ProcessFilter } from "./process-filter";

const PROCESSES: ProcessOption[] = [
	{ name: "web", isDefault: true },
	{ name: "worker", isDefault: false },
];

describe("ProcessFilter", () => {
	test("offers all processes plus one chip per process, in order", () => {
		render(
			<ProcessFilter
				processes={PROCESSES}
				value={undefined}
				onChange={() => {}}
			/>,
		);
		const chips = screen.getAllByRole("button");
		expect(chips.map((chip) => chip.textContent)).toEqual([
			"All processes",
			"web",
			"worker",
		]);
	});

	test("presses exactly the chip the value names", () => {
		const { rerender } = render(
			<ProcessFilter
				processes={PROCESSES}
				value={undefined}
				onChange={() => {}}
			/>,
		);
		const pressed = () =>
			screen
				.getAllByRole("button")
				.filter((chip) => chip.getAttribute("aria-pressed") === "true")
				.map((chip) => chip.textContent);
		expect(pressed()).toEqual(["All processes"]);
		rerender(
			<ProcessFilter
				processes={PROCESSES}
				value="worker"
				onChange={() => {}}
			/>,
		);
		expect(pressed()).toEqual(["worker"]);
	});

	test("picking a process reports its name", async () => {
		const onChange = mock((_next: string | undefined) => {});
		render(
			<ProcessFilter
				processes={PROCESSES}
				value={undefined}
				onChange={onChange}
			/>,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "worker" }));
		expect(onChange.mock.calls).toEqual([["worker"]]);
	});

	test("picking All processes clears the filter, also from a pressed process", async () => {
		const onChange = mock((_next: string | undefined) => {});
		render(
			<ProcessFilter processes={PROCESSES} value="web" onChange={onChange} />,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "All processes" }));
		expect(onChange.mock.calls).toEqual([[undefined]]);
	});

	test("a service without filterable processes only has All processes", () => {
		render(
			<ProcessFilter processes={[]} value={undefined} onChange={() => {}} />,
		);
		expect(screen.getAllByRole("button")).toHaveLength(1);
		expect(
			screen.getByRole("list", { name: "Show logs of" }),
		).toBeInTheDocument();
	});
});
