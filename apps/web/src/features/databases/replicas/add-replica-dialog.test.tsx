import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { AddReplicaDialog } from "./add-replica-dialog";
import { SAME_SERVER_NOTE } from "./model";
import { replicaPlan, serversPage, wireServer } from "./replicas.fixture";

const REPLICAS = "POST /services/:id/database/replicas";
const SERVERS = "GET /servers";
const MESH = "GET /mesh";

const primary = {
	id: "svc-primary",
	name: "Orders DB",
	slug: "orders-db",
	serverId: "srv-a",
};

const meshWith = (...serverIds: string[]) => ({
	body: make("View", {
		nodes: serverIds.map((serverId) =>
			make("NodeView", { serverId, state: "online" }),
		),
	}),
});

type Body = {
	serverId?: string;
	dryRun?: boolean;
	name?: string;
	acceptMeshChanges?: boolean;
};

/**
 * The replicas route: a dry run answers with the plan for the server asked,
 * a real request with `created` (the default accepts it).
 */
function replicasRoute(
	plans: Record<string, ReturnType<typeof replicaPlan>>,
	created: (body: Body, request: Request) => Response | undefined = () =>
		undefined,
) {
	return async (request: Request) => {
		const body = (await request.clone().json()) as Body;
		if (body.dryRun) {
			const plan = plans[body.serverId ?? ""];
			if (!plan) return Response.json({ detail: "no plan" }, { status: 500 });
			return Response.json({ dryRun: true, plan });
		}
		return (
			created(body, request) ??
			Response.json({
				dryRun: false,
				plan: plans[body.serverId ?? ""],
				serviceId: "svc-new",
				status: "creating",
			})
		);
	};
}

const isCreate = (call: { method: string; body: unknown }) =>
	call.method === "POST" && (call.body as Body).dryRun !== true;

async function open(
	props: { taken?: string[]; onOpenChange?: (open: boolean) => void } = {},
) {
	const onOpenChange = props.onOpenChange ?? (() => {});
	const result = await renderWithProviders(
		<AddReplicaDialog
			service={primary}
			taken={props.taken ?? []}
			open
			onOpenChange={onOpenChange}
		/>,
	);
	return { ...result, dialog: await screen.findByRole("dialog") };
}

const twoServers = serversPage(
	wireServer("srv-a", "eu-1"),
	wireServer("srv-b", "eu-2"),
);

