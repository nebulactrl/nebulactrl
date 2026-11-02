import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { OptionSelect } from "./option-select";

const OPTIONS = [
	{ value: "total", label: "Total" },
	{ value: "perReplica", label: "Per replica" },
] as const;

describe("OptionSelect", () => {
	test("the trigger carries the accessible name and shows the chosen label", () => {
		render(
			<OptionSelect
				aria-label="CPU series"
				value="perReplica"
				onChange={() => {}}
				options={OPTIONS}
			/>,
		);
		expect(
			screen.getByRole("combobox", { name: "CPU series" }),
		).toHaveTextContent("Per replica");
	});

	test("opening lists every option and picking one reports its value", async () => {
		const picked: string[] = [];
		const user = userEvent.setup();
		render(
			<OptionSelect
				aria-label="CPU series"
				value="total"
				onChange={(v) => picked.push(v)}
				options={OPTIONS}
			/>,
		);
		await user.click(screen.getByRole("combobox", { name: "CPU series" }));
		expect(
			(await screen.findAllByRole("option")).map((o) => o.textContent),
		).toEqual(["Total", "Per replica"]);
		await user.click(screen.getByRole("option", { name: "Per replica" }));
		expect(picked).toEqual(["perReplica"]);
	});
});
