import { describe, expect, it } from "bun:test";
import { screen } from "@testing-library/react";
import { renderWithProviders } from "#/test/render";
import { RESET_COMMAND } from "./messages";
import { ResetByHostHelp } from "./reset-help";

describe("ResetByHostHelp", () => {
	it("shows the caller's lead, the host command and a link to the lock-out docs", async () => {
		const { container } = await renderWithProviders(
			<p>
				<ResetByHostHelp lead="Email is off." />
			</p>,
		);
		expect(container).toHaveTextContent(
			"Email is off. Running the only admin account?",
		);
		expect(screen.getByText(RESET_COMMAND).tagName).toBe("CODE");
		const how = screen.getByRole("link", { name: "How" });
		expect(how.getAttribute("href")).toContain(
			"/docs/operations/troubleshooting",
		);
		expect(how.getAttribute("href")).toContain("#locked-out");
	});
});
