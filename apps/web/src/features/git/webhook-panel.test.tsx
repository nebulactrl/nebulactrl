import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { WebhookPanel } from "./webhook-panel";

describe("WebhookPanel", () => {
	test("shows the URL to paste into Bitbucket", () => {
		render(<WebhookPanel url="https://ctrl.example.com/hooks/bitbucket/abc" />);
		expect(screen.getByText("Webhook URL")).toBeInTheDocument();
		expect(
			screen.getByText("https://ctrl.example.com/hooks/bitbucket/abc"),
		).toBeInTheDocument();
		expect(screen.getByText(/Repository push/)).toBeInTheDocument();
	});

	test("without a secret there is no Secret line and no 'shown once' badge", () => {
		render(<WebhookPanel url="https://x.test/h" />);
		expect(screen.queryByText("Secret")).not.toBeInTheDocument();
		expect(screen.queryByText("Shown once")).not.toBeInTheDocument();
	});

	test("a fresh secret is shown with the shown-once warning", () => {
		render(<WebhookPanel url="https://x.test/h" secret="whsec_123" />);
		expect(screen.getByText("Shown once")).toBeInTheDocument();
		expect(screen.getByText("Secret")).toBeInTheDocument();
		expect(screen.getByText("whsec_123")).toBeInTheDocument();
		expect(screen.getAllByRole("button")).toHaveLength(2);
	});

	test("an unknown public address explains how to fix it instead of an empty URL", () => {
		render(<WebhookPanel url="" />);
		expect(screen.queryByText("Webhook URL")).not.toBeInTheDocument();
		expect(screen.getByText(/NEBULACTRL_PUBLIC_URL/)).toBeInTheDocument();
	});

	test("says watch paths cannot filter Bitbucket pushes", () => {
		render(<WebhookPanel url="https://x.test/h" />);
		expect(
			screen.getByText(/watch paths cannot filter it/),
		).toBeInTheDocument();
	});
});
