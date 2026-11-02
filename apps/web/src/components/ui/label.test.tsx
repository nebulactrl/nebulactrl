import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Label } from "#/components/ui/label";

describe("Label", () => {
	it("names the control it points at with htmlFor", () => {
		render(
			<>
				<Label htmlFor="email">Email</Label>
				<input id="email" />
			</>,
		);
		expect(screen.getByRole("textbox", { name: "Email" })).toBeInTheDocument();
	});

	it("focuses its control when the text is clicked", async () => {
		render(
			<>
				<Label htmlFor="email">Email</Label>
				<input id="email" />
			</>,
		);
		await userEvent.setup().click(screen.getByText("Email"));
		expect(screen.getByRole("textbox")).toHaveFocus();
	});

	it("names a nested control", () => {
		render(
			<Label className="mine">
				Token <input />
			</Label>,
		);
		const label = screen.getByText("Token", { exact: false });
		expect(label).toHaveAttribute("data-slot", "label");
		expect(label).toHaveClass("mine");
		expect(screen.getByRole("textbox", { name: "Token" })).toBeInTheDocument();
	});
});
