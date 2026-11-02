import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { newRow, type Row } from "./model";
import { RowsEditor, type RowsEditorProps } from "./rows-editor";

let latest: Row[] = [];

function Harness({
	initial,
	...props
}: { initial: Row[] } & Partial<RowsEditorProps>) {
	const [rows, setRows] = useState(initial);
	latest = rows;
	return (
		<RowsEditor
			label="Labels"
			addLabel="Add label"
			{...props}
			rows={rows}
			onChange={setRows}
		/>
	);
}

const pairs = (rows: Row[]) => rows.map((r) => [r.name, r.value]);

describe("RowsEditor", () => {
	test("shows only the add button for an empty list", () => {
		render(<Harness initial={[]} />);
		expect(screen.getByRole("button", { name: "Add label" })).toBeVisible();
		expect(screen.queryByLabelText("Labels: name")).toBeNull();
	});

	test("adds a blank row each time Add is pressed", async () => {
		const user = userEvent.setup();
		render(<Harness initial={[]} />);
		await user.click(screen.getByRole("button", { name: "Add label" }));
		await user.click(screen.getByRole("button", { name: "Add label" }));
		expect(pairs(latest)).toEqual([
			["", ""],
			["", ""],
		]);
		expect(screen.getAllByLabelText("Labels: name")).toHaveLength(2);
	});

	test("typing edits only the row it is in", async () => {
		const user = userEvent.setup();
		render(
			<Harness initial={[newRow("env", "prod"), newRow("team", "core")]} />,
		);
		const names = screen.getAllByLabelText("Labels: name");
		await user.type(names[1] as HTMLElement, "s");
		const values = screen.getAllByLabelText(/^Labels: value/);
		await user.clear(values[0] as HTMLElement);
		await user.type(values[0] as HTMLElement, "staging");
		expect(pairs(latest)).toEqual([
			["env", "staging"],
			["teams", "core"],
		]);
	});

	test("names a value field after its row's name", () => {
		render(<Harness initial={[newRow("env", "prod"), newRow("", "x")]} />);
		expect(screen.getByLabelText("Labels: value of env")).toHaveValue("prod");
		expect(screen.getByLabelText("Labels: value")).toHaveValue("x");
	});

	test("removes the row whose button is pressed and keeps the others", async () => {
		const user = userEvent.setup();
		render(
			<Harness
				initial={[newRow("a", "1"), newRow("b", "2"), newRow("", "3")]}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Remove b" }));
		expect(pairs(latest)).toEqual([
			["a", "1"],
			["", "3"],
		]);
		// A row without a name is announced as "row".
		await user.click(screen.getByRole("button", { name: "Remove row" }));
		expect(pairs(latest)).toEqual([["a", "1"]]);
	});

	test("hides values of a secret list but not its names", () => {
		render(<Harness initial={[newRow("DD-API-KEY", "s3cret")]} secret />);
		expect(
			screen.getByLabelText("Labels: value of DD-API-KEY"),
		).toHaveAttribute("type", "password");
		expect(screen.getByLabelText("Labels: name")).not.toHaveAttribute(
			"type",
			"password",
		);
	});

	test("shows values in clear text when the list is not secret, with the given placeholders", () => {
		render(
			<Harness
				initial={[newRow()]}
				namePlaceholder="label"
				valuePlaceholder="v"
			/>,
		);
		const value = screen.getByLabelText("Labels: value");
		expect(value).not.toHaveAttribute("type", "password");
		expect(value).toHaveAttribute("placeholder", "v");
		expect(screen.getByLabelText("Labels: name")).toHaveAttribute(
			"placeholder",
			"label",
		);
	});
});
