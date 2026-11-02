import { describe, expect, it, mock } from "bun:test";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import {
	Select,
	SelectContent,
	SelectGroup,
	SelectItem,
	SelectLabel,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";

const REGIONS = { fra: "Frankfurt", ams: "Amsterdam", nyc: "New York" };

function RegionSelect(
	props: Partial<React.ComponentProps<typeof Select<string>>> & {
		size?: "sm" | "default";
	},
) {
	const { size, ...root } = props;
	return (
		<Select items={REGIONS} {...root}>
			<SelectTrigger aria-label="Region" {...(size ? { size } : {})}>
				<SelectValue placeholder="Pick a region" />
			</SelectTrigger>
			<SelectContent>
				<SelectGroup>
					<SelectLabel>EU</SelectLabel>
					<SelectItem value="fra">Frankfurt</SelectItem>
					<SelectItem value="ams">Amsterdam</SelectItem>
				</SelectGroup>
				<SelectGroup>
					<SelectLabel>US</SelectLabel>
					<SelectItem value="nyc" disabled>
						New York
					</SelectItem>
				</SelectGroup>
			</SelectContent>
		</Select>
	);
}

function Harness() {
	const [value, setValue] = useState<string | null>("fra");
	return <RegionSelect value={value} onValueChange={(v) => setValue(v)} />;
}

describe("Select", () => {
	it("shows the placeholder, with the listbox closed, until a value is chosen", () => {
		render(<RegionSelect />);
		const trigger = screen.getByRole("combobox", { name: "Region" });
		expect(trigger).toHaveTextContent("Pick a region");
		expect(trigger).toHaveAttribute("aria-expanded", "false");
		expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
	});

	it("opens on click and lists the options by role", async () => {
		render(<RegionSelect />);
		await userEvent.setup().click(screen.getByRole("combobox"));
		const list = await screen.findByRole("listbox");
		expect(screen.getByRole("combobox")).toHaveAttribute(
			"aria-expanded",
			"true",
		);
		expect(
			within(list)
				.getAllByRole("option")
				.map((o) => o.textContent),
		).toEqual(["Frankfurt", "Amsterdam", "New York"]);
	});

	it("picks an option on click, emits its value, shows its label and closes", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(<RegionSelect onValueChange={onValueChange} />);
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: "Amsterdam" }));
		expect(onValueChange.mock.calls[0]?.[0]).toBe("ams");
		await waitFor(() =>
			expect(screen.queryByRole("listbox")).not.toBeInTheDocument(),
		);
		expect(screen.getByRole("combobox")).toHaveTextContent("Amsterdam");
	});

	it("marks the chosen option selected when reopened", async () => {
		const user = userEvent.setup();
		render(<RegionSelect defaultValue="ams" />);
		await user.click(screen.getByRole("combobox"));
		expect(
			await screen.findByRole("option", { name: "Amsterdam" }),
		).toHaveAttribute("aria-selected", "true");
		expect(screen.getByRole("option", { name: "Frankfurt" })).toHaveAttribute(
			"aria-selected",
			"false",
		);
	});

	it("is controlled: the shown value only changes when the parent updates it", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(<RegionSelect value="fra" onValueChange={onValueChange} />);
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: "Amsterdam" }));
		expect(onValueChange.mock.calls[0]?.[0]).toBe("ams");
		await waitFor(() =>
			expect(screen.queryByRole("listbox")).not.toBeInTheDocument(),
		);
		expect(screen.getByRole("combobox")).toHaveTextContent("Frankfurt");
	});

	it("follows parent state", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: "Amsterdam" }));
		await waitFor(() =>
			expect(screen.getByRole("combobox")).toHaveTextContent("Amsterdam"),
		);
	});

	it("opens from the keyboard and selects with arrows and Enter", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(<RegionSelect onValueChange={onValueChange} />);
		await user.tab();
		expect(screen.getByRole("combobox")).toHaveFocus();
		await user.keyboard("{Enter}");
		await screen.findByRole("listbox");
		await user.keyboard("{ArrowDown}");
		await user.keyboard("{Enter}");
		expect(onValueChange).toHaveBeenCalledTimes(1);
		// The first option is highlighted on open; one ArrowDown moves to the second.
		expect(onValueChange.mock.calls[0]?.[0]).toBe("ams");
		await waitFor(() =>
			expect(screen.queryByRole("listbox")).not.toBeInTheDocument(),
		);
	});

	it("closes on Escape without choosing", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(<RegionSelect onValueChange={onValueChange} />);
		await user.click(screen.getByRole("combobox"));
		await screen.findByRole("listbox");
		await user.keyboard("{Escape}");
		await waitFor(() =>
			expect(screen.queryByRole("listbox")).not.toBeInTheDocument(),
		);
		expect(onValueChange).not.toHaveBeenCalled();
	});

	it("does not pick a disabled option", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(<RegionSelect onValueChange={onValueChange} />);
		await user.click(screen.getByRole("combobox"));
		const ny = await screen.findByRole("option", { name: "New York" });
		expect(ny).toHaveAttribute("aria-disabled", "true");
		await user.click(ny);
		expect(onValueChange).not.toHaveBeenCalled();
	});

	it("does not open when the trigger is disabled", async () => {
		render(<RegionSelect disabled />);
		const trigger = screen.getByRole("combobox");
		expect(trigger).toBeDisabled();
		await userEvent.setup().click(trigger);
		expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
	});

	it("labels groups and tags the slots", async () => {
		render(<RegionSelect size="sm" />);
		const trigger = screen.getByRole("combobox");
		expect(trigger).toHaveAttribute("data-slot", "select-trigger");
		expect(trigger).toHaveAttribute("data-size", "sm");
		await userEvent.setup().click(trigger);
		await screen.findByRole("listbox");
		expect(screen.getByText("EU")).toHaveAttribute("data-slot", "select-label");
		expect(
			screen.getByText("US").closest("[data-slot=select-group]"),
		).not.toBeNull();
		expect(document.querySelector("[data-slot=select-content]")).not.toBeNull();
		expect(document.querySelector("[data-slot=select-item]")).not.toBeNull();
	});

	it("submits the chosen value with a form", async () => {
		const user = userEvent.setup();
		render(
			<form aria-label="f">
				<RegionSelect name="region" />
			</form>,
		);
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: "Frankfurt" }));
		await waitFor(() =>
			expect(screen.queryByRole("listbox")).not.toBeInTheDocument(),
		);
		expect(
			new FormData(screen.getByRole("form") as HTMLFormElement).get("region"),
		).toBe("fra");
	});
});
