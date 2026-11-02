import { describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Toggle, toggleVariants } from "#/components/ui/toggle";

function Harness() {
	const [on, setOn] = useState(false);
	return (
		<Toggle pressed={on} onPressedChange={setOn}>
			Bold
		</Toggle>
	);
}

describe("Toggle", () => {
	it("is an unpressed button that presses on click and reports the state", async () => {
		const onPressedChange = mock();
		render(<Toggle onPressedChange={onPressedChange}>Bold</Toggle>);
		const toggle = screen.getByRole("button", { name: "Bold" });
		expect(toggle).toHaveAttribute("aria-pressed", "false");
		const user = userEvent.setup();
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-pressed", "true");
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-pressed", "false");
		expect(onPressedChange.mock.calls.map((c) => c[0])).toEqual([true, false]);
	});

	it("toggles from the keyboard with Enter and Space", async () => {
		const onPressedChange = mock();
		const user = userEvent.setup();
		render(<Toggle onPressedChange={onPressedChange}>Bold</Toggle>);
		await user.tab();
		await user.keyboard("{Enter}");
		await user.keyboard(" ");
		expect(onPressedChange.mock.calls.map((c) => c[0])).toEqual([true, false]);
	});

	it("starts pressed with defaultPressed", () => {
		render(<Toggle defaultPressed>Bold</Toggle>);
		expect(screen.getByRole("button")).toHaveAttribute("aria-pressed", "true");
	});

	it("stays as the parent says when controlled", async () => {
		const onPressedChange = mock();
		render(
			<Toggle pressed={false} onPressedChange={onPressedChange}>
				Bold
			</Toggle>,
		);
		await userEvent.setup().click(screen.getByRole("button"));
		expect(onPressedChange).toHaveBeenCalledTimes(1);
		expect(screen.getByRole("button")).toHaveAttribute("aria-pressed", "false");
	});

	it("follows state held by the parent", async () => {
		render(<Harness />);
		await userEvent.setup().click(screen.getByRole("button"));
		expect(screen.getByRole("button")).toHaveAttribute("aria-pressed", "true");
	});

	it("ignores clicks when disabled", async () => {
		const onPressedChange = mock();
		render(
			<Toggle disabled onPressedChange={onPressedChange}>
				Bold
			</Toggle>,
		);
		const toggle = screen.getByRole("button");
		expect(toggle).toBeDisabled();
		await userEvent.setup().click(toggle);
		expect(onPressedChange).not.toHaveBeenCalled();
		expect(toggle).toHaveAttribute("aria-pressed", "false");
	});

	it("tags itself and merges class names", () => {
		render(
			<Toggle variant="outline" size="lg" className="mine">
				Bold
			</Toggle>,
		);
		const toggle = screen.getByRole("button");
		expect(toggle).toHaveAttribute("data-slot", "toggle");
		expect(toggle).toHaveClass("mine", "h-8", "border-line-control");
	});

	it("varies by variant and size in toggleVariants", () => {
		expect(toggleVariants({ variant: "segmented" })).toContain("font-mono");
		expect(toggleVariants({ variant: "default" })).not.toContain("font-mono");
		expect(toggleVariants({ size: "sm" })).toContain("h-7");
		expect(toggleVariants({ size: "segmented-lg" })).toContain("h-7 px-[11px]");
		expect(toggleVariants({})).toContain("h-[30px]");
	});
});
