import { afterEach, describe, expect, mock, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { useState } from "react";
import type {
	ConnectionView,
	CreateServiceInputBody,
	Environment,
	EnvTemplateScan,
	Repo,
	ServiceView,
} from "#/api/gen/types.gen";
import { type Server, toServer } from "#/api/models";
import { onOpenStagedReview } from "#/features/deploy-button/open-review";
import { browserTimezone } from "#/features/tasks/model";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	CreateServiceDialog,
	isServiceRequest,
	ServerPicker,
} from "./create-dialog";
import {
	connectionBody,
	scanBody,
	serverBody,
	serviceBody,
	suggested,
} from "./fixtures";
import { createActions, useCreateState } from "./store";

const STAGING: Environment = {
	id: "env-1",
	projectId: "proj-1",
	name: "Staging",
	slug: "staging",
	isProduction: false,
	kind: "standard",
	createdAt: "2026-01-01T00:00:00Z",
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
};
const PRODUCTION: Environment = {
	...STAGING,
	name: "Production",
	slug: "prod",
	isProduction: true,
};

const server = (over: Parameters<typeof serverBody>[0] = {}): Server =>
	toServer(serverBody(over));

const ALPHA = server();
const NO_SCAN: EnvTemplateScan = scanBody({
	status: "none",
	items: null,
	files: null,
});

afterEach(() => {
	act(() => createActions.closeDialog());
});

describe("isServiceRequest", () => {
	test("Git and image requests are service requests; a database one is not", () => {
		expect(isServiceRequest({ kind: "git" })).toBe(true);
		expect(isServiceRequest({ kind: "image" })).toBe(true);
		expect(isServiceRequest({ kind: "cron" })).toBe(true);
		expect(isServiceRequest({ kind: "database" })).toBe(false);
	});
});

function Picker({
	servers,
	initial = "",
	disabledReasons,
	onChange = () => {},
}: {
	servers: Server[];
	initial?: string;
	disabledReasons?: ReadonlyMap<string, string>;
	onChange?: (id: string) => void;
}) {
	const [value, setValue] = useState(initial);
	return (
		<ServerPicker
			servers={servers}
			value={value}
			{...(disabledReasons ? { disabledReasons } : {})}
			onChange={(id) => {
				setValue(id);
				onChange(id);
			}}
		/>
	);
}

describe("ServerPicker", () => {
	const BETA = server({ id: "srv-2", name: "beta" });
	const GAMMA = server({ id: "srv-3", name: "gamma" });

	test("lists each server as a radio with how much of its CPU, memory and disk is in use", async () => {
		await renderWithProviders(<Picker servers={[ALPHA]} initial="srv-1" />);
		const group = screen.getByRole("radiogroup", { name: "Server" });
		const alpha = within(group).getByRole("radio", { name: /alpha/ });
		expect(alpha).toHaveAttribute("aria-checked", "true");
		expect(alpha).toHaveTextContent("online");
		expect(alpha).toHaveTextContent("12%");
		expect(alpha).toHaveTextContent("50%");
		expect(alpha).toHaveTextContent("40%");
		expect(
			within(alpha).getByRole("progressbar", { name: "CPU in use" }),
		).toBeVisible();
	});

	test("clamps a CPU sample past 100% and leaves out a meter with no total", async () => {
		const busy = server({
			latest: {
				cpuPercent: 250,
				diskTotal: 0,
				diskUsed: 0,
				memTotal: 1000,
				memUsed: 2000,
				sampledAt: "2026-01-01T00:00:00Z",
			},
		});
		await renderWithProviders(<Picker servers={[busy]} initial="srv-1" />);
		const radio = screen.getByRole("radio", { name: /alpha/ });
		expect(radio).toHaveTextContent("CPU100%");
		expect(radio).toHaveTextContent("Memory100%");
		// Disk has no total: a dash, not a made-up number.
		expect(radio).toHaveTextContent("Disk—");
	});

	test("says there is no heartbeat sample instead of drawing bars for a server never measured", async () => {
		const fresh = server({ latest: { sampledAt: "" } as never });
		await renderWithProviders(<Picker servers={[fresh]} initial="srv-1" />);
		const radio = screen.getByRole("radio", { name: /alpha/ });
		expect(radio).toHaveTextContent("No heartbeat sample yet.");
		expect(within(radio).queryByRole("progressbar")).toBeNull();
	});

	test("an offline server can be chosen and warns the first deploy waits", async () => {
		const offline = server({ status: "offline" });
		await renderWithProviders(<Picker servers={[offline]} initial="" />);
		const radio = screen.getByRole("radio", { name: /alpha/ });
		expect(radio).toBeEnabled();
		expect(radio).toHaveTextContent(
			"offline: the first deploy waits until it is back",
		);
	});

	test.each([
		["draining", "being drained: not accepting services"],
		["drained", "drained: not accepting services"],
	] as const)(
		"a %s server is listed, says why, and cannot be chosen",
		async (drainState, reason) => {
			const onChange = mock((_id: string) => {});
			const user = userEvent.setup();
			await renderWithProviders(
				<Picker
					servers={[server({ drainState }), BETA]}
					initial="srv-2"
					onChange={onChange}
				/>,
			);
			const radio = screen.getByRole("radio", { name: /alpha/ });
			expect(radio).toBeDisabled();
			expect(radio).toHaveTextContent(reason);
			expect(within(radio).queryByRole("progressbar")).toBeNull();
			await user.click(radio);
			expect(onChange).not.toHaveBeenCalled();
		},
	);

	test("a server that does not accept workloads says so", async () => {
		await renderWithProviders(
			<Picker
				servers={[server({ acceptWorkloads: false, buildEnabled: true }), BETA]}
				initial="srv-2"
			/>,
		);
		expect(screen.getByRole("radio", { name: /alpha/ })).toHaveTextContent(
			"build server: not accepting services",
		);
	});

	test("a reason from the caller disables the server and replaces its headroom", async () => {
		const onChange = mock((_id: string) => {});
		const user = userEvent.setup();
		await renderWithProviders(
			<Picker
				servers={[ALPHA, BETA]}
				initial="srv-2"
				onChange={onChange}
				disabledReasons={new Map([["srv-1", "Docker is not installed"]])}
			/>,
		);
		const radio = screen.getByRole("radio", { name: /alpha/ });
		expect(radio).toBeDisabled();
		expect(radio).toHaveTextContent("Docker is not installed");
		expect(within(radio).queryByRole("progressbar")).toBeNull();
		await user.click(radio);
		expect(onChange).not.toHaveBeenCalled();
	});

	test("clicking a server chooses it", async () => {
		const onChange = mock((_id: string) => {});
		const user = userEvent.setup();
		await renderWithProviders(
			<Picker servers={[ALPHA, BETA]} initial="srv-1" onChange={onChange} />,
		);
		await user.click(screen.getByRole("radio", { name: /beta/ }));
		expect(onChange).toHaveBeenCalledWith("srv-2");
		expect(screen.getByRole("radio", { name: /beta/ })).toHaveAttribute(
			"aria-checked",
			"true",
		);
	});

	test("only the chosen radio is in the tab order; with none chosen, the first", async () => {
		const { unmount } = await renderWithProviders(
			<Picker servers={[ALPHA, BETA]} initial="srv-2" />,
		);
		expect(screen.getByRole("radio", { name: /alpha/ })).toHaveAttribute(
			"tabindex",
			"-1",
		);
		expect(screen.getByRole("radio", { name: /beta/ })).toHaveAttribute(
			"tabindex",
			"0",
		);
		unmount();
		await renderWithProviders(<Picker servers={[ALPHA, BETA]} initial="" />);
		expect(screen.getByRole("radio", { name: /alpha/ })).toHaveAttribute(
			"tabindex",
			"0",
		);
		expect(screen.getByRole("radio", { name: /beta/ })).toHaveAttribute(
			"tabindex",
			"-1",
		);
	});

	describe("arrow keys", () => {
		async function setup(user: UserEvent) {
			// alpha, a drained server, beta, gamma: the drained one is skipped.
			const drained = server({
				id: "srv-d",
				name: "delta",
				drainState: "drained",
			});
			await renderWithProviders(
				<Picker servers={[ALPHA, drained, BETA, GAMMA]} initial="srv-1" />,
			);
			await user.click(screen.getByRole("radio", { name: /alpha/ }));
		}

		test("ArrowDown and ArrowRight move to the next server that can be chosen, and focus it", async () => {
			const user = userEvent.setup();
			await setup(user);
			await user.keyboard("{ArrowDown}");
			const beta = screen.getByRole("radio", { name: /beta/ });
			expect(beta).toHaveAttribute("aria-checked", "true");
			expect(beta).toHaveFocus();
			await user.keyboard("{ArrowRight}");
			expect(screen.getByRole("radio", { name: /gamma/ })).toHaveFocus();
		});

		test("moving past the last server wraps to the first", async () => {
			const user = userEvent.setup();
			await setup(user);
			await user.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}");
			const alpha = screen.getByRole("radio", { name: /alpha/ });
			expect(alpha).toHaveAttribute("aria-checked", "true");
			expect(alpha).toHaveFocus();
		});

		test("ArrowUp and ArrowLeft go backwards, wrapping from the first to the last", async () => {
			const user = userEvent.setup();
			await setup(user);
			await user.keyboard("{ArrowUp}");
			expect(screen.getByRole("radio", { name: /gamma/ })).toHaveFocus();
			await user.keyboard("{ArrowLeft}");
			expect(screen.getByRole("radio", { name: /beta/ })).toHaveFocus();
			// The drained server in between is never selected.
			await user.keyboard("{ArrowUp}");
			expect(screen.getByRole("radio", { name: /alpha/ })).toHaveFocus();
			expect(screen.getByRole("radio", { name: /delta/ })).toHaveAttribute(
				"aria-checked",
				"false",
			);
		});

		test("other keys leave the choice alone", async () => {
			const user = userEvent.setup();
			await setup(user);
			await user.keyboard("a{Tab}");
			expect(screen.getByRole("radio", { name: /alpha/ })).toHaveAttribute(
				"aria-checked",
				"true",
			);
		});
	});
});

