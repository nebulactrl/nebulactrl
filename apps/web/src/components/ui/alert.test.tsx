import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import {
	Alert,
	AlertAction,
	AlertDescription,
	AlertTitle,
} from "#/components/ui/alert";

describe("Alert", () => {
	it("is an alert region holding its title, description and action", () => {
		render(
			<Alert>
				<AlertTitle>Build failed</AlertTitle>
				<AlertDescription>Exit code 1</AlertDescription>
				<AlertAction>
					<button type="button">Retry</button>
				</AlertAction>
			</Alert>,
		);
		const alert = screen.getByRole("alert");
		expect(alert).toHaveTextContent("Build failed");
		expect(alert).toHaveTextContent("Exit code 1");
		expect(alert).toContainElement(
			screen.getByRole("button", { name: "Retry" }),
		);
	});

	it("shows no tone and no icon by default", () => {
		render(<Alert>plain</Alert>);
		const alert = screen.getByRole("alert");
		expect(alert).not.toHaveAttribute("data-tone");
		expect(alert.querySelector("svg")).toBeNull();
	});

	it.each([
		["warning", "warning"],
		["destructive", "danger"],
		["success", "success"],
		["info", "info"],
	] as const)(
		"the %s variant carries the %s tone and a decorative icon",
		(variant, tone) => {
			render(<Alert variant={variant}>body</Alert>);
			const alert = screen.getByRole("alert");
			expect(alert).toHaveAttribute("data-tone", tone);
			const icon = alert.querySelector("svg");
			expect(icon).not.toBeNull();
			expect(icon).toHaveAttribute("aria-hidden", "true");
			// The icon comes first, before the caller's content.
			expect(alert.firstElementChild).toBe(icon);
		},
	);

	it("passes extra props and className through to the root", () => {
		render(
			<Alert id="a1" className="custom" aria-live="polite">
				x
			</Alert>,
		);
		const alert = screen.getByRole("alert");
		expect(alert).toHaveAttribute("id", "a1");
		expect(alert).toHaveAttribute("aria-live", "polite");
		expect(alert).toHaveClass("custom");
	});

	it("title, description and action expose their slots and accept className", () => {
		render(
			<Alert>
				<AlertTitle className="t">T</AlertTitle>
				<AlertDescription className="d">D</AlertDescription>
				<AlertAction className="a">A</AlertAction>
			</Alert>,
		);
		expect(screen.getByText("T")).toHaveAttribute("data-slot", "alert-title");
		expect(screen.getByText("T")).toHaveClass("t");
		expect(screen.getByText("D")).toHaveAttribute(
			"data-slot",
			"alert-description",
		);
		expect(screen.getByText("D")).toHaveClass("d");
		expect(screen.getByText("A")).toHaveAttribute("data-slot", "alert-action");
		expect(screen.getByText("A")).toHaveClass("a");
	});
});
