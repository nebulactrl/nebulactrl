import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import { Kbd } from "#/components/ui/kbd";

describe("Kbd", () => {
	it("renders a kbd element with its text", () => {
		render(<Kbd>esc</Kbd>);
		const kbd = screen.getByText("esc");
		expect(kbd.tagName).toBe("KBD");
		expect(kbd).toHaveAttribute("data-slot", "kbd");
	});

	it("keeps the text content intact while wrapping modifier glyphs", () => {
		render(<Kbd>⌘K</Kbd>);
		const kbd = document.querySelector("kbd") as HTMLElement;
		expect(kbd).toHaveTextContent("⌘K");
		const mods = kbd.querySelectorAll(".keycap-mod");
		expect(Array.from(mods).map((m) => m.textContent)).toEqual(["⌘"]);
	});

	it("wraps every glyph of a chord separately", () => {
		render(<Kbd>⌘⇧↵</Kbd>);
		const mods = document.querySelectorAll("kbd .keycap-mod");
		expect(Array.from(mods).map((m) => m.textContent)).toEqual(["⌘", "⇧", "↵"]);
	});

	it("does not wrap plain letters", () => {
		render(<Kbd>Ctrl</Kbd>);
		expect(document.querySelector(".keycap-mod")).toBeNull();
	});

	it("leaves element children alone", () => {
		render(
			<Kbd>
				<svg data-testid="icon" />⌥
			</Kbd>,
		);
		expect(screen.getByTestId("icon")).toBeInTheDocument();
		expect(document.querySelector(".keycap-mod")?.textContent).toBe("⌥");
	});

	it("applies variant sizes and merges className", () => {
		render(
			<>
				<Kbd variant="cap">a</Kbd>
				<Kbd variant="key" className="extra">
					b
				</Kbd>
				<Kbd>c</Kbd>
			</>,
		);
		expect(screen.getByText("a")).toHaveClass("h-6!");
		expect(screen.getByText("b")).toHaveClass("h-[18px]!", "extra");
		expect(screen.getByText("c")).not.toHaveClass("h-6!", "h-[18px]!");
	});
});