/** Opens the compact create dialog the way the canvas does, through the store. */
function Harness({
	environment = STAGING,
	servers = [ALPHA],
}: {
	environment?: Environment;
	servers?: Server[];
}) {
	const { request } = useCreateState();
	return (
		<CreateServiceDialog
			request={request && isServiceRequest(request) ? request : null}
			environment={environment}
			servers={servers}
		/>
	);
}

async function openDialog(
	kind: "git" | "image" | "cron",
	props: Parameters<typeof Harness>[0] = {},
	options: Parameters<typeof createActions.open>[1] = {},
) {
	const view = await renderWithProviders(<Harness {...props} />);
	act(() => createActions.open(kind, options));
	const dialog = await screen.findByRole("dialog");
	return { ...view, dialog };
}

const SERVICES = "POST /environments/:environmentId/services";

/** Replies to a create with a service of the requested name, remembering each Idempotency-Key. */
function createReply(
	keys: Array<string | null>,
	service: Partial<ServiceView> = {},
) {
	return async (request: Request) => {
		keys.push(request.headers.get("Idempotency-Key"));
		const { name } = (await request.clone().json()) as CreateServiceInputBody;
		return Response.json(serviceBody({ name, ...service }), { status: 201 });
	};
}

const createCalls = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((call) => call.path === "/environments/env-1/services");

const submitButton = () =>
	screen.getByRole("button", { name: /Create service/ });

