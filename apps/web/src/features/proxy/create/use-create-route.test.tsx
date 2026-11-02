import { afterEach, describe, expect, test } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { servicesQuery } from "#/api/hooks";
import { registerPlacer } from "#/features/canvas/use-layout-persistence";
import { onOpenStagedReview } from "#/features/deploy-button/open-review";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { routeCreate, useRouteCreateRequest } from "../create-store";
import { production, serviceView, staging } from "./test-fixtures";
import { useCreateRoute } from "./use-create-route";

const BODY = {
	name: "ha",
	serverId: "s1",
	protocol: "http" as const,
	upstreams: [{ address: "10.0.0.5:8123" }],
};

function Harness({
	environment,
	position,
}: {
	environment: typeof production;
	position?: { x: number; y: number };
}) {
	const create = useCreateRoute(environment, position);
	const request = useRouteCreateRequest();
	// Keeps a services list alive so a refetch after the create is observable.
	useQuery(servicesQuery(environment.id));
	return (
		<div>
			<button type="button" onClick={() => create.mutate(BODY)}>
				create
			</button>
			<p data-testid="dialog">{request ? "open" : "closed"}</p>
			{create.isError ? <p role="alert">{create.error.message}</p> : null}
		</div>
	);
}

const created = serviceView({
	id: "svc-ha",
	name: "ha",
	kind: "route",
	serverId: "s1",
});

afterEach(() => {
	routeCreate.close();
});

function baseRoutes(env: string, extra: Parameters<typeof stubApi>[0] = {}) {
	const idem: (string | null)[] = [];
	const api = stubApi({
		[`GET /environments/${env}/services`]: { body: { items: [] } },
		[`POST /environments/${env}/routes`]: (request: Request) => {
			idem.push(request.headers.get("Idempotency-Key"));
			return Response.json(created, { status: 201 });
		},
		...extra,
	});
	return { api, idem };
}

