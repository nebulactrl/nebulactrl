import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import { stepRows } from "./model";
import { StatusLine, StepList, WarningNote } from "./parts";

describe("StatusLine", () => {
	test.each([
		[{ status: "ready", step: "done" }, "Ready"],
		[{ status: "failed", step: "deploy" }, "Failed"],
		[{ status: "removing", step: "done" }, "Removing"],
		[{ status: "provisioning", step: "wire" }, "Connecting"],
	] as const)("%j reads %s", (view, words) => {
		render(<StatusLine view={view} />);
		expect(screen.getByText(words)).toBeInTheDocument();
	});
});

describe("StepList", () => {
	test("marks only the current stage and keeps the order", () => {
		render(
			<StepList rows={stepRows({ status: "provisioning", step: "deploy" })} />,
		);
		const items = within(screen.getByRole("list")).getAllByRole("listitem");
		expect(
			items.map((item) => [
				item.textContent,
				item.getAttribute("aria-current"),
			]),
		).toEqual([
			["Staging", null],
			["Deploying", "step"],
			["Waiting for it to answer", null],
			["Connecting", null],
		]);
	});

	test("a failed stage is not marked as the current step", () => {
		render(<StepList rows={stepRows({ status: "failed", step: "await" })} />);
		const items = screen.getAllByRole("listitem");
		expect(items.some((item) => item.hasAttribute("aria-current"))).toBe(false);
	});
});

describe("WarningNote", () => {
	test("shows an optional title above the text", () => {
		render(<WarningNote title="Heads up">Back up first.</WarningNote>);
		expect(screen.getByText("Heads up")).toBeInTheDocument();
		expect(screen.getByText("Back up first.")).toBeInTheDocument();
	});

	test("omits the title when there is none", () => {
		render(<WarningNote>Back up first.</WarningNote>);
		expect(screen.getByRole("alert")).toHaveTextContent("Back up first.");
	});
});
