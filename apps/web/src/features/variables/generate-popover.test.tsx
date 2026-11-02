import { describe, expect, it } from "bun:test";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "#/components/ui/tooltip";
import { GeneratePopover } from "./generate-popover";

// Literal `${NAME}` placeholder: the escaped `$` keeps it from being a template placeholder.
const ref = (name: string) => `\${${name}}`;

interface Inserted {
	expression: string;
	makesSecret: boolean;
}

function setup(props: { disabled?: boolean } = {}) {
	const inserted: Inserted[] = [];
	render(
		<TooltipProvider>
			<GeneratePopover
				{...props}
				onInsert={(expression, makesSecret) =>
					inserted.push({ expression, makesSecret })
				}
			/>
		</TooltipProvider>,
	);
	return inserted;
}

async function open() {
	const user = userEvent.setup();
	await user.click(screen.getByRole("button", { name: "Generate" }));
	await screen.findByRole("button", { name: "Insert" });
	return user;
}

const kind = (name: string) => screen.getByRole("button", { name });

describe("GeneratePopover", () => {
	it("starts closed", () => {
		setup();
		expect(screen.queryByRole("button", { name: "Insert" })).toBeNull();
	});

	it("cannot be opened while disabled", async () => {
		const user = userEvent.setup();
		setup({ disabled: true });
		const trigger = screen.getByRole("button", { name: "Generate" });
		expect(trigger).toBeDisabled();
		await user.click(trigger);
		expect(screen.queryByRole("button", { name: "Insert" })).toBeNull();
	});

	it("inserts a 32 character secret by default and closes", async () => {
		const inserted = setup();
		const user = await open();
		expect(
			screen.getByText(
				/Generates 32 characters: letters and digits, once, when you save\./,
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/The variable becomes a secret: its value is never shown\./,
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Insert" }));
		expect(inserted).toEqual([
			{ expression: ref("{secret(32)}"), makesSecret: true },
		]);
		await waitFor(() =>
			expect(screen.queryByRole("button", { name: "Insert" })).toBeNull(),
		);
	});

	it("uses the length that was typed", async () => {
		const inserted = setup();
		const user = await open();
		const length = screen.getByLabelText("Length");
		await user.clear(length);
		await user.type(length, "48");
		expect(screen.getByText(/Generates 48 characters/)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Insert" }));
		expect(inserted[0]?.expression).toBe(ref("{secret(48)}"));
	});

	it("refuses a length outside 1 to 1024 and blocks Insert", async () => {
		const inserted = setup();
		const user = await open();
		const length = screen.getByLabelText("Length");
		await user.clear(length);
		await user.type(length, "2000");
		expect(
			screen.getByText("Use between 1 and 1024 characters."),
		).toBeInTheDocument();
		expect(screen.queryByText(/Generates/)).toBeNull();
		const insert = screen.getByRole("button", { name: "Insert" });
		expect(insert).toBeDisabled();
		await user.click(insert);
		expect(inserted).toEqual([]);
		// Fixing the value unblocks it again.
		await user.clear(length);
		await user.type(length, "16");
		expect(screen.getByRole("button", { name: "Insert" })).toBeEnabled();
	});

	it("picks the character set of a secret", async () => {
		const inserted = setup();
		const user = await open();
		await user.click(screen.getByRole("combobox", { name: /Characters/ }));
		await user.click(
			await screen.findByRole("option", { name: "Hex (0-9, a-f)" }),
		);
		expect(
			screen.getByText(/Generates 32 characters: hex/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Insert" }));
		expect(inserted[0]?.expression).toBe(ref("{secret(32, hex)}"));
	});

	it("generates random bytes, a secret, with the chosen encoding", async () => {
		const inserted = setup();
		const user = await open();
		await user.click(kind("Bytes"));
		expect(screen.getByLabelText("Bytes")).toHaveValue(32);
		expect(screen.queryByLabelText("Characters")).toBeNull();
		expect(
			screen.getByText(/Generates 32 random bytes, base64-encoded/),
		).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "hex" }));
		expect(
			screen.getByText(/32 random bytes, hex-encoded/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Insert" }));
		expect(inserted).toEqual([
			{ expression: ref("{randomBytes(32, hex)}"), makesSecret: true },
		]);
	});

	it("omits the encoding for base64 bytes, the default", async () => {
		const inserted = setup();
		const user = await open();
		await user.click(kind("Bytes"));
		const bytes = screen.getByLabelText("Bytes");
		await user.clear(bytes);
		await user.type(bytes, "16");
		await user.click(screen.getByRole("button", { name: "Insert" }));
		expect(inserted[0]?.expression).toBe(ref("{randomBytes(16)}"));
	});

	it("inserts a UUID, which does not make the variable a secret", async () => {
		const inserted = setup();
		const user = await open();
		await user.click(kind("UUID"));
		expect(
			screen.getByText(
				/Generates a random UUID \(version 4\), once, when you save\./,
			),
		).toBeInTheDocument();
		expect(screen.queryByText(/becomes a secret/)).toBeNull();
		expect(screen.queryByLabelText("Length")).toBeNull();
		await user.click(screen.getByRole("button", { name: "Insert" }));
		expect(inserted).toEqual([
			{ expression: ref("{uuid()}"), makesSecret: false },
		]);
	});

	it("inserts a number in a range, not a secret", async () => {
		const inserted = setup();
		const user = await open();
		await user.click(kind("Number"));
		const min = screen.getByLabelText("Minimum");
		const max = screen.getByLabelText("Maximum");
		expect(min).toHaveValue(0);
		expect(max).toHaveValue(100);
		await user.clear(min);
		await user.type(min, "10");
		await user.clear(max);
		await user.type(max, "20");
		expect(
			screen.getByText(/a whole number from 10 to 20/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Insert" }));
		expect(inserted).toEqual([
			{ expression: ref("{randomInt(10, 20)}"), makesSecret: false },
		]);
	});

	it("refuses a minimum above the maximum", async () => {
		const inserted = setup();
		const user = await open();
		await user.click(kind("Number"));
		const min = screen.getByLabelText("Minimum");
		await user.clear(min);
		await user.type(min, "500");
		expect(
			screen.getByText("The minimum must not exceed the maximum."),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Insert" })).toBeDisabled();
		expect(inserted).toEqual([]);
	});

	it("refuses a non-whole number", async () => {
		setup();
		await open();
		fireEvent.click(kind("Number"));
		fireEvent.change(await screen.findByLabelText("Maximum"), {
			target: { value: "1.5" },
		});
		expect(screen.getByText("Use whole numbers.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Insert" })).toBeDisabled();
	});

	it("keeps its choices when it is reopened", async () => {
		setup();
		const user = await open();
		await user.click(kind("UUID"));
		await user.keyboard("{Escape}");
		await waitFor(() =>
			expect(screen.queryByRole("button", { name: "Insert" })).toBeNull(),
		);
		await user.click(screen.getByRole("button", { name: "Generate" }));
		await screen.findByRole("button", { name: "Insert" });
		expect(kind("UUID")).toHaveAttribute("aria-pressed", "true");
	});
});