describe("CreateServiceDialog: Docker image", () => {
	test("asks for an image, a name, a port and a server", async () => {
		await openDialog("image");
		expect(screen.getByText("New service from a Docker image")).toBeVisible();
		expect(
			screen.getByText("The image is pulled onto the server you pick."),
		).toBeVisible();
		expect(screen.getByLabelText("Image")).toHaveFocus();
		expect(screen.getByLabelText("Name")).toHaveValue("");
		expect(screen.getByLabelText("Port")).toHaveValue("");
		expect(screen.getByRole("radio", { name: /alpha/ })).toHaveAttribute(
			"aria-checked",
			"true",
		);
		expect(screen.queryByLabelText("Repository URL")).toBeNull();
	});

	test("Create stays disabled until there is an image", async () => {
		const user = userEvent.setup();
		await openDialog("image");
		expect(submitButton()).toBeDisabled();
		await user.type(screen.getByLabelText("Image"), "   ");
		expect(submitButton()).toBeDisabled();
		await user.type(screen.getByLabelText("Image"), "traefik/whoami:latest");
		expect(submitButton()).toBeEnabled();
	});

	test("suggests the name from the image until the user types their own", async () => {
		const user = userEvent.setup();
		await openDialog("image");
		const image = screen.getByLabelText("Image");
		await user.type(image, "ghcr.io/acme/api-gateway:1.4");
		expect(screen.getByLabelText("Name")).toHaveValue("api-gateway");

		await user.type(screen.getByLabelText("Name"), "-prod");
		expect(screen.getByLabelText("Name")).toHaveValue("api-gateway-prod");
		// Once edited, the image no longer rewrites the name.
		await user.clear(image);
		await user.type(image, "redis:8");
		expect(screen.getByLabelText("Name")).toHaveValue("api-gateway-prod");
	});

	test("an emptied name disables Create", async () => {
		const user = userEvent.setup();
		await openDialog("image");
		await user.type(screen.getByLabelText("Image"), "redis:8");
		await user.clear(screen.getByLabelText("Name"));
		expect(submitButton()).toBeDisabled();
	});

	test("creates the service with its image on the chosen server, then offers to deploy it", async () => {
		const keys: Array<string | null> = [];
		const api = stubApi({
			[SERVICES]: createReply(keys, { name: "whoami" }),
		});
		const user = userEvent.setup();
		await openDialog("image");
		await user.type(screen.getByLabelText("Image"), " traefik/whoami:latest ");
		await user.click(submitButton());

		expect(await screen.findByText("Created whoami")).toBeVisible();
		expect(createCalls(api)).toHaveLength(1);
		expect(createCalls(api)[0]).toMatchObject({
			method: "POST",
			body: {
				name: "whoami",
				serverId: "srv-1",
				kind: "app",
				sourceType: "image",
				config: { image: "traefik/whoami:latest" },
			},
		});
		expect(keys[0]).toMatch(/^[0-9a-f-]{36}$/);
		expect(
			screen.getByText("Nothing runs until it is deployed."),
		).toBeVisible();
		// The dialog closed itself.
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(api.unmatched).toEqual([]);
	});

	test("a port is sent as a number; nothing is sent for a worker without one", async () => {
		const api = stubApi({ [SERVICES]: createReply([]) });
		const user = userEvent.setup();
		await openDialog("image");
		await user.type(screen.getByLabelText("Image"), "nginx");
		await user.type(screen.getByLabelText("Port"), " 8080 ");
		await user.click(submitButton());
		await screen.findByText("Created nginx");
		expect(createCalls(api)[0]?.body).toMatchObject({
			config: { image: "nginx", port: 8080 },
		});
	});

	test("an image without a port sends no port key", async () => {
		const api = stubApi({ [SERVICES]: createReply([]) });
		const user = userEvent.setup();
		await openDialog("image");
		await user.type(screen.getByLabelText("Image"), "nginx");
		await user.click(submitButton());
		await screen.findByText("Created nginx");
		expect(createCalls(api)[0]?.body).toEqual({
			name: "nginx",
			serverId: "srv-1",
			kind: "app",
			sourceType: "image",
			config: { image: "nginx" },
		});
	});

	test.each(["abc", "0", "65536", "80.5", "-1"])(
		"port %j is rejected with a message and blocks Create",
		async (text) => {
			const user = userEvent.setup();
			await openDialog("image");
			await user.type(screen.getByLabelText("Image"), "nginx");
			await user.type(screen.getByLabelText("Port"), text);
			expect(
				screen.getByText("Use a whole number from 1 to 65535."),
			).toBeVisible();
			expect(screen.getByLabelText("Port")).toHaveAttribute(
				"aria-invalid",
				"true",
			);
			expect(submitButton()).toBeDisabled();
		},
	);

	test("the boundary ports 1 and 65535 are accepted", async () => {
		const user = userEvent.setup();
		await openDialog("image");
		await user.type(screen.getByLabelText("Image"), "nginx");
		await user.type(screen.getByLabelText("Port"), "65535");
		expect(
			screen.queryByText("Use a whole number from 1 to 65535."),
		).toBeNull();
		expect(submitButton()).toBeEnabled();
		await user.clear(screen.getByLabelText("Port"));
		await user.type(screen.getByLabelText("Port"), "1");
		expect(submitButton()).toBeEnabled();
	});

	describe("replicas", () => {
		test("are offered only for a service with a port", async () => {
			const user = userEvent.setup();
			await openDialog("image");
			expect(screen.queryByRole("group", { name: "Replicas" })).toBeNull();
			await user.type(screen.getByLabelText("Port"), "8080");
			expect(screen.getByRole("group", { name: "Replicas" })).toBeVisible();
			await user.clear(screen.getByLabelText("Port"));
			expect(screen.queryByRole("group", { name: "Replicas" })).toBeNull();
			await user.type(screen.getByLabelText("Port"), "x");
			expect(screen.queryByRole("group", { name: "Replicas" })).toBeNull();
		});

		test("stay between 1 and the maximum", async () => {
			const user = userEvent.setup();
			await openDialog("image");
			await user.type(screen.getByLabelText("Port"), "8080");
			const group = screen.getByRole("group", { name: "Replicas" });
			expect(
				within(group).getByRole("button", { name: "Decrease replicas" }),
			).toBeDisabled();
			for (let i = 0; i < 12; i++) {
				const more = within(group).getByRole("button", {
					name: "Increase replicas",
				});
				if (!(more as HTMLButtonElement).disabled) await user.click(more);
			}
			expect(group).toHaveTextContent("10");
			expect(
				within(group).getByRole("button", { name: "Increase replicas" }),
			).toBeDisabled();
		});

		test("more than one is set on the new service after it is created", async () => {
			const api = stubApi({
				[SERVICES]: createReply([]),
				"PUT /services/:serviceId/scaling": {
					body: serviceBody({ id: "svc-new" }),
				},
			});
			const user = userEvent.setup();
			await openDialog("image");
			await user.type(screen.getByLabelText("Image"), "nginx");
			await user.type(screen.getByLabelText("Port"), "80");
			const more = screen.getByRole("button", { name: "Increase replicas" });
			await user.click(more);
			await user.click(more);
			await user.click(submitButton());
			await screen.findByText("Created nginx");
			const scaling = api.calls.filter((c) => c.path.endsWith("/scaling"));
			expect(scaling).toHaveLength(1);
			expect(scaling[0]).toMatchObject({
				method: "PUT",
				path: "/services/svc-new/scaling",
				body: { replicas: 3 },
			});
			expect(api.unmatched).toEqual([]);
		});

		test("one replica sends no scaling call", async () => {
			const api = stubApi({ [SERVICES]: createReply([]) });
			const user = userEvent.setup();
			await openDialog("image");
			await user.type(screen.getByLabelText("Image"), "nginx");
			await user.type(screen.getByLabelText("Port"), "80");
			await user.click(submitButton());
			await screen.findByText("Created nginx");
			expect(api.calls.map((c) => c.path)).toEqual([
				"/environments/env-1/services",
			]);
		});

		test("a scaling failure leaves the service created and warns", async () => {
			stubApi({
				[SERVICES]: createReply([]),
				"PUT /services/:serviceId/scaling": problem(409, {
					title: "Conflict",
					detail: "no capacity",
					status: 409,
				}),
			});
			const user = userEvent.setup();
			await openDialog("image");
			await user.type(screen.getByLabelText("Image"), "nginx");
			await user.type(screen.getByLabelText("Port"), "80");
			await user.click(
				screen.getByRole("button", { name: "Increase replicas" }),
			);
			await user.click(submitButton());
			expect(
				await screen.findByText("nginx is created with 1 replica"),
			).toBeInTheDocument();
			expect(
				screen.getByText("Could not set 2: no capacity"),
			).toBeInTheDocument();
			expect(await screen.findByText("Created nginx")).toBeVisible();
		});
	});

	describe("the toast after creating", () => {
		test("Deploy now deploys the new service and confirms", async () => {
			const deployKeys: Array<string | null> = [];
			const api = stubApi({
				[SERVICES]: createReply([], { name: "whoami" }),
				"POST /services/:serviceId/deployments": (request) => {
					deployKeys.push(request.headers.get("Idempotency-Key"));
					return Response.json({}, { status: 202 });
				},
			});
			const user = userEvent.setup();
			await openDialog("image");
			await user.type(screen.getByLabelText("Image"), "traefik/whoami");
			await user.click(submitButton());
			await user.click(
				await screen.findByRole("button", { name: "Deploy now" }),
			);
			expect(await screen.findByText("Deploying whoami")).toBeVisible();
			expect(
				screen.getByText("Follow it on its card or under Deployments."),
			).toBeVisible();
			expect(
				api.calls.filter((c) => c.path === "/services/svc-new/deployments"),
			).toHaveLength(1);
			expect(deployKeys[0]).toMatch(/^[0-9a-f-]{36}$/);
		});

		test("a failed deploy reports the reason", async () => {
			stubApi({
				[SERVICES]: createReply([], { name: "whoami" }),
				"POST /services/:serviceId/deployments": problem(409, {
					title: "Conflict",
					detail: "server is offline",
					status: 409,
				}),
			});
			const user = userEvent.setup();
			await openDialog("image");
			await user.type(screen.getByLabelText("Image"), "traefik/whoami");
			await user.click(submitButton());
			await user.click(
				await screen.findByRole("button", { name: "Deploy now" }),
			);
			expect(await screen.findByText("Could not deploy whoami")).toBeVisible();
			expect(screen.getByText("server is offline")).toBeVisible();
		});

		test("in production the service is only staged and the action opens the review sheet", async () => {
			const api = stubApi({ [SERVICES]: createReply([]) });
			const opened = mock(() => {});
			const stop = onOpenStagedReview(opened);
			try {
				const user = userEvent.setup();
				await openDialog("image", { environment: PRODUCTION });
				await user.type(screen.getByLabelText("Image"), "nginx");
				await user.click(submitButton());
				expect(
					await screen.findByText(
						"Staged in Production. Nothing runs until you review and deploy.",
					),
				).toBeVisible();
				expect(screen.queryByRole("button", { name: "Deploy now" })).toBeNull();
				await user.click(
					screen.getByRole("button", { name: "Review & deploy" }),
				);
				expect(opened).toHaveBeenCalledTimes(1);
				// Reviewing never deploys by itself.
				expect(api.calls.map((c) => c.path)).toEqual([
					"/environments/env-1/services",
				]);
			} finally {
				stop();
			}
		});
	});

	describe("a failed create", () => {
		test("shows the reason, keeps the dialog and the typed values, and retries with the same key", async () => {
			const keys: Array<string | null> = [];
			let attempt = 0;
			stubApi({
				[SERVICES]: async (request) => {
					keys.push(request.headers.get("Idempotency-Key"));
					attempt += 1;
					if (attempt === 1) {
						return Response.json(
							{
								title: "Unprocessable",
								detail: "name already taken",
								status: 422,
							},
							{ status: 422 },
						);
					}
					return Response.json(serviceBody({ name: "nginx" }), { status: 201 });
				},
			});
			const user = userEvent.setup();
			await openDialog("image");
			await user.type(screen.getByLabelText("Image"), "nginx");
			await user.click(submitButton());
			expect(await screen.findByText("name already taken")).toBeVisible();
			expect(screen.getByRole("dialog")).toBeVisible();
			expect(screen.getByLabelText("Image")).toHaveValue("nginx");
			await waitFor(() => expect(submitButton()).toBeEnabled());

			await user.click(submitButton());
			expect(await screen.findByText("Created nginx")).toBeVisible();
			expect(keys).toHaveLength(2);
			// A lost response replays instead of duplicating.
			expect(keys[0]).toBe(keys[1]);
		});

		test("a new opening of the dialog starts clean with a new key", async () => {
			const keys: Array<string | null> = [];
			stubApi({ [SERVICES]: createReply(keys) });
			const user = userEvent.setup();
			await openDialog("image");
			await user.type(screen.getByLabelText("Image"), "nginx");
			await user.click(submitButton());
			await screen.findByText("Created nginx");
			await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

			act(() => createActions.open("image"));
			await screen.findByRole("dialog");
			// The previous form is gone ...
			await waitFor(() =>
				expect(screen.getByLabelText("Image")).toHaveValue(""),
			);
			await user.type(screen.getByLabelText("Image"), "redis");
			await user.click(submitButton());
			await screen.findByText("Created redis");
			// ... and so is its idempotency key.
			expect(keys).toHaveLength(2);
			expect(keys[0]).not.toBe(keys[1]);
		});
	});

	test("Cancel closes the dialog without creating anything", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await openDialog("image");
		await user.type(screen.getByLabelText("Image"), "nginx");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(api.calls).toEqual([]);
	});

	test("Enter in a field submits the form", async () => {
		const api = stubApi({ [SERVICES]: createReply([]) });
		const user = userEvent.setup();
		await openDialog("image");
		await user.type(screen.getByLabelText("Image"), "nginx{Enter}");
		await screen.findByText("Created nginx");
		expect(createCalls(api)).toHaveLength(1);
	});

	test("Enter before the form is complete creates nothing", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await openDialog("image");
		await user.type(screen.getByLabelText("Name"), "web{Enter}");
		expect(api.calls).toEqual([]);
		expect(screen.getByRole("dialog")).toBeVisible();
	});

	test("while the create is in flight the button cannot be pressed again", async () => {
		const gate = Promise.withResolvers<void>();
		const api = stubApi({
			[SERVICES]: async () => {
				await gate.promise;
				return Response.json(serviceBody({ name: "nginx" }), { status: 201 });
			},
		});
		const user = userEvent.setup();
		await openDialog("image");
		await user.type(screen.getByLabelText("Image"), "nginx{Enter}");
		await waitFor(() => expect(submitButton()).toBeDisabled());
		await user.type(screen.getByLabelText("Name"), "{Enter}");
		gate.resolve();
		await screen.findByText("Created nginx");
		expect(createCalls(api)).toHaveLength(1);
	});

	describe("servers", () => {
		test("with none enrolled it says so, links to enrolling one, and cannot create", async () => {
			const user = userEvent.setup();
			await openDialog("image", { servers: [] });
			await user.type(screen.getByLabelText("Image"), "nginx");
			expect(screen.getByText(/No server is enrolled yet\./)).toBeVisible();
			expect(submitButton()).toBeDisabled();
			const link = screen.getByRole("link", { name: "Add a server" });
			expect(link).toHaveAttribute("href", "/servers?enroll=true");
			await user.click(link);
			await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		});

		test("pending and revoked servers are not offered", async () => {
			await openDialog("image", {
				servers: [
					server({ id: "srv-p", name: "waiting", status: "pending" }),
					server({ id: "srv-r", name: "gone", status: "revoked" }),
					ALPHA,
				],
			});
			expect(screen.getAllByRole("radio")).toHaveLength(1);
			expect(screen.getByRole("radio", { name: /alpha/ })).toBeVisible();
		});

		test("a server that is only pending counts as none enrolled", async () => {
			await openDialog("image", {
				servers: [server({ id: "srv-p", name: "waiting", status: "pending" })],
			});
			expect(screen.getByText(/No server is enrolled yet\./)).toBeVisible();
		});

		test("starts on the first online server that takes services, not the first listed", async () => {
			await openDialog("image", {
				servers: [
					server({ id: "srv-o", name: "sleepy", status: "offline" }),
					server({
						id: "srv-d",
						name: "draining",
						drainState: "draining",
					}),
					server({ id: "srv-b", name: "builder", acceptWorkloads: false }),
					ALPHA,
				],
			});
			expect(screen.getByRole("radio", { name: /alpha/ })).toHaveAttribute(
				"aria-checked",
				"true",
			);
			expect(screen.getByRole("radio", { name: /sleepy/ })).toHaveAttribute(
				"aria-checked",
				"false",
			);
		});

		test("with only an offline server it starts there so the service can still be created", async () => {
			const api = stubApi({ [SERVICES]: createReply([]) });
			const user = userEvent.setup();
			await openDialog("image", {
				servers: [server({ id: "srv-o", name: "sleepy", status: "offline" })],
			});
			expect(screen.getByRole("radio", { name: /sleepy/ })).toHaveAttribute(
				"aria-checked",
				"true",
			);
			await user.type(screen.getByLabelText("Image"), "nginx");
			await user.click(submitButton());
			await screen.findByText("Created nginx");
			expect(createCalls(api)[0]?.body).toMatchObject({ serverId: "srv-o" });
		});

		test("when every server is closed to new services nothing is chosen and Create is blocked", async () => {
			const user = userEvent.setup();
			await openDialog("image", {
				servers: [server({ drainState: "drained" })],
			});
			await user.type(screen.getByLabelText("Image"), "nginx");
			expect(screen.getByRole("radio", { name: /alpha/ })).toBeDisabled();
			expect(submitButton()).toBeDisabled();
		});

		test("the service goes to the server picked, not the default", async () => {
			const api = stubApi({ [SERVICES]: createReply([]) });
			const user = userEvent.setup();
			await openDialog("image", {
				servers: [ALPHA, server({ id: "srv-2", name: "beta" })],
			});
			await user.type(screen.getByLabelText("Image"), "nginx");
			await user.click(screen.getByRole("radio", { name: /beta/ }));
			await user.click(submitButton());
			await screen.findByText("Created nginx");
			expect(createCalls(api)[0]?.body).toMatchObject({ serverId: "srv-2" });
		});
	});

	describe("dropping the node where the user pointed", () => {
		const canvas = {
			version: 3,
			layout: {
				nodes: { other: { x: 1, y: 2 } },
				groups: null,
				viewport: { x: 0, y: 0, zoom: 1 },
			},
		};

		test("saves the position in the environment's layout, keeping the other cards", async () => {
			const ifMatch: Array<string | null> = [];
			const api = stubApi({
				[SERVICES]: createReply([]),
				"GET /services/:serviceId": {
					body: serviceBody({ projectId: "proj-1", environmentId: "env-1" }),
				},
				"GET /projects/:projectId/environments/:environmentId/canvas": {
					body: canvas,
				},
				"PUT /projects/:projectId/environments/:environmentId/canvas": (
					request,
				) => {
					ifMatch.push(request.headers.get("If-Match"));
					return Response.json({ ...canvas, version: 4 });
				},
			});
			const user = userEvent.setup();
			await openDialog("image", {}, { position: { x: 120, y: 80 } });
			await user.type(screen.getByLabelText("Image"), "nginx");
			await user.click(submitButton());
			await screen.findByText("Created nginx");
			const put = api.calls.find((c) => c.method === "PUT");
			expect(put?.path).toBe("/projects/proj-1/environments/env-1/canvas");
			expect(put?.body).toMatchObject({
				layout: {
					nodes: { other: { x: 1, y: 2 }, "svc-new": { x: 120, y: 80 } },
				},
			});
			expect(ifMatch).toEqual(['"3"']);
		});

		test("if the position cannot be saved, the card is placed automatically and the user is told", async () => {
			stubApi({
				[SERVICES]: createReply([]),
				"GET /services/:serviceId": {
					body: serviceBody({ projectId: "proj-1", environmentId: "env-1" }),
				},
				"GET /projects/:projectId/environments/:environmentId/canvas": {
					body: canvas,
				},
				"PUT /projects/:projectId/environments/:environmentId/canvas": problem(
					500,
					{ title: "boom", status: 500 },
				),
			});
			const user = userEvent.setup();
			await openDialog("image", {}, { position: { x: 5, y: 6 } });
			await user.type(screen.getByLabelText("Image"), "nginx");
			await user.click(submitButton());
			expect(
				await screen.findByText("nginx was placed automatically"),
			).toBeInTheDocument();
			expect(
				screen.getByText(
					"Its position could not be saved where you dropped it.",
				),
			).toBeInTheDocument();
			// The service itself was still created.
			expect(await screen.findByText("Created nginx")).toBeVisible();
		});

		test("without a drop position no layout is touched", async () => {
			const api = stubApi({ [SERVICES]: createReply([]) });
			const user = userEvent.setup();
			await openDialog("image");
			await user.type(screen.getByLabelText("Image"), "nginx");
			await user.click(submitButton());
			await screen.findByText("Created nginx");
			expect(api.calls.map((c) => c.path)).toEqual([
				"/environments/env-1/services",
			]);
		});
	});

	test("an image dialog never loads Git connections", async () => {
		const api = stubApi({ [SERVICES]: createReply([]) });
		await openDialog("image");
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});
});

