import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ProcessSelect } from "./process-select";

const PROCESSES = [
	{ name: "web", isDefault: true },
	{ name: "worker", isDefault: false },
];

describe("ProcessSelect", () => {
	test("without a pick the trigger reads All processes", () => {
		render(
			<ProcessSelect
				processes={PROCESSES}
				value={undefined}
				onChange={() => {}}
			/>,
		);
		expect(screen.getByRole("combobox", { name: "Process" })).toHaveTextContent(
			"All processes",
		);
	});

	test("shows the picked process by name", () => {
		render(
			<ProcessSelect
				processes={PROCESSES}
				value="worker"
				onChange={() => {}}
			/>,
		);
		expect(screen.getByRole("combobox", { name: "Process" })).toHaveTextContent(
			"worker",
		);
	});

	test("lists All processes first, then each process", async () => {
		render(
			<ProcessSelect
				processes={PROCESSES}
				value={undefined}
				onChange={() => {}}
			/>,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("combobox", { name: "Process" }));
		expect(
			(await screen.findAllByRole("option")).map((o) => o.textContent),
		).toEqual(["All processes", "web", "worker"]);
	});

	test("picking a process reports its name", async () => {
		const picked: (string | undefined)[] = [];
		const user = userEvent.setup();
		render(
			<ProcessSelect
				processes={PROCESSES}
				value={undefined}
				onChange={(v) => picked.push(v)}
			/>,
		);
		await user.click(screen.getByRole("combobox", { name: "Process" }));
		await user.click(await screen.findByRole("option", { name: "worker" }));
		expect(picked).toEqual(["worker"]);
	});

	test("picking All processes after a process reports undefined, the service total", async () => {
		const picked: (string | undefined)[] = [];
		const user = userEvent.setup();
		render(
			<ProcessSelect
				processes={PROCESSES}
				value="worker"
				onChange={(v) => picked.push(v)}
			/>,
		);
		await user.click(screen.getByRole("combobox", { name: "Process" }));
		await user.click(
			await screen.findByRole("option", { name: "All processes" }),
		);
		expect(picked).toEqual([undefined]);
	});
});
