import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { PanelCard } from "./panel-card";

describe("PanelCard", () => {
	test("renders children inside the panel-card slot", () => {
		render(
			<PanelCard>
				<h1>Sign in</h1>
			</PanelCard>,
		);
		const heading = screen.getByRole("heading", { name: "Sign in" });
		expect(heading.parentElement).toHaveAttribute("data-slot", "panel-card");
	});

	test("merges className and forwards div props", () => {
		render(
			<PanelCard className="mine" role="dialog" aria-label="Welcome" id="p">
				x
			</PanelCard>,
		);
		const panel = screen.getByRole("dialog", { name: "Welcome" });
		expect(panel).toHaveClass("mine");
		expect(panel).toHaveAttribute("id", "p");
		expect(panel.className.split(/\s+/).length).toBeGreaterThan(1);
	});
});
