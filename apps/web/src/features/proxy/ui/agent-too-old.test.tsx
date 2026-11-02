import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { agentUpgradeCommand } from "../lib/errors";
import { AgentTooOldBanner } from "./agent-too-old";

describe("AgentTooOldBanner", () => {
	test("names the server and shows the installer command for this control plane", () => {
		render(<AgentTooOldBanner serverName="edge-1" />);
		expect(
			screen.getByText("The agent on edge-1 is too old for this"),
		).toBeInTheDocument();
		const command = agentUpgradeCommand(window.location.origin);
		expect(screen.getByText(command)).toBeInTheDocument();
		expect(command).toContain(`--url ${window.location.origin}`);
	});

	test("without a server name the title stays generic", () => {
		render(<AgentTooOldBanner />);
		expect(
			screen.getByText("The agent is too old for this"),
		).toBeInTheDocument();
		expect(screen.getByTestId("agent-too-old")).toBeInTheDocument();
	});
});
