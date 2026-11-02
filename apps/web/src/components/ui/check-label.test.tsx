import { describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CheckLabel } from "#/components/ui/check-label";

describe("CheckLabel", () => {
	it("names the checkbox with its label text", () => {
		render(
			<CheckLabel checked={false} onCheckedChange={() => {}}>
				Enable backups
			</CheckLabel>,
		);
		expect(
			screen.getByRole("checkbox", { name: "Enable backups" }),
		).toHaveAttribute("aria-checked", "false");
	});

	it("reflects the controlled checked prop", () => {
		render(
			<CheckLabel checked onCheckedChange={() => {}}>
				Enable backups
			</CheckLabel>,
		);
		expect(screen.getByRole("checkbox")).toHaveAttribute(
			"aria-checked",
			"true",
		);
	});

	it("toggles when the label text is clicked, emitting a boolean", async () => {
		const onCheckedChange = mock();
		render(
			<CheckLabel checked={false} onCheckedChange={onCheckedChange}>
				Enable backups
			</CheckLabel>,
		);
		await userEvent.setup().click(screen.getByText("Enable backups"));
		expect(onCheckedChange).toHaveBeenCalledTimes(1);
		expect(onCheckedChange).toHaveBeenCalledWith(true);
	});

	it("emits false when clicking a checked box", async () => {
		const onCheckedChange = mock();
		render(
			<CheckLabel checked onCheckedChange={onCheckedChange}>
				Enable backups
			</CheckLabel>,
		);
		await userEvent.setup().click(screen.getByRole("checkbox"));
		expect(onCheckedChange).toHaveBeenCalledWith(false);
	});

	it("does nothing when disabled, from the box or the label", async () => {
		const onCheckedChange = mock();
		const user = userEvent.setup();
		render(
			<CheckLabel checked={false} disabled onCheckedChange={onCheckedChange}>
				Enable backups
			</CheckLabel>,
		);
		await user.click(screen.getByText("Enable backups"));
		await user.click(screen.getByRole("checkbox"));
		expect(onCheckedChange).not.toHaveBeenCalled();
		expect(screen.getByRole("checkbox")).toHaveAttribute(
			"aria-disabled",
			"true",
		);
	});

	it("applies the layout, label and checkbox class hooks to their own elements", () => {
		render(
			<CheckLabel
				checked={false}
				onCheckedChange={() => {}}
				className="row-x"
				labelClassName="label-x"
				checkboxClassName="box-x"
			>
				Text
			</CheckLabel>,
		);
		expect(screen.getByText("Text")).toHaveClass("label-x");
		expect(screen.getByRole("checkbox")).toHaveClass("box-x");
		expect(screen.getByText("Text").parentElement).toHaveClass("row-x");
	});

	it("keeps two instances independent (distinct ids)", async () => {
		const a = mock();
		const b = mock();
		render(
			<>
				<CheckLabel checked={false} onCheckedChange={a}>
					Alpha
				</CheckLabel>
				<CheckLabel checked={false} onCheckedChange={b}>
					Beta
				</CheckLabel>
			</>,
		);
		await userEvent.setup().click(screen.getByText("Beta"));
		expect(a).not.toHaveBeenCalled();
		expect(b).toHaveBeenCalledWith(true);
	});
});
