import { afterEach, describe, expect, setDefaultTimeout, test } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import {
	act,
	configure,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	ConnectionView,
	CoolifyApplyResult,
	CoolifyPlan,
	Project,
	Server,
} from "#/api/gen/types.gen";
import { type ApiStub, stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CoolifyImportHost } from "./coolify-import-host";
import { coolifyImportActions } from "./store";
import {
	applied,
	envRow,
	plan,
	project,
	resource,
	serverWire,
} from "./test-fixtures";

const URL_VALUE = "https://coolify.example.com";
const TOKEN = "7|super-secret-token";

function Where() {
	return <output aria-label="location">{useLocation().pathname}</output>;
}

// The wizard debounces its plan by 400 ms and the machine may be busy.
setDefaultTimeout(30_000);
configure({ asyncUtilTimeout: 5_000 });

afterEach(() => coolifyImportActions.close());

interface Setup {
	plan?: CoolifyPlan | ((body: unknown) => Response | Promise<Response>);
	apply?: CoolifyApplyResult | ((req: Request) => Response | Promise<Response>);
	servers?: Server[];
	projects?: Project[];
	connections?: ConnectionView[];
}

function stub(setup: Setup = {}): ApiStub & { applyKeys: string[] } {
	const applyKeys: string[] = [];
	const api = stubApi({
		"GET /servers": {
			body: make("PageServer", { items: setup.servers ?? [serverWire()] }),
		},
		"GET /projects": {
			body: make("PageProject", { items: setup.projects ?? [] }),
		},
		"GET /git/connections": {
			body: make("ListConnectionsOutputBody", {
				items: setup.connections ?? [],
			}),
		},
		"POST /imports/coolify/plan": async (req) => {
			const body: unknown = await req.json();
			const reply = setup.plan ?? plan();
			return typeof reply === "function" ? reply(body) : Response.json(reply);
		},
		"POST /imports/coolify/apply": async (req) => {
			applyKeys.push(req.headers.get("Idempotency-Key") ?? "");
			const reply = setup.apply ?? applied();
			return typeof reply === "function"
				? reply(req)
				: Response.json(reply, { status: 201 });
		},
	});
	return Object.assign(api, { applyKeys });
}

async function openWizard() {
	const view = await renderWithProviders(
		<>
			<CoolifyImportHost />
			<Where />
		</>,
	);
	act(() => coolifyImportActions.open());
	await screen.findByText("Import from Coolify");
	return view;
}

async function connect(user: ReturnType<typeof userEvent.setup>) {
	await user.click(screen.getByLabelText("Coolify URL"));
	await user.paste(` ${URL_VALUE} `);
	await user.click(screen.getByLabelText("API token"));
	await user.paste(` ${TOKEN} `);
	await user.click(screen.getByRole("button", { name: "Read Coolify" }));
}

function planCalls(api: ApiStub) {
	return api.calls.filter((c) => c.path === "/imports/coolify/plan");
}

/**
 * Waits until the review is up and its plan has settled (Import enabled), and
 * returns how many plan requests have gone out by then.
 */
async function settled(api: ApiStub): Promise<number> {
	const button = await screen.findByRole("button", { name: /^Import/ });
	await waitFor(() => expect(button).toBeEnabled());
	return planCalls(api).length;
}

