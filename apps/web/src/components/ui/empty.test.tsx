import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import {
	Empty,
	EmptyContent,
	EmptyDescription,
	EmptyHeader,
	EmptyTitle,
} from "#/components/ui/empty";

describe("Empty", () => {
	it("composes title, description and content actions in their slots", () => {
		render(
			<Empty data-testid="root">
				<EmptyHeader>
					<EmptyTitle>No projects yet</EmptyTitle>
					<EmptyDescription>Create one to get started.</EmptyDescription>
				</EmptyHeader>
				<EmptyContent>
					<button type="button">New project</button>
				</EmptyContent>
			</Empty>,
		);
		const root = screen.getByTestId("root");
		expect(root).toHaveAttribute("data-slot", "empty");

		const header = screen.getByText("No projects yet").parentElement;
		expect(header).toHaveAttribute("data-slot", "empty-header");
		expect(header).toContainElement(
			screen.getByText("Create one to get started."),
		);
		expect(screen.getByText("No projects yet")).toHaveAttribute(
			"data-slot",
			"empty-title",
		);
		expect(screen.getByText("Create one to get started.")).toHaveAttribute(
			"data-slot",
			"empty-description",
		);
		const content = screen.getByRole("button", {
			name: "New project",
		}).parentElement;
		expect(content).toHaveAttribute("data-slot", "empty-content");
		expect(root).toContainElement(content);
	});

	it("merges className and forwards props on every part", () => {
		render(
			<Empty className="a" id="e">
				<EmptyHeader className="b" aria-label="header">
					<EmptyTitle className="c" title="t">
						T
					</EmptyTitle>
					<EmptyDescription className="d">D</EmptyDescription>
				</EmptyHeader>
				<EmptyContent className="e2" role="group">
					C
				</EmptyContent>
			</Empty>,
		);
		expect(document.getElementById("e")).toHaveClass("a");
		expect(screen.getByLabelText("header")).toHaveClass("b");
		expect(screen.getByTitle("t")).toHaveClass("c");
		expect(screen.getByText("D")).toHaveClass("d");
		expect(screen.getByRole("group")).toHaveClass("e2");
	});
});
