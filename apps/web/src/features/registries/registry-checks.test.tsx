import { describe, expect, test } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { registriesQuery } from "./queries";
import { RegistryChecks } from "./registry-checks";
import { registry, wireCheck, wireRegistry, wireServer } from "./test-data";

const SERVERS = [
	wireServer({ id: "srv-b", name: "beta" }),
	wireServer({ id: "srv-build", name: "builder", buildEnabled: true }),
	wireServer({ id: "srv-a", name: "alpha" }),
	wireServer({ id: "srv-off", name: "offline-box", status: "offline" }),
];

/** The card's live data: the registry list feeds RegistryChecks, as on the page. */
function Live() {
	const registries = useQuery(registriesQuery());
	const first = registries.data?.[0];
	return first ? <RegistryChecks registry={first} /> : <p>loading</p>;
}

describe("RegistryChecks", () => {
	test("offers only online servers, build servers first, and starts on the first", async () => {
		stubApi({ "GET /servers": { body: { items: SERVERS } } });
		const user = userEvent.setup();

		await renderWithProviders(<RegistryChecks registry={registry()} />);

		const trigger = await screen.findByRole("combobox", {
			name: "Server to test from",
		});
		await waitFor(() => expect(trigger).toHaveTextContent("builder"));
		await user.click(trigger);
		const options = await screen.findAllByRole("option");
		expect(options.map((o) => o.textContent)).toEqual([
			"builderbuild server",
			"alpha",
			"beta",
		]);
	});

	test("with no online server the select and the Test button are disabled", async () => {
		stubApi({
			"GET /servers": {
				body: {
					items: [wireServer({ id: "s", name: "x", status: "offline" })],
				},
			},
		});

		await renderWithProviders(<RegistryChecks registry={registry()} />);

		const trigger = await screen.findByRole("combobox", {
			name: "Server to test from",
		});
		await waitFor(() => expect(trigger).toHaveTextContent("No online server"));
		expect(trigger).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Test registry" }),
		).toBeDisabled();
		expect(screen.getByText(/Not tested yet/)).toBeInTheDocument();
	});

	test("Test registry posts the default server and shows the stored result afterwards", async () => {
		let checked = false;
		const api = stubApi({
			"GET /servers": { body: { items: SERVERS } },
			"GET /registries": () =>
				Response.json({
					items: [
						wireRegistry({
							lastCheck: checked
								? { "srv-build": wireCheck({ at: "2026-10-31T11:00:00Z" }) }
								: {},
						}),
					],
				}),
			"POST /registries/:id/check": () => {
				checked = true;
				return Response.json(wireCheck());
			},
		});
		const user = userEvent.setup();

		await renderWithProviders(<Live />);
		expect(await screen.findByText(/Not tested yet/)).toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Server to test from" }),
			).toHaveTextContent("builder"),
		);

		await user.click(screen.getByRole("button", { name: "Test registry" }));

		const row = (await screen.findByText("Push and pull work")).closest("li");
		expect(within(row as HTMLElement).getByText("builder")).toBeInTheDocument();
		expect(screen.queryByText(/Not tested yet/)).not.toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/registries/reg-1/check");
		expect(post?.query.get("serverId")).toBe("srv-build");
	});

	test("a picked server is the one tested", async () => {
		const api = stubApi({
			"GET /servers": { body: { items: SERVERS } },
			"GET /registries": { body: { items: [wireRegistry()] } },
			"POST /registries/:id/check": { body: wireCheck() },
		});
		const user = userEvent.setup();

		await renderWithProviders(<Live />);
		const trigger = await screen.findByRole("combobox", {
			name: "Server to test from",
		});
		await waitFor(() => expect(trigger).toHaveTextContent("builder"));
		await user.click(trigger);
		await user.click(await screen.findByRole("option", { name: "beta" }));
		await waitFor(() => expect(trigger).toHaveTextContent("beta"));
		await user.click(screen.getByRole("button", { name: "Test registry" }));

		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "POST")).toBe(true),
		);
		expect(
			api.calls.find((c) => c.method === "POST")?.query.get("serverId"),
		).toBe("srv-b");
	});

	test("the button is disabled while the test runs", async () => {
		let release: (() => void) | undefined;
		stubApi({
			"GET /servers": { body: { items: SERVERS } },
			"GET /registries": { body: { items: [wireRegistry()] } },
			"POST /registries/:id/check": () =>
				new Promise<Response>((resolve) => {
					release = () => resolve(Response.json(wireCheck()));
				}),
		});
		const user = userEvent.setup();

		await renderWithProviders(<Live />);
		const button = await screen.findByRole("button", { name: "Test registry" });
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Server to test from" }),
			).toHaveTextContent("builder"),
		);
		expect(button).toBeEnabled();
		await user.click(button);

		await waitFor(() => expect(button).toBeDisabled());
		await waitFor(() => expect(release).toBeDefined());
		release?.();
		await waitFor(() =>
			expect((button as HTMLButtonElement).disabled).toBe(false),
		);
	});

	test("a test that cannot run shows the reason", async () => {
		stubApi({
			"GET /servers": { body: { items: SERVERS } },
			"GET /registries": { body: { items: [wireRegistry()] } },
			"POST /registries/:id/check": problem(409, {
				code: "agent_offline",
				detail: "The agent is not connected.",
			}),
		});
		const user = userEvent.setup();

		await renderWithProviders(<Live />);
		const button = await screen.findByRole("button", { name: "Test registry" });
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Server to test from" }),
			).toHaveTextContent("builder"),
		);
		await user.click(button);

		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("The test did not run");
		expect(alert).toHaveTextContent("The agent is not connected.");
	});

	test("lists results newest first, names removed servers and explains failures once", async () => {
		stubApi({ "GET /servers": { body: { items: SERVERS } } });

		await renderWithProviders(
			<RegistryChecks
				registry={registry({
					lastCheck: {
						"srv-a": {
							ok: false,
							push: true,
							pull: false,
							error: "401 unauthorized: bad token",
							at: "2026-10-30T10:00:00Z",
						},
						"srv-gone": {
							ok: false,
							push: false,
							pull: false,
							error: "dial tcp: i/o timeout",
							at: "2026-10-31T10:00:00Z",
						},
						"srv-b": {
							ok: true,
							push: true,
							pull: true,
							error: "",
							at: "2026-10-29T10:00:00Z",
						},
					},
				})}
			/>,
		);

		await screen.findByText("alpha");
		const rows = screen.getAllByRole("listitem");
		expect(rows).toHaveLength(3);
		expect(rows[0]).toHaveTextContent(/^Removed server/);
		expect(rows[1]).toHaveTextContent(/^alpha/);
		expect(rows[2]).toHaveTextContent(/^beta/);
		expect(rows[0]).toHaveTextContent("dial tcp: i/o timeout");
		// The summary already is the error here, so it is not printed twice.
		expect(
			within(rows[0] as HTMLElement).getAllByText("dial tcp: i/o timeout"),
		).toHaveLength(1);
		expect(rows[1]).toHaveTextContent("Push works, pull failed");
		expect(
			within(rows[1] as HTMLElement).getByTitle("401 unauthorized: bad token"),
		).toHaveTextContent("401 unauthorized: bad token");
		expect(rows[2]).toHaveTextContent("Push and pull work");
	});
});
