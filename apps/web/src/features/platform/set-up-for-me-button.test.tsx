import { afterEach, describe, expect, test } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { platformServicesQuery } from "./queries";
import { SetUpHeadscaleButton } from "./set-up-for-me-button";
import { platformSetup, usePlatformSetupRequest } from "./store";
import { platformListing, platformService, serverView } from "./test-fixtures";

function Probe() {
	const request = usePlatformSetupRequest();
	return (
		<output aria-label="request">{request === null ? "closed" : "open"}</output>
	);
}

function Loaded() {
	const listing = useQuery(platformServicesQuery());
	return (
		<output aria-label="listing">{listing.data ? "loaded" : "loading"}</output>
	);
}

afterEach(() => platformSetup.close());

function stub(
	listing: unknown,
	servers: unknown[] | "pending" = [serverView()],
) {
	return stubApi({
		"GET /platform-services": { body: listing },
		"GET /servers":
			servers === "pending"
				? () => new Promise<Response>(() => {})
				: { body: { items: servers } },
	});
}

describe("SetUpHeadscaleButton", () => {
	test("opens the shared set-up dialog request", async () => {
		stub(platformListing());
		await renderWithProviders(
			<>
				<SetUpHeadscaleButton label="Set up Headscale for me" />
				<Probe />
			</>,
		);
		const button = await screen.findByRole("button", {
			name: "Set up Headscale for me",
		});
		expect(button).toBeEnabled();
		expect(button).toHaveAttribute(
			"title",
			"The control server of your private network.",
		);
		expect(screen.getByLabelText("request")).toHaveTextContent("closed");
		await userEvent.setup().click(button);
		expect(screen.getByLabelText("request")).toHaveTextContent("open");
	});

	test("is absent when Headscale already exists, whatever its state", async () => {
		stub(
			platformListing(platformService({ status: "failed", step: "deploy" })),
		);
		await renderWithProviders(
			<>
				<SetUpHeadscaleButton label="Set up" />
				<Loaded />
			</>,
		);
		await waitFor(() =>
			expect(screen.getByLabelText("listing")).toHaveTextContent("loaded"),
		);
		expect(screen.queryByRole("button", { name: "Set up" })).toBeNull();
	});

	test("is absent while the catalog has not loaded", async () => {
		stubApi({
			"GET /platform-services": () => new Promise<Response>(() => {}),
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(<SetUpHeadscaleButton label="Set up" />);
		expect(screen.queryByRole("button", { name: "Set up" })).toBeNull();
	});

	test("is disabled with the reason when no server is online", async () => {
		stub(platformListing(), [serverView({ status: "offline" })]);
		await renderWithProviders(
			<>
				<SetUpHeadscaleButton label="Set up" />
				<Probe />
			</>,
		);
		const button = await screen.findByRole("button", { name: "Set up" });
		await screen.findByTitle(
			"No server is online. Connect or start a server first.",
		);
		expect(button).toBeDisabled();
		await userEvent.setup().click(button);
		expect(screen.getByLabelText("request")).toHaveTextContent("closed");
	});

	test("does not flash disabled while the servers load", async () => {
		stub(platformListing(), "pending");
		await renderWithProviders(<SetUpHeadscaleButton label="Set up" />);
		expect(await screen.findByRole("button", { name: "Set up" })).toBeEnabled();
	});
});