// ---------------------------------------------------------------------------

const PREVIEW = "POST /environments/:environmentId/env-template-preview";
const SCAN = "POST /services/:serviceId/env-template-scan";
const CONNECTIONS = "GET /git/connections";

/** Typing settles for 700 ms before a repository is read. */
const SETTLED = { timeout: 5000 };

function repo(over: Partial<Repo> = {}): Repo {
	return {
		fullName: "acme/shop",
		defaultBranch: "main",
		cloneUrl: "https://github.com/acme/shop.git",
		description: "The shop",
		private: false,
		archived: false,
		htmlUrl: "https://github.com/acme/shop",
		...over,
	};
}

async function paste(user: UserEvent, label: string, text: string) {
	await user.click(screen.getByLabelText(label));
	await user.paste(text);
}

describe("CreateServiceDialog: Git repository, pasted URL", () => {
	function gitApi(extra: Parameters<typeof stubApi>[0] = {}) {
		return stubApi({
			[CONNECTIONS]: { body: { items: [] } },
			[PREVIEW]: { body: NO_SCAN },
			[SERVICES]: createReply([], { sourceType: "git" }),
			[SCAN]: { body: NO_SCAN },
			...extra,
		});
	}

	test("asks for a URL, a branch (main), and an optional token", async () => {
		gitApi();
		await openDialog("git");
		expect(screen.getByText("New service from a Git repository")).toBeVisible();
		expect(
			screen.getByText(/The repository is built on the server you pick/),
		).toBeVisible();
		expect(screen.getByLabelText("Repository URL")).toHaveFocus();
		expect(screen.getByLabelText("Branch")).toHaveValue("main");
		expect(screen.getByLabelText("Access token (private)")).toHaveAttribute(
			"type",
			"password",
		);
		expect(screen.queryByLabelText("Image")).toBeNull();
		// No connected account: no switch between account and URL.
		await waitFor(() =>
			expect(
				screen.queryByRole("group", {
					name: "Where the repository comes from",
				}),
			).toBeNull(),
		);
	});

	test("Create needs a URL and a branch", async () => {
		gitApi();
		const user = userEvent.setup();
		await openDialog("git");
		expect(submitButton()).toBeDisabled();
		await paste(user, "Repository URL", "git@github.com:acme/shop.git");
		expect(submitButton()).toBeEnabled();
		await user.clear(screen.getByLabelText("Branch"));
		expect(submitButton()).toBeDisabled();
		await user.type(screen.getByLabelText("Branch"), "dev");
		expect(submitButton()).toBeEnabled();
	});

	test("suggests the service name from the repository", async () => {
		gitApi();
		const user = userEvent.setup();
		await openDialog("git");
		await paste(
			user,
			"Repository URL",
			"https://github.com/acme/Shop-Front.git",
		);
		expect(screen.getByLabelText("Name")).toHaveValue("shop-front");
	});

	test("creates the service from the URL, branch and token, then reads its example files", async () => {
		const api = gitApi();
		const user = userEvent.setup();
		await openDialog("git");
		await paste(user, "Repository URL", " https://github.com/acme/shop.git ");
		await user.clear(screen.getByLabelText("Branch"));
		await user.type(screen.getByLabelText("Branch"), " release ");
		await paste(user, "Access token (private)", "ghp_secret");
		await user.click(submitButton());
		await screen.findByText("Created shop");

		expect(createCalls(api)[0]?.body).toEqual({
			name: "shop",
			serverId: "srv-1",
			kind: "app",
			sourceType: "git",
			config: {
				gitUrl: "https://github.com/acme/shop.git",
				gitBranch: "release",
			},
			gitToken: "ghp_secret",
		});
		// The repository's example files are stored as the suggestion source.
		await waitFor(() =>
			expect(
				api.calls.some((c) => c.path === "/services/svc-new/env-template-scan"),
			).toBe(true),
		);
		// A URL (not a connected account) is never linked for deploy-on-push.
		expect(api.calls.some((c) => c.path.endsWith("/git"))).toBe(false);
	});

	test("without a token no gitToken is sent", async () => {
		const api = gitApi();
		const user = userEvent.setup();
		await openDialog("git");
		await paste(user, "Repository URL", "https://github.com/acme/shop.git");
		await user.click(submitButton());
		await screen.findByText("Created shop");
		expect(createCalls(api)[0]?.body).not.toHaveProperty("gitToken");
	});

	test("a GitHub URL brings the token help", async () => {
		gitApi();
		const user = userEvent.setup();
		await openDialog("git");
		await paste(user, "Repository URL", "https://github.com/acme/shop.git");
		expect(
			await screen.findByText(/Create a GitHub token with read access/),
		).toBeVisible();
	});

	test("a failed scan of the example files never disturbs the created service", async () => {
		const api = gitApi({
			[SCAN]: problem(500, { title: "boom", status: 500 }),
		});
		const user = userEvent.setup();
		await openDialog("git");
		await paste(user, "Repository URL", "https://github.com/acme/shop.git");
		await user.click(submitButton());
		expect(await screen.findByText("Created shop")).toBeVisible();
		await waitFor(() =>
			expect(api.calls.some((c) => c.path.endsWith("/env-template-scan"))).toBe(
				true,
			),
		);
	});

	describe("reading the repository's example files while typing", () => {
		test("a readable URL is read once typing settles, with the branch, token and server", async () => {
			const api = gitApi();
			const user = userEvent.setup();
			await openDialog("git");
			await paste(user, "Repository URL", "https://github.com/acme/shop.git");
			await paste(user, "Access token (private)", "tok");
			await waitFor(
				() =>
					expect(
						api.calls.some((c) => c.path.endsWith("/env-template-preview")),
					).toBe(true),
				SETTLED,
			);
			const previews = api.calls.filter((c) =>
				c.path.endsWith("/env-template-preview"),
			);
			// Only the settled form is read, never a half-typed one.
			expect(previews.at(-1)).toMatchObject({
				path: "/environments/env-1/env-template-preview",
				body: {
					gitUrl: "https://github.com/acme/shop.git",
					branch: "main",
					gitToken: "tok",
					serverId: "srv-1",
				},
			});
		});

		test("an SSH URL is not read: its deploy key does not exist yet", async () => {
			const api = gitApi();
			const user = userEvent.setup();
			await openDialog("git");
			await paste(user, "Repository URL", "git@github.com:acme/shop.git");
			await new Promise((resolve) => setTimeout(resolve, 1000));
			expect(
				api.calls.filter((c) => c.path.endsWith("/env-template-preview")),
			).toEqual([]);
		});

		test("a URL with no owner/name path is not read", async () => {
			const api = gitApi();
			const user = userEvent.setup();
			await openDialog("git");
			await paste(user, "Repository URL", "https://github.com/acme");
			await new Promise((resolve) => setTimeout(resolve, 1000));
			expect(
				api.calls.filter((c) => c.path.endsWith("/env-template-preview")),
			).toEqual([]);
		});

		test("an unreadable repository explains why and still creates normally", async () => {
			gitApi({
				[PREVIEW]: {
					body: scanBody({
						status: "unavailable",
						message: "authentication required",
						items: null,
						files: null,
					}),
				},
			});
			const user = userEvent.setup();
			await openDialog("git");
			await paste(user, "Repository URL", "https://github.com/acme/shop.git");
			expect(
				await screen.findByText(
					"The example files could not be read: authentication required",
					undefined,
					SETTLED,
				),
			).toBeVisible();
			expect(submitButton()).toBeEnabled();
		});

		test("with keys found, the primary button reviews them first, then Create stages them with the service", async () => {
			const writes = mock((_body: unknown) => {});
			const api = gitApi({
				[PREVIEW]: {
					body: scanBody({
						items: [
							suggested({ key: "LOG_LEVEL", value: "info" }),
							suggested({
								key: "API_TOKEN",
								value: "",
								needsInput: true,
								proposedSecret: true,
							}),
							suggested({ key: "DEBUG", value: "false" }),
						],
						files: [{ path: ".env.example", keys: 3 }],
					}),
				},
				"PUT /services/:serviceId/variables": async (request) => {
					writes(await request.clone().json());
					return Response.json({});
				},
			});
			const user = userEvent.setup();
			await openDialog("git");
			await paste(user, "Repository URL", "https://github.com/acme/shop.git");
			expect(
				await screen.findByText(/3 keys in/, undefined, SETTLED),
			).toBeVisible();
			const review = screen.getByRole("button", { name: "Review variables" });
			expect(
				screen.queryByRole("button", { name: /Create service/ }),
			).toBeNull();
			await user.click(review);

			// Step two: the keys, nothing created yet.
			expect(screen.getByText("Variables from .env.example")).toBeVisible();
			expect(createCalls(api)).toEqual([]);
			expect(screen.getByRole("button", { name: "Back" })).toBeVisible();
			expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
			expect(
				screen.getByText(/3 of 3 selected · 1 without a value/),
			).toBeVisible();

			await user.type(screen.getByLabelText("Value for API_TOKEN"), "abc");
			await user.click(screen.getByRole("checkbox", { name: "Add DEBUG" }));
			expect(screen.getByText(/2 of 3 selected/)).toBeVisible();

			await user.click(submitButton());
			await screen.findByText("Created shop");
			await waitFor(() => expect(writes).toHaveBeenCalledTimes(1));
			expect(writes.mock.calls[0]?.[0]).toEqual({
				variables: [
					{ key: "LOG_LEVEL", value: "info", source: "suggested" },
					{
						key: "API_TOKEN",
						value: "abc",
						source: "suggested",
						isSecret: true,
					},
				],
			});
			expect(createCalls(api)).toHaveLength(1);
		});

		test("Back from the review returns to the source with everything typed intact", async () => {
			gitApi({
				[PREVIEW]: { body: scanBody() },
			});
			const user = userEvent.setup();
			await openDialog("git");
			await paste(user, "Repository URL", "https://github.com/acme/shop.git");
			await user.click(
				await screen.findByRole(
					"button",
					{ name: "Review variables" },
					SETTLED,
				),
			);
			await user.click(screen.getByRole("button", { name: "Back" }));
			expect(
				screen.getByText("New service from a Git repository"),
			).toBeVisible();
			expect(screen.getByLabelText("Repository URL")).toHaveValue(
				"https://github.com/acme/shop.git",
			);
			expect(
				screen.getByRole("button", { name: "Review variables" }),
			).toBeVisible();
		});

		test("choices made in the review survive going back and forth", async () => {
			gitApi({
				[PREVIEW]: {
					body: scanBody({
						items: [
							suggested({ key: "A", value: "1" }),
							suggested({ key: "B", value: "2" }),
						],
					}),
				},
			});
			const user = userEvent.setup();
			await openDialog("git");
			await paste(user, "Repository URL", "https://github.com/acme/shop.git");
			await user.click(
				await screen.findByRole(
					"button",
					{ name: "Review variables" },
					SETTLED,
				),
			);
			await user.click(screen.getByRole("checkbox", { name: "Add A" }));
			await user.click(screen.getByRole("button", { name: "Back" }));
			await user.click(
				screen.getByRole("button", { name: "Review variables" }),
			);
			expect(screen.getByRole("checkbox", { name: "Add A" })).not.toBeChecked();
			expect(screen.getByRole("checkbox", { name: "Add B" })).toBeChecked();
		});

		test("a failure to stage the variables leaves the service created and says where to add them", async () => {
			gitApi({
				[PREVIEW]: { body: scanBody() },
				"PUT /services/:serviceId/variables": problem(500, {
					title: "boom",
					detail: "store unavailable",
					status: 500,
				}),
			});
			const user = userEvent.setup();
			await openDialog("git");
			await paste(user, "Repository URL", "https://github.com/acme/shop.git");
			await user.click(
				await screen.findByRole(
					"button",
					{ name: "Review variables" },
					SETTLED,
				),
			);
			await user.click(submitButton());
			expect(
				await screen.findByText("shop is created without its variables"),
			).toBeInTheDocument();
			expect(
				screen.getByText("store unavailable Add them from the Variables tab."),
			).toBeInTheDocument();
			expect(await screen.findByText("Created shop")).toBeVisible();
		});

		test("unchecking every key still creates the service and stages nothing", async () => {
			const api = gitApi({
				[PREVIEW]: { body: scanBody() },
			});
			const user = userEvent.setup();
			await openDialog("git");
			await paste(user, "Repository URL", "https://github.com/acme/shop.git");
			await user.click(
				await screen.findByRole(
					"button",
					{ name: "Review variables" },
					SETTLED,
				),
			);
			await user.click(screen.getByRole("checkbox", { name: "Add LOG_LEVEL" }));
			await user.click(submitButton());
			await screen.findByText("Created shop");
			expect(api.calls.some((c) => c.path.endsWith("/variables"))).toBe(false);
		});
	});
});

