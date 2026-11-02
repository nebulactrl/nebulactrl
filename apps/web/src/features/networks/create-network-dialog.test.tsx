import { describe, expect, it } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Server } from "#/api/gen/types.gen";
import { serverView } from "#/features/platform/test-fixtures";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CreateNetworkDialog } from "./create-network-dialog";

function Harness() {
	const [open, setOpen] = useState(true);
	return (
		<>
			<p data-testid="open">{String(open)}</p>
			<CreateNetworkDialog open={open} onOpenChange={setOpen} />
		</>
	);
}

const servers = (...items: Server[]) => ({
	"GET /servers": { body: make("PageServer", { items }) },
});
const hetzner = serverView({ id: "srv-1", name: "hetzner-01" });
const create = () => screen.getByRole("button", { name: "Create network" });

describe("CreateNetworkDialog", () => {
	it("keeps Create locked until it has a name and a server", async () => {
		const user = userEvent.setup();
		stubApi(servers(hetzner));
		await renderWithProviders(<Harness />);
		expect(create()).toBeDisabled();
		await user.type(screen.getByLabelText("Name"), "  ");
		expect(create()).toBeDisabled();
		await user.type(screen.getByLabelText("Name"), "backend");
		await waitFor(() => expect(create()).toBeEnabled());
	});

	it("has no server to pick when none is usable (revoked and pending are left out)", async () => {
		const user = userEvent.setup();
		stubApi(
			servers(
				serverView({ id: "r", name: "gone", status: "revoked" }),
				serverView({ id: "p", name: "new", status: "pending" }),
			),
		);
		await renderWithProviders(<Harness />);
		await user.type(screen.getByLabelText("Name"), "backend");
		expect(await screen.findByText("No server yet")).toBeInTheDocument();
		expect(create()).toBeDisabled();
	});

	it("creates the network on the first usable server with the trimmed name, then closes", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...servers(
				serverView({ id: "r", name: "gone", status: "revoked" }),
				hetzner,
			),
			"POST /networks": {
				status: 201,
				body: make("NetworkView", { id: "n1", name: "backend" }),
			},
			"GET /networks": { body: { items: [] } },
		});
		await renderWithProviders(<Harness />);
		await user.type(screen.getByLabelText("Name"), "  backend ");
		await waitFor(() => expect(create()).toBeEnabled());
		await user.click(create());
		expect(await screen.findByText("Created backend")).toBeInTheDocument();
		expect(
			screen.getByText("Attach services to it from their settings."),
		).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/networks");
		expect(post?.body).toEqual({ name: "backend", serverId: "srv-1" });
		await waitFor(() =>
			expect(screen.getByTestId("open")).toHaveTextContent("false"),
		);
	});

	it("creates on the server the reader picked", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...servers(hetzner, serverView({ id: "srv-2", name: "ovh-02" })),
			"POST /networks": {
				status: 201,
				body: make("NetworkView", { id: "n1", name: "backend" }),
			},
			"GET /networks": { body: { items: [] } },
		});
		await renderWithProviders(<Harness />);
		await user.type(screen.getByLabelText("Name"), "backend");
		await user.click(screen.getByLabelText("Server"));
		await user.click(await screen.findByRole("option", { name: "ovh-02" }));
		await user.click(create());
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
				name: "backend",
				serverId: "srv-2",
			}),
		);
	});

	it("shows the server's reason and stays open when creating fails", async () => {
		const user = userEvent.setup();
		stubApi({
			...servers(hetzner),
			"POST /networks": problem(409, {
				title: "Conflict",
				status: 409,
				detail: "Name already used",
			}),
		});
		await renderWithProviders(<Harness />);
		await user.type(screen.getByLabelText("Name"), "backend");
		await waitFor(() => expect(create()).toBeEnabled());
		await user.click(create());
		expect(await screen.findByText("Name already used")).toBeInTheDocument();
		expect(screen.getByTestId("open")).toHaveTextContent("true");
	});

	it("Cancel closes without creating", async () => {
		const user = userEvent.setup();
		const api = stubApi(servers(hetzner));
		await renderWithProviders(<Harness />);
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(screen.getByTestId("open")).toHaveTextContent("false");
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
	});
});
