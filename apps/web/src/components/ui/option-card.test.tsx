import { describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { OptionCard, OptionCardGroup } from "#/components/ui/option-card";

function Sizes({
	onValueChange = () => {},
	value = "s",
}: {
	onValueChange?: (v: string) => void;
	value?: string;
}) {
	return (
		<OptionCardGroup
			aria-label="Size"
			value={value}
			onValueChange={onValueChange}
		>
			<OptionCard value="s" title="S" description="1 vCPU" />
			<OptionCard value="m" title="M" description="2 vCPU" />
			<OptionCard value="l" title="L" disabled />
		</OptionCardGroup>
	);
}

function Harness() {
	const [value, setValue] = useState("s");
	return <Sizes value={value} onValueChange={setValue} />;
}

describe("OptionCardGroup", () => {
	it("is a radiogroup named by aria-label with the value's card checked", () => {
		render(<Sizes value="m" />);
		expect(
			screen.getByRole("radiogroup", { name: "Size" }),
		).toBeInTheDocument();
		expect(screen.getByRole("radio", { name: /^S/ })).toHaveAttribute(
			"aria-checked",
			"false",
		);
		expect(screen.getByRole("radio", { name: /^M/ })).toHaveAttribute(
			"aria-checked",
			"true",
		);
	});

	it("calls onValueChange with the clicked card's value", async () => {
		const onValueChange = mock();
		render(<Sizes onValueChange={onValueChange} />);
		await userEvent.setup().click(screen.getByRole("radio", { name: /^M/ }));
		expect(onValueChange).toHaveBeenCalledTimes(1);
		expect(onValueChange.mock.calls[0]?.[0]).toBe("m");
	});

	it("does not re-emit when the checked card is clicked again", async () => {
		const onValueChange = mock();
		render(<Sizes onValueChange={onValueChange} value="s" />);
		await userEvent.setup().click(screen.getByRole("radio", { name: /^S/ }));
		expect(onValueChange).not.toHaveBeenCalled();
	});

	it("is controlled: stays on the parent's value until updated", async () => {
		render(<Sizes value="s" />);
		await userEvent.setup().click(screen.getByRole("radio", { name: /^M/ }));
		expect(screen.getByRole("radio", { name: /^S/ })).toHaveAttribute(
			"aria-checked",
			"true",
		);
		expect(screen.getByRole("radio", { name: /^M/ })).toHaveAttribute(
			"aria-checked",
			"false",
		);
	});

	it("follows state held by the parent", async () => {
		render(<Harness />);
		await userEvent.setup().click(screen.getByRole("radio", { name: /^M/ }));
		expect(screen.getByRole("radio", { name: /^M/ })).toHaveAttribute(
			"aria-checked",
			"true",
		);
		expect(screen.getByRole("radio", { name: /^S/ })).toHaveAttribute(
			"aria-checked",
			"false",
		);
	});

	it("moves the choice with the arrow keys, skipping a disabled card", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(<Sizes onValueChange={onValueChange} value="s" />);
		await user.tab();
		expect(screen.getByRole("radio", { name: /^S/ })).toHaveFocus();
		await user.keyboard("{ArrowRight}");
		expect(onValueChange.mock.calls.at(-1)?.[0]).toBe("m");
		onValueChange.mockClear();
		await user.keyboard("{ArrowRight}");
		// L is disabled: it is never chosen.
		expect(onValueChange.mock.calls.some((c) => c[0] === "l")).toBe(false);
	});

	it("ignores a disabled card", async () => {
		const onValueChange = mock();
		render(<Sizes onValueChange={onValueChange} />);
		const l = screen.getByRole("radio", { name: "L" });
		expect(l).toHaveAttribute("aria-disabled", "true");
		await userEvent.setup().click(l);
		expect(onValueChange).not.toHaveBeenCalled();
	});

	it("disables every card when the group is disabled", async () => {
		const onValueChange = mock();
		render(
			<OptionCardGroup
				aria-label="Size"
				value="s"
				onValueChange={onValueChange}
				disabled
			>
				<OptionCard value="s" title="S" />
				<OptionCard value="m" title="M" />
			</OptionCardGroup>,
		);
		await userEvent.setup().click(screen.getByRole("radio", { name: "M" }));
		expect(onValueChange).not.toHaveBeenCalled();
		for (const radio of screen.getAllByRole("radio"))
			expect(radio).toHaveAttribute("aria-disabled", "true");
	});

	it("passes aria-labelledby and aria-describedby to the group", () => {
		render(
			<>
				<span id="lbl">Instance size</span>
				<span id="why">Locked while deploying</span>
				<OptionCardGroup
					aria-labelledby="lbl"
					aria-describedby="why"
					value="s"
					onValueChange={() => {}}
				>
					<OptionCard value="s" title="S" />
				</OptionCardGroup>
			</>,
		);
		const group = screen.getByRole("radiogroup", { name: "Instance size" });
		expect(group).toHaveAccessibleDescription("Locked while deploying");
	});
});

