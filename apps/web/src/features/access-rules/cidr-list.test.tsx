import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { renderWithProviders } from "#/test/render";
import { CidrListEditor } from "./cidr-list";

/** A host that keeps the list, so typing and reordering show through to the DOM. */
function Host({
	initial,
	errors,
	disabled,
	onChange,
}: {
	initial: string[];
	errors?: Record<string, string>;
	disabled?: boolean;
	onChange?: (next: string[]) => void;
}) {
	const [values, setValues] = useState(initial);
	return (
		<CidrListEditor
			label="Allowed network"
			values={values}
			{...(errors ? { errors } : {})}
			{...(disabled ? { disabled } : {})}
			onChange={(next) => {
				setValues(next);
				onChange?.(next);
			}}
		/>
	);
}

const rowInputs = () =>
	screen
		.getAllByRole("textbox")
		.filter((el) =>
			/^Allowed network \d+$/.test(el.getAttribute("aria-label") ?? ""),
		)
		.map((el) => (el as HTMLInputElement).value);

describe("CidrListEditor rows", () => {
	test("shows one numbered input per entry, in order", async () => {
		await renderWithProviders(
			<Host initial={["10.0.0.0/8", "192.168.1.20"]} />,
		);
		const list = screen.getByRole("list", { name: "Allowed network" });
		expect(within(list).getAllByRole("listitem")).toHaveLength(2);
		expect(screen.getByLabelText("Allowed network 1")).toHaveValue(
			"10.0.0.0/8",
		);
		expect(screen.getByLabelText("Allowed network 2")).toHaveValue(
			"192.168.1.20",
		);
	});

	test("typing edits only that row and reports the whole list", async () => {
		const user = userEvent.setup();
		const seen: string[][] = [];
		await renderWithProviders(
			<Host initial={["10.0.0.0/8", ""]} onChange={(n) => seen.push(n)} />,
		);
		await user.type(screen.getByLabelText("Allowed network 2"), "1.2");
		expect(rowInputs()).toEqual(["10.0.0.0/8", "1.2"]);
		expect(seen.at(-1)).toEqual(["10.0.0.0/8", "1.2"]);
	});

	test("Add network appends an empty row", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Host initial={["10.0.0.0/8"]} />);
		await user.click(screen.getByRole("button", { name: "Add network" }));
		expect(rowInputs()).toEqual(["10.0.0.0/8", ""]);
	});

	test("Remove drops that row", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Host initial={["a", "b", "c"]} />);
		await user.click(
			screen.getByRole("button", { name: "Remove Allowed network 2" }),
		);
		expect(rowInputs()).toEqual(["a", "c"]);
	});

	test("moving a row up or down swaps it with its neighbour", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Host initial={["a", "b", "c"]} />);
		await user.click(
			screen.getByRole("button", { name: "Move Allowed network 3 up" }),
		);
		expect(rowInputs()).toEqual(["a", "c", "b"]);
		await user.click(
			screen.getByRole("button", { name: "Move Allowed network 1 down" }),
		);
		expect(rowInputs()).toEqual(["c", "a", "b"]);
	});

	test("the first row cannot move up and the last cannot move down", async () => {
		await renderWithProviders(<Host initial={["a", "b"]} />);
		expect(
			screen.getByRole("button", { name: "Move Allowed network 1 up" }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Move Allowed network 2 down" }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Move Allowed network 1 down" }),
		).toBeEnabled();
	});

	test("a row's error is shown under it and marks only that input invalid", async () => {
		await renderWithProviders(
			<Host
				initial={["10.0.0.0/8", "nope"]}
				errors={{ "allowCidrs.1": "“nope” is not a CIDR or IP address." }}
			/>,
		);
		expect(
			screen.getByText("“nope” is not a CIDR or IP address."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Allowed network 2")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByLabelText("Allowed network 1")).toHaveAttribute(
			"aria-invalid",
			"false",
		);
	});

	test("disabled freezes every control", async () => {
		await renderWithProviders(<Host initial={["a", "b"]} disabled />);
		expect(screen.getByLabelText("Allowed network 1")).toBeDisabled();
		for (const name of [
			"Move Allowed network 1 down",
			"Move Allowed network 2 up",
			"Remove Allowed network 1",
			"Add network",
			"Paste a list",
		]) {
			expect(screen.getByRole("button", { name })).toBeDisabled();
		}
	});
});

describe("CidrListEditor paste box", () => {
	test("pasted lines, commas and comments are added after the existing entries, deduplicated", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Host initial={["10.0.0.0/8"]} />);
		await user.click(screen.getByRole("button", { name: "Paste a list" }));
		expect(screen.queryByRole("button", { name: "Add network" })).toBeNull();
		const box = screen.getByRole("textbox", { name: "Paste allowed networks" });
		await user.click(box);
		await user.paste(
			"10.1.2.3/8 # same network as the first\n192.168.1.20, 2001:db8::/32\n",
		);
		await user.click(screen.getByRole("button", { name: "Add to list" }));
		expect(rowInputs()).toEqual([
			"10.0.0.0/8",
			"192.168.1.20",
			"2001:db8::/32",
		]);
		// the box closes and the normal buttons return
		expect(screen.queryByRole("textbox", { name: /Paste/ })).toBeNull();
		expect(
			screen.getByRole("button", { name: "Add network" }),
		).toBeInTheDocument();
	});

	test("Add to list stays off until something parsable is pasted", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Host initial={[]} />);
		await user.click(screen.getByRole("button", { name: "Paste a list" }));
		const add = screen.getByRole("button", { name: "Add to list" });
		expect(add).toBeDisabled();
		await user.type(
			screen.getByRole("textbox", { name: "Paste allowed networks" }),
			"# only a comment",
		);
		expect(add).toBeDisabled();
		await user.type(
			screen.getByRole("textbox", { name: "Paste allowed networks" }),
			"\n1.2.3.4",
		);
		expect(add).toBeEnabled();
	});

	test("Cancel discards the pasted text", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Host initial={["a"]} />);
		await user.click(screen.getByRole("button", { name: "Paste a list" }));
		await user.type(
			screen.getByRole("textbox", { name: "Paste allowed networks" }),
			"1.2.3.4",
		);
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(rowInputs()).toEqual(["a"]);
		await user.click(screen.getByRole("button", { name: "Paste a list" }));
		expect(
			screen.getByRole("textbox", { name: "Paste allowed networks" }),
		).toHaveValue("");
	});
});
