import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { SectionLabel } from "./section-label";

describe("SectionLabel", () => {
	test("renders an h3 heading by default", () => {
		render(<SectionLabel>Deployments</SectionLabel>);
		const heading = screen.getByRole("heading", {
			name: "Deployments",
			level: 3,
		});
		expect(heading).toHaveAttribute("data-slot", "section-label");
	});

	test.each([
		["h2", 2],
		["h3", 3],
		["h4", 4],
	] as const)("as=%s renders a level-%i heading", (as, level) => {
		render(<SectionLabel as={as}>Scaling</SectionLabel>);
		expect(
			screen.getByRole("heading", { name: "Scaling", level }),
		).toBeInTheDocument();
	});

	test("as=div renders no heading", () => {
		render(<SectionLabel as="div">Source</SectionLabel>);
		expect(screen.queryByRole("heading")).toBeNull();
		expect(screen.getByText("Source").tagName).toBe("DIV");
	});

	test("without aside, className lands on the heading itself", () => {
		render(<SectionLabel className="mt-2">Plain</SectionLabel>);
		expect(screen.getByRole("heading")).toHaveClass("mt-2");
	});

	test("with aside, the slot wraps the heading and the trailing content", () => {
		render(
			<SectionLabel aside={<button type="button">Add</button>} className="mt-2">
				Variables
			</SectionLabel>,
		);
		const heading = screen.getByRole("heading", { name: "Variables" });
		const wrapper = heading.parentElement as HTMLElement;
		expect(wrapper).toHaveAttribute("data-slot", "section-label");
		expect(wrapper).toHaveClass("mt-2");
		// The heading itself is not the slot when an aside is present.
		expect(heading).not.toHaveAttribute("data-slot");
		expect(screen.getByRole("button", { name: "Add" })).toBeInTheDocument();
		expect(wrapper).toContainElement(
			screen.getByRole("button", { name: "Add" }),
		);
	});

	test("heading level is honoured when an aside is present", () => {
		render(
			<SectionLabel as="h2" aside={<span>note</span>}>
				Title
			</SectionLabel>,
		);
		expect(
			screen.getByRole("heading", { level: 2, name: "Title" }),
		).toBeInTheDocument();
		expect(screen.getByText("note")).toBeInTheDocument();
	});
});