describe("AddReplicaDialog placement", () => {
	test("starts on another online server that is in the mesh and plans it", async () => {
		const api = stubApi({
			[SERVERS]: { body: twoServers },
			[MESH]: meshWith("srv-b"),
			[REPLICAS]: replicasRoute({
				"srv-b": replicaPlan({
					devicesAdded: 1,
					links: [{ from: "eu-1", to: "eu-2", purpose: "replication" }],
				}),
			}),
		});
		const { dialog } = await open();
		await waitFor(() =>
			expect(
				within(dialog).getByRole("radio", { name: /eu-2/ }),
			).toHaveAttribute("aria-checked", "true"),
		);
		expect(within(dialog).getByRole("radio", { name: /eu-1/ })).toHaveAttribute(
			"aria-checked",
			"false",
		);
		expect(
			await within(dialog).findByText(
				"Another server: replication goes over the mesh (1 link); this adds 1 mesh device.",
			),
		).toBeInTheDocument();
		expect(
			within(dialog).getByRole("list", { name: "Mesh links" }),
		).toHaveTextContent("eu-1 → eu-2 (replication)");
		const plans = api.calls.filter((c) => c.method === "POST");
		expect(plans).toHaveLength(1);
		expect(plans[0]).toMatchObject({
			path: "/services/svc-primary/database/replicas",
			body: { serverId: "srv-b", dryRun: true },
		});
	});

	test("falls back to the primary's own server when no other server is meshed", async () => {
		stubApi({
			[SERVERS]: { body: twoServers },
			[MESH]: meshWith(),
			[REPLICAS]: replicasRoute({
				"srv-a": replicaPlan({
					serverId: "srv-a",
					serverName: "eu-1",
					sameServer: true,
				}),
			}),
		});
		const { dialog } = await open();
		expect(
			await within(dialog).findByText(SAME_SERVER_NOTE),
		).toBeInTheDocument();
		expect(within(dialog).getByRole("radio", { name: /eu-1/ })).toHaveAttribute(
			"aria-checked",
			"true",
		);
		expect(
			within(dialog).getByText(
				/Same server: it reaches the primary over the environment network/,
			),
		).toBeInTheDocument();
		expect(within(dialog).queryByRole("checkbox")).not.toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Add standby replica" }),
		).toBeEnabled();
	});

	test("also falls back to the primary's server when the mesh cannot be read", async () => {
		const api = stubApi({
			[SERVERS]: { body: twoServers },
			[MESH]: problem(500, { title: "x", detail: "boom", code: "internal" }),
			[REPLICAS]: replicasRoute({
				"srv-a": replicaPlan({
					serverId: "srv-a",
					serverName: "eu-1",
					sameServer: true,
				}),
			}),
		});
		const { dialog } = await open();
		expect(
			await within(dialog).findByText(SAME_SERVER_NOTE),
		).toBeInTheDocument();
		expect(
			api.calls
				.filter((c) => c.method === "POST")
				.map((c) => (c.body as Body).serverId),
		).toEqual(["srv-a"]);
	});

	test("choosing another server plans that server and asks for the mesh consent again", async () => {
		const api = stubApi({
			[SERVERS]: { body: twoServers },
			[MESH]: meshWith("srv-b"),
			[REPLICAS]: replicasRoute({
				"srv-a": replicaPlan({
					serverId: "srv-a",
					serverName: "eu-1",
					sameServer: true,
				}),
				"srv-b": replicaPlan({ devicesAdded: 2, links: [] }),
			}),
		});
		const user = userEvent.setup();
		const { dialog } = await open();
		const consent = await within(dialog).findByRole("checkbox", {
			name: /adds 2 devices/,
		});
		await user.click(consent);
		expect(consent).toBeChecked();
		expect(
			within(dialog).getByRole("button", { name: "Add standby replica" }),
		).toBeEnabled();

		await user.click(within(dialog).getByRole("radio", { name: /eu-1/ }));
		expect(
			await within(dialog).findByText(SAME_SERVER_NOTE),
		).toBeInTheDocument();
		expect(within(dialog).queryByRole("checkbox")).not.toBeInTheDocument();

		await user.click(within(dialog).getByRole("radio", { name: /eu-2/ }));
		const again = await within(dialog).findByRole("checkbox", {
			name: /adds 2 devices/,
		});
		expect(again).not.toBeChecked();
		expect(
			within(dialog).getByRole("button", { name: "Add standby replica" }),
		).toBeDisabled();
		expect(
			api.calls
				.filter((c) => c.method === "POST")
				.map((c) => (c.body as Body).serverId),
		).toEqual(["srv-b", "srv-a", "srv-b"]);
	});

	test("an offline server is listed but cannot be chosen", async () => {
		stubApi({
			[SERVERS]: {
				body: serversPage(
					wireServer("srv-a", "eu-1"),
					wireServer("srv-b", "eu-2", { status: "offline" }),
				),
			},
			[MESH]: meshWith("srv-b"),
			[REPLICAS]: replicasRoute({
				"srv-a": replicaPlan({
					serverId: "srv-a",
					serverName: "eu-1",
					sameServer: true,
				}),
			}),
		});
		const { dialog } = await open();
		await within(dialog).findByText(SAME_SERVER_NOTE);
		const offline = within(dialog).getByRole("radio", { name: /eu-2/ });
		expect(offline).toBeDisabled();
		expect(offline).toHaveTextContent("offline");
	});

	test("revoked and pending servers are not offered at all", async () => {
		stubApi({
			[SERVERS]: {
				body: serversPage(
					wireServer("srv-a", "eu-1"),
					wireServer("srv-b", "gone", { status: "revoked" }),
					wireServer("srv-c", "joining", { status: "pending" }),
				),
			},
			[MESH]: meshWith(),
			[REPLICAS]: replicasRoute({
				"srv-a": replicaPlan({ serverId: "srv-a", sameServer: true }),
			}),
		});
		const { dialog } = await open();
		await within(dialog).findByText(SAME_SERVER_NOTE);
		expect(within(dialog).getAllByRole("radio")).toHaveLength(1);
		expect(within(dialog).queryByRole("radio", { name: /gone/ })).toBeNull();
		expect(within(dialog).queryByRole("radio", { name: /joining/ })).toBeNull();
	});

	test("with no usable server it says so and cannot add anything", async () => {
		const api = stubApi({
			[SERVERS]: {
				body: serversPage(wireServer("srv-b", "gone", { status: "revoked" })),
			},
			[MESH]: meshWith(),
		});
		const { dialog } = await open();
		expect(
			await within(dialog).findByText(
				"No server is available to run a replica.",
			),
		).toBeInTheDocument();
		expect(within(dialog).queryByRole("radio")).not.toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Add standby replica" }),
		).toBeDisabled();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});
});

