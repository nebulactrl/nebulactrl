import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { LinkPreview } from "#/api/gen/types.gen";
import { onOpenStagedReview } from "#/features/deploy-button/open-review";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ConnectPopover, type ConnectRequest } from "./connect-popover";
import { preview, proposal, service } from "./test-fixtures";

// Builds a literal `${NAME}` placeholder (the escaped `$` keeps it from interpolating).
const ref = (name: string) => `\${${name}}`;

const web = service("web");
const worker = service("worker");
const db = service("postgres", { kind: "database" });
const services = [web, worker, db];

function request(
	consumerIds: string[] = ["web"],
	patch: Partial<ConnectRequest> = {},
): ConnectRequest {
	return {
		id: 1,
		providerId: "postgres",
		consumerIds,
		anchor: { x: 120, y: 80 },
		...patch,
	};
}

async function show(
	props: { request?: ConnectRequest | null; onClose?: () => void } = {},
) {
	const onClose = props.onClose ?? (() => {});
	return renderWithProviders(
		<ConnectPopover
			request={props.request === undefined ? request() : props.request}
			environmentId="env-1"
			services={services}
			onClose={onClose}
		/>,
	);
}

function previews(byConsumer: Record<string, LinkPreview>) {
	return {
		"GET /environments/:id/link-proposals": (req: Request) => {
			const from = new URL(req.url).searchParams.get("from") ?? "";
			const body = byConsumer[from];
			return body
				? Response.json(body)
				: Response.json({ detail: "no preview" }, { status: 500 });
		},
	};
}

const dbPreview = preview({
	proposedVariables: [
		proposal("DATABASE_URL", { secret: true, description: "Connection URL" }),
		proposal("DATABASE_HOST"),
		proposal("DATABASE_PORT", { recommended: false }),
	],
});

const stageRoutes = {
	"PUT /services/:id/variables": { body: make("VariableList", {}) },
	"POST /environments/:id/links": { status: 201, body: make("LinkResult", {}) },
};

