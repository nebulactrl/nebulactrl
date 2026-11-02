import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import {
	type LampTone,
	StatusPill,
	statusKindOfTone,
} from "#/components/ui/lamp";

describe("statusKindOfTone", () => {
	it("maps every tone to its status icon kind", () => {
		const expected: Record<LampTone, string> = {
			live: "live",
			ok: "healthy",
			building: "building",
			deploying: "deploying",
			failed: "failed",
			bad: "failed",
			stopped: "stopped",
			offline: "offline",
			off: "offline",
			staged: "pending",
			warn: "warning",
			degraded: "degraded",
			info: "unknown",
		};
		for (const [tone, kind] of Object.entries(expected))
			expect(statusKindOfTone(tone as LampTone)).toBe(kind as never);
	});
});

describe("StatusPill", () => {
	it("shows the label and draws the icon for its tone", () => {
		render(<StatusPill tone="ok">Healthy now</StatusPill>);
		const pill = screen
			.getByText("Healthy now")
			.closest("[data-slot=status-pill]");
		expect(pill).toHaveAttribute("data-kind", "healthy");
		expect(pill).toHaveAttribute("data-tone", "ok");
		expect(pill?.querySelector("[data-slot=status-icon]")).toHaveAttribute(
			"data-kind",
			"healthy",
		);
	});

	it("lets an explicit kind win over the tone", () => {
		render(
			<StatusPill kind="building" tone="failed">
				Building
			</StatusPill>,
		);
		const pill = document.querySelector("[data-slot=status-pill]");
		expect(pill).toHaveAttribute("data-kind", "building");
		expect(pill).toHaveAttribute("data-tone", "failed");
	});

	it("omits data-tone when only a kind is given", () => {
		render(<StatusPill kind="live">Live</StatusPill>);
		const pill = document.querySelector("[data-slot=status-pill]");
		expect(pill).toHaveAttribute("data-kind", "live");
		expect(pill).not.toHaveAttribute("data-tone");
	});

	it("falls back to the off look when neither kind nor tone is given", () => {
		render(<StatusPill>Unknown</StatusPill>);
		expect(document.querySelector("[data-slot=status-pill]")).toHaveAttribute(
			"data-kind",
			"offline",
		);
	});

	it("does not announce the state twice: the icon is silent beside the label", () => {
		render(<StatusPill tone="live">Running</StatusPill>);
		const pill = document.querySelector(
			"[data-slot=status-pill]",
		) as HTMLElement;
		expect(pill.querySelector(".sr-only")).toBeNull();
		expect(pill).toHaveTextContent("Running");
	});

	it("merges a className", () => {
		render(
			<StatusPill tone="ok" className="ml-2">
				x
			</StatusPill>,
		);
		expect(document.querySelector("[data-slot=status-pill]")).toHaveClass(
			"ml-2",
		);
	});
});