describe("AddReplicaDialog plan feedback", () => {
	test("shows the plan's warnings", async () => {
		stubApi({
			[SERVERS]: { body: twoServers },
			[MESH]: meshWith("srv-b"),
			[REPLICAS]: replicasRoute({
				"srv-b": replicaPlan({
					warnings: [
						{ code: "w", message: "The primary is under heavy write load" },
					],
				}),
			}),
		});
		const { dialog } = await open();
		expect(
			await within(dialog).findByText("Before you add it"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("The primary is under heavy write load"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Add standby replica" }),
		).toBeEnabled();
	});

	test("a mesh blocker explains itself, links to the server's mesh settings and blocks adding", async () => {
		const onOpenChange = mock((_open: boolean) => {});
		stubApi({
			[SERVERS]: { body: twoServers },
			[MESH]: meshWith("srv-b"),
			[REPLICAS]: replicasRoute({
				"srv-b": replicaPlan({
					devicesAdded: 1,
					blockers: [
						{
							code: "mesh_server_required",
							message: "eu-2 is not in the mesh",
						},
					],
				}),
			}),
		});
		const user = userEvent.setup();
		const { dialog } = await open({ onOpenChange });
		expect(
			await within(dialog).findByText("It cannot be added right now"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("eu-2 is not in the mesh"),
		).toBeInTheDocument();
		// A blocked plan never asks for the mesh consent.
		expect(within(dialog).queryByRole("checkbox")).not.toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Add standby replica" }),
		).toBeDisabled();
		const link = within(dialog).getByRole("link", {
			name: "Enable the mesh on eu-2",
		});
		expect(link).toHaveAttribute("href", "/servers/srv-b/settings/mesh");
		await user.click(link);
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("a blocker other than the mesh has no mesh link", async () => {
		stubApi({
			[SERVERS]: { body: twoServers },
			[MESH]: meshWith("srv-b"),
			[REPLICAS]: replicasRoute({
				"srv-b": replicaPlan({
					blockers: [
						{
							code: "max_replicas",
							message: "A primary has at most 5 replicas",
						},
					],
				}),
			}),
		});
		const { dialog } = await open();
		expect(
			await within(dialog).findByText("A primary has at most 5 replicas"),
		).toBeInTheDocument();
		expect(within(dialog).queryByRole("link")).not.toBeInTheDocument();
	});

	test("a plan that fails shows the control plane's reason", async () => {
		stubApi({
			[SERVERS]: { body: twoServers },
			[MESH]: meshWith("srv-b"),
			[REPLICAS]: {
				status: 409,
				body: {
					status: 409,
					title: "Conflict",
					detail: "The primary is not deployed.",
					code: "not_deployed",
				},
			},
		});
		const { dialog } = await open();
		expect(
			await within(dialog).findByText("The replica could not be planned"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("The primary is not deployed."),
		).toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Add standby replica" }),
		).toBeDisabled();
	});
});

describe("AddReplicaDialog submit", () => {
	test("suggests a free name and sends the request with consent and an idempotency key", async () => {
		let key: string | null = null;
		const api = stubApi({
			[SERVERS]: { body: twoServers },
			[MESH]: meshWith("srv-b"),
			[REPLICAS]: replicasRoute(
				{ "srv-b": replicaPlan({ devicesAdded: 1 }) },
				(_body, request) => {
					key = request.headers.get("Idempotency-Key");
					return undefined;
				},
			),
		});
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		const { dialog } = await open({
			taken: ["orders-db-replica"],
			onOpenChange,
		});
		expect(within(dialog).getByLabelText("Name")).toHaveAttribute(
			"placeholder",
			"orders-db-replica-2",
		);
		const submit = within(dialog).getByRole("button", {
			name: "Add standby replica",
		});
		await user.click(
			await within(dialog).findByRole("checkbox", { name: /adds 1 device/ }),
		);
		await user.click(submit);

		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		const created = api.calls.filter(isCreate);
		expect(created).toHaveLength(1);
		// No typed name: the server names it; the consent is explicit.
		expect(created[0]?.body).toEqual({
			serverId: "srv-b",
			acceptMeshChanges: true,
		});
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		expect(
			(await screen.findAllByText("Creating orders-db-replica-2")).length,
		).toBeGreaterThan(0);
		expect(api.unmatched).toEqual([]);
	});

	test("a typed name is trimmed, sent and used in the confirmation", async () => {
		const api = stubApi({
			[SERVERS]: { body: serversPage(wireServer("srv-a", "eu-1")) },
			[MESH]: meshWith(),
			[REPLICAS]: replicasRoute({
				"srv-a": replicaPlan({ serverId: "srv-a", sameServer: true }),
			}),
		});
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		const { dialog } = await open({ onOpenChange });
		await within(dialog).findByText(SAME_SERVER_NOTE);
		await user.type(within(dialog).getByLabelText("Name"), "  reporting-ro  ");
		await user.click(
			within(dialog).getByRole("button", { name: "Add standby replica" }),
		);
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls.filter(isCreate)[0]?.body).toEqual({
			serverId: "srv-a",
			name: "reporting-ro",
			acceptMeshChanges: false,
		});
		expect(
			(await screen.findAllByText("Creating reporting-ro")).length,
		).toBeGreaterThan(0);
	});

	test("Enter in the name field submits the form", async () => {
		const api = stubApi({
			[SERVERS]: { body: serversPage(wireServer("srv-a", "eu-1")) },
			[MESH]: meshWith(),
			[REPLICAS]: replicasRoute({
				"srv-a": replicaPlan({ serverId: "srv-a", sameServer: true }),
			}),
		});
		const user = userEvent.setup();
		const { dialog } = await open();
		await within(dialog).findByText(SAME_SERVER_NOTE);
		await user.type(within(dialog).getByLabelText("Name"), "ro{Enter}");
		await waitFor(() => expect(api.calls.filter(isCreate)).toHaveLength(1));
	});

	test("cannot be submitted while the mesh consent is missing", async () => {
		const api = stubApi({
			[SERVERS]: { body: twoServers },
			[MESH]: meshWith("srv-b"),
			[REPLICAS]: replicasRoute({ "srv-b": replicaPlan({ devicesAdded: 1 }) }),
		});
		const user = userEvent.setup();
		const { dialog } = await open();
		await within(dialog).findByRole("checkbox", { name: /adds 1 device/ });
		await user.type(within(dialog).getByLabelText("Name"), "ro{Enter}");
		expect(
			within(dialog).getByRole("button", { name: "Add standby replica" }),
		).toBeDisabled();
		expect(api.calls.filter(isCreate)).toEqual([]);
	});

	test("a refused request shows the reason, keeps the dialog open and lets the reader retry", async () => {
		const onOpenChange = mock((_open: boolean) => {});
		const api = stubApi({
			[SERVERS]: { body: serversPage(wireServer("srv-a", "eu-1")) },
			[MESH]: meshWith(),
			[REPLICAS]: replicasRoute(
				{ "srv-a": replicaPlan({ serverId: "srv-a", sameServer: true }) },
				() =>
					Response.json(
						{
							status: 409,
							title: "Conflict",
							detail: "That name is taken.",
							code: "name_taken",
						},
						{ status: 409 },
					),
			),
		});
		const user = userEvent.setup();
		const { dialog } = await open({ onOpenChange });
		await within(dialog).findByText(SAME_SERVER_NOTE);
		await user.click(
			within(dialog).getByRole("button", { name: "Add standby replica" }),
		);
		expect(
			await within(dialog).findByText("The replica was refused"),
		).toBeInTheDocument();
		expect(within(dialog).getByText("That name is taken.")).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(
			within(dialog).getByRole("button", { name: "Add standby replica" }),
		).toBeEnabled();
		expect(api.calls.filter(isCreate)).toHaveLength(1);
	});

	test("Cancel closes without creating anything", async () => {
		const onOpenChange = mock((_open: boolean) => {});
		const api = stubApi({
			[SERVERS]: { body: serversPage(wireServer("srv-a", "eu-1")) },
			[MESH]: meshWith(),
			[REPLICAS]: replicasRoute({
				"srv-a": replicaPlan({ serverId: "srv-a", sameServer: true }),
			}),
		});
		const user = userEvent.setup();
		const { dialog } = await open({ onOpenChange });
		await within(dialog).findByText(SAME_SERVER_NOTE);
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls.filter(isCreate)).toEqual([]);
	});
});
