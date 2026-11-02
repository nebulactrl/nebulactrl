import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { DrainChip } from "./drain-chip";

describe("DrainChip", () => {
	test.each([
		["draining", "Draining"],
		["drained", "Drained"],
		["failed", "Drain failed"],
	] as const)("%s reads as %s", (state, label) => {
		render(<DrainChip state={state} />);
		expect(screen.getByText(label)).toBeInTheDocument();
	});

	test("renders nothing for a server that is not drained", () => {
		const { container } = render(<DrainChip state="none" message="ignored" />);
		expect(container).toBeEmptyDOMElement();
	});

	test("carries the failure message as a tooltip title", () => {
		render(<DrainChip state="failed" message="move of api failed" />);
		expect(screen.getByTitle("move of api failed")).toHaveTextContent(
			"Drain failed",
		);
	});

	test("has no title without a message", () => {
		render(<DrainChip state="drained" />);
		expect(screen.getByText("Drained").closest("[title]")).toBeNull();
	});
});