describe("ConnectPopover", () => {
	it("renders nothing without a request", async () => {
		stubApi({});
		await show({ request: null });
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(screen.queryByText(/Stage/)).not.toBeInTheDocument();
	});

	it("shows the recommended references ticked, the rest behind a count", async () => {
		const user = userEvent.setup();
		const api = stubApi(previews({ web: dbPreview }));
		await show();
		const dialog = await screen.findByRole("dialog", {
			name: "Connect services",
		});
		expect(
			await within(dialog).findByLabelText("Stage DATABASE_URL on web"),
		).toBeChecked();
		expect(screen.getByLabelText("Stage DATABASE_HOST on web")).toBeChecked();
		expect(screen.queryByLabelText("Stage DATABASE_PORT on web")).toBeNull();
		expect(
			screen.getByText(
				"Stages these variables on web. They apply on its next deploy.",
			),
		).toBeVisible();
		expect(
			screen.getByText("Resolves to a secret.", { exact: false }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Stage 2 variables" }),
		).toBeEnabled();
		expect(api.calls[0]?.query.get("from")).toBe("web");
		expect(api.calls[0]?.query.get("to")).toBe("postgres");

		await user.click(screen.getByRole("button", { name: "1 more reference" }));
		expect(
			screen.getByLabelText("Stage DATABASE_PORT on web"),
		).not.toBeChecked();
		expect(screen.queryByRole("button", { name: /more reference/ })).toBeNull();
		// Ticking it adds it to the count.
		await user.click(screen.getByLabelText("Stage DATABASE_PORT on web"));
		expect(
			screen.getByRole("button", { name: "Stage 3 variables" }),
		).toBeVisible();
	});

	it("stages the ticked variables under their chosen names, then closes", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...previews({ web: dbPreview }), ...stageRoutes });
		const closed: string[] = [];
		const reviews: unknown[] = [];
		const stop = onOpenStagedReview((options) => reviews.push(options));
		await show({ onClose: () => closed.push("closed") });
		const name = await screen.findByLabelText("Name of DATABASE_HOST on web");
		await user.clear(name);
		await user.type(name, "PGHOST");
		await user.click(screen.getByLabelText("Stage DATABASE_URL on web"));
		await user.click(screen.getByRole("button", { name: "Stage 1 variable" }));
		await waitFor(() => expect(closed).toEqual(["closed"]));
		const puts = api.calls.filter((call) => call.method === "PUT");
		expect(puts).toHaveLength(1);
		expect(puts[0]?.path).toBe("/services/web/variables");
		expect(puts[0]?.body).toEqual({
			variables: [
				{
					key: "PGHOST",
					value: ref("{postgres.DATABASE_HOST}"),
					isSecret: false,
				},
			],
		});
		// Variables carry the link: no cable is drawn by hand.
		expect(api.calls.some((call) => call.method === "POST")).toBe(false);
		expect(
			await screen.findByText("Staged 1 variable for postgres"),
		).toBeVisible();
		expect(
			screen.getByText("web read postgres after the next deploy."),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Review" }));
		expect(reviews).toEqual([{}]);
		stop();
	});

	it("refuses a name that is invalid or used twice, and says why", async () => {
		const user = userEvent.setup();
		stubApi(previews({ web: dbPreview }));
		await show();
		const host = await screen.findByLabelText("Name of DATABASE_HOST on web");
		await user.clear(host);
		await user.type(host, "1BAD");
		expect(
			screen.getByText(
				"Use letters, digits and underscores; do not start with a digit.",
			),
		).toBeVisible();
		expect(host).toHaveAttribute("aria-invalid", "true");
		expect(
			screen.getByRole("button", { name: "Stage 2 variables" }),
		).toBeDisabled();
		await user.clear(host);
		await user.type(host, "DATABASE_URL");
		expect(screen.getAllByText("Another row uses this name.")).toHaveLength(2);
		expect(
			screen.getByRole("button", { name: "Stage 2 variables" }),
		).toBeDisabled();
		await user.clear(host);
		expect(screen.getByText("Give it a name.")).toBeVisible();
		await user.type(host, "PGHOST");
		expect(
			screen.getByRole("button", { name: "Stage 2 variables" }),
		).toBeEnabled();
	});

	it("disables the name of a row that is not ticked", async () => {
		const user = userEvent.setup();
		stubApi(previews({ web: dbPreview }));
		await show();
		await user.click(await screen.findByLabelText("Stage DATABASE_URL on web"));
		expect(screen.getByLabelText("Name of DATABASE_URL on web")).toBeDisabled();
	});

	it("tells what the consumer already holds", async () => {
		stubApi(
			previews({
				web: preview({
					existingKeys: ["DATABASE_URL", "DATABASE_HOST"],
					proposedVariables: [
						proposal("DATABASE_URL", { status: "unchanged" }),
						proposal("DATABASE_HOST", { status: "replaces" }),
					],
				}),
			}),
		);
		await show();
		await screen.findByLabelText("Stage DATABASE_URL on web");
		// An identical reference is not ticked: nothing would change.
		expect(
			screen.getByLabelText("Stage DATABASE_URL on web"),
		).not.toBeChecked();
		expect(screen.getByText("Already set.")).toBeVisible();
		expect(screen.getByLabelText("Stage DATABASE_HOST on web")).toBeChecked();
		expect(
			screen.getByText("web already has DATABASE_HOST; this replaces it."),
		).toBeVisible();
	});

	it("links by hand when nothing is ticked", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...previews({ web: dbPreview }), ...stageRoutes });
		const closed: string[] = [];
		await show({ onClose: () => closed.push("x") });
		await user.click(await screen.findByLabelText("Stage DATABASE_URL on web"));
		await user.click(screen.getByLabelText("Stage DATABASE_HOST on web"));
		await user.click(screen.getByRole("button", { name: "Link only" }));
		await waitFor(() => expect(closed).toEqual(["x"]));
		expect(api.calls.some((call) => call.method === "PUT")).toBe(false);
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.path).toBe("/environments/env-1/links");
		expect(post?.body).toEqual({
			fromServiceId: "web",
			toServiceId: "postgres",
		});
		expect(await screen.findByText("Linked to postgres")).toBeVisible();
		expect(
			screen.getByText("The cable is drawn; no variable was added."),
		).toBeVisible();
		expect(screen.queryByRole("button", { name: "Review" })).toBeNull();
	});

	it("offers nothing to do for a pair that is already linked with nothing to add", async () => {
		stubApi(
			previews({
				web: preview({
					linked: true,
					existingKeys: ["DATABASE_URL"],
					proposedVariables: [
						proposal("DATABASE_URL", { status: "unchanged" }),
					],
				}),
			}),
		);
		await show();
		expect(
			await screen.findByText("Already connected. Add more references below."),
		).toBeVisible();
		expect(
			screen.getByRole("button", { name: "Nothing to stage" }),
		).toBeDisabled();
	});

	it("shows the previews' warnings once", async () => {
		stubApi(
			previews({
				web: preview({ warnings: ["postgres is stopped"] }),
				worker: preview({ warnings: ["postgres is stopped", "Other"] }),
			}),
		);
		await show({ request: request(["web", "worker"]) });
		expect(await screen.findAllByText("postgres is stopped")).toHaveLength(1);
		expect(screen.getByText("Other")).toBeVisible();
	});

	it("shows why the preview failed and offers no confirmation", async () => {
		stubApi({
			"GET /environments/:id/link-proposals": problem(404, {
				code: "not_found",
				detail: "postgres is gone",
			}),
		});
		await show();
		expect(await screen.findByText("postgres is gone")).toBeVisible();
		expect(screen.getByRole("button", { name: "Link only" })).toBeDisabled();
	});

	it("stages on each consumer of a database suggestion", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...previews({
				web: dbPreview,
				worker: preview({ proposedVariables: [proposal("DATABASE_URL")] }),
			}),
			...stageRoutes,
		});
		const closed: string[] = [];
		await show({
			request: request(["web", "worker"]),
			onClose: () => closed.push("x"),
		});
		expect(await screen.findByText("2 services")).toBeVisible();
		expect(
			screen.getByText(/postgres is not wired into these services yet/),
		).toBeVisible();
		expect(
			screen.getByRole("region", { name: "Variables for web" }),
		).toBeVisible();
		expect(
			screen.getByRole("region", { name: "Variables for worker" }),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Stage 3 variables" }));
		await waitFor(() => expect(closed).toEqual(["x"]));
		expect(
			api.calls
				.filter((call) => call.method === "PUT")
				.map((call) => call.path),
		).toEqual(["/services/web/variables", "/services/worker/variables"]);
		expect(
			await screen.findByText("Staged 3 variables for postgres"),
		).toBeVisible();
		expect(
			screen.getByText("The services read postgres after the next deploy."),
		).toBeVisible();
	});

	it("stays open and says what failed when writing a variable fails", async () => {
		const user = userEvent.setup();
		stubApi({
			...previews({ web: dbPreview }),
			"PUT /services/:id/variables": problem(409, {
				code: "generation_conflict",
				detail: "Someone else changed web",
			}),
		});
		const closed: string[] = [];
		await show({ onClose: () => closed.push("x") });
		await user.click(
			await screen.findByRole("button", { name: "Stage 2 variables" }),
		);
		expect(await screen.findByText("Someone else changed web")).toBeVisible();
		expect(closed).toEqual([]);
		expect(
			screen.getByRole("button", { name: "Stage 2 variables" }),
		).toBeEnabled();
	});

	it("closes with Cancel without writing anything", async () => {
		const user = userEvent.setup();
		const api = stubApi(previews({ web: dbPreview }));
		const closed: string[] = [];
		await show({ onClose: () => closed.push("x") });
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(closed).toEqual(["x"]);
		expect(api.calls.every((call) => call.method === "GET")).toBe(true);
	});

	describe("over the mesh", () => {
		const crossing = (extra: Partial<LinkPreview["mesh"]> = {}) =>
			preview({
				mesh: {
					crossServer: true,
					devicesAdded: 1,
					blockers: null,
					links: [
						{
							fromServerId: "s1",
							fromServerName: "app-01",
							toServerId: "s2",
							toServerName: "db-01",
							providers: ["postgres"],
							purpose: "service",
							state: "new",
						},
					],
					...extra,
				},
			});

		it("needs the new device accepted before a link without variables", async () => {
			const user = userEvent.setup();
			const api = stubApi({ ...previews({ web: crossing() }), ...stageRoutes });
			const closed: string[] = [];
			await show({ onClose: () => closed.push("x") });
			expect(await screen.findByText("app-01 → db-01")).toBeVisible();
			const accept = screen.getByRole("checkbox", {
				name: "Connect over the mesh: adds 1 device",
			});
			expect(
				screen.getByRole("button", { name: "Link over the mesh" }),
			).toBeDisabled();
			await user.click(accept);
			await user.click(
				screen.getByRole("button", { name: "Link over the mesh" }),
			);
			await waitFor(() => expect(closed).toEqual(["x"]));
			expect(api.calls.find((call) => call.method === "POST")?.body).toEqual({
				fromServiceId: "web",
				toServiceId: "postgres",
				acceptMeshChanges: true,
			});
		});

		it("does not draw the cable itself when variables carry the link", async () => {
			const user = userEvent.setup();
			const api = stubApi({
				...previews({
					web: preview({
						...crossing(),
						proposedVariables: [proposal("DATABASE_URL")],
					}),
				}),
				...stageRoutes,
			});
			const closed: string[] = [];
			await show({ onClose: () => closed.push("x") });
			await user.click(
				await screen.findByRole("button", { name: "Stage 1 variable" }),
			);
			await waitFor(() => expect(closed).toEqual(["x"]));
			expect(api.calls.some((call) => call.method === "POST")).toBe(false);
		});

		it("stops on a blocker and offers no acceptance", async () => {
			stubApi(
				previews({
					web: crossing({
						blockers: [
							{
								code: "mesh_server_required",
								message: "db-01 is not on the mesh.",
								serverId: "s2",
								serverName: "db-01",
							},
						],
					}),
				}),
			);
			await show();
			expect(
				await screen.findByText(/db-01 is not on the mesh\./),
			).toBeVisible();
			expect(
				screen.getByRole("link", { name: "Enable mesh on db-01" }),
			).toBeVisible();
			expect(
				screen.queryByRole("checkbox", { name: /Connect over the mesh/ }),
			).toBeNull();
			expect(
				screen.getByRole("button", { name: "Link over the mesh" }),
			).toBeDisabled();
		});

		it("links over existing mesh links without asking", async () => {
			const user = userEvent.setup();
			const api = stubApi({
				...previews({
					web: crossing({
						devicesAdded: 0,
						links: [
							{
								fromServerId: "s1",
								fromServerName: "app-01",
								toServerId: "s2",
								toServerName: "db-01",
								providers: [],
								purpose: "service",
								state: "connected",
							},
						],
					}),
				}),
				...stageRoutes,
			});
			const closed: string[] = [];
			await show({ onClose: () => closed.push("x") });
			expect(
				await screen.findByText(
					/uses links that already exist, adds no device/,
				),
			).toBeVisible();
			await user.click(
				screen.getByRole("button", { name: "Link over the mesh" }),
			);
			await waitFor(() => expect(closed).toEqual(["x"]));
			expect(api.calls.find((call) => call.method === "POST")?.body).toEqual({
				fromServiceId: "web",
				toServiceId: "postgres",
			});
		});
	});
});