describe("useCreateRoute", () => {
	test("posts the route to the environment, closes the dialog and refreshes services", async () => {
		const { api, idem } = baseRoutes("env-stg");
		const user = userEvent.setup();
		routeCreate.open({ mode: "external" });
		await renderWithProviders(<Harness environment={staging} />);
		await waitFor(() =>
			expect(api.calls.filter((c) => c.method === "GET")).toHaveLength(1),
		);
		await user.click(screen.getByRole("button", { name: "create" }));

		expect(await screen.findByText("Created ha")).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/environments/env-stg/routes");
		expect(post?.body).toEqual(BODY);
		expect(idem[0]).toMatch(/^[0-9a-f-]{36}$/);
		expect(screen.getByTestId("dialog")).toHaveTextContent("closed");
		await waitFor(() =>
			expect(
				api.calls.filter((c) => c.method === "GET").length,
			).toBeGreaterThan(1),
		);
		expect(api.unmatched).toEqual([]);
	});

	test("a non-production route says it is not published until deployed and offers Deploy now", async () => {
		const deployKeys: (string | null)[] = [];
		const { api } = baseRoutes("env-stg", {
			"POST /services/:id/deployments": (request) => {
				deployKeys.push(request.headers.get("Idempotency-Key"));
				return Response.json({ id: "dep-1" }, { status: 202 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness environment={staging} />);
		await user.click(screen.getByRole("button", { name: "create" }));
		expect(
			await screen.findByText("Nothing is published until it is deployed."),
		).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Deploy now" }));
		expect(await screen.findByText("Deploying ha")).toBeInTheDocument();
		expect(
			api.calls.find((c) => c.path === "/services/svc-ha/deployments")?.method,
		).toBe("POST");
		expect(deployKeys[0]).toMatch(/^[0-9a-f-]{36}$/);
	});

	test("a failed Deploy now raises an error toast with the server's message", async () => {
		baseRoutes("env-stg", {
			"POST /services/:id/deployments": problem(409, {
				code: "deploy_in_progress",
				detail: "A deploy is already running.",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness environment={staging} />);
		await user.click(screen.getByRole("button", { name: "create" }));
		await user.click(await screen.findByRole("button", { name: "Deploy now" }));
		expect(await screen.findByText("Could not deploy ha")).toBeInTheDocument();
		expect(
			screen.getByText("A deploy is already running."),
		).toBeInTheDocument();
	});

	test("a production route is staged: the action opens the staged review instead of deploying", async () => {
		const { api } = baseRoutes("env-prod");
		const opened: unknown[] = [];
		const off = onOpenStagedReview((options) => opened.push(options));
		try {
			const user = userEvent.setup();
			await renderWithProviders(<Harness environment={production} />);
			await user.click(screen.getByRole("button", { name: "create" }));
			expect(
				await screen.findByText(
					"Staged in production. Nothing is published until you review and deploy.",
				),
			).toBeInTheDocument();
			await user.click(screen.getByRole("button", { name: "Review & deploy" }));
			expect(opened).toEqual([{}]);
			expect(api.calls.some((c) => c.path.endsWith("/deployments"))).toBe(
				false,
			);
		} finally {
			off();
		}
	});

	test("with a drop position and a canvas on screen, the canvas places the node", async () => {
		const placed: unknown[] = [];
		const release = registerPlacer((id, position) =>
			placed.push([id, position]),
		);
		try {
			const { api } = baseRoutes("env-stg");
			const user = userEvent.setup();
			await renderWithProviders(
				<Harness environment={staging} position={{ x: 120, y: 80 }} />,
			);
			await user.click(screen.getByRole("button", { name: "create" }));
			await screen.findByText("Created ha");
			expect(placed).toEqual([["svc-ha", { x: 120, y: 80 }]]);
			expect(api.calls.some((c) => c.path.includes("/canvas"))).toBe(false);
		} finally {
			release();
		}
	});

	test("without a canvas the layout is written directly, retrying once on a stale If-Match", async () => {
		const puts: { match: string | null; body: unknown }[] = [];
		let versions = 0;
		baseRoutes("env-stg", {
			"GET /services/:id": {
				body: { ...created, projectId: "proj-1", environmentId: "env-stg" },
			},
			"GET /projects/:p/environments/:e/canvas": () => {
				versions += 1;
				return Response.json({
					version: versions,
					layout: { nodes: { other: { x: 1, y: 2 } }, groups: [] },
				});
			},
			"PUT /projects/:p/environments/:e/canvas": async (request) => {
				puts.push({
					match: request.headers.get("If-Match"),
					body: await request.clone().json(),
				});
				return puts.length === 1
					? Response.json(
							{ code: "generation_conflict", title: "stale" },
							{ status: 412 },
						)
					: Response.json({ version: 3, layout: { nodes: {}, groups: [] } });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness environment={staging} position={{ x: 7, y: 9 }} />,
		);
		await user.click(screen.getByRole("button", { name: "create" }));
		await screen.findByText("Created ha");
		expect(puts.map((p) => p.match)).toEqual(['"1"', '"2"']);
		const retried = puts[1];
		if (!retried) throw new Error("expected a retried PUT");
		const retriedBody: { layout: { nodes: unknown } } = retried.body as {
			layout: { nodes: unknown };
		};
		expect(retriedBody.layout.nodes).toEqual({
			other: { x: 1, y: 2 },
			"svc-ha": { x: 7, y: 9 },
		});
		expect(screen.queryByText(/placed automatically/)).not.toBeInTheDocument();
	});

	test("when the position cannot be saved the route is still created and the user is told", async () => {
		baseRoutes("env-stg", {
			"GET /services/:id": problem(500, { title: "boom" }),
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness environment={staging} position={{ x: 7, y: 9 }} />,
		);
		await user.click(screen.getByRole("button", { name: "create" }));
		expect(
			await screen.findByText("ha was placed automatically"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Its position could not be saved where you dropped it."),
		).toBeInTheDocument();
		expect(screen.getByText("Created ha")).toBeInTheDocument();
	});

	test("a rejected create keeps the dialog open and exposes the error", async () => {
		const api = stubApi({
			"GET /environments/env-stg/services": { body: { items: [] } },
			"POST /environments/env-stg/routes": problem(422, {
				code: "upstream_denied",
				detail: "Loopback upstreams are not allowed.",
			}),
		});
		routeCreate.open({ mode: "external" });
		const user = userEvent.setup();
		await renderWithProviders(<Harness environment={staging} />);
		await user.click(screen.getByRole("button", { name: "create" }));
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"Loopback upstreams are not allowed.",
		);
		expect(screen.getByTestId("dialog")).toHaveTextContent("open");
		expect(screen.queryByText("Created ha")).not.toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1);
	});

	test("a retry after a failure replays the same idempotency key", async () => {
		const keys: (string | null)[] = [];
		stubApi({
			"GET /environments/env-stg/services": { body: { items: [] } },
			"POST /environments/env-stg/routes": (request) => {
				keys.push(request.headers.get("Idempotency-Key"));
				return keys.length === 1
					? Response.json({ title: "lost" }, { status: 500 })
					: Response.json(created, { status: 201 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness environment={staging} />);
		await user.click(screen.getByRole("button", { name: "create" }));
		await screen.findByRole("alert");
		await user.click(screen.getByRole("button", { name: "create" }));
		await screen.findByText("Created ha");
		expect(keys).toHaveLength(2);
		expect(keys[0]).toBe(keys[1]);
	});
});
