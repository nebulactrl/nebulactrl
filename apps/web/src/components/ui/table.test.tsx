import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import {
	Table,
	TableActionsCell,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "./table";

function Sample({ stack = false }: { stack?: boolean }) {
	return (
		<Table stack={stack} aria-label="Servers">
			<TableHeader>
				<TableRow>
					<TableHead>Name</TableHead>
					<TableHead>Status</TableHead>
					<TableHead />
				</TableRow>
			</TableHeader>
			<TableBody>
				<TableRow>
					<TableCell label="Name" pin="start">
						alpha
					</TableCell>
					<TableCell label="Status" pin="end">
						online
					</TableCell>
					<TableActionsCell>
						<button type="button">Actions for alpha</button>
					</TableActionsCell>
				</TableRow>
				<TableRow aria-selected="true">
					<TableCell>beta</TableCell>
					<TableCell>offline</TableCell>
					<TableActionsCell />
				</TableRow>
			</TableBody>
		</Table>
	);
}

describe("Table", () => {
	test("exposes table, column header, row and cell semantics", () => {
		render(<Sample />);
		const table = screen.getByRole("table", { name: "Servers" });
		expect(
			within(table)
				.getAllByRole("columnheader")
				.map((h) => h.textContent),
		).toEqual(["Name", "Status", ""]);
		// header row + two body rows
		expect(within(table).getAllByRole("row")).toHaveLength(3);
		expect(
			within(table).getByRole("cell", { name: "alpha" }),
		).toBeInTheDocument();
		expect(
			within(table).getByRole("button", { name: "Actions for alpha" }),
		).toBeInTheDocument();
	});

	test("wraps the table in a scroll container carrying the slot markers", () => {
		const { container } = render(<Sample />);
		const wrapper = container.firstElementChild as HTMLElement;
		expect(wrapper).toHaveAttribute("data-slot", "table-container");
		expect(wrapper.firstElementChild).toBe(screen.getByRole("table"));
		expect(screen.getByRole("table")).toHaveAttribute("data-slot", "table");
	});

	test("data-stack is set only when stack is requested", () => {
		const { rerender } = render(<Sample />);
		expect(screen.getByRole("table")).not.toHaveAttribute("data-stack");
		rerender(<Sample stack />);
		expect(screen.getByRole("table")).toHaveAttribute("data-stack", "true");
		rerender(<Sample stack={false} />);
		expect(screen.getByRole("table")).not.toHaveAttribute("data-stack");
	});

	test("cells carry their stacked label and pin as data attributes", () => {
		render(<Sample stack />);
		const alpha = screen.getByRole("cell", { name: "alpha" });
		expect(alpha).toHaveAttribute("data-label", "Name");
		expect(alpha).toHaveAttribute("data-pin", "start");
		const online = screen.getByRole("cell", { name: "online" });
		expect(online).toHaveAttribute("data-label", "Status");
		expect(online).toHaveAttribute("data-pin", "end");
		const beta = screen.getByRole("cell", { name: "beta" });
		expect(beta).not.toHaveAttribute("data-label");
		expect(beta).not.toHaveAttribute("data-pin");
	});

	test("label and pin are not forwarded as raw props to the td", () => {
		render(<Sample />);
		const alpha = screen.getByRole("cell", { name: "alpha" });
		expect(alpha).not.toHaveAttribute("label");
		expect(alpha).not.toHaveAttribute("pin");
	});

	test("rows pass aria-selected and arbitrary props through", () => {
		render(
			<Table>
				<TableBody>
					<TableRow
						aria-selected="true"
						data-testid="sel"
						data-state="selected"
					>
						<TableCell>x</TableCell>
					</TableRow>
				</TableBody>
			</Table>,
		);
		const row = screen.getByTestId("sel");
		expect(row).toHaveAttribute("aria-selected", "true");
		expect(row).toHaveAttribute("data-state", "selected");
		expect(row).toHaveAttribute("data-slot", "table-row");
	});

	test("className is appended to, not replacing, the base styling", () => {
		render(
			<Table className="custom-table">
				<TableHeader className="custom-header">
					<TableRow className="custom-row">
						<TableHead className="custom-head">H</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody className="custom-body">
					<TableRow>
						<TableCell className="custom-cell">C</TableCell>
						<TableActionsCell className="custom-actions" />
					</TableRow>
				</TableBody>
			</Table>,
		);
		const expectBoth = (el: HTMLElement, custom: string) => {
			expect(el).toHaveClass(custom);
			expect(el.className.split(/\s+/).length).toBeGreaterThan(1);
		};
		expectBoth(screen.getByRole("table"), "custom-table");
		expectBoth(
			screen.getAllByRole("rowgroup")[0] as HTMLElement,
			"custom-header",
		);
		expectBoth(
			screen.getAllByRole("rowgroup")[1] as HTMLElement,
			"custom-body",
		);
		expectBoth(screen.getByRole("columnheader"), "custom-head");
		expectBoth(screen.getByRole("cell", { name: "C" }), "custom-cell");
		expect(
			document.querySelector("[data-slot=table-actions-cell]"),
		).toHaveClass("custom-actions");
	});
});
