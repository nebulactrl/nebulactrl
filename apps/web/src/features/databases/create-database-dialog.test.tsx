import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Environment, Server } from "#/api/models";
import { registerPlacer } from "#/features/canvas/use-layout-persistence";
import { createActions, useCreateState } from "#/features/create/store";
import {
	type OpenReviewOptions,
	onOpenStagedReview,
} from "#/features/deploy-button/open-review";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CreateDatabaseDialog } from "./create-database-dialog";
import { catalogBody, problem, server, serviceView } from "./database.fixture";

const environment: Pick<Environment, "id" | "name" | "isProduction"> = {
	id: "env-1",
	name: "Staging",
	isProduction: false,
};

/** Mounts the dialog the way the canvas does: the open request comes from the store. */
function Host({
	servers = [server()],
	env = environment,
}: {
	servers?: Server[];
	env?: typeof environment;
}) {
	const { request } = useCreateState();
	return (
		<CreateDatabaseDialog
			request={request}
			environment={env}
			servers={servers}
		/>
	);
}

const existing = (...names: string[]) => ({
	body: make("PageServiceView", {
		items: names.map((name, index) =>
			serviceView({ id: `existing-${index}`, name }),
		),
	}),
});

function routes(extra: Parameters<typeof stubApi>[0] = {}) {
	return {
		"GET /database-engines": { body: catalogBody },
		"GET /environments/:id/services": existing(),
		...extra,
	};
}

const createButton = () =>
	screen.getByRole("button", { name: "Create database" });

beforeEach(() => createActions.closeDialog());
afterEach(() => createActions.closeDialog());

async function openDialog(
	options?: Parameters<typeof createActions.open>[1],
	props?: Parameters<typeof Host>[0],
) {
	createActions.open("database", options);
	await renderWithProviders(<Host {...props} />);
	await screen.findByRole("radiogroup", { name: "Database engine" });
}

