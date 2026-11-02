import { describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Switch } from "#/components/ui/switch";

function Harness() {
	const [on, setOn] = useState(false);
	return <Switch aria-label="x" checked={on} onCheckedChange={setOn} />;
}

describe("Switch", () => {
	it("is an off switch that turns on by click and reports the state", async () => {
		const onCheckedChange = mock();
		render(
			<Switch aria-label="Auto deploy" onCheckedChange={onCheckedChange} />,
		);
		const sw = screen.getByRole("switch", { name: "Auto deploy" });
		expect(sw).toHaveAttribute("aria-checked", "false");
		const user = userEvent.setup();
		await user.click(sw);
		expect(sw).toHaveAttribute("aria-checked", "true");
		await user.click(sw);
		expect(onCheckedChange.mock.calls.map((c) => c[0])).toEqual([true, false]);
	});

	it("toggles with the Space key when focused", async () => {
		const onCheckedChange = mock();
		const user = userEvent.setup();
		render(
			<Switch aria-label="Auto deploy" onCheckedChange={onCheckedChange} />,
		);
		await user.tab();
		expect(screen.getByRole("switch")).toHaveFocus();
		await user.keyboard(" ");
		expect(onCheckedChange).toHaveBeenCalledWith(true, expect.anything());
		expect(screen.getByRole("switch")).toHaveAttribute("aria-checked", "true");
	});

	it("uses defaultChecked when uncontrolled", () => {
		render(<Switch aria-label="x" defaultChecked />);
		expect(screen.getByRole("switch")).toHaveAttribute("aria-checked", "true");
	});

	it("stays put when controlled and the parent does not update", async () => {
		const onCheckedChange = mock();
		render(
			<Switch
				aria-label="x"
				checked={false}
				onCheckedChange={onCheckedChange}
			/>,
		);
		await userEvent.setup().click(screen.getByRole("switch"));
		expect(onCheckedChange).toHaveBeenCalledTimes(1);
		expect(screen.getByRole("switch")).toHaveAttribute("aria-checked", "false");
	});

	it("follows state held by the parent", async () => {
		render(<Harness />);
		await userEvent.setup().click(screen.getByRole("switch"));
		expect(screen.getByRole("switch")).toHaveAttribute("aria-checked", "true");
	});

	it("ignores input when disabled", async () => {
		const onCheckedChange = mock();
		const user = userEvent.setup();
		render(
			<Switch aria-label="x" disabled onCheckedChange={onCheckedChange} />,
		);
		const sw = screen.getByRole("switch");
		expect(sw).toHaveAttribute("aria-disabled", "true");
		await user.click(sw);
		await user.keyboard(" ");
		expect(onCheckedChange).not.toHaveBeenCalled();
		expect(sw).toHaveAttribute("aria-checked", "false");
	});

	it("reports its size on the root", () => {
		const { rerender } = render(<Switch aria-label="x" />);
		expect(screen.getByRole("switch")).toHaveAttribute("data-size", "default");
		rerender(<Switch aria-label="x" size="sm" />);
		expect(screen.getByRole("switch")).toHaveAttribute("data-size", "sm");
	});

	it("submits its value with a form only while on", async () => {
		render(
			<form aria-label="f">
				<Switch aria-label="x" name="auto" />
			</form>,
		);
		const form = screen.getByRole("form") as HTMLFormElement;
		expect(new FormData(form).get("auto")).toBeNull();
		await userEvent.setup().click(screen.getByRole("switch"));
		expect(new FormData(form).get("auto")).toBe("on");
	});
});
