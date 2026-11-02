import { afterEach, beforeEach, describe, expect, it, mock } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Segmented } from "#/components/ui/segmented";
import { installLayoutEnv, type LayoutEnv } from "./layout-env.testkit";

const OPTIONS = [
	{ value: "1h", label: "1h" },
	{ value: "24h", label: "24h" },
	{ value: "7d", label: "7d" },
] as const;

function Harness() {
	const [value, setValue] = useState<string>("1h");
	return (
		<Segmented
			aria-label="Range"
			value={value}
			onValueChange={setValue}
			options={OPTIONS}
		/>
	);
}

describe("Segmented", () => {
	let env: LayoutEnv;
	beforeEach(() => {
		env = installLayoutEnv();
	});
	afterEach(() => env.restore());

	it("exposes a named group with the current option pressed", () => {
		render(
			<Segmented
				aria-label="Time range"
				value="24h"
				onValueChange={() => {}}
				options={OPTIONS}
			/>,
		);
		expect(
			screen.getByRole("group", { name: "Time range" }),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "1h" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
		expect(screen.getByRole("button", { name: "24h" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "7d" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	it("calls onValueChange with the clicked option's value", async () => {
		const onValueChange = mock();
		render(
			<Segmented
				aria-label="Time range"
				value="1h"
				onValueChange={onValueChange}
				options={OPTIONS}
			/>,
		);
		await userEvent.setup().click(screen.getByRole("button", { name: "7d" }));
		expect(onValueChange).toHaveBeenCalledTimes(1);
		expect(onValueChange).toHaveBeenCalledWith("7d");
	});

	it("keeps the pressed segment when it is clicked again (never empty)", async () => {
		const onValueChange = mock();
		render(
			<Segmented
				aria-label="Time range"
				value="1h"
				onValueChange={onValueChange}
				options={OPTIONS}
			/>,
		);
		await userEvent.setup().click(screen.getByRole("button", { name: "1h" }));
		expect(onValueChange).not.toHaveBeenCalled();
		expect(screen.getByRole("button", { name: "1h" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	it("is controlled: the pressed segment only moves when the parent changes value", async () => {
		const user = userEvent.setup();
		render(
			<Segmented
				aria-label="Time range"
				value="1h"
				onValueChange={() => {}}
				options={OPTIONS}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "24h" }));
		expect(screen.getByRole("button", { name: "1h" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "24h" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	it("follows parent state across selections", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.click(screen.getByRole("button", { name: "7d" }));
		expect(screen.getByRole("button", { name: "7d" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		await user.click(screen.getByRole("button", { name: "24h" }));
		expect(screen.getByRole("button", { name: "24h" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.getByRole("button", { name: "7d" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	it("selects from the keyboard with Space", async () => {
		const onValueChange = mock();
		const user = userEvent.setup();
		render(
			<Segmented
				aria-label="Range"
				value="1h"
				onValueChange={onValueChange}
				options={OPTIONS}
			/>,
		);
		await user.tab();
		// The group is one tab stop; arrows move between segments.
		await user.keyboard("{ArrowRight}");
		expect(screen.getByRole("button", { name: "24h" })).toHaveFocus();
		await user.keyboard(" ");
		expect(onValueChange).toHaveBeenCalledWith("24h");
	});

	it("does not select a disabled option", async () => {
		const onValueChange = mock();
		render(
			<Segmented
				aria-label="Role"
				value="a"
				onValueChange={onValueChange}
				options={[
					{ value: "a", label: "Owner" },
					{ value: "b", label: "Admin", disabled: true },
				]}
			/>,
		);
		const admin = screen.getByRole("button", { name: "Admin" });
		expect(admin).toBeDisabled();
		await userEvent.setup().click(admin);
		expect(onValueChange).not.toHaveBeenCalled();
	});

	it("uses the mono font by default and sans on request", () => {
		const { rerender } = render(
			<Segmented
				aria-label="x"
				value="1h"
				onValueChange={() => {}}
				options={OPTIONS}
			/>,
		);
		expect(screen.getByRole("button", { name: "1h" })).not.toHaveClass(
			"font-sans",
		);
		rerender(
			<Segmented
				aria-label="x"
				value="1h"
				font="sans"
				onValueChange={() => {}}
				options={OPTIONS}
			/>,
		);
		expect(screen.getByRole("button", { name: "1h" })).toHaveClass("font-sans");
	});

	it("maps density to the group's item size", () => {
		const { rerender } = render(
			<Segmented
				aria-label="x"
				value="1h"
				onValueChange={() => {}}
				options={OPTIONS}
			/>,
		);
		expect(screen.getByRole("group")).toHaveAttribute("data-size", "segmented");
		rerender(
			<Segmented
				aria-label="x"
				value="1h"
				density="regular"
				onValueChange={() => {}}
				options={OPTIONS}
			/>,
		);
		expect(screen.getByRole("group")).toHaveAttribute(
			"data-size",
			"segmented-lg",
		);
	});

	it("passes the indicator mode through", () => {
		render(
			<Segmented
				aria-label="x"
				value="1h"
				indicator="instant"
				onValueChange={() => {}}
				options={OPTIONS}
			/>,
		);
		expect(screen.getByRole("group")).toHaveAttribute(
			"data-indicator",
			"instant",
		);
	});

	it("renders ReactNode labels", () => {
		render(
			<Segmented
				aria-label="x"
				value="a"
				onValueChange={() => {}}
				options={[{ value: "a", label: <span data-testid="rich">All</span> }]}
			/>,
		);
		expect(screen.getByTestId("rich")).toBeInTheDocument();
	});

	it("fits: no scroll clipping and no fade mask when everything is visible", () => {
		render(
			<Segmented
				aria-label="x"
				value="1h"
				onValueChange={() => {}}
				options={OPTIONS}
				className="mine"
			/>,
		);
		const group = screen.getByRole("group");
		expect(group).toHaveClass("mine");
		expect(group).not.toHaveClass("overflow-x-auto");
		expect(group.style.maskImage).toBeFalsy();
	});

	it("overflowing: scrolls sideways and fades the continuing end", async () => {
		env.layout = (el) =>
			el.getAttribute("data-slot") === "toggle-group"
				? { clientWidth: 100, scrollWidth: 300 }
				: undefined;
		render(
			<Segmented
				aria-label="x"
				value="1h"
				onValueChange={() => {}}
				options={OPTIONS}
			/>,
		);
		const group = screen.getByRole("group");
		await act(async () => {
			group.dispatchEvent(new Event("scroll"));
		});
		expect(group).toHaveClass("overflow-x-auto");
		expect(group.style.maskImage).toContain("linear-gradient");
		expect(group.style.maskImage).toContain("transparent 100%");
	});

	it("brings the pressed segment into view on first paint and when the value changes", async () => {
		// The pressed segment sits at x=400 in a 100px viewport at x=0.
		env.layout = (el) => {
			if (el.getAttribute("data-slot") === "toggle-group")
				return { rectLeft: 0, rectWidth: 100, clientWidth: 100 };
			if (el.getAttribute("aria-pressed") === "true")
				return { rectLeft: 400, rectWidth: 50 };
			return undefined;
		};
		const { rerender } = render(
			<Segmented
				aria-label="x"
				value="1h"
				onValueChange={() => {}}
				options={OPTIONS}
			/>,
		);
		const first = env.scrollCalls.filter(
			(c) => c.el.getAttribute("data-slot") === "toggle-group",
		);
		expect(first).toHaveLength(1);
		// item right edge 450 + 32 margin - viewport right 100 = 382
		expect(first[0]?.options).toEqual({ left: 382 });
		rerender(
			<Segmented
				aria-label="x"
				value="24h"
				onValueChange={() => {}}
				options={OPTIONS}
			/>,
		);
		const second = env.scrollCalls.filter(
			(c) => c.el.getAttribute("data-slot") === "toggle-group",
		);
		expect(second).toHaveLength(2);
		// A rerender with the same value does not scroll again.
		rerender(
			<Segmented
				aria-label="x"
				value="24h"
				onValueChange={() => {}}
				options={OPTIONS}
				className="y"
			/>,
		);
		expect(
			env.scrollCalls.filter(
				(c) => c.el.getAttribute("data-slot") === "toggle-group",
			),
		).toHaveLength(2);
	});

	it("does not scroll when the pressed segment is already visible", () => {
		env.layout = (el) => {
			if (el.getAttribute("data-slot") === "toggle-group")
				return { rectLeft: 0, rectWidth: 300, clientWidth: 300 };
			if (el.getAttribute("aria-pressed") === "true")
				return { rectLeft: 100, rectWidth: 50 };
			return undefined;
		};
		render(
			<Segmented
				aria-label="x"
				value="1h"
				onValueChange={() => {}}
				options={OPTIONS}
			/>,
		);
		expect(
			env.scrollCalls.filter(
				(c) => c.el.getAttribute("data-slot") === "toggle-group",
			),
		).toHaveLength(0);
	});
});
