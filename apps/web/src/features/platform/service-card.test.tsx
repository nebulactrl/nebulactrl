import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	PlatformKindView,
	PlatformServiceView,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { PlatformServiceCard } from "./service-card";
import { platformSetup, usePlatformSetupRequest } from "./store";
import { platformKind, platformService } from "./test-fixtures";

function SetupProbe() {
	const request = usePlatformSetupRequest();
	return (
		<output aria-label="setup request">{request ? "open" : "closed"}</output>
	);
}

async function renderCard(
	instance: PlatformServiceView | undefined,
	options: { usableServers?: number; kind?: Partial<PlatformKindView> } = {},
) {
	return renderWithProviders(
		<>
			<PlatformServiceCard
				catalog={platformKind({
					...(instance ? { instance } : {}),
					...options.kind,
				})}
				usableServers={options.usableServers ?? 1}
			/>
			<SetupProbe />
		</>,
	);
}

describe("PlatformServiceCard before set-up", () => {
	test("invites with the catalog words and opens the setup dialog", async () => {
		platformSetup.close();
		await renderCard(undefined, {
			kind: { notice: "Needs outbound internet." },
		});
		const card = screen.getByRole("article", { name: "Headscale" });
		expect(within(card).getByText("Not set up")).toBeInTheDocument();
		expect(
			within(card).getByText("headscale/headscale · BSD-3-Clause"),
		).toBeInTheDocument();
		expect(
			within(card).getByText("The control server of your private network."),
		).toBeInTheDocument();
		expect(
			within(card).getByText("About 100 MB of memory."),
		).toBeInTheDocument();
		expect(
			within(card).getByText("Needs outbound internet."),
		).toBeInTheDocument();
		expect(screen.queryByTestId("platform-reason")).toBeNull();

		expect(screen.getByLabelText("setup request")).toHaveTextContent("closed");
		await userEvent
			.setup()
			.click(within(card).getByRole("button", { name: "Set up for me" }));
		expect(screen.getByLabelText("setup request")).toHaveTextContent("open");
		platformSetup.close();
	});

	test("with no usable server the button is disabled and says why", async () => {
		platformSetup.close();
		await renderCard(undefined, { usableServers: 0 });
		const button = screen.getByRole("button", { name: "Set up for me" });
		expect(button).toBeDisabled();
		expect(screen.getByTestId("platform-reason")).toHaveTextContent(
			"No server is online. Connect or start a server first.",
		);
		await userEvent.setup().click(button);
		expect(screen.getByLabelText("setup request")).toHaveTextContent("closed");
	});
});

