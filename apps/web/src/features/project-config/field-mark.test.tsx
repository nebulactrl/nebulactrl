import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	ConfigFieldState,
	ServiceConfigSource,
} from "#/api/gen/types.gen";
import { type ApiStub, stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ConfigServiceProvider, FieldMark } from "./field-mark";
import { change, environment, plan } from "./test-support";

const ROUTE = "/p/$projectId/$envSlug";
const URL_PATH = "/p/p1/staging";

function source(
	fields: ConfigFieldState[],
	extra: Overrides<ServiceConfigSource> = {},
) {
	return make("ServiceConfigSource", {
		managed: true,
		state: "in_sync",
		origin: "git",
		repo: "acme/shop",
		branch: "main",
		path: "nebula.toml",
		commit: "3f9c2a1deadbeef",
		fields,
		pending: [],
		...extra,
	});
}

const serviceView = make("ServiceView", {
	id: "svc_1",
	slug: "web",
	name: "web",
	kind: "app",
});

/** The calls a drifted mark makes to learn its service and where it lives. */
function surroundings(extra: Parameters<typeof stubApi>[0] = {}) {
	return {
		"GET /services/:id": { body: serviceView },
		"GET /projects": {
			body: make("PageProject", {
				items: [make("Project", { id: "p1", name: "Shop" })],
			}),
		},
		"GET /projects/:id/environments": {
			body: make("PageEnvironment", { items: [environment()] }),
		},
		...extra,
	};
}

async function settle(api: ApiStub, path: string) {
	await waitFor(() => expect(api.calls.map((c) => c.path)).toContain(path));
}

