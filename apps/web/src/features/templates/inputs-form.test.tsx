import { describe, expect, mock, test } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { InputsForm } from "./inputs-form";
import type { TemplateInput } from "./lib";

// Literal `${NAME}` placeholder: the escaped `$` keeps it from being a template placeholder.
const ref = (name: string) => `\${${name}}`;

function input(over: Partial<TemplateInput> & { key: string }): TemplateInput {
	return {
		label: over.key,
		default: "",
		required: false,
		secret: false,
		...over,
	};
}

const renderForm = (
	inputs: TemplateInput[],
	typed: Record<string, string> = {},
	onChange = mock(),
) =>
	renderWithProviders(
		<InputsForm
			inputs={inputs}
			typed={typed}
			onChange={onChange}
			idPrefix="t"
		/>,
	).then(() => onChange);

describe("InputsForm", () => {
	test("renders nothing for a template without inputs", async () => {
		const { container } = await renderWithProviders(
			<InputsForm inputs={[]} typed={{}} onChange={() => {}} idPrefix="t" />,
		);
		expect(container.querySelector("input")).toBeNull();
	});

	test("a required input is labelled required and starts with its literal default", async () => {
		await renderForm([
			input({
				key: "domain",
				label: "Domain",
				default: "example.com",
				required: true,
			}),
		]);
		const field = screen.getByLabelText(/Domain/);
		expect(field).toHaveValue("example.com");
		expect(field).toBeRequired();
		expect(screen.getByText("(required)")).toBeInTheDocument();
	});

	test("an optional input is not marked required", async () => {
		await renderForm([input({ key: "tz", label: "Timezone" })]);
		expect(screen.getByLabelText("Timezone")).not.toBeRequired();
		expect(screen.queryByText("(required)")).not.toBeInTheDocument();
	});

	test("typing reports the key and the new value", async () => {
		const user = userEvent.setup();
		const onChange = await renderForm([
			input({ key: "tz", label: "Timezone" }),
		]);
		await user.type(screen.getByLabelText("Timezone"), "U");
		expect(onChange).toHaveBeenCalledWith("tz", "U");
	});

	test("the typed value wins over the default", async () => {
		await renderForm(
			[input({ key: "tz", label: "Timezone", default: "UTC" })],
			{ tz: "Europe/Berlin" },
		);
		expect(screen.getByLabelText("Timezone")).toHaveValue("Europe/Berlin");
	});

	test("a secret is a password field that says it is stored encrypted", async () => {
		await renderForm([
			input({ key: "pw", label: "Password", secret: true, required: true }),
		]);
		const field = screen.getByLabelText(/Password/);
		expect(field).toHaveAttribute("type", "password");
		expect(field).toHaveAttribute("autocomplete", "new-password");
		expect(screen.getByText("Stored encrypted.")).toBeInTheDocument();
	});

	test("a generated secret starts empty with a Generated placeholder and hint", async () => {
		await renderForm([
			input({
				key: "key",
				label: "App key",
				secret: true,
				default: ref("{secret(32)}"),
			}),
		]);
		const field = screen.getByLabelText("App key");
		expect(field).toHaveValue("");
		expect(field).toHaveAttribute("placeholder", "Generated");
		expect(
			screen.getByText("Generated and stored encrypted if left empty."),
		).toBeInTheDocument();
	});

	test("an optional secret without a generator says it is optional", async () => {
		await renderForm([input({ key: "tok", label: "Token", secret: true })]);
		expect(screen.getByText("Optional. Stored encrypted.")).toBeInTheDocument();
	});

	test.each([
		["email", "email"],
		["url", "url"],
		["hostname", "text"],
		["number", "text"],
	] as const)("a %s input uses the %s field type", async (format, type) => {
		await renderForm([input({ key: "f", label: "Field", format })]);
		const field = screen.getByLabelText("Field");
		expect(field).toHaveAttribute("type", type);
		if (format === "number")
			expect(field).toHaveAttribute("inputmode", "numeric");
		else expect(field).not.toHaveAttribute("inputmode");
	});

	test("shows the input's own description", async () => {
		await renderForm([
			input({ key: "d", label: "Domain", description: "Where it is served" }),
		]);
		expect(screen.getByText("Where it is served")).toBeInTheDocument();
	});

	test("leaving a required field empty flags it after blur, and typing clears the flag", async () => {
		const user = userEvent.setup();
		await renderForm([
			input({ key: "domain", label: "Domain", required: true }),
		]);
		const field = screen.getByLabelText(/Domain/);
		expect(screen.queryByText("Domain is required.")).not.toBeInTheDocument();
		expect(field).toHaveAttribute("aria-invalid", "false");
		await user.click(field);
		await user.tab();
		expect(await screen.findByText("Domain is required.")).toBeInTheDocument();
		expect(field).toHaveAttribute("aria-invalid", "true");
		expect(field).toHaveAccessibleDescription(/Domain is required\./);
	});

	test("a filled required field is not flagged after blur", async () => {
		const user = userEvent.setup();
		await renderForm(
			[input({ key: "domain", label: "Domain", required: true })],
			{ domain: "a.com" },
		);
		await user.click(screen.getByLabelText(/Domain/));
		await user.tab();
		expect(screen.queryByText("Domain is required.")).not.toBeInTheDocument();
	});

	test("an optional field is never flagged", async () => {
		const user = userEvent.setup();
		await renderForm([input({ key: "tz", label: "Timezone" })]);
		await user.click(screen.getByLabelText("Timezone"));
		await user.tab();
		expect(screen.queryByText(/is required/)).not.toBeInTheDocument();
	});

	test("an input with options is a select that reports the chosen option", async () => {
		const user = userEvent.setup();
		const onChange = await renderForm([
			input({
				key: "db",
				label: "Database",
				options: ["sqlite", "postgres"],
				default: "sqlite",
			}),
		]);
		expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
		const trigger = screen.getByRole("combobox");
		expect(trigger).toHaveTextContent("sqlite");
		await user.click(trigger);
		await user.click(await screen.findByRole("option", { name: "postgres" }));
		expect(onChange).toHaveBeenCalledWith("db", "postgres");
	});
});
