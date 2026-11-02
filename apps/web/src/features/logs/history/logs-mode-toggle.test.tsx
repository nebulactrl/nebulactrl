import { describe, expect, mock, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "#/components/ui/tooltip";
import { LogsModeToggle } from "./logs-mode-toggle";

function setup(props: Partial<Parameters<typeof LogsModeToggle>[0]> = {}) {
	const onChange = mock();
	render(
		<TooltipProvider>
			<LogsModeToggle mode="live" onChange={onChange} {...props} />
		</TooltipProvider>,
	);
	return onChange;
}

describe("LogsModeToggle", () => {
	test("offers Live and History only for an environment's view", () => {
		setup();
		expect(
			screen.getByRole("group", { name: "Log source" }),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Live" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "History" })).toBeInTheDocument();
		expect(screen.queryByText(/Central logs/)).not.toBeInTheDocument();
	});

	test("offers Central logs for a service", () => {
		setup({ central: true });
		expect(
			screen.getByRole("button", { name: "Central logs (7 days)" }),
		).toBeInTheDocument();
	});

	test("marks the current mode as pressed", () => {
		setup({ mode: "history" });
		expect(screen.getByRole("button", { name: "History" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "Live" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	test("reports the mode the person picks", async () => {
		const onChange = setup({ central: true });
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "History" }));
		expect(onChange).toHaveBeenCalledWith("history");
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Central logs (7 days)" }));
		expect(onChange).toHaveBeenLastCalledWith("central");
	});

	test("clicking the pressed mode does not report a change", async () => {
		const onChange = setup({ mode: "live" });
		await userEvent.setup().click(screen.getByRole("button", { name: "Live" }));
		expect(onChange).not.toHaveBeenCalled();
	});
});