describe("FieldMark: fields the file owns", () => {
	test("an in-sync field gets a quiet mark that says where it is set", async () => {
		stubApi({
			"GET /services/:id/config-source": {
				body: source([{ field: "config.port", state: "in_sync" }]),
			},
		});
		await renderWithProviders(
			<FieldMark field="config.port" serviceId="svc_1" />,
		);
		expect(await screen.findByText("Set by nebula.toml")).toBeInTheDocument();
		await userEvent.setup().hover(screen.getByText("Set by nebula.toml"));
		expect(
			await screen.findByText(
				"Set by nebula.toml (acme/shop@3f9c2a1). Editing here is allowed and shows as drift until the file changes or you re-apply.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("button")).toBeNull();
	});

	test("a field the file does not own gets no mark", async () => {
		const api = stubApi({
			"GET /services/:id/config-source": {
				body: source([{ field: "config.port", state: "in_sync" }]),
			},
		});
		const { container, queryClient } = await renderWithProviders(
			<FieldMark field="config.cpus" serviceId="svc_1" />,
		);
		await settle(api, "/services/svc_1/config-source");
		await waitFor(() => expect(queryClient.isFetching()).toBe(0));
		expect(container).toBeEmptyDOMElement();
	});

	test("a service the file does not manage gets no mark", async () => {
		const api = stubApi({
			"GET /services/:id/config-source": {
				body: { managed: false, fields: null, pending: null },
			},
		});
		const { container, queryClient } = await renderWithProviders(
			<FieldMark field="config.port" serviceId="svc_1" />,
		);
		await settle(api, "/services/svc_1/config-source");
		await waitFor(() => expect(queryClient.isFetching()).toBe(0));
		expect(container).toBeEmptyDOMElement();
	});

	test("a group name stands for the fields under it, but exact does not", async () => {
		const body = source([{ field: "variables.NODE_ENV", state: "in_sync" }]);
		stubApi({ "GET /services/:id/config-source": { body } });
		const group = await renderWithProviders(
			<FieldMark field="variables" serviceId="svc_1" />,
		);
		expect(await screen.findByText("Set by nebula.toml")).toBeInTheDocument();
		group.unmount();

		const exact = await renderWithProviders(
			<FieldMark field="variables" serviceId="svc_1" exact />,
		);
		await waitFor(() => expect(exact.queryClient.isFetching()).toBe(0));
		expect(exact.container).toBeEmptyDOMElement();
	});

	test("takes its service from the nearest ConfigServiceProvider", async () => {
		const api = stubApi({
			"GET /services/:id/config-source": {
				body: source([{ field: "scale", state: "in_sync" }]),
			},
		});
		await renderWithProviders(
			<ConfigServiceProvider serviceId="svc_ctx">
				<FieldMark field="scale" />
			</ConfigServiceProvider>,
		);
		expect(await screen.findByText("Set by nebula.toml")).toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/services/svc_ctx/config-source");
	});

	test("an explicit serviceId wins over the provider's", async () => {
		const api = stubApi({
			"GET /services/:id/config-source": {
				body: source([{ field: "scale", state: "in_sync" }]),
			},
		});
		await renderWithProviders(
			<ConfigServiceProvider serviceId="svc_ctx">
				<FieldMark field="scale" serviceId="svc_own" />
			</ConfigServiceProvider>,
		);
		await screen.findByText("Set by nebula.toml");
		expect(api.calls.map((c) => c.path)).toEqual([
			"/services/svc_own/config-source",
		]);
	});

	test("outside any service there is nothing to ask and no mark", async () => {
		const api = stubApi({});
		const { container } = await renderWithProviders(
			<FieldMark field="scale" />,
		);
		expect(container).toBeEmptyDOMElement();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});
});

describe("FieldMark: edited here", () => {
	const drifted = source([{ field: "config.port", state: "drift" }], {
		state: "drift",
	});

	async function open(api = surroundings(), field = drifted) {
		const stub = stubApi({
			"GET /services/:id/config-source": { body: field },
			...api,
		});
		await renderWithProviders(
			<FieldMark field="config.port" serviceId="svc_1" />,
			{ path: URL_PATH, routePath: ROUTE },
		);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", {
				name: "Edited here, differs from nebula.toml",
			}),
		);
		return { api: stub, user };
	}

	test("a drifted field says so in words and explains the choices", async () => {
		await open();
		expect(screen.getByText("edited here")).toBeInTheDocument();
		const popover = await screen.findByText("Edited here");
		expect(popover).toBeInTheDocument();
		expect(
			screen.getByText(/Set by nebula.toml \(acme\/shop@3f9c2a1\)/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Revert to file" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Stop managing" }),
		).toBeInTheDocument();
	});

	test("a field changed on both sides says both changed", async () => {
		await open(
			surroundings(),
			source([{ field: "config.port", state: "conflict" }], {
				state: "conflict",
			}),
		);
		expect(screen.getByText("both changed")).toBeInTheDocument();
		expect(
			await screen.findByText("Changed here and in the file"),
		).toBeInTheDocument();
	});

	test("Revert to file stages only the changes to this field", async () => {
		let applyKey: string | null = null;
		const { api, user } = await open(
			surroundings({
				"POST /projects/:id/config/plan": {
					body: plan({
						services: [
							make("ConfigPlanService", {
								slug: "web",
								action: "update",
								changes: [
									change({ path: "services.web.port", field: "config.port" }),
									change({ path: "services.web.cpus", field: "config.cpus" }),
								],
							}),
							make("ConfigPlanService", {
								slug: "other",
								action: "update",
								changes: [
									change({ path: "services.other.port", field: "config.port" }),
								],
							}),
						],
					}),
				},
				"POST /projects/:id/config/apply": (request) => {
					applyKey = request.headers.get("Idempotency-Key");
					return Response.json(make("ConfigApplyResult", { status: "staged" }));
				},
			}),
		);
		await user.click(screen.getByRole("button", { name: "Revert to file" }));
		expect(
			await screen.findByText("Reverted to nebula.toml"),
		).toBeInTheDocument();
		expect(screen.getByText("Staged: deploy to ship it.")).toBeInTheDocument();
		const posts = api.calls.filter((c) => c.method === "POST");
		expect(posts.map((c) => c.path)).toEqual([
			"/projects/p1/config/plan",
			"/projects/p1/config/apply",
		]);
		expect(posts[0]?.body).toEqual({
			environment: "staging",
			source: { git: {} },
		});
		expect(posts[1]?.body).toEqual({
			environment: "staging",
			source: { git: {} },
			only: ["services.web.port"],
			deploy: false,
		});
		expect(applyKey).toMatch(/^[0-9a-f-]{36}$/);
	});

	test("when the file already agrees there is nothing to revert and nothing is applied", async () => {
		const { api, user } = await open(
			surroundings({
				"POST /projects/:id/config/plan": {
					body: plan({
						services: [
							make("ConfigPlanService", {
								slug: "web",
								action: "update",
								changes: [],
							}),
						],
					}),
				},
			}),
		);
		await user.click(screen.getByRole("button", { name: "Revert to file" }));
		expect(await screen.findByText("Nothing to revert")).toBeInTheDocument();
		expect(
			screen.getByText("The file no longer differs from this field."),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.path.endsWith("/apply"))).toEqual([]);
	});

	test("a failed plan is reported and nothing is applied", async () => {
		const { api, user } = await open(
			surroundings({
				"POST /projects/:id/config/plan": problem(422, {
					code: "config_invalid",
					detail: "nebula.toml:3 is not valid",
				}),
			}),
		);
		await user.click(screen.getByRole("button", { name: "Revert to file" }));
		expect(await screen.findByText("Could not revert")).toBeInTheDocument();
		expect(screen.getByText("nebula.toml:3 is not valid")).toBeInTheDocument();
		expect(api.calls.filter((c) => c.path.endsWith("/apply"))).toEqual([]);
	});

	test("a failed apply is reported", async () => {
		const { user } = await open(
			surroundings({
				"POST /projects/:id/config/plan": { body: plan() },
				"POST /projects/:id/config/apply": problem(409, {
					code: "plan_stale",
					detail: "The plan is out of date.",
				}),
			}),
		);
		await user.click(screen.getByRole("button", { name: "Revert to file" }));
		expect(await screen.findByText("Could not revert")).toBeInTheDocument();
		expect(screen.getByText("The plan is out of date.")).toBeInTheDocument();
		expect(screen.queryByText("Reverted to nebula.toml")).toBeNull();
	});

	test("Stop managing releases exactly the fields behind the mark", async () => {
		const { api, user } = await open(
			surroundings({
				"POST /services/:id/config-source/release": { body: source([]) },
			}),
			source(
				[
					{ field: "config.port", state: "drift" },
					{ field: "config.port#secret", state: "in_sync" },
					{ field: "config.cpus", state: "in_sync" },
				],
				{ state: "drift" },
			),
		);
		await user.click(screen.getByRole("button", { name: "Stop managing" }));
		expect(await screen.findByText("No longer managed")).toBeInTheDocument();
		expect(
			screen.getByText(
				"The value stays as it is; nebula.toml does not touch it any more.",
			),
		).toBeInTheDocument();
		const release = api.calls.find((c) => c.path.endsWith("/release"));
		expect(release?.path).toBe("/services/svc_1/config-source/release");
		expect(release?.body).toEqual({
			paths: ["config.port", "config.port#secret"],
		});
	});

	test("a refused release says why", async () => {
		const { user } = await open(
			surroundings({
				"POST /services/:id/config-source/release": problem(403, {
					code: "forbidden",
					detail: "You need the admin scope.",
				}),
			}),
		);
		await user.click(screen.getByRole("button", { name: "Stop managing" }));
		expect(
			await screen.findByText("Could not stop managing"),
		).toBeInTheDocument();
		expect(screen.getByText("You need the admin scope.")).toBeInTheDocument();
		expect(screen.queryByText("No longer managed")).toBeNull();
	});

	test("outside an environment there is no file to revert to, only Stop managing", async () => {
		stubApi({
			"GET /services/:id/config-source": { body: drifted },
			...surroundings(),
		});
		const { queryClient } = await renderWithProviders(
			<FieldMark field="config.port" serviceId="svc_1" />,
			{ path: "/p/p1", routePath: "/p/$projectId" },
		);
		await userEvent.setup().click(
			await screen.findByRole("button", {
				name: "Edited here, differs from nebula.toml",
			}),
		);
		const popover = await screen.findByRole("button", {
			name: "Stop managing",
		});
		// The service and the environments have arrived: the missing button is not a loading gap.
		await waitFor(() => expect(queryClient.isFetching()).toBe(0));
		expect(
			within(popover.parentElement as HTMLElement).queryByRole("button", {
				name: "Revert to file",
			}),
		).toBeNull();
	});
});
