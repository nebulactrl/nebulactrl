import { describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Checkbox } from "#/components/ui/checkbox";

function Harness() {
	const [on, setOn] = useState(false);
	return <Checkbox aria-label="Agree" checked={on} onCheckedChange={setOn} />;
}

describe("Checkbox", () => {
	it("starts unchecked and toggles on click, reporting the new state", async () => {
		const onCheckedChange = mock();
		render(<Checkbox aria-label="Agree" onCheckedChange={onCheckedChange} />);
		const box = screen.getByRole("checkbox", { name: "Agree" });
		expect(box).toHaveAttribute("aria-checked", "false");
		const user = userEvent.setup();
		await user.click(box);
		expect(box).toHaveAttribute("aria-checked", "true");
		await user.click(box);
		expect(box).toHaveAttribute("aria-checked", "false");
		expect(onCheckedChange.mock.calls.map((c) => c[0])).toEqual([true, false]);
	});

	it("toggles with the Space key when focused, but not Enter", async () => {
		const onCheckedChange = mock();
		const user = userEvent.setup();
		render(<Checkbox aria-label="Agree" onCheckedChange={onCheckedChange} />);
		await user.tab();
		const box = screen.getByRole("checkbox");
		expect(box).toHaveFocus();
		await user.keyboard("{Enter}");
		expect(onCheckedChange).not.toHaveBeenCalled();
		await user.keyboard(" ");
		expect(onCheckedChange).toHaveBeenCalledTimes(1);
		expect(box).toHaveAttribute("aria-checked", "true");
	});

	it("honours defaultChecked when uncontrolled", async () => {
		render(<Checkbox aria-label="Agree" defaultChecked />);
		const box = screen.getByRole("checkbox");
		expect(box).toHaveAttribute("aria-checked", "true");
		await userEvent.setup().click(box);
		expect(box).toHaveAttribute("aria-checked", "false");
	});

	it("stays as the parent says when controlled", async () => {
		const onCheckedChange = mock();
		render(
			<Checkbox
				aria-label="Agree"
				checked={false}
				onCheckedChange={onCheckedChange}
			/>,
		);
		const box = screen.getByRole("checkbox");
		await userEvent.setup().click(box);
		expect(onCheckedChange).toHaveBeenCalledWith(true, expect.anything());
		expect(box).toHaveAttribute("aria-checked", "false");
	});

	it("follows a controlled state owned by the parent", async () => {
		render(<Harness />);
		await userEvent.setup().click(screen.getByRole("checkbox"));
		expect(screen.getByRole("checkbox")).toHaveAttribute(
			"aria-checked",
			"true",
		);
	});

	it("shows the check glyph only when checked", () => {
		const { rerender } = render(
			<Checkbox
				aria-label="Agree"
				checked={false}
				onCheckedChange={() => {}}
			/>,
		);
		expect(document.querySelector("[data-slot=checkbox-indicator]")).toBeNull();
		rerender(
			<Checkbox aria-label="Agree" checked onCheckedChange={() => {}} />,
		);
		expect(
			document.querySelector("[data-slot=checkbox-indicator] svg"),
		).not.toBeNull();
	});

	it("exposes the mixed state when indeterminate", () => {
		render(
			<Checkbox
				aria-label="All"
				indeterminate
				checked={false}
				onCheckedChange={() => {}}
			/>,
		);
		expect(screen.getByRole("checkbox")).toHaveAttribute(
			"aria-checked",
			"mixed",
		);
	});

	it("ignores clicks and is flagged disabled when disabled", async () => {
		const onCheckedChange = mock();
		render(
			<Checkbox
				aria-label="Agree"
				disabled
				onCheckedChange={onCheckedChange}
			/>,
		);
		const box = screen.getByRole("checkbox");
		expect(box).toHaveAttribute("aria-disabled", "true");
		await userEvent.setup().click(box);
		expect(onCheckedChange).not.toHaveBeenCalled();
		expect(box).toHaveAttribute("aria-checked", "false");
	});

	it("does not change when readOnly", async () => {
		const onCheckedChange = mock();
		render(
			<Checkbox
				aria-label="Agree"
				readOnly
				onCheckedChange={onCheckedChange}
			/>,
		);
		await userEvent.setup().click(screen.getByRole("checkbox"));
		expect(onCheckedChange).not.toHaveBeenCalled();
	});

	it("submits its value with a form only while checked", async () => {
		render(
			<form aria-label="f">
				<Checkbox aria-label="Agree" name="tos" value="yes" />
			</form>,
		);
		const form = screen.getByRole("form") as HTMLFormElement;
		expect(new FormData(form).get("tos")).toBeNull();
		await userEvent.setup().click(screen.getByRole("checkbox"));
		expect(new FormData(form).get("tos")).toBe("yes");
	});
});
