import { describe, expect, mock, test } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { fromLocalInput, type RangeSelection, toLocalInput } from "./model";
import { RangePicker } from "./range-picker";

async function open(value: RangeSelection) {
	const onChange = mock();
	await renderWithProviders(<RangePicker value={value} onChange={onChange} />);
	const user = userEvent.setup();
	await user.click(screen.getByRole("button", { name: "Time range" }));
	return { onChange, user };
}

const field = (label: string) =>
	screen.getByLabelText(label) as HTMLInputElement;

describe("RangePicker", () => {
	test("the trigger says the preset in words", async () => {
		await renderWithProviders(
			<RangePicker value={{ kind: "preset", id: "6h" }} onChange={() => {}} />,
		);
		expect(
			screen.getByRole("button", { name: "Time range" }),
		).toHaveTextContent("Last 6 hours");
	});

	test("lists the five presets and marks the active one", async () => {
		await open({ kind: "preset", id: "24h" });
		for (const label of [
			"Last 15 minutes",
			"Last hour",
			"Last 6 hours",
			"Last 24 hours",
			"Last 7 days",
		]) {
			expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
		}
		expect(
			screen.getByRole("button", { name: "Last 24 hours" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(screen.getByRole("button", { name: "Last hour" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	test("choosing a preset reports it and closes the popover", async () => {
		const { onChange, user } = await open({ kind: "preset", id: "1h" });
		await user.click(screen.getByRole("button", { name: "Last 7 days" }));
		expect(onChange).toHaveBeenCalledWith({ kind: "preset", id: "7d" });
		expect(screen.queryByText("Custom range")).not.toBeInTheDocument();
	});

	test("no preset is active while a custom range is shown", async () => {
		await open({
			kind: "custom",
			from: "2026-10-20T10:00:00.000Z",
			to: undefined,
		});
		const presets = screen.getAllByRole("button", { name: /^Last / });
		expect(presets).toHaveLength(5);
		for (const button of presets) {
			expect(button).toHaveAttribute("aria-pressed", "false");
		}
	});

	test("the custom fields start from the range on screen", async () => {
		const from = "2026-10-20T10:00:00.000Z";
		const to = "2026-10-20T12:30:00.000Z";
		await open({ kind: "custom", from, to });
		expect(field("From")).toHaveValue(toLocalInput(from));
		expect(field("To (empty: now)")).toHaveValue(toLocalInput(to));
	});

	test("applying a custom range reports the instants and closes", async () => {
		const { onChange, user } = await open({ kind: "preset", id: "1h" });
		await user.clear(field("From"));
		await user.type(field("From"), "2026-10-20T10:00");
		await user.type(field("To (empty: now)"), "2026-10-20T12:00");
		await user.click(screen.getByRole("button", { name: "Apply range" }));
		expect(onChange).toHaveBeenCalledWith({
			kind: "custom",
			from: fromLocalInput("2026-10-20T10:00"),
			to: fromLocalInput("2026-10-20T12:00"),
		});
		expect(screen.queryByText("Custom range")).not.toBeInTheDocument();
	});

	test("an open end means now: only a start is accepted", async () => {
		const { onChange, user } = await open({ kind: "preset", id: "1h" });
		await user.clear(field("From"));
		await user.type(field("From"), "2026-10-20T10:00");
		await user.click(screen.getByRole("button", { name: "Apply range" }));
		expect(onChange).toHaveBeenCalledWith({
			kind: "custom",
			from: fromLocalInput("2026-10-20T10:00"),
			to: undefined,
		});
	});

	test("an end before the start is refused with a message", async () => {
		const { onChange, user } = await open({ kind: "preset", id: "1h" });
		await user.clear(field("From"));
		await user.type(field("From"), "2026-10-20T12:00");
		await user.type(field("To (empty: now)"), "2026-10-20T10:00");
		await user.click(screen.getByRole("button", { name: "Apply range" }));
		expect(
			screen.getByText("The end must be after the start."),
		).toBeInTheDocument();
		expect(onChange).not.toHaveBeenCalled();
	});

	test("both fields empty asks for a start or an end", async () => {
		const { onChange, user } = await open({ kind: "preset", id: "1h" });
		await user.clear(field("From"));
		await user.click(screen.getByRole("button", { name: "Apply range" }));
		expect(screen.getByText("Pick a start or an end.")).toBeInTheDocument();
		expect(onChange).not.toHaveBeenCalled();
	});

	test("reopening clears an earlier problem", async () => {
		const { user } = await open({ kind: "preset", id: "1h" });
		await user.clear(field("From"));
		await user.click(screen.getByRole("button", { name: "Apply range" }));
		expect(screen.getByText("Pick a start or an end.")).toBeInTheDocument();
		await user.keyboard("{Escape}");
		await user.click(screen.getByRole("button", { name: "Time range" }));
		expect(
			screen.queryByText("Pick a start or an end."),
		).not.toBeInTheDocument();
	});
});