describe("PlatformServiceCard with a service", () => {
	test("provisioning shows the stages, the current one marked, and the message", async () => {
		await renderCard(
			platformService({
				status: "provisioning",
				step: "await",
				message: "Waiting for the proxy certificate.",
			}),
		);
		const card = screen.getByRole("article", { name: "Headscale" });
		const rows = within(card).getAllByRole("listitem");
		expect(rows.map((row) => row.textContent)).toEqual([
			"Staging",
			"Deploying",
			"Waiting for it to answer",
			"Connecting",
		]);
		expect(rows.map((row) => row.getAttribute("aria-current"))).toEqual([
			null,
			null,
			"step",
			null,
		]);
		expect(
			within(card).getByText("Waiting for the proxy certificate."),
		).toBeInTheDocument();
		expect(within(card).queryByRole("button", { name: "Retry" })).toBeNull();
		expect(
			within(card).queryByRole("button", { name: "Reconnect" }),
		).toBeNull();
		expect(
			within(card).getByRole("button", { name: "Remove" }),
		).toBeInTheDocument();
	});

	test("removing hides Remove and shows the progress sentence", async () => {
		await renderCard(platformService({ status: "removing", step: "done" }));
		const card = screen.getByRole("article", { name: "Headscale" });
		expect(
			within(card).getByText("Removing", { selector: "span" }),
		).toBeInTheDocument();
		expect(
			within(card).getByText("Removing it and what it was wired into."),
		).toBeInTheDocument();
		expect(within(card).queryByRole("button", { name: "Remove" })).toBeNull();
	});

	test("a failed set-up shows the reason and retries with POST /retry", async () => {
		const api = stubApi({
			"POST /platform-services/:id/retry": {
				status: 202,
				body: platformService({ status: "provisioning", step: "deploy" }),
			},
		});
		await renderCard(
			platformService({
				status: "failed",
				step: "deploy",
				message: "Image pull failed.",
			}),
		);
		const card = screen.getByRole("article", { name: "Headscale" });
		expect(within(card).getByText("Set-up failed")).toBeInTheDocument();
		expect(within(card).getByText("Image pull failed.")).toBeInTheDocument();
		// The stage list belongs to provisioning; a failure shows its alert instead.
		expect(within(card).queryByRole("list")).toBeNull();

		await userEvent
			.setup()
			.click(within(card).getByRole("button", { name: "Retry" }));
		await waitFor(() =>
			expect(api.calls).toEqual([
				expect.objectContaining({
					method: "POST",
					path: "/platform-services/ps-1/retry",
				}),
			]),
		);
		expect(api.unmatched).toEqual([]);
	});

	test("a failed set-up without a message still says something", async () => {
		await renderCard(
			platformService({ status: "failed", step: "wire", message: "" }),
		);
		expect(
			screen.getByText("It stopped without saying why."),
		).toBeInTheDocument();
	});

	test("a refused retry shows the server's reason in a toast", async () => {
		const api = stubApi({
			"POST /platform-services/:id/retry": problem(409, {
				title: "Conflict",
				detail: "A retry is already running.",
			}),
		});
		await renderCard(
			platformService({ status: "failed", step: "deploy", message: "x" }),
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Retry" }));
		expect(await screen.findByText("Could not retry")).toBeInTheDocument();
		expect(screen.getByText("A retry is already running.")).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("a ready service lists its facts, containers, wiring and links", async () => {
		await renderCard(
			platformService({
				status: "ready",
				publicUrl: "https://hs.example.com",
				hostname: "hs.example.com",
				serverName: "hetzner-01",
				details: { apiKeyExpires: "in 90 days", zeta_value: "z", empty: "" },
				projectId: "proj-platform",
				environmentSlug: "",
				services: [
					{ id: "c1", name: "headscale", slug: "headscale", state: "running" },
					{ id: "c2", name: "caddy", slug: "caddy", state: "missing" },
				],
				warnings: ["The API key expires soon."],
				wiring: {
					id: "w1",
					name: "Headscale tailnet",
					type: "tailscale_integration",
				},
				backup: {
					configured: false,
					hint: "Configure backups to protect the data.",
				},
			} as never),
		);
		const card = screen.getByRole("article", { name: "Headscale" });
		expect(
			within(card).getByText("Ready", { selector: "span" }),
		).toBeInTheDocument();
		expect(within(card).getByTitle("https://hs.example.com")).toHaveTextContent(
			"https://hs.example.com",
		);
		expect(within(card).getByText("hetzner-01")).toBeInTheDocument();
		expect(
			within(card).getByText("Reachable from your servers and tailnet only"),
		).toBeInTheDocument();
		// Known detail keys lead with their own label; unknown ones are sentence-cased; empty ones are dropped.
		const labels = within(card)
			.getAllByRole("term")
			.map((term) => term.textContent);
		expect(labels).toEqual([
			"Address",
			"Server",
			"Access",
			"API key expires",
			"Zeta value",
			"Wired into",
		]);
		expect(within(card).getByText("in 90 days")).toBeInTheDocument();
		const wired = within(card).getByRole("link", { name: "Integrations" });
		expect(wired).toHaveAttribute("href", "/settings/integrations");
		expect(within(card).getByText(": Headscale tailnet")).toBeInTheDocument();

		const containers = within(card).getByRole("list", { name: "Services" });
		expect(
			within(containers)
				.getAllByRole("listitem")
				.map((i) => i.textContent),
		).toEqual(["headscaleRunning", "caddyMissing"]);
		expect(
			within(card).getByText("The API key expires soon."),
		).toBeInTheDocument();
		expect(
			within(card).getByText("Configure backups to protect the data."),
		).toBeInTheDocument();
		// The canvas link defaults the environment to "platform".
		expect(
			within(card).getByRole("button", { name: "Open in canvas" }),
		).toHaveAttribute("href", "/p/proj-platform/platform");
		expect(within(card).queryByRole("button", { name: "Retry" })).toBeNull();
	});

	test("a ready service falls back to its hostname and hides what it lacks", async () => {
		await renderCard(
			platformService({
				status: "ready",
				publicUrl: "",
				hostname: "hs.internal",
			}),
		);
		const card = screen.getByRole("article", { name: "Headscale" });
		expect(within(card).getByTitle("hs.internal")).toBeInTheDocument();
		expect(
			within(card).queryByRole("button", { name: "Open in canvas" }),
		).toBeNull();
		expect(within(card).queryByText("Wired into")).toBeNull();
		expect(within(card).queryByRole("list", { name: "Services" })).toBeNull();
	});

	test("backup guidance stays hidden when backups are configured", async () => {
		await renderCard(
			platformService({
				backup: { configured: true, hint: "Backups run nightly." },
			} as never),
		);
		expect(screen.queryByText("Backups run nightly.")).toBeNull();
	});

	test("Reconnect posts to /reconnect and confirms in a toast", async () => {
		const api = stubApi({
			"POST /platform-services/:id/reconnect": {
				status: 202,
				body: platformService({ status: "ready" }),
			},
		});
		await renderCard(platformService({ status: "ready" }));
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Reconnect" }));
		expect(
			await screen.findByText("Reconnecting Headscale"),
		).toBeInTheDocument();
		expect(
			screen.getByText("The wiring runs again and is tested."),
		).toBeInTheDocument();
		expect(api.calls).toEqual([
			expect.objectContaining({
				method: "POST",
				path: "/platform-services/ps-1/reconnect",
			}),
		]);
	});

	test("a refused reconnect names the reason", async () => {
		stubApi({
			"POST /platform-services/:id/reconnect": problem(502, {
				title: "Bad Gateway",
				detail: "Headscale did not answer.",
			}),
		});
		await renderCard(platformService({ status: "ready" }));
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Reconnect" }));
		expect(await screen.findByText("Could not reconnect")).toBeInTheDocument();
		expect(screen.getByText("Headscale did not answer.")).toBeInTheDocument();
	});

	test("Remove opens the confirmation, locked until the kind is typed", async () => {
		const api = stubApi({
			"DELETE /platform-services/:id": { status: 204 },
		});
		const user = userEvent.setup();
		await renderCard(platformService({ status: "ready" }));
		await user.click(screen.getByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Remove Headscale?")).toBeInTheDocument();
		const confirm = within(dialog).getByRole("button", { name: "Remove" });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByRole("textbox"), "headscale");
		expect(confirm).toBeEnabled();
		await user.click(confirm);
		await waitFor(() => expect(api.calls.length).toBe(1));
		expect(api.calls[0]).toMatchObject({
			method: "DELETE",
			path: "/platform-services/ps-1",
		});
		expect(Object.fromEntries(api.calls[0]?.query ?? [])).toEqual({
			confirm: "headscale",
		});
	});
});