describe("connect step", () => {
	test("renders nothing until the store opens it, and Cancel closes it", async () => {
		const api = stub();
		const user = userEvent.setup();
		await renderWithProviders(<CoolifyImportHost />);
		expect(screen.queryByText("Import from Coolify")).not.toBeInTheDocument();

		act(() => coolifyImportActions.open());
		expect(await screen.findByText("Import from Coolify")).toBeInTheDocument();
		const steps = screen.getByRole("list", { name: "Steps" });
		expect(within(steps).getByText("Connect").closest("li")).toHaveAttribute(
			"aria-current",
			"step",
		);
		expect(
			screen.getByText(/Read a Coolify instance with an API token/),
		).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByText("Import from Coolify")).not.toBeInTheDocument(),
		);
		expect(api.unmatched).toEqual([]);
	});

	test("Read Coolify needs both an address and a token", async () => {
		stub();
		const user = userEvent.setup();
		await openWizard();
		const read = screen.getByRole("button", { name: "Read Coolify" });
		expect(read).toBeDisabled();

		await user.type(screen.getByLabelText("Coolify URL"), URL_VALUE);
		expect(read).toBeDisabled();

		await user.type(screen.getByLabelText("API token"), "   ");
		expect(read).toBeDisabled();

		await user.type(screen.getByLabelText("API token"), TOKEN);
		expect(read).toBeEnabled();
		expect(screen.getByLabelText("API token")).toHaveAttribute(
			"type",
			"password",
		);
	});

	test("reads Coolify with the trimmed address and token and the default server, then shows the review", async () => {
		const api = stub({
			plan: plan({
				coolifyVersion: "4.0.0-beta.400",
				totals: {
					projects: 1,
					services: 1,
					databases: 0,
					variables: 3,
					secrets: 1,
					domains: 2,
				},
			}),
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);

		expect(await screen.findByText("Coolify 4.0.0-beta.400")).toBeVisible();
		// Read right after typing plans once; the settled debounce does not plan it again.
		expect(await settled(api)).toBe(1);
		expect(planCalls(api)[0]?.body).toEqual({
			source: { url: URL_VALUE, token: TOKEN },
			choices: {},
			serverId: "srv-1",
		});
		expect(
			screen.getByText(
				"1 project · 1 service · 0 databases · 3 variables (1 secret) · 2 domains",
			),
		).toBeInTheDocument();
		const steps = screen.getByRole("list", { name: "Steps" });
		expect(within(steps).getByText("Review").closest("li")).toHaveAttribute(
			"aria-current",
			"step",
		);
		expect(within(steps).getByText("(done)")).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("a rejected address shows the server's reason, never the token, and typing clears it", async () => {
		const api = stub({
			plan: () =>
				Response.json(
					{
						code: "invalid_coolify_url",
						title: "Bad request",
						status: 400,
						detail: `Cannot reach ${URL_VALUE} with ${TOKEN}`,
					},
					{ status: 400 },
				),
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);

		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Coolify could not be read");
		expect(alert).toHaveTextContent(`Cannot reach ${URL_VALUE} with •••`);
		expect(alert).toHaveTextContent("Nothing was changed.");
		expect(alert).not.toHaveTextContent(TOKEN);
		expect(screen.getByLabelText("Coolify URL")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByLabelText("API token")).toHaveAttribute(
			"aria-invalid",
			"false",
		);
		// Still on the connect step.
		expect(screen.getByRole("button", { name: "Read Coolify" })).toBeEnabled();

		await user.type(screen.getByLabelText("Coolify URL"), "/");
		await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
		expect(api.unmatched).toEqual([]);
	});

	test("a refused token marks the token field", async () => {
		stub({
			plan: () =>
				Response.json(
					{
						code: "coolify_unauthorized",
						title: "Unauthorized",
						status: 400,
						detail: "Coolify refused the token.",
					},
					{ status: 400 },
				),
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);

		await screen.findByText("Coolify could not be read");
		expect(screen.getByLabelText("API token")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByLabelText("Coolify URL")).toHaveAttribute(
			"aria-invalid",
			"false",
		);
	});

	test("without a server the step explains it and Read Coolify stays possible only to the extent the plan allows", async () => {
		stub({ servers: [] });
		await openWizard();
		expect(await screen.findByText(/No server is enrolled yet/)).toBeVisible();
		expect(screen.getByRole("link", { name: "Add a server" })).toHaveAttribute(
			"href",
			"/servers",
		);
		expect(screen.queryByRole("radiogroup", { name: "Server" })).toBeNull();
	});

	test("revoked and pending servers are not offered; a restricted one is explained", async () => {
		stub({
			servers: [
				serverWire({ id: "srv-r", name: "gone", status: "revoked" }),
				serverWire({ id: "srv-p", name: "waiting", status: "pending" }),
				serverWire({ id: "srv-1", name: "alpha" }),
			],
		});
		await openWizard();
		const group = await screen.findByRole("radiogroup", { name: "Server" });
		expect(within(group).getAllByRole("radio")).toHaveLength(1);
		expect(within(group).getByRole("radio", { name: /alpha/ })).toBeChecked();
		expect(screen.getByText(/This server is restricted/)).toBeInTheDocument();
	});

	test("an unrestricted server says host-access options are accepted, and picking another server changes the plan request", async () => {
		const api = stub({
			servers: [
				serverWire({ id: "srv-1", name: "alpha", unrestricted: true }),
				serverWire({ id: "srv-2", name: "beta", status: "offline" }),
			],
		});
		const user = userEvent.setup();
		await openWizard();
		expect(
			await screen.findByText(/This server is unrestricted/),
		).toBeInTheDocument();

		await user.click(screen.getByRole("radio", { name: /beta/ }));
		expect(screen.getByText(/This server is restricted/)).toBeInTheDocument();
		await connect(user);
		await screen.findByText(/1 project ·/);
		expect(planCalls(api)[0]?.body).toMatchObject({ serverId: "srv-2" });
	});

	test("a git connection can be linked; only active connections are offered", async () => {
		const api = stub({
			connections: [
				make("ConnectionView", {
					id: "gc-1",
					name: "acme-github",
					status: "active",
				}),
				make("ConnectionView", {
					id: "gc-2",
					name: "broken-gitlab",
					status: "error",
				}),
			],
		});
		const user = userEvent.setup();
		await openWizard();
		const trigger = await screen.findByRole("combobox", {
			name: /Git connection/,
		});
		expect(trigger).toHaveTextContent("None");
		await user.click(trigger);
		expect(screen.queryByRole("option", { name: "broken-gitlab" })).toBeNull();
		await user.click(
			await screen.findByRole("option", { name: "acme-github" }),
		);

		await connect(user);
		await screen.findByText(/1 project ·/);
		expect(planCalls(api)[0]?.body).toMatchObject({
			choices: { gitConnectionId: "gc-1" },
		});
	});

	test("no git connection select when none is usable", async () => {
		stub({
			connections: [
				make("ConnectionView", { id: "gc-2", name: "broken", status: "error" }),
			],
		});
		await openWizard();
		await screen.findByRole("radiogroup", { name: "Server" });
		expect(screen.queryByText("Git connection (optional)")).toBeNull();
	});
});

describe("review and import", () => {
	test("Back returns to connect with what was typed", async () => {
		stub();
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		await screen.findByText(/1 project ·/);

		await user.click(screen.getByRole("button", { name: "Back" }));
		expect(screen.getByLabelText("Coolify URL")).toHaveValue(` ${URL_VALUE} `);
		expect(screen.getByLabelText("API token")).toHaveValue(` ${TOKEN} `);
	});

	test("imports under an idempotency key and shows the result, then Open goes to the project", async () => {
		const api = stub({
			apply: applied({
				warnings: ["Volume data is not copied."],
				followups: [
					"db: the data is not copied. Dump it on the Coolify host (docker exec abc pg_dump -U postgres -Fc app > db.dump), then run: nebulactrl databases restore db --file db.dump (see docs/operations/database-backups.md#restore-from-a-dump-file).",
				],
			}),
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		const importButton = await screen.findByRole("button", { name: "Import" });
		await waitFor(() => expect(importButton).toBeEnabled());

		await user.click(importButton);

		expect(await screen.findByText(/^Imported: /)).toHaveTextContent(
			"Imported: 1 project, 1 service; nothing deployed.",
		);
		const applyCalls = api.calls.filter(
			(c) => c.path === "/imports/coolify/apply",
		);
		expect(applyCalls).toHaveLength(1);
		expect(applyCalls[0]?.body).toEqual({
			source: { url: URL_VALUE, token: TOKEN },
			choices: {},
			serverId: "srv-1",
		});
		expect(api.applyKeys[0]).toMatch(/^[0-9a-f-]{36}$/);
		expect(screen.getByText("Volume data is not copied.")).toBeInTheDocument();
		expect(
			screen.getByText("docker exec abc pg_dump -U postgres -Fc app > db.dump"),
		).toBeInTheDocument();
		expect(
			screen.getAllByRole("button", { name: "Copy command" }),
		).toHaveLength(2);

		await user.click(screen.getByRole("button", { name: "Open Shop" }));
		await waitFor(() =>
			expect(screen.queryByText("Import from Coolify")).toBeNull(),
		);
		expect(screen.getByLabelText("location")).toHaveTextContent(
			"/p/p-1/production",
		);
		expect(api.unmatched).toEqual([]);
	});

	test("Deploy right away sends deploy and the button and result say so", async () => {
		const api = stub({ apply: applied({ deployments: ["d-1"] }) });
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		await user.click(
			await screen.findByRole("checkbox", { name: "Deploy right away" }),
		);
		const button = await screen.findByRole("button", {
			name: "Import and deploy",
		});
		await waitFor(() => expect(button).toBeEnabled());
		await user.click(button);

		expect(await screen.findByText(/^Imported: /)).toHaveTextContent(
			"1 deploying",
		);
		expect(screen.getByText(/The services are deploying/)).toBeInTheDocument();
		const call = api.calls.find((c) => c.path === "/imports/coolify/apply");
		expect(call?.body).toMatchObject({ deploy: true });

		await user.click(screen.getByRole("button", { name: "Close" }));
		await waitFor(() =>
			expect(screen.queryByText("Import from Coolify")).toBeNull(),
		);
	});

	test("a failed import keeps the review, never echoes secrets, and a retry reuses the key", async () => {
		let attempt = 0;
		const api = stub({
			apply: () => {
				attempt += 1;
				return attempt === 1
					? Response.json(
							{
								code: "internal",
								title: "Boom",
								status: 500,
								detail: `upstream said ${TOKEN}`,
							},
							{ status: 500 },
						)
					: Response.json(applied(), { status: 201 });
			},
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		const button = await screen.findByRole("button", { name: "Import" });
		await waitFor(() => expect(button).toBeEnabled());
		await user.click(button);

		const failed = await screen.findByText("The import failed");
		const alert = failed.closest("[role=alert]") as HTMLElement;
		expect(alert).toHaveTextContent("upstream said •••");
		expect(alert).not.toHaveTextContent(TOKEN);
		expect(alert).toHaveTextContent("you get its result instead");

		await user.click(screen.getByRole("button", { name: "Import" }));
		await screen.findByText(/^Imported: /);
		expect(api.applyKeys).toHaveLength(2);
		expect(api.applyKeys[1]).toBe(api.applyKeys[0] as string);
	});

	test("a changed option is a new request and gets a new key", async () => {
		let attempt = 0;
		const api = stub({
			apply: () => {
				attempt += 1;
				return attempt === 1
					? Response.json({ title: "Boom", status: 500 }, { status: 500 })
					: Response.json(applied(), { status: 201 });
			},
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		const button = await screen.findByRole("button", { name: "Import" });
		await waitFor(() => expect(button).toBeEnabled());
		await user.click(button);
		await screen.findByText("The import failed");

		await user.click(
			screen.getByRole("checkbox", { name: "Deploy right away" }),
		);
		// Changing an option clears the old failure.
		expect(screen.queryByText("The import failed")).toBeNull();
		await user.click(
			await screen.findByRole("button", { name: "Import and deploy" }),
		);
		await screen.findByText(/^Imported: /);
		expect(api.applyKeys[1]).not.toBe(api.applyKeys[0]);
	});

	test("Import stays disabled and says why when the plan has blockers", async () => {
		stub({
			plan: plan({
				installable: false,
				blockers: ["Two projects would use the slug shop"],
			}),
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);

		const alert = (await screen.findByText("This import is blocked")).closest(
			"[role=alert]",
		) as HTMLElement;
		expect(alert).toHaveTextContent("Two projects would use the slug shop");
		expect(screen.getByText("1 blocker")).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.getAllByRole("status").map((s) => s.textContent)).toContain(
				"Two projects would use the slug shop",
			),
		);
		expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();

		await user.click(
			within(alert).getByRole("button", { name: "Back to connect" }),
		);
		expect(screen.getByLabelText("Coolify URL")).toBeInTheDocument();
	});

	test("Import says nothing is included when every resource is left out", async () => {
		stub({
			plan: plan({
				totals: {
					projects: 1,
					services: 0,
					databases: 0,
					variables: 0,
					secrets: 0,
					domains: 0,
				},
			}),
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		await waitFor(() =>
			expect(screen.getAllByRole("status").map((s) => s.textContent)).toContain(
				"Nothing is included.",
			),
		);
		expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
	});

	test("a change of choices re-plans with those choices and Import waits for the answer", async () => {
		let release: () => void = () => {};
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		let hold = false;
		const api = stub({
			plan: async () => {
				if (hold) await gate;
				return Response.json(plan());
			},
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		const before = await settled(api);
		const importButton = screen.getByRole("button", { name: "Import" });
		hold = true;

		await user.click(screen.getByRole("checkbox", { name: "Include web" }));

		await waitFor(() => expect(planCalls(api)).toHaveLength(before + 1));
		expect(planCalls(api)[before]?.body).toEqual({
			source: { url: URL_VALUE, token: TOKEN },
			choices: { resources: { "res-web": { include: false } } },
			serverId: "srv-1",
		});
		expect(importButton).toBeDisabled();
		expect(screen.getAllByRole("status").map((s) => s.textContent)).toContain(
			"Checking the plan…",
		);
		expect(screen.getByText("Updating")).toHaveAttribute(
			"aria-hidden",
			"false",
		);

		await act(async () => release());
		await waitFor(() => expect(importButton).toBeEnabled());
	});

	test("a failed re-plan keeps the last plan, offers Try again, and Import is blocked", async () => {
		let failing = false;
		const api = stub({
			plan: () => {
				if (failing) {
					return Response.json(
						{ title: "Down", status: 502, detail: `no route using ${TOKEN}` },
						{ status: 502 },
					);
				}
				return Response.json(plan());
			},
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		const before = await settled(api);
		failing = true;

		await user.click(screen.getByRole("checkbox", { name: "Include web" }));
		const failed = await screen.findByText("The plan could not be updated");
		const alert = failed.closest("[role=alert]") as HTMLElement;
		expect(alert).toHaveTextContent("no route using •••");
		expect(alert).toHaveTextContent(
			"The cards show the last plan that worked.",
		);
		expect(
			screen.getByRole("checkbox", { name: "Include web" }),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
		expect(screen.getAllByRole("status").map((s) => s.textContent)).toContain(
			"The plan could not be updated. Change a choice to try again.",
		);

		failing = false;
		await user.click(screen.getByRole("button", { name: "Try again" }));
		await waitFor(() => expect(planCalls(api)).toHaveLength(before + 2));
		await waitFor(() =>
			expect(screen.queryByText("The plan could not be updated")).toBeNull(),
		);
		expect(planCalls(api)[before + 1]?.body).toMatchObject({
			choices: { resources: { "res-web": { include: false } } },
		});
	});

	test("choices made in the review go out in the apply request", async () => {
		const api = stub({
			plan: plan({
				projects: [
					project({}, [
						resource({
							env: [envRow({ key: "API_URL" })],
						}),
					]),
				],
			}),
			projects: [
				make("Project", {
					id: "np-1",
					name: "Existing shop",
					slug: "existing",
					system: false,
				}),
				make("Project", {
					id: "np-sys",
					name: "System",
					slug: "system",
					system: true,
				}),
			],
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		const before = await settled(api);

		await user.clear(screen.getByLabelText("New project name"));
		await user.type(screen.getByLabelText("New project name"), "Storefront");
		await waitFor(() =>
			expect(planCalls(api).at(-1)?.body).toMatchObject({
				choices: { projects: { "proj-1": { name: "Storefront" } } },
			}),
		);
		expect(planCalls(api).length).toBeGreaterThan(before);
		const button = screen.getByRole("button", { name: "Import" });
		await waitFor(() => expect(button).toBeEnabled());
		await user.click(button);
		await screen.findByText(/^Imported: /);
		const call = api.calls.find((c) => c.path === "/imports/coolify/apply");
		expect(call?.body).toMatchObject({
			choices: { projects: { "proj-1": { name: "Storefront" } } },
		});
	});

	test("merging into an existing project offers only non-system projects", async () => {
		const api = stub({
			projects: [
				make("Project", {
					id: "np-1",
					name: "Existing shop",
					slug: "existing",
					system: false,
				}),
				make("Project", {
					id: "np-sys",
					name: "System",
					slug: "system",
					system: true,
				}),
			],
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		const before = await settled(api);
		await user.click(
			await screen.findByRole("combobox", { name: "Import into" }),
		);
		expect(screen.queryByRole("option", { name: "System" })).toBeNull();
		await user.click(screen.getByRole("option", { name: "Existing shop" }));

		await waitFor(() => expect(planCalls(api)).toHaveLength(before + 1));
		expect(planCalls(api)[before]?.body).toMatchObject({
			choices: { projects: { "proj-1": { existingProjectId: "np-1" } } },
		});
		expect(
			screen.getByText(/Environments with the same name are reused/),
		).toBeInTheDocument();
	});

	test("re-reading the same Coolify keeps choices; a different address starts them over", async () => {
		const api = stub();
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		const before = await settled(api);
		await user.click(screen.getByRole("checkbox", { name: "Include web" }));
		await waitFor(() => expect(planCalls(api)).toHaveLength(before + 1));

		await user.click(screen.getByRole("button", { name: "Back" }));
		await user.click(screen.getByRole("button", { name: "Read Coolify" }));
		await screen.findByRole("checkbox", { name: "Include web" });
		expect(
			screen.getByRole("checkbox", { name: "Include web" }),
		).not.toBeChecked();
		const same = planCalls(api).at(-1);
		expect(same?.body).toMatchObject({
			choices: { resources: { "res-web": { include: false } } },
		});

		await user.click(screen.getByRole("button", { name: "Back" }));
		await user.clear(screen.getByLabelText("Coolify URL"));
		await user.type(
			screen.getByLabelText("Coolify URL"),
			"https://other.example",
		);
		await user.click(screen.getByRole("button", { name: "Read Coolify" }));
		await waitFor(() =>
			expect(planCalls(api).at(-1)?.body).toMatchObject({
				source: { url: "https://other.example" },
			}),
		);
		expect(planCalls(api).at(-1)?.body).toMatchObject({ choices: {} });
		await waitFor(() =>
			expect(
				screen.getByRole("checkbox", { name: "Include web" }),
			).toBeChecked(),
		);
	});

	test("a restricted server is warned about services that need host access", async () => {
		stub({
			plan: plan({
				projects: [
					project({}, [resource({ needsUnrestricted: ["privileged: true"] })]),
				],
			}),
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		const title = await screen.findByText("These need an unrestricted server");
		const note = title.closest("[role=note]") as HTMLElement;
		expect(note).toHaveTextContent("privileged: true");
		expect(note).toHaveTextContent("alpha is restricted");
	});

	test("while Coolify is read the button says so and the form stays until the plan arrives", async () => {
		let release: () => void = () => {};
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		stub({
			plan: async () => {
				await gate;
				return Response.json(plan());
			},
		});
		const user = userEvent.setup();
		await openWizard();
		await connect(user);
		const reading = await screen.findByRole("button", {
			name: /Reading Coolify/,
		});
		expect(reading).toBeDisabled();
		expect(screen.getByLabelText("Coolify URL")).toBeInTheDocument();
		await act(async () => release());
		await screen.findByText(/1 project ·/);
	});
});