describe("OptionCard", () => {
	it("shows title and description, and names the radio with both", () => {
		render(
			<OptionCardGroup aria-label="g" value="m" onValueChange={() => {}}>
				<OptionCard value="m" title="M" description="2 vCPU · 4 GiB" />
			</OptionCardGroup>,
		);
		expect(screen.getByText("M")).toBeInTheDocument();
		expect(screen.getByText("2 vCPU · 4 GiB")).toBeInTheDocument();
		expect(
			screen.getByRole("radio", { name: "M 2 vCPU · 4 GiB" }),
		).toBeInTheDocument();
	});

	it("shows only the title when no description is given", () => {
		render(
			<OptionCardGroup aria-label="g" value="m" onValueChange={() => {}}>
				<OptionCard value="m" title="M" />
			</OptionCardGroup>,
		);
		const radio = screen.getByRole("radio", { name: "M" });
		expect(radio).toHaveTextContent(/^M$/);
	});

	it("shows the check only on the chosen card", () => {
		render(
			<OptionCardGroup aria-label="g" value="a" onValueChange={() => {}}>
				<OptionCard value="a" title="A" />
				<OptionCard value="b" title="B" />
			</OptionCardGroup>,
		);
		const a = screen.getByRole("radio", { name: "A" });
		const b = screen.getByRole("radio", { name: "B" });
		expect(a.querySelector("svg")).not.toBeNull();
		expect(b.querySelector("svg")).toBeNull();
	});

	it("uses mono type for title and description when mono", () => {
		render(
			<OptionCardGroup aria-label="g" value="a" onValueChange={() => {}}>
				<OptionCard value="a" title="Title" description="Desc" mono />
				<OptionCard value="b" title="Plain" description="Words" />
			</OptionCardGroup>,
		);
		expect(screen.getByText("Title")).toHaveClass("font-mono");
		expect(screen.getByText("Desc")).toHaveClass("font-mono");
		expect(screen.getByText("Plain")).not.toHaveClass("font-mono");
		expect(screen.getByText("Words")).not.toHaveClass("font-mono");
	});

	it("tags itself and takes a className", () => {
		render(
			<OptionCardGroup
				aria-label="g"
				value="a"
				onValueChange={() => {}}
				className="group-x"
			>
				<OptionCard value="a" title="A" className="card-x" dashed />
			</OptionCardGroup>,
		);
		expect(screen.getByRole("radio")).toHaveAttribute(
			"data-slot",
			"option-card",
		);
		expect(screen.getByRole("radio")).toHaveClass("card-x", "border-dashed!");
		expect(screen.getByRole("radiogroup")).toHaveClass("group-x");
		expect(screen.getByRole("radiogroup")).toHaveAttribute(
			"data-slot",
			"option-card-group",
		);
	});
});
