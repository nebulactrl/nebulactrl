import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import { renderWithProviders } from "#/test/render";
import type { PlacementView } from "./model";
import { PlacementReadout } from "./placement-readout";

const placement = (over: Partial<PlacementView> = {}): PlacementView => ({
	location: "build_server",
	buildServerId: "srv-b",
	buildServerName: "build-01",
	registryId: "reg-1",
	registryHost: "ghcr.io/acme",
	platform: "linux/arm64",
	reason: "sticky",
	sticky: true,
	warnings: [],
	...over,
});

describe("PlacementReadout", () => {
	test("reads the build, the registry and the target in one sentence with the platform", async () => {
		await renderWithProviders(
			<PlacementReadout placement={placement()} targetName="web-02" />,
		);
		const readout = screen.getByTestId("plan-placement");
		expect(readout).toHaveTextContent(
			"Builds on build-01 (sticky), pushes to ghcr.io/acme, deploys to web-02 for linux/arm64.",
		);
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	test("leaves the platform out when the plan does not name one", async () => {
		await renderWithProviders(
			<PlacementReadout
				placement={placement({
					platform: "",
					location: "target",
					buildServerName: null,
				})}
				targetName="web-02"
			/>,
		);
		const readout = screen.getByTestId("plan-placement");
		expect(readout).toHaveTextContent("Builds and deploys on web-02.");
		expect(readout).not.toHaveTextContent(" for ");
	});

	test("shows an alert per warning, titled by its kind", async () => {
		await renderWithProviders(
			<PlacementReadout
				placement={placement({
					reason: "fallback: no build server online",
					warnings: [
						{
							code: "fallback",
							message: "No build server available; building on web-02",
						},
						{ code: "emulation", message: "Building linux/arm64 under QEMU" },
						{ code: "cold_cache", message: "First build on this server" },
						{ code: "weird", message: "Something unrelated" },
					],
				})}
				targetName="web-02"
			/>,
		);
		const titles = [
			[
				"Falls back to the app's server",
				"No build server available; building on web-02",
			],
			["Cross-architecture build", "Building linux/arm64 under QEMU"],
			["Cold build cache", "First build on this server"],
			["Heads up", "Something unrelated"],
		] as const;
		for (const [title, message] of titles) {
			expect(screen.getByText(title)).toBeInTheDocument();
			expect(screen.getByText(message)).toBeInTheDocument();
		}
		expect(screen.getByTestId("plan-placement")).toHaveTextContent(
			"(fallback)",
		);
	});
});
