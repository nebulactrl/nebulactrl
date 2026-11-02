import { describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Stepper } from "#/components/ui/stepper";

function Harness() {
	const [value, setValue] = useState(1);
	return (
		<Stepper
			aria-label="Replicas"
			value={value}
			min={0}
			max={2}
			onValueChange={setValue}
		/>
	);
}

describe("Stepper", () => {
	it("shows the value in a polite live output inside a named group", () => {
		render(
			<Stepper aria-label="Replicas" value={3} onValueChange={() => {}} />,
		);
		expect(screen.getByRole("group", { name: "Replicas" })).toBeInTheDocument();
		const out = screen.getByRole("status");
		expect(out).toHaveTextContent("3");
		expect(out).toHaveAttribute("aria-live", "polite");
	});

	it("increments and decrements by one, emitting the next number", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<Stepper aria-label="Replicas" value={3} onValueChange={onValueChange} />,
		);
		await user.click(screen.getByRole("button", { name: "Increase replicas" }));
		await user.click(screen.getByRole("button", { name: "Decrease replicas" }));
		expect(onValueChange.mock.calls.map((c) => c[0])).toEqual([4, 2]);
	});

	it("honours a custom step", async () => {
		const onValueChange = mock();
		render(
			<Stepper
				aria-label="Memory"
				value={10}
				step={5}
				onValueChange={onValueChange}
			/>,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Increase memory" }));
		expect(onValueChange).toHaveBeenCalledWith(15);
	});

	it("disables decrease at min and increase at max", () => {
		const { rerender } = render(
			<Stepper aria-label="Replicas" value={0} onValueChange={() => {}} />,
		);
		expect(
			screen.getByRole("button", { name: "Decrease replicas" }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Increase replicas" }),
		).toBeEnabled();
		rerender(
			<Stepper
				aria-label="Replicas"
				value={5}
				min={1}
				max={5}
				onValueChange={() => {}}
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Increase replicas" }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Decrease replicas" }),
		).toBeEnabled();
	});

	it("never goes below min even when a step overshoots", async () => {
		const onValueChange = mock();
		render(
			<Stepper
				aria-label="Replicas"
				value={1}
				min={0}
				step={5}
				onValueChange={onValueChange}
			/>,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Decrease replicas" }));
		expect(onValueChange).toHaveBeenCalledWith(0);
	});

	it("never goes above max even when a step overshoots", async () => {
		const onValueChange = mock();
		render(
			<Stepper
				aria-label="Replicas"
				value={8}
				max={10}
				step={5}
				onValueChange={onValueChange}
			/>,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Increase replicas" }));
		expect(onValueChange).toHaveBeenCalledWith(10);
	});

	it("disables both buttons and flags the group when disabled", async () => {
		const onValueChange = mock();
		render(
			<Stepper
				aria-label="Replicas"
				value={3}
				disabled
				onValueChange={onValueChange}
			/>,
		);
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: "Increase replicas" }));
		await user.click(screen.getByRole("button", { name: "Decrease replicas" }));
		expect(onValueChange).not.toHaveBeenCalled();
		expect(screen.getByRole("group")).toHaveAttribute("aria-disabled", "true");
	});

	it("does not flag the group when enabled", () => {
		render(
			<Stepper aria-label="Replicas" value={3} onValueChange={() => {}} />,
		);
		expect(screen.getByRole("group")).not.toHaveAttribute("aria-disabled");
	});

	it("points aria-describedby at the explanation", () => {
		render(
			<>
				<p id="why">Scaling is paused</p>
				<Stepper
					aria-label="Replicas"
					aria-describedby="why"
					value={3}
					onValueChange={() => {}}
				/>
			</>,
		);
		expect(
			screen.getByRole("group", { name: "Replicas" }),
		).toHaveAccessibleDescription("Scaling is paused");
	});

	it("is controlled: shows the parent's value until it changes", async () => {
		render(
			<Stepper aria-label="Replicas" value={3} onValueChange={() => {}} />,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Increase replicas" }));
		expect(screen.getByRole("status")).toHaveTextContent("3");
	});

	it("walks to the boundary with repeated clicks under parent state", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.click(screen.getByRole("button", { name: "Increase replicas" }));
		expect(screen.getByRole("status")).toHaveTextContent("2");
		expect(
			screen.getByRole("button", { name: "Increase replicas" }),
		).toBeDisabled();
		await user.click(screen.getByRole("button", { name: "Decrease replicas" }));
		await user.click(screen.getByRole("button", { name: "Decrease replicas" }));
		expect(screen.getByRole("status")).toHaveTextContent("0");
		expect(
			screen.getByRole("button", { name: "Decrease replicas" }),
		).toBeDisabled();
	});

	it("activates from the keyboard", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<Stepper aria-label="Replicas" value={3} onValueChange={onValueChange} />,
		);
		await user.tab();
		expect(
			screen.getByRole("button", { name: "Decrease replicas" }),
		).toHaveFocus();
		await user.keyboard("{Enter}");
		await user.tab();
		await user.keyboard(" ");
		expect(onValueChange.mock.calls.map((c) => c[0])).toEqual([2, 4]);
	});

	it("shows a staged hint only when 'was' differs from the value", () => {
		const { rerender } = render(
			<Stepper
				aria-label="Replicas"
				value={3}
				was={3}
				onValueChange={() => {}}
			/>,
		);
		expect(screen.queryByText(/staged/)).not.toBeInTheDocument();
		rerender(
			<Stepper
				aria-label="Replicas"
				value={4}
				was={3}
				onValueChange={() => {}}
			/>,
		);
		expect(screen.getByText("staged · was 3")).toBeInTheDocument();
	});

	it("shows no hint when 'was' is omitted", () => {
		render(
			<Stepper aria-label="Replicas" value={4} onValueChange={() => {}} />,
		);
		expect(screen.queryByText(/staged/)).not.toBeInTheDocument();
	});

	it("shows a staged hint for 'was' of zero", () => {
		render(
			<Stepper
				aria-label="Replicas"
				value={1}
				was={0}
				onValueChange={() => {}}
			/>,
		);
		expect(screen.getByText("staged · was 0")).toBeInTheDocument();
	});

	it("builds the button names from a mixed-case label", () => {
		render(
			<Stepper aria-label="Web Replicas" value={1} onValueChange={() => {}} />,
		);
		expect(
			screen.getByRole("button", { name: "Increase web replicas" }),
		).toBeInTheDocument();
	});

	it("renders every size with a className on the wrapper", () => {
		const { container } = render(
			<>
				<Stepper
					aria-label="a"
					value={1}
					size="sm"
					className="w-a"
					onValueChange={() => {}}
				/>
				<Stepper
					aria-label="b"
					value={1}
					size="lg"
					className="w-b"
					onValueChange={() => {}}
				/>
			</>,
		);
		expect(container.querySelector(".w-a")).toHaveAttribute(
			"data-slot",
			"stepper",
		);
		expect(container.querySelector(".w-b")).toHaveAttribute(
			"data-slot",
			"stepper",
		);
		expect(screen.getByRole("button", { name: "Increase a" })).toHaveClass(
			"size-[26px]",
		);
		expect(screen.getByRole("button", { name: "Increase b" })).toHaveClass(
			"size-8",
		);
	});
});