describe("CreateDatabaseDialog", () => {
	test("opens on Valkey and creates it with the default version on the only server", async () => {
		const seen: { key: string | null }[] = [];
		const api = stubApi(
			routes({
				"POST /environments/:id/databases": (request) => {
					seen.push({ key: request.headers.get("Idempotency-Key") });
					return Response.json(serviceView(), { status: 201 });
				},
			}),
		);
		const user = userEvent.setup();
		await openDialog();

		expect(screen.getByRole("radio", { name: /Valkey/ })).toBeChecked();
		expect(screen.getByLabelText("Name")).toHaveValue("valkey");
		// A key-value engine has no named database.
		expect(screen.queryByLabelText("Database name")).not.toBeInTheDocument();

		await user.click(createButton());

		expect(await screen.findByText("Created valkey")).toBeInTheDocument();
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.path).toBe("/environments/env-1/databases");
		expect(post?.body).toEqual({
			engine: "valkey",
			name: "valkey",
			version: "9",
			serverId: "srv-1",
		});
		expect(seen[0]?.key).toMatch(/^[0-9a-f-]{36}$/);
		// The dialog closes after a successful create.
		await waitFor(() =>
			expect(
				screen.queryByRole("button", { name: "Create database" }),
			).not.toBeInTheDocument(),
		);
		expect(api.unmatched).toEqual([]);
	});

	test("a request for an engine preselects it and shows its database name field", async () => {
		stubApi(routes());
		await openDialog({ engine: "postgres" });
		expect(screen.getByRole("radio", { name: /PostgreSQL/ })).toBeChecked();
		expect(screen.getByLabelText("Database name")).toHaveAttribute(
			"placeholder",
			"app",
		);
		expect(screen.getByLabelText("Name")).toHaveValue("postgres");
		expect(
			screen.getByText("The reference relational database."),
		).toBeInTheDocument();
	});

	test("sends the typed database name and a typed service name", async () => {
		const api = stubApi(
			routes({
				"POST /environments/:id/databases": {
					status: 201,
					body: serviceView({ name: "orders-db" }),
				},
			}),
		);
		const user = userEvent.setup();
		await openDialog({ engine: "postgres" });
		const name = screen.getByLabelText("Name");
		await user.clear(name);
		await user.type(name, "  orders-db ");
		await user.type(screen.getByLabelText("Database name"), "orders");
		await user.click(createButton());

		expect(await screen.findByText("Created orders-db")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			engine: "postgres",
			name: "orders-db",
			version: "18",
			serverId: "srv-1",
			databaseName: "orders",
		});
	});

	test("an invalid database name is flagged and blocks Create", async () => {
		const api = stubApi(routes());
		const user = userEvent.setup();
		await openDialog({ engine: "postgres" });
		const field = screen.getByLabelText("Database name");
		await user.type(field, "Bad-Name");

		expect(
			screen.getByText(/Lowercase letters, digits and underscores/),
		).toBeInTheDocument();
		expect(field).toHaveAttribute("aria-invalid", "true");
		expect(createButton()).toBeDisabled();
		expect(screen.getByText("The database name is not valid.")).toBeVisible();

		await user.clear(field);
		await user.type(field, "good_name_2");
		expect(createButton()).toBeEnabled();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});

	test("an empty name blocks Create with the reason", async () => {
		stubApi(routes());
		const user = userEvent.setup();
		await openDialog();
		await user.clear(screen.getByLabelText("Name"));
		expect(createButton()).toBeDisabled();
		expect(screen.getByText("Name the database.")).toBeInTheDocument();
	});

	test("a licence-gated engine stays blocked until the licence is ticked, then sends acceptLicense", async () => {
		const api = stubApi(
			routes({
				"POST /environments/:id/databases": {
					status: 201,
					body: serviceView({ name: "mongodb" }),
				},
			}),
		);
		const user = userEvent.setup();
		await openDialog();
		expect(
			screen.queryByText(/Source-available licence: SSPL-1.0/),
		).not.toBeInTheDocument();

		await user.click(screen.getByRole("radio", { name: /MongoDB/ }));
		expect(
			screen.getByText("Source-available licence: SSPL-1.0"),
		).toBeInTheDocument();
		expect(screen.getByText(/not OSI-approved/)).toBeInTheDocument();
		expect(createButton()).toBeDisabled();
		expect(
			screen.getByText("Confirm that you have reviewed the licence."),
		).toBeInTheDocument();

		const ack = screen.getByRole("checkbox", {
			name: "I have reviewed the licence",
		});
		await user.click(ack);
		expect(createButton()).toBeEnabled();
		await user.click(createButton());

		expect(await screen.findByText("Created mongodb")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			engine: "mongodb",
			name: "mongodb",
			version: "8.0",
			serverId: "srv-1",
			acceptLicense: true,
		});
	});

	test("switching engine forgets the licence tick", async () => {
		stubApi(routes());
		const user = userEvent.setup();
		await openDialog();
		await user.click(screen.getByRole("radio", { name: /MongoDB/ }));
		await user.click(
			screen.getByRole("checkbox", { name: "I have reviewed the licence" }),
		);
		expect(createButton()).toBeEnabled();
		await user.click(screen.getByRole("radio", { name: /Redis/ }));
		expect(
			screen.getByRole("checkbox", { name: "I have reviewed the licence" }),
		).not.toBeChecked();
		expect(createButton()).toBeDisabled();
	});

	test("a copyleft engine states its licence without gating Create", async () => {
		stubApi(routes());
		const user = userEvent.setup();
		await openDialog();
		await user.click(screen.getByRole("radio", { name: /MySQL/ }));
		expect(
			screen.getByText("Licence: GPL-2.0 (Copyleft licence)."),
		).toBeInTheDocument();
		expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
		expect(createButton()).toBeEnabled();
	});

	test("the version picker offers the engine's versions and sends the chosen one", async () => {
		const api = stubApi(
			routes({
				"POST /environments/:id/databases": {
					status: 201,
					body: serviceView({ name: "mysql" }),
				},
			}),
		);
		const user = userEvent.setup();
		await openDialog();
		await user.click(screen.getByRole("radio", { name: /MySQL/ }));
		const version = screen.getByRole("combobox", { name: "Version" });
		expect(version).toHaveTextContent("MySQL 8.4 LTS");

		await user.click(version);
		expect(
			await screen.findByRole("option", { name: "MySQL 8.4 LTS · default" }),
		).toBeInTheDocument();
		await user.click(screen.getByRole("option", { name: "MySQL 9.7" }));
		await waitFor(() => expect(version).toHaveTextContent("MySQL 9.7"));
		await user.click(createButton());

		expect(await screen.findByText("Created mysql")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.body).toMatchObject({
			engine: "mysql",
			version: "9.7",
		});
	});

	test("shows the notes of the chosen version", async () => {
		stubApi(routes());
		await openDialog({ engine: "postgres" });
		expect(
			screen.getByText(/data lives under \/var\/lib\/postgresql\/18\/docker/),
		).toBeInTheDocument();
	});

	test("the PostGIS flavor narrows the versions and is sent as flavor", async () => {
		const api = stubApi(
			routes({
				"POST /environments/:id/databases": {
					status: 201,
					body: serviceView({ name: "postgres" }),
				},
			}),
		);
		const user = userEvent.setup();
		await openDialog({ engine: "postgres" });
		expect(screen.getByRole("combobox", { name: "Version" })).toHaveTextContent(
			"PostgreSQL 18",
		);

		await user.click(screen.getByRole("button", { name: "PostGIS" }));
		// PostGIS is not offered for 18: the newest it offers is preselected.
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Version" }),
			).toHaveTextContent("PostgreSQL 17"),
		);
		await user.click(screen.getByRole("combobox", { name: "Version" }));
		expect(
			await screen.findByRole("option", { name: "PostgreSQL 16" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("option", { name: /PostgreSQL 18/ }),
		).not.toBeInTheDocument();
		await user.keyboard("{Escape}");
		await user.click(createButton());

		expect(await screen.findByText("Created postgres")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.body).toMatchObject({
			engine: "postgres",
			flavor: "postgis",
			version: "17",
		});
	});

	test("the name defaults to the first free one and a taken name blocks Create", async () => {
		stubApi(
			routes({
				"GET /environments/:id/services": existing("valkey", "valkey-2"),
			}),
		);
		const user = userEvent.setup();
		await openDialog();
		await waitFor(() =>
			expect(screen.getByLabelText("Name")).toHaveValue("valkey-3"),
		);
		expect(createButton()).toBeEnabled();

		const name = screen.getByLabelText("Name");
		await user.clear(name);
		await user.type(name, "VALKEY");
		expect(
			screen.getByText(/A service named VALKEY already exists in Staging/),
		).toBeInTheDocument();
		expect(createButton()).toBeDisabled();
		expect(
			screen.getByText("A service named VALKEY already exists here."),
		).toBeInTheDocument();
	});

	test("without a usable server it says so, links to enrolment and blocks Create", async () => {
		stubApi(routes());
		await openDialog(undefined, {
			servers: [
				server({ id: "r", name: "gone", status: "revoked" }),
				server({ id: "p", name: "waiting", status: "pending" }),
			],
		});
		expect(screen.getByText(/No server is enrolled yet/)).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "Add a server" })).toHaveAttribute(
			"href",
			expect.stringContaining("/servers"),
		);
		expect(screen.queryByRole("radiogroup", { name: "Server" })).toBeNull();
		expect(createButton()).toBeDisabled();
		expect(screen.getByText("Choose a server.")).toBeInTheDocument();
	});

	test("prefers an online server and lets the user choose another; revoked ones are not listed", async () => {
		const api = stubApi(
			routes({
				"POST /environments/:id/databases": {
					status: 201,
					body: serviceView(),
				},
			}),
		);
		const user = userEvent.setup();
		await openDialog(undefined, {
			servers: [
				server({ id: "off", name: "offline-box", status: "offline" }),
				server({ id: "on", name: "online-box" }),
				server({ id: "rev", name: "revoked-box", status: "revoked" }),
				server({ id: "drain", name: "draining-box", drainState: "draining" }),
			],
		});
		const group = screen.getByRole("radiogroup", { name: "Server" });
		expect(within(group).queryByText("revoked-box")).not.toBeInTheDocument();
		expect(
			within(group).getByRole("radio", { name: /online-box/ }),
		).toBeChecked();
		expect(
			within(group).getByRole("radio", { name: /draining-box/ }),
		).toBeDisabled();

		await user.click(within(group).getByRole("radio", { name: /offline-box/ }));
		await user.click(createButton());
		await screen.findByText("Created valkey");
		expect(api.calls.find((c) => c.method === "POST")?.body).toMatchObject({
			serverId: "off",
		});
	});

	test("an engine catalog that fails to load can be retried", async () => {
		let attempts = 0;
		stubApi({
			"GET /database-engines": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "catalog down", code: "x" },
							{ status: 500 },
						)
					: Response.json(catalogBody);
			},
			"GET /environments/:id/services": existing(),
		});
		const user = userEvent.setup();
		createActions.open("database");
		await renderWithProviders(<Host />);
		expect(
			await screen.findByText("The engine catalog did not load"),
		).toBeInTheDocument();
		expect(screen.getByText(/catalog down/)).toBeInTheDocument();
		expect(createButton()).toBeDisabled();
		// Nothing says "Choose an engine" while the catalog has not answered.
		expect(screen.queryByText("Choose an engine.")).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Retry" }));
		expect(
			await screen.findByRole("radiogroup", { name: "Database engine" }),
		).toBeInTheDocument();
		expect(screen.getByRole("radio", { name: /Valkey/ })).toBeChecked();
	});

	test("shows a loading placeholder while the catalog is pending", async () => {
		stubApi({
			"GET /database-engines": () => new Promise<Response>(() => {}),
			"GET /environments/:id/services": existing(),
		});
		createActions.open("database");
		await renderWithProviders(<Host />);
		expect(screen.getByRole("status")).toBeInTheDocument();
		expect(createButton()).toBeDisabled();
		expect(
			screen.queryByRole("radiogroup", { name: "Database engine" }),
		).not.toBeInTheDocument();
	});

	test("a refused create keeps the dialog open and shows the server's reason", async () => {
		const api = stubApi(
			routes({
				"POST /environments/:id/databases": problem(
					409,
					"the server is out of disk",
					"conflict",
				),
			}),
		);
		const user = userEvent.setup();
		await openDialog();
		await user.click(createButton());
		expect(
			await screen.findByText("the server is out of disk"),
		).toBeInTheDocument();
		expect(screen.queryByText(/^Created /)).not.toBeInTheDocument();
		expect(createButton()).toBeEnabled();
		// Retrying replays the same idempotency key.
		expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1);
	});

	test("a retry after a refusal reuses the Idempotency-Key", async () => {
		const keys: (string | null)[] = [];
		let attempts = 0;
		stubApi(
			routes({
				"POST /environments/:id/databases": (request) => {
					keys.push(request.headers.get("Idempotency-Key"));
					attempts += 1;
					return attempts === 1
						? Response.json(
								{ status: 502, title: "x", detail: "lost", code: "x" },
								{ status: 502 },
							)
						: Response.json(serviceView(), { status: 201 });
				},
			}),
		);
		const user = userEvent.setup();
		await openDialog();
		await user.click(createButton());
		await screen.findByText("lost");
		await user.click(createButton());
		await screen.findByText("Created valkey");
		expect(keys).toHaveLength(2);
		expect(keys[0]).toBe(keys[1]);
	});

	test("Cancel closes the dialog without sending anything", async () => {
		const api = stubApi(routes());
		const user = userEvent.setup();
		await openDialog();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(
				screen.queryByRole("button", { name: "Create database" }),
			).not.toBeInTheDocument(),
		);
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});

	test("in a non-production environment the toast deploys the new database", async () => {
		const api = stubApi(
			routes({
				"POST /environments/:id/databases": {
					status: 201,
					body: serviceView(),
				},
				"POST /services/:id/deployments": {
					status: 202,
					body: make("DeploymentView", { status: "queued" }),
				},
			}),
		);
		const user = userEvent.setup();
		await openDialog();
		await user.click(createButton());
		expect(
			await screen.findByText("Nothing runs until it is deployed."),
		).toBeInTheDocument();
		await user.click(
			await screen.findByRole(
				"button",
				{ name: "Deploy now" },
				{ timeout: 4000 },
			),
		);
		expect(await screen.findByText("Deploying valkey")).toBeInTheDocument();
		const deploy = api.calls.find((c) => c.path.endsWith("/deployments"));
		expect(deploy?.path).toBe("/services/svc-db/deployments");
		expect(deploy?.method).toBe("POST");
	});

	test("Deploy now reports a failed deploy", async () => {
		stubApi(
			routes({
				"POST /environments/:id/databases": {
					status: 201,
					body: serviceView(),
				},
				"POST /services/:id/deployments": problem(
					422,
					"no server accepts it",
					"unprocessable",
				),
			}),
		);
		const user = userEvent.setup();
		await openDialog();
		await user.click(createButton());
		await user.click(
			await screen.findByRole(
				"button",
				{ name: "Deploy now" },
				{ timeout: 4000 },
			),
		);
		expect(
			await screen.findByText("Could not deploy valkey"),
		).toBeInTheDocument();
		expect(screen.getByText("no server accepts it")).toBeInTheDocument();
	});

	test("in production the toast offers the staged review instead of deploying", async () => {
		const opened: OpenReviewOptions[] = [];
		const stop = onOpenStagedReview((options) => opened.push(options));
		const api = stubApi(
			routes({
				"POST /environments/:id/databases": {
					status: 201,
					body: serviceView(),
				},
			}),
		);
		const user = userEvent.setup();
		await openDialog(undefined, {
			env: { id: "env-1", name: "Production", isProduction: true },
		});
		await user.click(createButton());
		expect(
			await screen.findByText(
				"Staged in Production. Nothing runs until you review and deploy.",
			),
		).toBeInTheDocument();
		await user.click(
			await screen.findByRole("button", { name: "Review & deploy" }),
		);
		stop();
		expect(opened).toHaveLength(1);
		expect(api.calls.filter((c) => c.path.endsWith("/deployments"))).toEqual(
			[],
		);
	});

	test("a dropped position is saved for the new node", async () => {
		const placed: { id: string; at: { x: number; y: number } }[] = [];
		const unregister = registerPlacer((id, at) => placed.push({ id, at }));
		stubApi(
			routes({
				"POST /environments/:id/databases": {
					status: 201,
					body: serviceView({ id: "svc-new" }),
				},
			}),
		);
		const user = userEvent.setup();
		await openDialog({ position: { x: 120, y: 40 } });
		await user.click(createButton());
		await screen.findByText("Created valkey");
		unregister();
		expect(placed).toEqual([{ id: "svc-new", at: { x: 120, y: 40 } }]);
	});

	test("a position that cannot be saved is reported but the database is still created", async () => {
		stubApi(
			routes({
				"POST /environments/:id/databases": {
					status: 201,
					body: serviceView({ id: "svc-new" }),
				},
				"GET /services/:id": problem(500, "canvas down", "internal"),
			}),
		);
		const user = userEvent.setup();
		await openDialog({ position: { x: 1, y: 2 } });
		await user.click(createButton());
		expect(
			await screen.findByText("valkey was placed automatically"),
		).toBeInTheDocument();
		expect(await screen.findByText("Created valkey")).toBeInTheDocument();
	});
});
