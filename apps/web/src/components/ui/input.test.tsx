import { describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Input } from "#/components/ui/input";

function Harness() {
	const [value, setValue] = useState("");
	return (
		<>
			<Input
				aria-label="Slug"
				value={value}
				onChange={(e) => setValue(e.target.value.toUpperCase())}
			/>
			<output>{value}</output>
		</>
	);
}

describe("Input", () => {
	it("emits typed text through onChange, one change per keystroke", async () => {
		const onChange = mock(
			(e: React.ChangeEvent<HTMLInputElement>) => e.target.value,
		);
		render(<Input aria-label="Name" onChange={onChange} />);
		await userEvent
			.setup()
			.type(screen.getByRole("textbox", { name: "Name" }), "abc");
		expect(onChange.mock.results.map((r) => r.value)).toEqual([
			"a",
			"ab",
			"abc",
		]);
	});

	it("is uncontrolled with defaultValue and editable", async () => {
		render(<Input aria-label="Name" defaultValue="web" />);
		const input = screen.getByRole("textbox", { name: "Name" });
		expect(input).toHaveValue("web");
		await userEvent.setup().type(input, "-1");
		expect(input).toHaveValue("web-1");
	});

	it("follows a controlled value and keeps it when the parent refuses the change", async () => {
		render(<Input aria-label="Name" value="fixed" onChange={() => {}} />);
		const input = screen.getByRole("textbox", { name: "Name" });
		await userEvent.setup().type(input, "x");
		expect(input).toHaveValue("fixed");
	});

	it("works as a controlled field with state", async () => {
		render(<Harness />);
		await userEvent
			.setup()
			.type(screen.getByRole("textbox", { name: "Slug" }), "ab");
		expect(screen.getByRole("textbox", { name: "Slug" })).toHaveValue("AB");
		expect(screen.getByRole("status")).toHaveTextContent("AB");
	});

	it("does not accept typing when disabled", async () => {
		const onChange = mock();
		render(<Input aria-label="Name" disabled onChange={onChange} />);
		const input = screen.getByRole("textbox", { name: "Name" });
		expect(input).toBeDisabled();
		await userEvent.setup().type(input, "abc");
		expect(onChange).not.toHaveBeenCalled();
		expect(input).toHaveValue("");
	});

	it("does not accept typing when readOnly but stays focusable", async () => {
		const onChange = mock();
		const user = userEvent.setup();
		render(
			<Input
				aria-label="Name"
				readOnly
				defaultValue="locked"
				onChange={onChange}
			/>,
		);
		const input = screen.getByRole("textbox", { name: "Name" });
		await user.type(input, "abc");
		expect(input).toHaveFocus();
		expect(input).toHaveValue("locked");
		expect(onChange).not.toHaveBeenCalled();
	});

	it("passes type, placeholder and aria-invalid to the native input", () => {
		render(<Input type="email" placeholder="you@example.com" aria-invalid />);
		const input = screen.getByPlaceholderText("you@example.com");
		expect(input).toHaveAttribute("type", "email");
		expect(input).toHaveAttribute("aria-invalid", "true");
		expect(input).toHaveAttribute("data-slot", "input");
	});

	it("scales: lg is taller than default and xl taller still", () => {
		render(
			<>
				<Input aria-label="d" />
				<Input aria-label="l" scale="lg" />
				<Input aria-label="x" scale="xl" />
			</>,
		);
		expect(screen.getByLabelText("d")).toHaveClass("h-8");
		expect(screen.getByLabelText("l")).toHaveClass("h-9");
		expect(screen.getByLabelText("x")).toHaveClass("h-10");
	});

	it("merges a caller className", () => {
		render(<Input aria-label="n" className="w-60" />);
		expect(screen.getByLabelText("n")).toHaveClass("w-60");
	});
});
