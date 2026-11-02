import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import type { TailnetView } from "#/api/gen/types.gen";
import { TailnetChip } from "./tailnet-node-chip";

function view(overrides: Partial<TailnetView> = {}): TailnetView {
	return {
		fqdn: "web-prod.tail1234.ts.net.",
		funnel: false,
		hidePublic: false,
		hostname: "web-prod",
		ips: ["100.64.0.5"],
		message: "",
		needsReprovision: false,
		observedAt: null,
		provider: "tailscale",
		state: "online",
		tcp: null,
		url: "https://web-prod.tail1234.ts.net",
		web: true,
		...overrides,
	};
}

describe("TailnetChip", () => {
	test("shows the hostname and states the node and its state in words", () => {
		const { container } = render(<TailnetChip tailnet={view()} />);
		const chip = container.querySelector('[data-slot="tailnet-chip"]');
		expect(chip).toHaveTextContent("web-prod");
		expect(chip).toHaveAttribute(
			"title",
			"Tailnet node web-prod.tail1234.ts.net · Online",
		);
		// The same sentence is available to screen readers.
		expect(
			screen.getByText("Tailnet node web-prod.tail1234.ts.net · Online", {
				selector: ".sr-only",
			}),
		).toBeInTheDocument();
	});

	test("marks a Funnel node as Public", () => {
		const { container } = render(
			<TailnetChip tailnet={view({ funnel: true })} />,
		);
		expect(screen.getByText("Public")).toBeInTheDocument();
		expect(
			container.querySelector('[data-slot="tailnet-chip"]'),
		).toHaveAttribute("title", expect.stringContaining("Public via Funnel"));
	});

	test("says Tailnet only in the tooltip when public domains are paused", () => {
		const { container } = render(
			<TailnetChip tailnet={view({ hidePublic: true })} />,
		);
		expect(screen.queryByText("Public")).not.toBeInTheDocument();
		expect(
			container.querySelector('[data-slot="tailnet-chip"]'),
		).toHaveAttribute(
			"title",
			expect.stringContaining("Tailnet only: public domains are paused"),
		);
	});

	test("shows neither mark for a plain node", () => {
		const { container } = render(<TailnetChip tailnet={view()} />);
		expect(screen.queryByText("Public")).not.toBeInTheDocument();
		expect(
			container
				.querySelector('[data-slot="tailnet-chip"]')
				?.getAttribute("title"),
		).not.toContain("Tailnet only");
	});

	test("carries the failure message of a node that needs attention", () => {
		const { container } = render(
			<TailnetChip
				tailnet={view({
					state: "needs_attention",
					message: "auth key expired",
				})}
			/>,
		);
		expect(
			container.querySelector('[data-slot="tailnet-chip"]'),
		).toHaveAttribute(
			"title",
			"Tailnet node web-prod.tail1234.ts.net · Needs attention · auth key expired",
		);
	});

	test("falls back to the hostname for the node name while the fqdn is unknown", () => {
		const { container } = render(
			<TailnetChip
				tailnet={view({ fqdn: "", state: "provisioning", url: "" })}
			/>,
		);
		expect(
			container.querySelector('[data-slot="tailnet-chip"]'),
		).toHaveAttribute("title", "Tailnet node web-prod · Provisioning");
	});
});
