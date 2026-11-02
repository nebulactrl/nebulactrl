import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { PlatformSetupHost } from "./setup-host";
import { platformSetup, usePlatformSetupRequest } from "./store";
import { platformListing, serverView } from "./test-fixtures";

afterEach(() => platformSetup.close());

function stub() {
	return stubApi({
		"GET /platform-services": { body: platformListing() },
		"GET /servers": { body: { items: [serverView()] } },
	});
}

function Probe() {
	return (
		<output aria-label="request">
			{usePlatformSetupRequest() === null ? "closed" : "open"}
		</output>
	);
}

describe("PlatformSetupHost", () => {
	test("shows nothing until a button asks, then opens with the requested defaults", async () => {
		stub();
		await renderWithProviders(<PlatformSetupHost />);
		expect(screen.queryByRole("dialog")).toBeNull();
		act(() => platformSetup.open({ hostname: "hs.example.org" }));
		expect(await screen.findByRole("dialog")).toBeInTheDocument();
		expect(await screen.findByLabelText(/Hostname/)).toHaveValue(
			"hs.example.org",
		);
	});

	test("closing from the dialog clears the request", async () => {
		stub();
		await renderWithProviders(
			<>
				<PlatformSetupHost />
				<Probe />
			</>,
		);
		act(() => platformSetup.open());
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(screen.getByLabelText("request")).toHaveTextContent("closed");
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	test("each opening starts a fresh form", async () => {
		stub();
		const user = userEvent.setup();
		await renderWithProviders(<PlatformSetupHost />);
		act(() => platformSetup.open());
		await user.type(
			await screen.findByLabelText(/Hostname/),
			"typed.example.com",
		);
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		act(() => platformSetup.open());
		expect(await screen.findByLabelText(/Hostname/)).toHaveValue("");
	});
});
