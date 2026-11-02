import { afterEach, describe, expect, test } from "bun:test";
import type { QueryClient } from "@tanstack/react-query";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { getDatabasePublicPortQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import type { PublicPortView } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { onOpenStagedReview } from "#/features/deploy-button/open-review";
import { type ApiRoutes, stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import {
	NO_PUBLIC_ADDRESS,
	PLAINTEXT_NOTE,
	RECREATE_PROXY_NOTE,
} from "./public-port";
import { PublicPortSection } from "./public-port-section";

const PORT = "/services/:id/database/public-port";
const GET = `GET ${PORT}`;
const PUT = `PUT ${PORT}`;
const DELETE = `DELETE ${PORT}`;
const CONNECTION = `POST ${PORT}/connection`;

const service = {
	id: "svc-db",
	name: "Orders DB",
	slug: "orders-db",
	server: { id: "srv-a", name: "eu-1" },
} as unknown as Service;

function view(overrides: Partial<PublicPortView> = {}): PublicPortView {
	return {
		enabled: false,
		staged: false,
		deployed: false,
		newEntrypoint: true,
		openToAll: false,
		suggestedPort: 15432,
		serverId: "srv-a",
		serverName: "eu-1",
		tls: "passthrough",
		allowCidrs: [],
		denyCidrs: [],
		warnings: [],
		...overrides,
	};
}

/** A port that is on, applied and reachable. */
const live = (overrides: Partial<PublicPortView> = {}) =>
	view({
		enabled: true,
		deployed: true,
		newEntrypoint: false,
		listenPort: 15432,
		host: "203.0.113.7",
		routeId: "route-1",
		allowCidrs: ["203.0.113.0/24"],
		...overrides,
	});

const problem = (status: number, detail: string, code: string) => ({
	status,
	body: { status, title: "Error", detail, code },
});

/** What the control plane currently holds: a write changes what the next GET returns. */
let server: PublicPortView;

/** A reply that also moves the stored state, as a real write would. */
const becomes = (next: PublicPortView) => () => {
	server = next;
	return Response.json(next);
};

async function mount(
	current: PublicPortView,
	extra: ApiRoutes = {},
	queryClient?: QueryClient,
) {
	server = current;
	const api = stubApi({ [GET]: () => Response.json(server), ...extra });
	const result = await renderWithProviders(
		<PublicPortSection service={service} />,
		queryClient ? { queryClient } : {},
	);
	return { api, ...result };
}

const sw = () =>
	screen.findByRole("switch", { name: "Expose on a public port" });
const portInput = () => screen.getByLabelText("Port on the server");

async function addAllowed(
	user: ReturnType<typeof userEvent.setup>,
	network: string,
) {
	await user.click(screen.getByRole("button", { name: "Add network" }));
	await user.type(screen.getByLabelText("Allowed network 1"), network);
}

const puts = (api: { calls: { method: string; body: unknown }[] }) =>
	api.calls.filter((c) => c.method === "PUT");

let originalClipboard: PropertyDescriptor | undefined;
afterEach(() => {
	if (originalClipboard) {
		Object.defineProperty(navigator, "clipboard", originalClipboard);
		originalClipboard = undefined;
	}
});

describe("PublicPortSection loading", () => {
	test("keeps the heading while the view loads", async () => {
		stubApi({ [GET]: () => new Promise<Response>(() => {}) });
		await renderWithProviders(<PublicPortSection service={service} />);
		expect(screen.getByText("Public port")).toBeInTheDocument();
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
	});

	test("a failed load shows the reason and Retry recovers", async () => {
		let attempts = 0;
		stubApi({
			[GET]: () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{
								status: 500,
								title: "x",
								detail: "proxy store unavailable",
								code: "internal",
							},
							{ status: 500 },
						)
					: Response.json(view());
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<PublicPortSection service={service} />);
		expect(
			await screen.findByText("proxy store unavailable"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Retry" }));
		expect(await sw()).not.toBeChecked();
		expect(
			screen.queryByText("proxy store unavailable"),
		).not.toBeInTheDocument();
	});
});

describe("PublicPortSection exposing a port", () => {
	test("is off by default with nothing to configure", async () => {
		const { api } = await mount(view());
		const toggle = await sw();
		expect(toggle).not.toBeChecked();
		expect(
			screen.queryByLabelText("Port on the server"),
		).not.toBeInTheDocument();
		expect(screen.queryByText("Staged")).not.toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/services/svc-db/database/public-port");
	});

	test("switching on offers the suggested free port and warns that the proxy restarts and traffic is plaintext", async () => {
		const user = userEvent.setup();
		await mount(view());
		await user.click(await sw());
		expect(await sw()).toBeChecked();
		expect(portInput()).toHaveValue("15432");
		expect(screen.getByText(/15432 is free on eu-1\./)).toBeInTheDocument();
		expect(screen.getByText(RECREATE_PROXY_NOTE)).toBeInTheDocument();
		expect(screen.getByText(PLAINTEXT_NOTE)).toBeInTheDocument();
		// Nothing is allowed yet, so it cannot be exposed.
		expect(screen.getByRole("button", { name: "Expose port" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
	});

	test("the control plane's own warnings replace the generic plaintext note", async () => {
		const user = userEvent.setup();
		await mount(
			view({ warnings: ["Port 5432 is reachable from the internet."] }),
		);
		await user.click(await sw());
		expect(
			screen.getByText("Port 5432 is reachable from the internet."),
		).toBeInTheDocument();
		expect(screen.queryByText(PLAINTEXT_NOTE)).not.toBeInTheDocument();
	});

	test("Cancel puts the switch back off without any request", async () => {
		const user = userEvent.setup();
		const { api } = await mount(view());
		await user.click(await sw());
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(await sw()).not.toBeChecked();
		expect(
			screen.queryByLabelText("Port on the server"),
		).not.toBeInTheDocument();
		expect(api.calls.filter((c) => c.method !== "GET")).toEqual([]);
	});

	test("exposing sends the port and allow list, stages the route and offers the review", async () => {
		const reviews: unknown[] = [];
		const stop = onOpenStagedReview((options) => reviews.push(options));
		const user = userEvent.setup();
		const staged = view({
			enabled: true,
			staged: true,
			listenPort: 15432,
			routeId: "route-1",
			allowCidrs: ["203.0.113.0/24"],
			host: "203.0.113.7",
		});
		const { api } = await mount(view(), { [PUT]: becomes(staged) });
		await user.click(await sw());
		await addAllowed(user, "203.0.113.0/24");
		const expose = screen.getByRole("button", { name: "Expose port" });
		expect(expose).toBeEnabled();
		await user.click(expose);

		expect(
			(await screen.findAllByText("Public port staged")).length,
		).toBeGreaterThan(0);
		expect(puts(api)).toHaveLength(1);
		expect(api.calls.find((c) => c.method === "PUT")).toMatchObject({
			path: "/services/svc-db/database/public-port",
			body: { listenPort: 15432, allowCidrs: ["203.0.113.0/24"] },
		});
		// The staged view is shown at once: status, the review shortcut and the saved form.
		expect(await screen.findByText("Staged")).toBeInTheDocument();
		expect(screen.getByText("Deploy the route to apply.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
		await user.click(screen.getByRole("button", { name: "Review and deploy" }));
		expect(reviews).toEqual([{ serviceId: "route-1" }]);
		stop();
	});

	test("a custom port and a deny list are sent as typed", async () => {
		const user = userEvent.setup();
		const { api } = await mount(view(), { [PUT]: becomes(live()) });
		await user.click(await sw());
		await user.clear(portInput());
		await user.type(portInput(), "6543");
		await addAllowed(user, "198.51.100.0/24");
		await user.click(
			screen.getByRole("button", { name: "Add denied network" }),
		);
		await user.type(screen.getByLabelText("Denied network 1"), "198.51.100.7");
		await user.click(screen.getByRole("button", { name: "Expose port" }));
		await waitFor(() => expect(puts(api)).toHaveLength(1));
		expect(puts(api)[0]?.body).toEqual({
			listenPort: 6543,
			allowCidrs: ["198.51.100.0/24"],
			denyCidrs: ["198.51.100.7"],
		});
	});

	test("a port outside 1024-65535 is flagged and cannot be submitted", async () => {
		const user = userEvent.setup();
		const { api } = await mount(view());
		await user.click(await sw());
		await addAllowed(user, "203.0.113.0/24");
		await user.clear(portInput());
		await user.type(portInput(), "80");
		expect(
			screen.getAllByText("Use a port from 1024 to 65535.").length,
		).toBeGreaterThan(0);
		expect(portInput()).toHaveAttribute("aria-invalid", "true");
		expect(screen.getByRole("button", { name: "Expose port" })).toBeDisabled();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	test("an empty port field is not yelled at but still blocks submitting", async () => {
		const user = userEvent.setup();
		await mount(view());
		await user.click(await sw());
		await addAllowed(user, "203.0.113.0/24");
		await user.clear(portInput());
		expect(portInput()).toHaveAttribute("aria-invalid", "false");
		expect(screen.getByRole("button", { name: "Expose port" })).toBeDisabled();
	});

	test("an invalid network is flagged on its row and blocks submitting", async () => {
		const user = userEvent.setup();
		await mount(view());
		await user.click(await sw());
		await addAllowed(user, "not-a-network");
		expect(screen.getByLabelText("Allowed network 1")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByRole("button", { name: "Expose port" })).toBeDisabled();
		expect(
			screen.getByText("Fix the highlighted networks."),
		).toBeInTheDocument();
	});

	test("opening to every address needs the database's slug typed, and sends it", async () => {
		const user = userEvent.setup();
		const { api } = await mount(view(), {
			[PUT]: becomes(live({ allowCidrs: ["0.0.0.0/0"], openToAll: true })),
		});
		await user.click(await sw());
		await addAllowed(user, "0.0.0.0/0");
		expect(
			screen.getByText(/This lets every address connect to the database\./),
		).toBeInTheDocument();
		const confirm = screen.getByLabelText("Type the database slug to confirm");
		const expose = screen.getByRole("button", { name: "Expose port" });
		expect(expose).toBeDisabled();
		await user.type(confirm, "orders");
		expect(expose).toBeDisabled();
		await user.type(confirm, "-db");
		expect(expose).toBeEnabled();
		await user.click(expose);
		await waitFor(() => expect(puts(api)).toHaveLength(1));
		expect(puts(api)[0]?.body).toEqual({
			listenPort: 15432,
			allowCidrs: ["0.0.0.0/0"],
			confirmOpen: "orders-db",
		});
	});

	test("a restricted allow list asks for no slug", async () => {
		const user = userEvent.setup();
		await mount(view());
		await user.click(await sw());
		await addAllowed(user, "203.0.113.0/24");
		expect(
			screen.queryByLabelText("Type the database slug to confirm"),
		).not.toBeInTheDocument();
	});
});

describe("PublicPortSection refusals", () => {
	async function submitRefused(code: string, status: number, detail: string) {
		const user = userEvent.setup();
		const { api } = await mount(view(), {
			[PUT]: problem(status, detail, code),
		});
		await user.click(await sw());
		await addAllowed(user, "203.0.113.0/24");
		await user.click(screen.getByRole("button", { name: "Expose port" }));
		await waitFor(() => expect(puts(api)).toHaveLength(1));
		return user;
	}

	test("a port that is taken is reported under the port field", async () => {
		await submitRefused(
			"port_in_use",
			409,
			"Port 15432 is used by another route.",
		);
		expect(
			await screen.findByText("Port 15432 is used by another route."),
		).toBeInTheDocument();
		expect(portInput()).toHaveAttribute("aria-invalid", "true");
		// The form stays open and editable.
		expect(screen.getByRole("button", { name: "Expose port" })).toBeEnabled();
	});

	test("editing after a refusal clears it", async () => {
		const user = await submitRefused(
			"port_in_use",
			409,
			"Port 15432 is used by another route.",
		);
		await screen.findByText("Port 15432 is used by another route.");
		await user.type(portInput(), "1");
		expect(
			screen.queryByText("Port 15432 is used by another route."),
		).not.toBeInTheDocument();
	});

	test("an invalid listen port refused by the server shows the server's words", async () => {
		await submitRefused("invalid_listen_port", 422, "Port 15432 is reserved.");
		expect(
			await screen.findByText("Port 15432 is reserved."),
		).toBeInTheDocument();
	});

	test("a missing allow list is reported under the lists", async () => {
		await submitRefused("allowlist_required", 422, "raw");
		expect(
			(await screen.findAllByText("Add at least one allowed network.")).length,
		).toBeGreaterThan(0);
	});

	test("a missing open-confirmation is reported under the slug field", async () => {
		const user = userEvent.setup();
		const { api } = await mount(view(), {
			[PUT]: problem(422, "raw", "open_confirmation_required"),
		});
		await user.click(await sw());
		await addAllowed(user, "0.0.0.0/0");
		await user.type(
			screen.getByLabelText("Type the database slug to confirm"),
			"orders-db",
		);
		await user.click(screen.getByRole("button", { name: "Expose port" }));
		await waitFor(() => expect(puts(api)).toHaveLength(1));
		expect(
			(
				await screen.findAllByText(
					"Type the database's slug to open the port to every address.",
				)
			).length,
		).toBeGreaterThan(0);
	});

	test("a database without a server is told to place it on one", async () => {
		await submitRefused("no_server", 422, "raw");
		expect(
			await screen.findByText(
				"Place the database on a server before opening a port.",
			),
		).toBeInTheDocument();
	});

	test("an agent that is too old is told to update", async () => {
		await submitRefused("agent_too_old", 422, "raw");
		expect(
			await screen.findByText(
				/The agent on this server is too old to publish a port\. Update it/,
			),
		).toBeInTheDocument();
	});

	test("any other refusal shows the control plane's message", async () => {
		await submitRefused("internal", 500, "The proxy could not be reached.");
		expect(
			await screen.findByText("The proxy could not be reached."),
		).toBeInTheDocument();
	});
});

describe("PublicPortSection an enabled port", () => {
	test("shows the saved settings, the applied status and the external address", async () => {
		await mount(live());
		expect(await sw()).toBeChecked();
		expect(portInput()).toHaveValue("15432");
		expect(screen.getByLabelText("Allowed network 1")).toHaveValue(
			"203.0.113.0/24",
		);
		expect(screen.getByText("Applied")).toBeInTheDocument();
		expect(screen.getByText("Open on eu-1, port 15432.")).toBeInTheDocument();
		expect(screen.getByText("External connection")).toBeInTheDocument();
		expect(screen.getByText("203.0.113.7:15432")).toBeInTheDocument();
		// Nothing changed: nothing to save or discard, and no proxy restart is announced.
		expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Discard" })).toBeDisabled();
		expect(screen.queryByText(RECREATE_PROXY_NOTE)).not.toBeInTheDocument();
		expect(
			screen.queryByText(/Nothing listens on this port/),
		).not.toBeInTheDocument();
	});

	test("brackets an IPv6 host in the address", async () => {
		await mount(live({ host: "2001:db8::7" }));
		expect(await screen.findByText("[2001:db8::7]:15432")).toBeInTheDocument();
	});

	test("changing the port announces the proxy restart and saves the new port", async () => {
		const user = userEvent.setup();
		const { api } = await mount(live(), {
			[PUT]: becomes(
				live({ listenPort: 16000, deployed: false, staged: true }),
			),
		});
		await screen.findByText("Applied");
		await user.clear(portInput());
		await user.type(portInput(), "16000");
		expect(screen.getByText(RECREATE_PROXY_NOTE)).toBeInTheDocument();
		const save = screen.getByRole("button", { name: "Save changes" });
		expect(save).toBeEnabled();
		await user.click(save);
		await waitFor(() => expect(puts(api)).toHaveLength(1));
		expect(puts(api)[0]?.body).toEqual({
			listenPort: 16000,
			allowCidrs: ["203.0.113.0/24"],
		});
		expect(await screen.findByText("Staged")).toBeInTheDocument();
		expect(portInput()).toHaveValue("16000");
	});

	test("Discard puts the saved settings back and sends nothing", async () => {
		const user = userEvent.setup();
		const { api } = await mount(live());
		await screen.findByText("Applied");
		await user.clear(portInput());
		await user.type(portInput(), "16000");
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(portInput()).toHaveValue("15432");
		expect(
			screen.queryByText(/Close the public port of/),
		).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Discard" })).toBeDisabled();
		expect(puts(api)).toEqual([]);
	});

	test("editing the allow list alone saves it without a proxy restart notice", async () => {
		const user = userEvent.setup();
		const { api } = await mount(live(), {
			[PUT]: becomes(live({ allowCidrs: ["203.0.113.0/24", "198.51.100.1"] })),
		});
		await screen.findByText("Applied");
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(screen.getByLabelText("Allowed network 2"), "198.51.100.1");
		expect(screen.queryByText(RECREATE_PROXY_NOTE)).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		await waitFor(() => expect(puts(api)).toHaveLength(1));
		expect(puts(api)[0]?.body).toEqual({
			listenPort: 15432,
			allowCidrs: ["203.0.113.0/24", "198.51.100.1"],
		});
	});

	test("an enabled route that is not deployed yet says nothing listens", async () => {
		await mount(live({ deployed: false }));
		expect(await screen.findByText("Not applied")).toBeInTheDocument();
		expect(
			screen.getByText("The route is not deployed yet."),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Nothing listens on this port until the route is deployed.",
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Review and deploy" }),
		).toBeNull();
	});

	test("a server without a public address cannot hand out a connection string", async () => {
		await mount(live({ host: "" }));
		expect(await screen.findByText(NO_PUBLIC_ADDRESS)).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /Copy connection string/ }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: /Reveal for 60 s/ }),
		).toBeDisabled();
	});
});

describe("PublicPortSection closing", () => {
	test("Close public port asks, then deletes the port and reports it", async () => {
		const user = userEvent.setup();
		const { api } = await mount(live(), { [DELETE]: becomes(view()) });
		await user.click(
			await screen.findByRole("button", { name: "Close public port" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Close the public port of Orders DB?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				/stops accepting connections on port 15432 of eu-1 right away/,
			),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Close public port" }),
		);

		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.filter((c) => c.method === "DELETE")).toHaveLength(1);
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/services/svc-db/database/public-port",
		);
		expect(
			(await screen.findAllByText("Public port closed")).length,
		).toBeGreaterThan(0);
		expect(await sw()).not.toBeChecked();
		expect(
			screen.queryByLabelText("Port on the server"),
		).not.toBeInTheDocument();
	});

	test("the switch asks the same question when turned off", async () => {
		const user = userEvent.setup();
		const { api } = await mount(live());
		await user.click(await sw());
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Close the public port of Orders DB?"),
		).toBeInTheDocument();
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(await sw()).toBeChecked();
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
	});

	test("a failed close keeps the dialog open with the reason", async () => {
		const user = userEvent.setup();
		await mount(live(), {
			[DELETE]: problem(500, "The proxy refused the change.", "internal"),
		});
		await user.click(
			await screen.findByRole("button", { name: "Close public port" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Close public port" }),
		);
		expect(
			await within(dialog).findByText("The proxy refused the change."),
		).toBeInTheDocument();
		expect(screen.queryByText("Public port closed")).not.toBeInTheDocument();
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		expect(await sw()).toBeChecked();
	});
});

describe("PublicPortSection polling", () => {
	function interval(queryClient: QueryClient): unknown {
		const query = queryClient.getQueryCache().find({
			queryKey: getDatabasePublicPortQueryKey({
				path: { serviceId: "svc-db" },
			}),
		});
		const option = query?.observers[0]?.options.refetchInterval;
		return typeof option === "function" ? option(query as never) : option;
	}

	test("polls every 4 s while a change is staged", async () => {
		const { queryClient } = await mount(live({ staged: true }));
		await screen.findByText("Staged");
		expect(interval(queryClient)).toBe(4_000);
	});

	test("polls every 4 s while an enabled route waits to be deployed", async () => {
		const { queryClient } = await mount(live({ deployed: false }));
		await screen.findByText("Not applied");
		expect(interval(queryClient)).toBe(4_000);
	});

	test("stops polling once applied, and while off", async () => {
		const applied = await mount(live());
		await screen.findByText("Applied");
		expect(interval(applied.queryClient)).toBe(false);
		applied.unmount();
		const off = await mount(view());
		await sw();
		expect(interval(off.queryClient)).toBe(false);
	});
});

describe("PublicPortSection connection string", () => {
	const revealed = {
		key: "DATABASE_PUBLIC_URL",
		value: "postgres://app:s3cret@203.0.113.7:15432/orders",
		host: "203.0.113.7",
		port: 15432,
		revealedAt: new Date().toISOString(),
		expiresAt: new Date(Date.now() + 60_000).toISOString(),
	};

	test("Reveal for 60 s fetches the string, shows it, and Hide removes it", async () => {
		const user = userEvent.setup();
		const { api } = await mount(live(), { [CONNECTION]: { body: revealed } });
		await user.click(
			await screen.findByRole("button", { name: /Reveal for 60 s/ }),
		);
		const field = await screen.findByLabelText(
			"DATABASE_PUBLIC_URL (revealed)",
		);
		expect(field).toHaveValue(revealed.value);
		expect(api.calls.find((c) => c.method === "POST")).toMatchObject({
			path: "/services/svc-db/database/public-port/connection",
			body: {},
		});
		expect(
			screen.queryByRole("button", { name: /Reveal for 60 s/ }),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Hide" }));
		expect(
			screen.queryByLabelText("DATABASE_PUBLIC_URL (revealed)"),
		).not.toBeInTheDocument();
		expect(screen.queryByDisplayValue(revealed.value)).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /Reveal for 60 s/ }),
		).toBeEnabled();
	});

	test("Copy connection string puts the revealed value on the clipboard without showing it", async () => {
		const user = userEvent.setup();
		originalClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
		const written: string[] = [];
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: { writeText: async (text: string) => void written.push(text) },
		});
		await mount(live(), { [CONNECTION]: { body: revealed } });
		await user.click(
			await screen.findByRole("button", { name: /Copy connection string/ }),
		);
		await waitFor(() => expect(written).toEqual([revealed.value]));
		expect(screen.queryByDisplayValue(revealed.value)).not.toBeInTheDocument();
	});

	test("a server without a public address is explained next to the control", async () => {
		const user = userEvent.setup();
		await mount(live(), {
			[CONNECTION]: problem(422, "raw", "no_public_address"),
		});
		await user.click(
			await screen.findByRole("button", { name: /Reveal for 60 s/ }),
		);
		await waitFor(() =>
			expect(screen.getAllByText(NO_PUBLIC_ADDRESS).length).toBeGreaterThan(0),
		);
		expect(
			screen.queryByLabelText("DATABASE_PUBLIC_URL (revealed)"),
		).not.toBeInTheDocument();
	});

	test("a closed port is explained when the reveal is refused", async () => {
		const user = userEvent.setup();
		await mount(live(), {
			[CONNECTION]: problem(409, "raw", "public_port_disabled"),
		});
		await user.click(
			await screen.findByRole("button", { name: /Reveal for 60 s/ }),
		);
		expect(
			await screen.findByText("The public port is closed. Expose it first."),
		).toBeInTheDocument();
	});

	test("any other reveal failure shows the control plane's message", async () => {
		const user = userEvent.setup();
		await mount(live(), {
			[CONNECTION]: problem(403, "You may not reveal secrets.", "forbidden"),
		});
		await user.click(
			await screen.findByRole("button", { name: /Reveal for 60 s/ }),
		);
		expect(
			await screen.findByText("You may not reveal secrets."),
		).toBeInTheDocument();
	});
});
