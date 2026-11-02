import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
	FieldTitle,
} from "#/components/ui/field";

describe("Field", () => {
	it("is a labelled group that reports its orientation", () => {
		render(<Field aria-label="Server name" orientation="horizontal" />);
		const group = screen.getByRole("group", { name: "Server name" });
		expect(group).toHaveAttribute("data-slot", "field");
		expect(group).toHaveAttribute("data-orientation", "horizontal");
	});

	it("defaults to a vertical orientation", () => {
		render(<Field aria-label="x" />);
		expect(screen.getByRole("group")).toHaveAttribute(
			"data-orientation",
			"vertical",
		);
	});

	it("wires a FieldLabel to its input so clicking the text focuses it", async () => {
		render(
			<Field>
				<FieldLabel htmlFor="slug">Slug</FieldLabel>
				<input id="slug" />
				<FieldDescription>Lowercase letters only.</FieldDescription>
			</Field>,
		);
		expect(screen.getByRole("textbox", { name: "Slug" })).toBeInTheDocument();
		await userEvent.setup().click(screen.getByText("Slug"));
		expect(screen.getByRole("textbox")).toHaveFocus();
		expect(screen.getByText("Lowercase letters only.")).toHaveAttribute(
			"data-slot",
			"field-description",
		);
	});

	it("renders FieldTitle as a non-label heading line", () => {
		render(<FieldTitle>Access</FieldTitle>);
		const title = screen.getByText("Access");
		expect(title.tagName).toBe("DIV");
		expect(title).toHaveAttribute("data-slot", "field-label");
	});
});

describe("FieldError", () => {
	it("renders nothing without children or errors", () => {
		const { container } = render(<FieldError />);
		expect(container).toBeEmptyDOMElement();
	});

	it("renders nothing for an empty errors array", () => {
		const { container } = render(<FieldError errors={[]} />);
		expect(container).toBeEmptyDOMElement();
	});

	it("announces children as an alert", () => {
		render(<FieldError>Name is taken</FieldError>);
		const alert = screen.getByRole("alert");
		expect(alert).toHaveTextContent("Name is taken");
		expect(alert).toHaveAttribute("data-tone", "danger");
	});

	it("prefers children over errors", () => {
		render(
			<FieldError errors={[{ message: "from errors" }]}>
				from children
			</FieldError>,
		);
		expect(screen.getByRole("alert")).toHaveTextContent("from children");
		expect(screen.queryByText("from errors")).not.toBeInTheDocument();
	});

	it("shows a single error as plain text, not a list", () => {
		render(<FieldError errors={[{ message: "Too short" }]} />);
		expect(screen.getByRole("alert")).toHaveTextContent("Too short");
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});

	it("collapses duplicate messages into one", () => {
		render(
			<FieldError
				errors={[{ message: "Required" }, { message: "Required" }]}
			/>,
		);
		expect(screen.getByRole("alert")).toHaveTextContent("Required");
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});

	it("lists several distinct errors and skips ones without a message", () => {
		render(
			<FieldError
				errors={[
					{ message: "Too short" },
					undefined,
					{ message: "No spaces" },
					{},
				]}
			/>,
		);
		const items = screen.getAllByRole("listitem");
		expect(items.map((li) => li.textContent)).toEqual([
			"Too short",
			"No spaces",
		]);
	});
});
