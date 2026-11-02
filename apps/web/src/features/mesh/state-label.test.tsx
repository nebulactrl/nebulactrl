import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { StateLabel } from "./state-label";

describe("StateLabel", () => {
	test("says the state in words", () => {
		render(<StateLabel chip={{ kind: "failed", label: "Needs attention" }} />);
		expect(screen.getByText("Needs attention")).toBeInTheDocument();
	});

	test("passes a class name through to the label", () => {
		render(
			<StateLabel
				chip={{ kind: "live", label: "Online" }}
				className="custom-marker"
			/>,
		);
		expect(screen.getByText("Online")).toHaveClass("custom-marker");
	});
});
