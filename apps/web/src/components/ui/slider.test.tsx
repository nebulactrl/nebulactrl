import { describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Slider } from "#/components/ui/slider";

function Harness() {
	const [value, setValue] = useState(3);
	return (
		<>
			<Slider
				aria-label="Memory"
				value={value}
				min={0}
				max={10}
				onValueChange={(v) => setValue(v)}
			/>
			<output>{value}</output>
		</>
	);
}

describe("Slider", () => {
	it("exposes one labelled thumb with the default value, min and max", () => {
		render(<Slider aria-label="Memory" defaultValue={4} min={1} max={16} />);
		const thumb = screen.getByRole("slider", { name: "Memory" });
		expect(thumb).toHaveValue("4");
		expect(thumb).toHaveAttribute("min", "1");
		expect(thumb).toHaveAttribute("max", "16");
	});

	it("moves by step with the arrow keys and reports the number", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<Slider
				aria-label="Memory"
				defaultValue={4}
				min={0}
				max={10}
				step={2}
				onValueChange={onValueChange}
			/>,
		);
		const thumb = screen.getByRole("slider");
		await user.tab();
		expect(thumb).toHaveFocus();
		await user.keyboard("{ArrowRight}");
		expect(thumb).toHaveValue("6");
		await user.keyboard("{ArrowLeft}{ArrowLeft}");
		expect(thumb).toHaveValue("2");
		expect(onValueChange.mock.calls.map((c) => c[0])).toEqual([6, 4, 2]);
	});

	it("jumps to the ends with Home and End and never exceeds them", async () => {
		const user = userEvent.setup();
		render(<Slider aria-label="Memory" defaultValue={5} min={0} max={10} />);
		const thumb = screen.getByRole("slider");
		await user.tab();
		await user.keyboard("{End}");
		expect(thumb).toHaveValue("10");
		await user.keyboard("{ArrowRight}");
		expect(thumb).toHaveValue("10");
		await user.keyboard("{Home}");
		expect(thumb).toHaveValue("0");
		await user.keyboard("{ArrowLeft}");
		expect(thumb).toHaveValue("0");
	});

	it("is controlled: keeps the parent's value until it updates", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<Slider
				aria-label="Memory"
				value={3}
				min={0}
				max={10}
				onValueChange={onValueChange}
			/>,
		);
		await user.tab();
		await user.keyboard("{ArrowRight}");
		expect(onValueChange.mock.calls[0]?.[0]).toBe(4);
		expect(screen.getByRole("slider")).toHaveValue("3");
	});

	it("follows state held by the parent", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.tab();
		await user.keyboard("{ArrowRight}{ArrowRight}");
		expect(screen.getByRole("slider")).toHaveValue("5");
		expect(screen.getByRole("status")).toHaveTextContent("5");
	});

	it("renders one thumb per value of a range, each named by index", () => {
		render(
			<Slider aria-label="Memory" defaultValue={[2, 8]} min={0} max={10} />,
		);
		const first = screen.getByRole("slider", { name: "Memory 1" });
		const second = screen.getByRole("slider", { name: "Memory 2" });
		expect(first).toHaveValue("2");
		expect(second).toHaveValue("8");
		expect(screen.getAllByRole("slider")).toHaveLength(2);
	});

	it("a range reports an array and the thumbs cannot cross", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<Slider
				aria-label="Range"
				defaultValue={[4, 6]}
				min={0}
				max={10}
				onValueChange={onValueChange}
			/>,
		);
		const first = screen.getByRole("slider", { name: "Range 1" });
		const second = screen.getByRole("slider", { name: "Range 2" });
		await user.click(first);
		first.focus();
		await user.keyboard("{ArrowRight}");
		expect(onValueChange.mock.calls.at(-1)?.[0]).toEqual([5, 6]);
		await user.keyboard("{ArrowRight}{ArrowRight}{ArrowRight}");
		expect(first).toHaveValue("6");
		expect(second).toHaveValue("6");
		expect(onValueChange.mock.calls.at(-1)?.[0]).toEqual([6, 6]);
	});

	it("does not move when disabled", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<Slider
				aria-label="Memory"
				defaultValue={4}
				disabled
				onValueChange={onValueChange}
			/>,
		);
		const thumb = screen.getByRole("slider");
		expect(thumb).toBeDisabled();
		await user.tab();
		await user.keyboard("{ArrowRight}");
		expect(onValueChange).not.toHaveBeenCalled();
		expect(thumb).toHaveValue("4");
	});

	it("falls back to a single thumb at the minimum with no value given", () => {
		render(<Slider aria-label="Level" min={2} max={9} />);
		expect(screen.getAllByRole("slider")).toHaveLength(1);
		expect(screen.getByRole("slider")).toHaveValue("2");
	});

	it("leaves the thumb unnamed when no aria-label is given", () => {
		render(<Slider defaultValue={1} />);
		expect(screen.getByRole("slider")).not.toHaveAccessibleName();
	});

	it("tags its parts and merges className", () => {
		render(<Slider aria-label="x" defaultValue={1} className="mine" />);
		const root = document.querySelector("[data-slot=slider]");
		expect(root).toHaveClass("mine");
		for (const slot of [
			"slider-control",
			"slider-track",
			"slider-range",
			"slider-thumb",
		])
			expect(document.querySelector(`[data-slot=${slot}]`)).not.toBeNull();
	});
});
