import { describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Textarea } from "#/components/ui/textarea";

describe("Textarea", () => {
	it("emits the full text on change, newlines included", async () => {
		const onChange = mock(
			(e: React.ChangeEvent<HTMLTextAreaElement>) => e.target.value,
		);
		render(<Textarea aria-label="Notes" onChange={onChange} />);
		await userEvent
			.setup()
			.type(screen.getByRole("textbox", { name: "Notes" }), "a{Enter}b");
		expect(onChange.mock.results.at(-1)?.value).toBe("a\nb");
	});

	it("is uncontrolled with defaultValue", async () => {
		render(<Textarea aria-label="Notes" defaultValue="hello" />);
		const area = screen.getByRole("textbox", { name: "Notes" });
		expect(area).toHaveValue("hello");
		await userEvent.setup().type(area, "!");
		expect(area).toHaveValue("hello!");
	});

	it("keeps a controlled value when the parent ignores the change", async () => {
		render(<Textarea aria-label="Notes" value="fixed" onChange={() => {}} />);
		const area = screen.getByRole("textbox", { name: "Notes" });
		await userEvent.setup().type(area, "zzz");
		expect(area).toHaveValue("fixed");
	});

	it("ignores typing when disabled", async () => {
		const onChange = mock();
		render(<Textarea aria-label="Notes" disabled onChange={onChange} />);
		const area = screen.getByRole("textbox", { name: "Notes" });
		expect(area).toBeDisabled();
		await userEvent.setup().type(area, "zzz");
		expect(onChange).not.toHaveBeenCalled();
	});

	it("forwards placeholder, rows and className onto a textarea element", () => {
		render(<Textarea placeholder="Say more" rows={5} className="extra" />);
		const area = screen.getByPlaceholderText("Say more");
		expect(area.tagName).toBe("TEXTAREA");
		expect(area).toHaveAttribute("rows", "5");
		expect(area).toHaveAttribute("data-slot", "textarea");
		expect(area).toHaveClass("extra");
	});
});