describe("CreateServiceDialog: Git repository, connected account", () => {
	const REPOS = "GET /git/connections/:connectionId/repos";
	const BRANCHES = "GET /git/connections/:connectionId/branches";
	const LINK = "PUT /services/:serviceId/git";

	function accountApi(
		extra: Parameters<typeof stubApi>[0] = {},
		connection: ConnectionView = connectionBody(),
	) {
		return stubApi({
			[CONNECTIONS]: { body: { items: [connection] } },
			[REPOS]: {
				body: {
					items: [
						repo(),
						repo({
							fullName: "acme/api",
							cloneUrl: "https://github.com/acme/api.git",
							defaultBranch: "trunk",
							private: true,
						}),
					],
				},
			},
			[BRANCHES]: { body: { items: ["main", "dev"] } },
			[PREVIEW]: { body: NO_SCAN },
			[SERVICES]: createReply([], { sourceType: "git" }),
			[SCAN]: { body: NO_SCAN },
			[LINK]: {
				body: make("GitLinkView", {
					linked: true,
					connectionId: "conn-1",
					connectionName: "acme on GitHub",
					repo: "acme/api",
					repoUrl: "https://github.com/acme/api.git",
					branch: "trunk",
					autoDeploy: true,
				}),
			},
			...extra,
		});
	}

	test("starts on the connected account with its repositories and a switch to a pasted URL", async () => {
		accountApi();
		await openDialog("git");
		expect(
			await screen.findByRole("button", { name: /acme\/shop/ }),
		).toBeVisible();
		expect(screen.getByText("acme on GitHub")).toBeVisible();
		expect(screen.getByRole("button", { name: /acme\/api/ })).toHaveTextContent(
			"trunk",
		);
		expect(screen.queryByLabelText("Repository URL")).toBeNull();
		expect(
			screen.getByRole("group", { name: "Where the repository comes from" }),
		).toBeVisible();
	});

	test("Create waits for a repository and branch", async () => {
		accountApi();
		const user = userEvent.setup();
		await openDialog("git");
		await user.click(await screen.findByRole("button", { name: /acme\/api/ }));
		await waitFor(() => expect(submitButton()).toBeEnabled());
	});

	test("picking a repository suggests its name and creates from its clone URL, then links it for deploy on push", async () => {
		const api = accountApi();
		const user = userEvent.setup();
		await openDialog("git");
		await user.click(await screen.findByRole("button", { name: /acme\/api/ }));
		expect(screen.getByLabelText("Name")).toHaveValue("api");
		await user.click(submitButton());
		await screen.findByText("Created api");

		expect(createCalls(api)[0]?.body).toEqual({
			name: "api",
			serverId: "srv-1",
			kind: "app",
			sourceType: "git",
			config: {
				gitUrl: "https://github.com/acme/api.git",
				gitBranch: "trunk",
			},
		});
		const link = api.calls.find((c) => c.path === "/services/svc-new/git");
		expect(link).toMatchObject({
			method: "PUT",
			body: { connectionId: "conn-1", repo: "acme/api", branch: "trunk" },
		});
		// The account's credentials are used: no token is sent.
		expect(createCalls(api)[0]?.body).not.toHaveProperty("gitToken");
	});

	test("a link that fails leaves the service created and says deploy on push is off", async () => {
		accountApi({
			[LINK]: problem(403, {
				title: "Forbidden",
				detail: "app not installed",
				status: 403,
			}),
		});
		const user = userEvent.setup();
		await openDialog("git");
		await user.click(await screen.findByRole("button", { name: /acme\/api/ }));
		await user.click(submitButton());
		expect(
			await screen.findByText("api is created without a repository link"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Deploy on push is off: app not installed"),
		).toBeInTheDocument();
		expect(await screen.findByText("Created api")).toBeVisible();
	});

	test("the repository chosen is read for example files without waiting for a URL", async () => {
		const api = accountApi();
		const user = userEvent.setup();
		await openDialog("git");
		await user.click(await screen.findByRole("button", { name: /acme\/api/ }));
		await waitFor(
			() =>
				expect(
					api.calls.some((c) => c.path.endsWith("/env-template-preview")),
				).toBe(true),
			SETTLED,
		);
		expect(
			api.calls.find((c) => c.path.endsWith("/env-template-preview"))?.body,
		).toEqual({
			connectionId: "conn-1",
			repo: "acme/api",
			branch: "trunk",
			serverId: "srv-1",
		});
	});

	test("switching to Any URL shows the URL fields and creates from the pasted URL with no link", async () => {
		const api = accountApi();
		const user = userEvent.setup();
		await openDialog("git");
		await user.click(await screen.findByRole("button", { name: /acme\/api/ }));
		await user.click(screen.getByRole("button", { name: "Any URL" }));
		expect(screen.getByLabelText("Repository URL")).toBeVisible();
		expect(screen.queryByRole("button", { name: /acme\/api/ })).toBeNull();
		await paste(user, "Repository URL", "https://gitlab.com/team/tool.git");
		expect(screen.getByLabelText("Name")).toHaveValue("tool");
		await user.click(submitButton());
		await screen.findByText("Created tool");
		expect(createCalls(api)[0]?.body).toMatchObject({
			sourceType: "git",
			config: { gitUrl: "https://gitlab.com/team/tool.git", gitBranch: "main" },
		});
		expect(api.calls.some((c) => c.path.endsWith("/git"))).toBe(false);
	});

	test("a connection that cannot list repositories takes a typed owner/name and branch", async () => {
		const api = accountApi(
			{},
			connectionBody({
				canListRepos: false,
				provider: "bitbucket",
				name: "acme on Bitbucket",
			}),
		);
		const user = userEvent.setup();
		await openDialog("git");
		const typed = await screen.findByLabelText("Repository");
		await user.type(typed, "acme/shop");
		await waitFor(() => expect(submitButton()).toBeEnabled());
		expect(screen.getByLabelText("Name")).toHaveValue("shop");
		await user.click(submitButton());
		await screen.findByText("Created shop");
		expect(createCalls(api)[0]?.body).toMatchObject({
			sourceType: "git",
			config: { gitBranch: "main" },
		});
		expect(
			api.calls.find((c) => c.path === "/services/svc-new/git"),
		).toMatchObject({
			body: { connectionId: "conn-1", repo: "acme/shop", branch: "main" },
		});
		expect(api.calls.some((c) => c.path.endsWith("/repos"))).toBe(false);
	});

	test("a connection that is not active is not offered: the dialog falls back to the URL fields", async () => {
		accountApi({}, connectionBody({ status: "pending" }));
		await openDialog("git");
		expect(await screen.findByLabelText("Repository URL")).toBeVisible();
		expect(
			screen.queryByRole("group", { name: "Where the repository comes from" }),
		).toBeNull();
	});
});

describe("CreateServiceDialog: Cron job", () => {
	const SCHEDULE_PREVIEW = "POST /task-schedules/preview";
	const preview = {
		[SCHEDULE_PREVIEW]: {
			body: { valid: true, description: "At 03:00", nextRuns: [] },
		},
	};
	const submitCron = () =>
		screen.getByRole("button", { name: /Create cron job/ });

	test("asks for a source, a schedule, a zone and an optional command, with no port or replicas", async () => {
		stubApi({ ...preview, [CONNECTIONS]: { body: { items: [] } } });
		await openDialog("cron");
		expect(screen.getByText("New cron job")).toBeVisible();
		// Docker image is the default source.
		expect(screen.getByLabelText("Image")).toBeVisible();
		expect(screen.queryByLabelText("Repository URL")).toBeNull();
		expect(
			screen.getByRole("button", { name: "Docker image", pressed: true }),
		).toBeVisible();
		expect(screen.getByLabelText("Cron expression")).toHaveValue("0 3 * * *");
		expect(
			screen.getByRole("combobox", { name: "Schedule" }),
		).toHaveTextContent("Daily 03:00");
		expect(screen.getByLabelText("Command")).toHaveValue("");
		expect(screen.queryByLabelText("Port")).toBeNull();
		expect(screen.queryByText("Replicas")).toBeNull();
		expect(submitCron()).toBeDisabled();
	});

	test("the time zone starts at the browser's, UTC when the browser's is not an IANA zone", async () => {
		stubApi({ ...preview, [CONNECTIONS]: { body: { items: [] } } });
		await openDialog("cron");
		const zone = browserTimezone();
		expect(
			screen.getByRole("combobox", { name: "Time zone" }),
		).toHaveTextContent(zone);
	});

	test("creates a one-shot worker with the schedule and zone, and no port", async () => {
		const api = stubApi({
			...preview,
			[CONNECTIONS]: { body: { items: [] } },
			[SERVICES]: createReply([], { name: "busybox", kind: "worker" }),
		});
		const user = userEvent.setup();
		await openDialog("cron");
		await user.type(screen.getByLabelText("Image"), "busybox");
		expect(screen.getByLabelText("Name")).toHaveValue("busybox");
		await user.click(submitCron());
		await screen.findByText("Created busybox");
		expect(createCalls(api)[0]).toMatchObject({
			method: "POST",
			body: {
				name: "busybox",
				serverId: "srv-1",
				kind: "worker",
				sourceType: "image",
				config: {
					image: "busybox",
					oneshot: true,
					cron: { schedule: "0 3 * * *", timezone: browserTimezone() },
				},
			},
		});
		expect(createCalls(api)[0]?.body).not.toHaveProperty("config.port");
		expect(createCalls(api)[0]?.body).not.toHaveProperty("config.startCommand");
		expect(
			screen.getByText("Nothing runs until it is deployed."),
		).toBeVisible();
	});

	test("a preset, a custom expression and a command are sent as typed", async () => {
		const api = stubApi({
			...preview,
			[CONNECTIONS]: { body: { items: [] } },
			[SERVICES]: createReply([], { name: "cleanup", kind: "worker" }),
		});
		const user = userEvent.setup();
		await openDialog("cron");
		await user.type(screen.getByLabelText("Image"), "acme/tools:2");
		await user.click(screen.getByRole("combobox", { name: "Schedule" }));
		await user.click(await screen.findByRole("option", { name: "Hourly" }));
		expect(screen.getByLabelText("Cron expression")).toHaveValue("@hourly");
		await user.clear(screen.getByLabelText("Cron expression"));
		await user.type(screen.getByLabelText("Cron expression"), "*/5 * * * *");
		expect(
			screen.getByRole("combobox", { name: "Schedule" }),
		).toHaveTextContent("Custom");
		await user.type(
			screen.getByLabelText("Command"),
			'sh -c "rake cleanup && rake report"',
		);
		await user.click(submitCron());
		await screen.findByText("Created cleanup");
		expect(createCalls(api)[0]?.body).toMatchObject({
			kind: "worker",
			config: {
				image: "acme/tools:2",
				oneshot: true,
				startCommand: ["sh", "-c", "rake cleanup && rake report"],
				cron: { schedule: "*/5 * * * *" },
			},
		});
	});

	test("an empty schedule blocks Create", async () => {
		stubApi({ ...preview, [CONNECTIONS]: { body: { items: [] } } });
		const user = userEvent.setup();
		await openDialog("cron");
		await user.type(screen.getByLabelText("Image"), "busybox");
		expect(submitCron()).toBeEnabled();
		await user.clear(screen.getByLabelText("Cron expression"));
		expect(submitCron()).toBeDisabled();
	});

	test("the Git repository source asks for a repository and sends its url and branch", async () => {
		const api = stubApi({
			...preview,
			[CONNECTIONS]: { body: { items: [] } },
			[PREVIEW]: { body: NO_SCAN },
			[SCAN]: { body: NO_SCAN },
			[SERVICES]: createReply([], { name: "shop", kind: "worker" }),
		});
		const user = userEvent.setup();
		await openDialog("cron");
		await user.click(screen.getByRole("button", { name: "Git repository" }));
		expect(screen.queryByLabelText("Image")).toBeNull();
		await user.type(
			await screen.findByLabelText("Repository URL"),
			"https://github.com/acme/shop.git",
		);
		await user.click(submitCron());
		await screen.findByText("Created shop");
		expect(createCalls(api)[0]?.body).toMatchObject({
			kind: "worker",
			sourceType: "git",
			config: {
				gitUrl: "https://github.com/acme/shop.git",
				gitBranch: "main",
				oneshot: true,
				cron: { schedule: "0 3 * * *" },
			},
		});
	});
});
