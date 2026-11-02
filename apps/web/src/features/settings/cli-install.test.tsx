import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { CliInstall } from "./cli-install";

const system = (publicUrl: string) => ({
	mcpEnabled: false,
	mcpLocked: false,
	mcpUrl: "",
	publicUrl,
	version: "0.1.0",
});

describe("CliInstall", () => {
	test("names the configured public URL, without trailing slashes", async () => {
		stubApi({ "GET /system": { body: system("https://ctrl.example.com//") } });
		await renderWithProviders(<CliInstall />);

		expect(
			await screen.findByText(
				"curl -fsSL https://ctrl.example.com/cli.sh | sh",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"nebulactrl login --url https://ctrl.example.com --token <token>",
			),
		).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "amd64" })).toHaveAttribute(
			"href",
			"https://ctrl.example.com/download/cli/windows/amd64",
		);
		expect(screen.getByRole("link", { name: "arm64" })).toHaveAttribute(
			"href",
			"https://ctrl.example.com/download/cli/windows/arm64",
		);
	});

	test("falls back to the browser's origin when no public URL is configured", async () => {
		const api = stubApi({ "GET /system": { body: system("") } });
		await renderWithProviders(<CliInstall />);
		// It shows the fallback at once and keeps it once the (empty) URL arrives.
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(
			screen.getByText(`curl -fsSL ${window.location.origin}/cli.sh | sh`),
		).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "amd64" })).toHaveAttribute(
			"href",
			`${window.location.origin}/download/cli/windows/amd64`,
		);
	});

	test("falls back to the origin while the system call fails", async () => {
		const api = stubApi({
			"GET /system": {
				status: 500,
				body: { status: 500, title: "x", detail: "boom", code: "internal" },
			},
		});
		await renderWithProviders(<CliInstall />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(
			screen.getByRole("heading", { name: "Install the CLI" }),
		).toBeInTheDocument();
		expect(
			screen.getByText(`curl -fsSL ${window.location.origin}/cli.sh | sh`),
		).toBeInTheDocument();
	});
});
