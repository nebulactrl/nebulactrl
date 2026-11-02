import { afterEach, describe, expect, test } from "bun:test";
import { act, screen } from "@testing-library/react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { closeFiles, closeShell, openFiles, openShell } from "./store";
import { TerminalHost } from "./terminal-host";
import { fileEntry, listing, serviceView, terminalInfo } from "./test-support";

afterEach(() => {
	act(() => {
		closeShell();
		closeFiles();
	});
});

function stubService() {
	return stubApi({
		"GET /services/:serviceId": { body: serviceView() },
		"GET /services/:serviceId/terminal": { body: terminalInfo() },
		"GET /services/:serviceId/files": { body: listing([fileEntry("a.txt")]) },
	});
}

describe("TerminalHost", () => {
	test("shows no panel until one is opened", async () => {
		const api = stubService();
		await renderWithProviders(<TerminalHost />);
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(api.calls).toEqual([]);
	});

	test("openShell shows the terminal panel and not the file browser", async () => {
		stubService();
		await renderWithProviders(<TerminalHost />);
		act(() => openShell("svc-1"));
		expect(await screen.findByText("Shell · api")).toBeInTheDocument();
		expect(screen.queryByText("Files · api")).toBeNull();
	});

	test("openFiles shows the file browser and not the terminal panel", async () => {
		stubService();
		await renderWithProviders(<TerminalHost />);
		act(() => openFiles("svc-1"));
		expect(await screen.findByText("Files · api")).toBeInTheDocument();
		expect(screen.queryByText("Shell · api")).toBeNull();
		expect(await screen.findByText("a.txt")).toBeInTheDocument();
	});
});
