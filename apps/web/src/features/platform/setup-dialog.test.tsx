import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PlatformServiceView } from "#/api/gen/types.gen";
import { type ApiRoutes, stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { PlatformSetupDialog } from "./setup-dialog";
import { platformListing, platformService, serverView } from "./test-fixtures";

function routes(
	overrides: ApiRoutes = {},
	servers = [serverView()],
): ApiRoutes {
	return {
		"GET /platform-services": { body: platformListing() },
		"GET /servers": { body: { items: servers } },
		...overrides,
	};
}

async function renderDialog(
	props: {
		defaults?: Parameters<typeof PlatformSetupDialog>[0]["defaults"];
	} = {},
) {
	const onOpenChange = mock((_open: boolean) => {});
	await renderWithProviders(
		<PlatformSetupDialog open onOpenChange={onOpenChange} {...props} />,
	);
	return onOpenChange;
}

function createCalls(api: ReturnType<typeof stubApi>) {
	return api.calls.filter((call) => call.method === "POST");
}

describe("PlatformSetupDialog loading the catalog", () => {
	test("shows a skeleton and Close while the catalog loads", async () => {
		stubApi(
			routes({
				"GET /platform-services": () =>
					new Promise<Response>(() => {
						/* never answers */
					}),
			}),
		);
		const onOpenChange = await renderDialog();
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByText("Set up Headscale for me"),
		).toBeInTheDocument();
		expect(within(dialog).queryByRole("button", { name: "Set up" })).toBeNull();
		await userEvent
			.setup()
			.click(within(dialog).getByRole("button", { name: "Close" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("a failed catalog says so, with the server's reason", async () => {
		stubApi(
			routes({
				"GET /platform-services": problem(500, {
					title: "Internal",
					detail: "The catalog is down.",
				}),
			}),
		);
		await renderDialog();
		expect(
			await screen.findByText("Headscale did not load"),
		).toBeInTheDocument();
		expect(screen.getByText("The catalog is down.")).toBeInTheDocument();
	});
});

describe("PlatformSetupDialog form", () => {
	test("describes what runs and sends only the server when nothing is typed", async () => {
		const api = stubApi(
			routes({
				"GET /platform-services": {
					body: platformListing(undefined, { notice: "Needs the internet." }),
				},
				"POST /platform-services": {
					status: 202,
					body: {
						service: platformService({
							status: "provisioning",
							step: "configure",
						}),
					},
				},
				"GET /platform-services/:id": {
					body: platformService({ status: "provisioning", step: "configure" }),
				},
			}),
		);
		await renderDialog();
		const dialog = await screen.findByRole("dialog");
		expect(
			await within(dialog).findByText("Set up Headscale for me"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("The control server of your private network."),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/Runs headscale\/headscale as a service/),
		).toBeInTheDocument();
		expect(
			within(dialog).getByTestId("derp-disclosure"),
		).not.toBeEmptyDOMElement();
		expect(within(dialog).getByText("Needs the internet.")).toBeInTheDocument();
		expect(within(dialog).getByText("Hostname (optional)")).toBeInTheDocument();

		// The only usable server is chosen for the person.
		const server = await within(dialog).findByRole("radio", {
			name: /hetzner-01/,
		});
		expect(server).toHaveAttribute("aria-checked", "true");
		const submit = within(dialog).getByRole("button", { name: "Set up" });
		expect(submit).toBeEnabled();

		await userEvent.setup().click(submit);
		await waitFor(() => expect(createCalls(api).length).toBe(1));
		expect(createCalls(api)[0]).toMatchObject({
			path: "/platform-services",
			body: { kind: "headscale", serverId: "srv-1" },
		});
		expect(createCalls(api)[0]?.body).not.toHaveProperty("hostname");
		expect(createCalls(api)[0]?.body).not.toHaveProperty("replace");
		expect(api.unmatched).toEqual([]);
	});

	test("sends the chosen server, a lowercased hostname and the address override", async () => {
		const api = stubApi(
			routes(
				{
					"POST /platform-services": {
						status: 202,
						body: {
							service: platformService({
								status: "provisioning",
								step: "deploy",
							}),
						},
					},
					"GET /platform-services/:id": {
						body: platformService({ status: "provisioning", step: "deploy" }),
					},
				},
				[serverView(), serverView({ id: "srv-2", name: "ovh-02" })],
			),
		);
		const user = userEvent.setup();
		await renderDialog();
		await user.click(await screen.findByRole("radio", { name: /ovh-02/ }));
		await user.type(screen.getByLabelText(/Hostname/), "HS.Example.COM");
		await user.click(screen.getByText("Advanced"));
		await user.type(
			screen.getByLabelText("Address override"),
			"https://hs.example.com",
		);
		await user.click(screen.getByRole("button", { name: "Set up" }));
		await waitFor(() => expect(createCalls(api).length).toBe(1));
		expect(createCalls(api)[0]?.body).toEqual({
			kind: "headscale",
			serverId: "srv-2",
			hostname: "hs.example.com",
			publicUrl: "https://hs.example.com",
		});
	});

	test("prefills from the defaults the opener passed", async () => {
		stubApi(routes());
		await renderDialog({ defaults: { hostname: "hs.example.org" } });
		expect(await screen.findByLabelText(/Hostname/)).toHaveValue(
			"hs.example.org",
		);
	});

	test("an invalid hostname shows its problem and blocks the submit", async () => {
		const api = stubApi(routes());
		const user = userEvent.setup();
		await renderDialog();
		await screen.findByRole("radio", { name: /hetzner-01/ });
		await user.type(
			screen.getByLabelText(/Hostname/),
			"https://hs.example.com",
		);
		expect(
			screen.getByText(
				"Enter the hostname only, without https://, a port or a path.",
			),
		).toBeInTheDocument();
		expect(screen.getByLabelText(/Hostname/)).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		const submit = screen.getByRole("button", { name: "Set up" });
		expect(submit).toBeDisabled();
		expect(submit).toHaveAttribute(
			"title",
			"Enter the hostname only, without https://, a port or a path.",
		);
		expect(createCalls(api)).toEqual([]);
	});

	test("an address override must use the hostname", async () => {
		stubApi(routes());
		const user = userEvent.setup();
		await renderDialog();
		await screen.findByRole("radio", { name: /hetzner-01/ });
		await user.type(screen.getByLabelText(/Hostname/), "hs.example.com");
		await user.click(screen.getByText("Advanced"));
		await user.type(
			screen.getByLabelText("Address override"),
			"https://other.example.com",
		);
		expect(
			screen.getByText("The address must use the hostname hs.example.com."),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Set up" })).toBeDisabled();
	});

	test("a server without a domain makes the hostname required", async () => {
		stubApi(routes({}, [serverView({ baseDomain: null, publicIp: null })]));
		const user = userEvent.setup();
		await renderDialog();
		await screen.findByRole("radio", { name: /hetzner-01/ });
		expect(screen.getByText("Hostname")).toBeInTheDocument();
		expect(
			screen.getByText(/This server has no domain to make a name from/),
		).toBeInTheDocument();
		const submit = screen.getByRole("button", { name: "Set up" });
		expect(submit).toBeDisabled();
		expect(submit).toHaveAttribute(
			"title",
			"Enter a hostname: this server has no domain to generate one from.",
		);
		// Guidance, not an error, until something is typed.
		expect(screen.getByLabelText("Hostname")).not.toHaveAttribute(
			"aria-invalid",
			"true",
		);
		await user.type(screen.getByLabelText("Hostname"), "hs.example.com");
		expect(submit).toBeEnabled();
	});

	test("with no usable server it offers to add one and cannot submit", async () => {
		stubApi(routes({}, [serverView({ status: "offline" })]));
		const onOpenChange = await renderDialog();
		expect(await screen.findByText("No server is online")).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "Add a server" })).toHaveAttribute(
			"href",
			expect.stringContaining("/servers"),
		);
		expect(screen.getByText("Pick the server it runs on.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Set up" })).toBeDisabled();
		await userEvent
			.setup()
			.click(screen.getByRole("link", { name: "Add a server" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("Cancel closes the dialog", async () => {
		stubApi(routes());
		const onOpenChange = await renderDialog();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("Enter in the hostname field submits the form", async () => {
		const api = stubApi(
			routes({
				"POST /platform-services": {
					status: 202,
					body: {
						service: platformService({
							status: "provisioning",
							step: "configure",
						}),
					},
				},
				"GET /platform-services/:id": {
					body: platformService({ status: "provisioning", step: "configure" }),
				},
			}),
		);
		const user = userEvent.setup();
		await renderDialog();
		await screen.findByRole("radio", { name: /hetzner-01/ });
		await user.type(screen.getByLabelText(/Hostname/), "hs.example.com{Enter}");
		await waitFor(() => expect(createCalls(api).length).toBe(1));
	});
});

describe("PlatformSetupDialog refusals", () => {
	test("an existing Tailscale integration can be replaced", async () => {
		let attempt = 0;
		const api = stubApi(
			routes({
				"POST /platform-services": () => {
					attempt++;
					return attempt === 1
						? Response.json(
								{
									title: "Conflict",
									code: "integration_exists",
									detail:
										"A Tailscale integration named Office already exists.",
								},
								{ status: 409 },
							)
						: Response.json(
								{
									service: platformService({
										status: "provisioning",
										step: "configure",
									}),
								},
								{ status: 201 },
							);
				},
				"GET /platform-services/:id": {
					body: platformService({ status: "provisioning", step: "configure" }),
				},
			}),
		);
		const user = userEvent.setup();
		await renderDialog();
		await screen.findByRole("radio", { name: /hetzner-01/ });
		await user.click(screen.getByRole("button", { name: "Set up" }));
		expect(
			await screen.findByText("A Tailscale integration already exists"),
		).toBeInTheDocument();
		expect(
			screen.getByText("A Tailscale integration named Office already exists."),
		).toBeInTheDocument();
		expect(createCalls(api)[0]?.body).not.toHaveProperty("replace");

		await user.click(screen.getByRole("button", { name: "Replace it" }));
		await waitFor(() => expect(createCalls(api).length).toBe(2));
		expect(createCalls(api)[1]?.body).toEqual({
			kind: "headscale",
			serverId: "srv-1",
			replace: true,
		});
		expect(await screen.findByText("Setting up Headscale")).toBeInTheDocument();
	});

	test("Keep it dismisses the replace offer without another request", async () => {
		const api = stubApi(
			routes({
				"POST /platform-services": problem(409, {
					title: "Conflict",
					code: "integration_exists",
					detail: "Already there.",
				}),
			}),
		);
		const user = userEvent.setup();
		await renderDialog();
		await screen.findByRole("radio", { name: /hetzner-01/ });
		await user.click(screen.getByRole("button", { name: "Set up" }));
		await user.click(await screen.findByRole("button", { name: "Keep it" }));
		expect(
			screen.queryByText("A Tailscale integration already exists"),
		).toBeNull();
		expect(createCalls(api).length).toBe(1);
	});

	test("a missing base domain turns the hostname into a required field", async () => {
		stubApi(
			routes({
				"POST /platform-services": problem(422, {
					title: "Unprocessable",
					code: "no_base_domain",
					detail: "no base domain",
				}),
			}),
		);
		const user = userEvent.setup();
		await renderDialog();
		await screen.findByRole("radio", { name: /hetzner-01/ });
		expect(screen.getByText("Hostname (optional)")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Set up" }));
		expect(
			await screen.findByText(/neither a base domain nor a public address/),
		).toBeInTheDocument();
		expect(screen.getByText("Hostname")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Set up" })).toBeDisabled();
	});

	test("other refusals show the server's sentence with a next step", async () => {
		stubApi(
			routes({
				"POST /platform-services": problem(422, {
					title: "Unprocessable",
					code: "acme_email_required",
					detail: "Set an ACME email on the server.",
				}),
			}),
		);
		await renderDialog();
		await screen.findByRole("radio", { name: /hetzner-01/ });
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Set up" }));
		expect(
			await screen.findByText(
				"Set an ACME email on the server. Then try again.",
			),
		).toBeInTheDocument();
		// A refusal leaves the form in place to fix and resend.
		expect(screen.getByRole("button", { name: "Set up" })).toBeEnabled();
	});
});

describe("PlatformSetupDialog following the service", () => {
	// The dialog refetches the followed service once it is created.
	const created = (overrides: Partial<PlatformServiceView>) =>
		routes({
			"POST /platform-services": {
				status: 202,
				body: { service: platformService(overrides) },
			},
			"GET /platform-services/:id": { body: platformService(overrides) },
		});

	async function submit() {
		await screen.findByRole("radio", { name: /hetzner-01/ });
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Set up" }));
	}

	test("a running set-up lists its stages and offers Close", async () => {
		stubApi(
			created({
				status: "provisioning",
				step: "deploy",
				message: "Pulling the image.",
			}),
		);
		const onOpenChange = await renderDialog();
		await submit();
		expect(await screen.findByText("Setting up Headscale")).toBeInTheDocument();
		expect(
			screen.getByText(/it keeps going in the background/),
		).toBeInTheDocument();
		const section = screen.getByRole("region", { name: "Headscale" });
		expect(within(section).getByText("Pulling the image.")).toBeInTheDocument();
		const rows = within(section).getAllByRole("listitem");
		expect(rows.map((row) => row.getAttribute("aria-current"))).toEqual([
			null,
			"step",
			null,
			null,
		]);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Close" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("a ready service says where it was wired and offers Done", async () => {
		stubApi(
			created({
				status: "ready",
				step: "done",
				serverName: "hetzner-01",
				publicUrl: "https://hs.example.com",
				warnings: ["The API key expires soon."],
				wiring: {
					id: "w1",
					name: "Headscale tailnet",
					type: "tailscale_integration",
				},
			}),
		);
		await renderDialog();
		await submit();
		expect(await screen.findByText("Headscale is ready")).toBeInTheDocument();
		expect(
			screen.getByText("Everything NebulaCtrl set up is listed below."),
		).toBeInTheDocument();
		expect(screen.getByText("Headscale tailnet")).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Team settings → Integrations" }),
		).toHaveAttribute("href", "/settings/integrations");
		expect(screen.getByText("https://hs.example.com")).toBeInTheDocument();
		expect(screen.getByText("The API key expires soon.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Done" })).toBeInTheDocument();
	});

	test("a ready service without wiring says where it runs", async () => {
		stubApi(
			created({ status: "ready", step: "done", serverName: "hetzner-01" }),
		);
		await renderDialog();
		await submit();
		expect(
			await screen.findByText("It is running on hetzner-01."),
		).toBeInTheDocument();
	});

	test("a failed set-up shows its reason and Retry follows the retried service", async () => {
		let retried = false;
		const api = stubApi({
			...created({
				status: "failed",
				step: "await",
				message: "It never answered.",
			}),
			"POST /platform-services/:id/retry": () => {
				retried = true;
				return new Response(null, { status: 204 });
			},
			"GET /platform-services/:id": () =>
				Response.json(
					retried
						? platformService({
								status: "ready",
								step: "done",
								serverName: "hetzner-01",
							})
						: platformService({
								status: "failed",
								step: "await",
								message: "It never answered.",
							}),
				),
		});
		await renderDialog();
		await submit();
		expect(
			await screen.findByText("Headscale did not finish"),
		).toBeInTheDocument();
		expect(screen.getByText("Set-up failed")).toBeInTheDocument();
		expect(screen.getByText("It never answered.")).toBeInTheDocument();
		const section = screen.getByRole("region", { name: "Headscale" });
		expect(
			within(section)
				.getAllByRole("listitem")
				.map((row) => row.textContent),
		).toEqual([
			"Staging",
			"Deploying",
			"Waiting for it to answer",
			"Connecting",
		]);

		await userEvent
			.setup()
			.click(within(section).getByRole("button", { name: "Retry" }));
		// The retry restarts the stages: the dialog follows the service until it is ready.
		expect(await screen.findByText("Headscale is ready")).toBeInTheDocument();
		expect(
			api.calls.some(
				(call) =>
					call.method === "POST" &&
					call.path === "/platform-services/ps-1/retry",
			),
		).toBe(true);
	});

	test("a refused retry toasts the reason and keeps the failure", async () => {
		stubApi({
			...created({ status: "failed", step: "await", message: "" }),
			"POST /platform-services/:id/retry": problem(409, {
				title: "Conflict",
				detail: "Retry is not allowed now.",
			}),
		});
		await renderDialog();
		await submit();
		expect(
			await screen.findByText("It stopped without saying why."),
		).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Retry" }));
		expect(await screen.findByText("Could not retry")).toBeInTheDocument();
		expect(screen.getByText("Retry is not allowed now.")).toBeInTheDocument();
		expect(screen.getByText("Headscale did not finish")).toBeInTheDocument();
	});
});
