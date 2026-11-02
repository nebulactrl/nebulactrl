import { describe, expect, mock, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ComponentFilter } from "./component-filter";

const COMPONENTS = [
	{ name: "db", index: 0 },
	{ name: "web", index: 1 },
];

describe("ComponentFilter", () => {
	test("lists All plus one chip per component, with All pressed when nothing is chosen", () => {
		render(
			<ComponentFilter
				components={COMPONENTS}
				value={undefined}
				onChange={() => {}}
			/>,
		);
		expect(
			screen.getByRole("list", { name: "Show logs of" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "All components" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(screen.getByRole("button", { name: "db" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
		expect(screen.getByRole("button", { name: "web" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	test("presses exactly the chosen component", () => {
		render(
			<ComponentFilter
				components={COMPONENTS}
				value="web"
				onChange={() => {}}
			/>,
		);
		expect(screen.getByRole("button", { name: "web" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(
			screen.getByRole("button", { name: "All components" }),
		).toHaveAttribute("aria-pressed", "false");
		expect(screen.getByRole("button", { name: "db" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	test("choosing a chip reports its name; All reports undefined", async () => {
		const user = userEvent.setup();
		const onChange = mock((_next: string | undefined) => {});
		render(
			<ComponentFilter
				components={COMPONENTS}
				value="web"
				onChange={onChange}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "db" }));
		expect(onChange).toHaveBeenLastCalledWith("db");
		await user.click(screen.getByRole("button", { name: "All components" }));
		expect(onChange).toHaveBeenLastCalledWith(undefined);
	});

	test("a stack with no components still offers All", () => {
		render(
			<ComponentFilter components={[]} value={undefined} onChange={() => {}} />,
		);
		expect(screen.getAllByRole("button")).toHaveLength(1);
	});
});
