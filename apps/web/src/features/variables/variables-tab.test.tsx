import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	DatabaseView,
	FieldChange,
	VariableList,
	Variable as WireVariable,
} from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { VariablesTab } from "./variables-tab";

// Literal `${NAME}` placeholder: the escaped `$` keeps it from being a template placeholder.
const ref = (name: string) => `\${${name}}`;

const LIST = "GET /services/:id/variables";
const PENDING = "GET /services/:id/pending-changes";
const DISCARD = "DELETE /services/:id/pending-changes/:field";
const DELETE = "DELETE /services/:id/variables/:key";

/** A wire variable: plain and runtime-scoped with an empty value unless the test says otherwise. */
function variable(wire: Overrides<WireVariable> & { key: string }) {
	return make("Variable", {
		value: wire.isSecret ? undefined : "",
		isSecret: false,
		computedSecret: false,
		source: "manual",
		scope: "runtime",
		comment: "",
		generated: [],
		references: [],
		rotateEveryDays: null,
		ageDays: 0,
		rotationDue: false,
		set: true,
		version: 1,
		updatedAt: "2026-01-01T00:00:00Z",
		...wire,
	});
}

const list = (over: Overrides<VariableList> = {}) => ({
	body: make("VariableList", over),
});

/** Routes every request the tab (and its closed dialogs) can make; overrides win. */
function routes(overrides: Parameters<typeof stubApi>[0] = {}) {
	return {
		[LIST]: list({ items: [variable({ key: "LEVEL", value: "info" })] }),
		"GET /services/:id/config-source": problem(404, {
			code: "not_found",
			title: "Not found",
		}),
		"GET /services/:id/suggested-variables": {
			body: make("SuggestedVariableList"),
		},
		"GET /environments/:id/services": { body: make("PageServiceView") },
		"GET /environments/:id/shared-variables": {
			body: make("SharedVariableList"),
		},
		"GET /team/shared-variables": { body: make("SharedVariableList") },
		"GET /secret-managers": { body: make("ListOutputBody") },
		...overrides,
	};
}

async function renderTab(service: Service = makeService({ id: "svc-1" })) {
	return renderWithProviders(<VariablesTab service={service} />);
}

const rowsOf = async () =>
	within(
		await screen.findByRole("list", { name: "Service variables" }),
	).getAllByRole("listitem");
const rowFor = async (key: string) => {
	const row = (await rowsOf()).find((r) => r.textContent?.startsWith(key));
	if (!row) throw new Error(`no row for ${key}`);
	return row;
};

async function openActions(
	user: ReturnType<typeof userEvent.setup>,
	key: string,
) {
	await user.click(
		await screen.findByRole("button", { name: `Actions for ${key}` }),
	);
}

describe("VariablesTab: loading states", () => {
	it("shows a loading state until the variables arrive", async () => {
		const reply = Promise.withResolvers<Response>();
		stubApi(routes({ [LIST]: () => reply.promise }));
		await renderTab();
		expect(
			await screen.findByRole("status", { name: "Loading variables" }),
		).toBeInTheDocument();
		reply.resolve(
			Response.json(
				make("VariableList", { items: [variable({ key: "A", value: "1" })] }),
			),
		);
		expect(await screen.findByText("A")).toBeInTheDocument();
		expect(
			screen.queryByRole("status", { name: "Loading variables" }),
		).toBeNull();
	});

	it("shows the failure with the server's reason and retries on request", async () => {
		const user = userEvent.setup();
		let attempts = 0;
		stubApi(
			routes({
				[LIST]: () => {
					attempts += 1;
					return attempts === 1
						? Response.json(
								problem(503, {
									code: "unavailable",
									title: "Unavailable",
									detail: "control plane is restarting",
								}).body,
								{ status: 503 },
							)
						: Response.json(
								make("VariableList", {
									items: [variable({ key: "BACK", value: "1" })],
								}),
							);
				},
			}),
		);
		await renderTab();
		expect(
			await screen.findByText("Variables did not load"),
		).toBeInTheDocument();
		expect(screen.getByText("control plane is restarting")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(await screen.findByText("BACK")).toBeInTheDocument();
		expect(screen.queryByText("Variables did not load")).toBeNull();
	});

	it("invites adding the first variable when there are none", async () => {
		stubApi(routes({ [LIST]: list() }));
		await renderTab();
		expect(await screen.findByText("No variables yet")).toBeInTheDocument();
		expect(
			screen.queryByRole("list", { name: "Service variables" }),
		).toBeNull();
	});
});

describe("VariablesTab: the list", () => {
	it("shows plain values, empty values and masked secrets that cannot be revealed", async () => {
		stubApi(
			routes({
				[LIST]: list({
					items: [
						variable({ key: "LEVEL", value: "info" }),
						variable({ key: "BLANK", value: "" }),
						variable({
							key: "TOKEN",
							isSecret: true,
							value: "leaked-by-server",
						}),
					],
				}),
			}),
		);
		await renderTab();
		const level = await rowFor("LEVEL");
		expect(level).toHaveTextContent("info");
		expect(await rowFor("BLANK")).toHaveTextContent("empty");
		const token = await rowFor("TOKEN");
		expect(token).toHaveTextContent("••••");
		expect(within(token).getByText("Secret value, hidden")).toBeInTheDocument();
		expect(within(token).getByText("secret")).toBeInTheDocument();
		// Even a value the server sent by mistake is never drawn.
		expect(document.body.textContent).not.toContain("leaked-by-server");
	});

	it("annotates what a variable is: generated, shared, build scoped, references", async () => {
		stubApi(
			routes({
				[LIST]: list({
					items: [
						variable({
							key: "SESSION",
							isSecret: true,
							source: "generated",
							generated: [
								{ fn: "secret", args: ["32"] },
								{ fn: "uuid", args: null },
							],
						}),
						variable({
							key: "URL",
							value: "x",
							computedSecret: true,
							scope: "both",
							references: [
								{ ns: "shared", key: "A" },
								{ ns: "postgres", key: "HOST" },
							],
						}),
						variable({ key: "PLAIN", value: "p" }),
					],
				}),
			}),
		);
		await renderTab();
		const session = await rowFor("SESSION");
		expect(session).toHaveTextContent("generated · secret(32) + uuid()");
		const url = await rowFor("URL");
		expect(url).toHaveTextContent("contains a secret");
		expect(url).toHaveTextContent("shared");
		expect(url).toHaveTextContent("both");
		expect(url).toHaveTextContent("references 2");
		expect(screen.getByTitle("shared.A, postgres.HOST")).toBeInTheDocument();
		// A plain variable carries no badges at all.
		expect(await rowFor("PLAIN")).not.toHaveTextContent(
			/shared|generated|references/,
		);
	});

	it("flags a due rotation, and shows the age of an old secret only", async () => {
		stubApi(
			routes({
				[LIST]: list({
					items: [
						variable({
							key: "DUE",
							isSecret: true,
							rotationDue: true,
							ageDays: 400,
						}),
						variable({ key: "OLD", isSecret: true, ageDays: 45 }),
						variable({ key: "NEW", isSecret: true, ageDays: 29 }),
						variable({ key: "OLD_PLAIN", value: "x", ageDays: 90 }),
					],
				}),
			}),
		);
		await renderTab();
		const due = await rowFor("DUE");
		expect(due).toHaveTextContent("rotation due");
		expect(due).not.toHaveTextContent("age 400d");
		expect(await rowFor("OLD")).toHaveTextContent("age 45d");
		expect(await rowFor("NEW")).not.toHaveTextContent(/age/);
		expect(await rowFor("OLD_PLAIN")).not.toHaveTextContent(/age/);
	});

	it("explains a variable that does not resolve", async () => {
		stubApi(
			routes({
				[LIST]: list({
					items: [
						variable({
							key: "URL",
							value: ref("{db.HOST}"),
							problem: "db has no HOST",
							references: [{ ns: "db", key: "HOST" }],
						}),
					],
				}),
			}),
		);
		await renderTab();
		expect(await rowFor("URL")).toHaveTextContent(
			"does not resolve: db has no HOST",
		);
	});
});

describe("VariablesTab: filtering", () => {
	const many = () =>
		Array.from({ length: 9 }, (_, index) =>
			variable({
				key: `KEY_${index}`,
				value: index === 3 ? "needle-value" : `v${index}`,
			}),
		);

	it("offers no filter for a short list", async () => {
		stubApi(routes({ [LIST]: list({ items: many().slice(0, 8) }) }));
		await renderTab();
		await rowsOf();
		expect(
			screen.queryByRole("searchbox", { name: "Filter variables" }),
		).toBeNull();
	});

	it("filters a long list by name or by plain value and says how many show", async () => {
		const user = userEvent.setup();
		stubApi(routes({ [LIST]: list({ items: many() }) }));
		await renderTab();
		const filter = await screen.findByRole("searchbox", {
			name: "Filter variables",
		});
		expect(await rowsOf()).toHaveLength(9);

		await user.type(filter, "key_7");
		expect(await rowsOf()).toHaveLength(1);
		expect((await rowsOf())[0]).toHaveTextContent("KEY_7");
		expect(screen.getByText("Showing 1 of 9.")).toBeInTheDocument();

		await user.clear(filter);
		await user.type(filter, "NEEDLE");
		expect(await rowsOf()).toHaveLength(1);
		expect((await rowsOf())[0]).toHaveTextContent("KEY_3");
	});

	it("never matches the text of a secret and says nothing matches", async () => {
		const user = userEvent.setup();
		const items = [
			...many(),
			variable({ key: "TOKEN", isSecret: true, value: "hunter2" }),
		];
		stubApi(routes({ [LIST]: list({ items }) }));
		await renderTab();
		const filter = await screen.findByRole("searchbox", {
			name: "Filter variables",
		});
		await user.type(filter, "hunter2");
		expect(
			await screen.findByText("No variable matches “hunter2”."),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("list", { name: "Service variables" }),
		).toBeNull();
		// A filter that matches by name still finds the secret itself.
		await user.clear(filter);
		await user.type(filter, "token");
		expect((await rowsOf())[0]).toHaveTextContent("TOKEN");
	});

	it("shows no 'Showing' note when everything matches", async () => {
		const user = userEvent.setup();
		stubApi(routes({ [LIST]: list({ items: many() }) }));
		await renderTab();
		await user.type(
			await screen.findByRole("searchbox", { name: "Filter variables" }),
			"key",
		);
		expect(await rowsOf()).toHaveLength(9);
		expect(screen.queryByText(/Showing/)).toBeNull();
	});
});

describe("VariablesTab: dialogs", () => {
	it("New variable opens the add dialog", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderTab();
		await user.click(
			await screen.findByRole("button", { name: "New variable" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("New variable")).toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Add variable" }),
		).toBeDisabled();
	});

	it("only a git service can scope a variable to the build", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderTab(makeService({ id: "svc-1", sourceType: "git" }));
		await user.click(
			await screen.findByRole("button", { name: "New variable" }),
		);
		expect(await screen.findByText("Available at")).toBeInTheDocument();
	});

	it("an image service has no build scope", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderTab(makeService({ id: "svc-1", sourceType: "image" }));
		await user.click(
			await screen.findByRole("button", { name: "New variable" }),
		);
		await screen.findByRole("dialog");
		expect(screen.queryByText("Available at")).toBeNull();
	});

	it("Edit opens the variable with its current value", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderTab();
		await openActions(user, "LEVEL");
		await user.click(await screen.findByRole("menuitem", { name: "Edit" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Edit LEVEL")).toBeInTheDocument();
		expect(within(dialog).getByLabelText("Value")).toHaveValue("info");
	});

	it("adding a variable stages it and refreshes the list", async () => {
		const user = userEvent.setup();
		let items = [variable({ key: "LEVEL", value: "info" })];
		const api = stubApi(
			routes({
				[LIST]: () => Response.json(make("VariableList", { items })),
				"PUT /services/:id/variables": () => {
					items = [...items, variable({ key: "NEW_ONE", value: "1" })];
					return Response.json(make("VariableList", { items }));
				},
			}),
		);
		await renderTab();
		await user.click(
			await screen.findByRole("button", { name: "New variable" }),
		);
		await user.type(await screen.findByLabelText("Name"), "NEW_ONE");
		await user.type(screen.getByLabelText("Value"), "1");
		await user.click(screen.getByRole("button", { name: "Add variable" }));
		expect(await screen.findByText("Added NEW_ONE")).toBeInTheDocument();
		await waitFor(async () => expect(await rowsOf()).toHaveLength(2));
		expect(api.calls.filter((c) => c.method === "PUT")).toHaveLength(1);
	});

	it("an existing name cannot be added again", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await renderTab();
		await user.click(
			await screen.findByRole("button", { name: "New variable" }),
		);
		await user.type(await screen.findByLabelText("Name"), "LEVEL");
		await user.click(screen.getByRole("button", { name: "Add variable" }));
		expect(
			await screen.findByText("LEVEL already exists; edit it instead."),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	it("Raw editor opens with the plain variables as text", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await renderTab();
		await user.click(await screen.findByRole("button", { name: "Raw editor" }));
		expect(
			await screen.findByLabelText("Variables in .env format"),
		).toHaveValue("LEVEL=info");
	});

	it("History opens the versions of that variable", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes({
				"GET /services/:id/variables/:key/history": {
					body: make("HistoryOutputBody"),
				},
			}),
		);
		await renderTab();
		await openActions(user, "LEVEL");
		await user.click(await screen.findByRole("menuitem", { name: "History" }));
		expect(await screen.findByText("History of LEVEL")).toBeInTheDocument();
		expect(
			api.calls.some(
				(c) => c.path === "/services/svc-1/variables/LEVEL/history",
			),
		).toBe(true);
	});

	it("offers Rotate only for a generated variable and plans before staging", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes({
				[LIST]: list({
					items: [
						variable({ key: "PLAIN", value: "x" }),
						variable({ key: "GEN", isSecret: true, source: "generated" }),
					],
				}),
				"POST /services/:id/variables/:key/rotate": {
					body: make("VariableRotationPlan", {
						key: "GEN",
						kind: "app-secret",
						staged: false,
						affected: [],
						blockers: [],
						deployOrder: [],
						warnings: [],
					}),
				},
			}),
		);
		await renderTab();
		await openActions(user, "PLAIN");
		expect(screen.queryByRole("menuitem", { name: /Rotate/ })).toBeNull();
		await user.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());

		await openActions(user, "GEN");
		await user.click(await screen.findByRole("menuitem", { name: /Rotate/ }));
		expect(await screen.findByText("Rotate GEN")).toBeInTheDocument();
		await screen.findByRole("button", { name: "Stage rotation" });
		expect(
			api.calls.filter((c) => c.path.endsWith("/rotate")).map((c) => c.body),
		).toEqual([{ dryRun: true }]);
	});
});

describe("VariablesTab: deleting", () => {
	it("asks first, then deletes the variable and toasts", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes({ [DELETE]: { status: 204 } }));
		await renderTab();
		await openActions(user, "LEVEL");
		await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
		expect(await screen.findByText("Delete LEVEL?")).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
		await user.click(screen.getByRole("button", { name: "Delete variable" }));
		expect(await screen.findByText("Deleted LEVEL")).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		const del = api.calls.filter((c) => c.method === "DELETE");
		expect(del.map((c) => c.path)).toEqual(["/services/svc-1/variables/LEVEL"]);
	});

	it("changes nothing when the confirmation is cancelled", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes({ [DELETE]: { status: 204 } }));
		await renderTab();
		await openActions(user, "LEVEL");
		await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
		await screen.findByText("Delete LEVEL?");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByText("Delete LEVEL?")).toBeNull());
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
		expect(screen.queryByText("Deleted LEVEL")).toBeNull();
	});
});

describe("VariablesTab: staged changes", () => {
	const pending = (items: FieldChange[]) => ({
		[PENDING]: { body: make("ChangesOutputBody", { items }) },
	});

	it("loads the staged changes only when the service has staged variable changes", async () => {
		const api = stubApi(routes());
		await renderTab(
			makeService({
				id: "svc-1",
				pendingChangeCounts: { variables: 0, settings: 2 },
			}),
		);
		await rowsOf();
		expect(api.calls.some((c) => c.path.endsWith("/pending-changes"))).toBe(
			false,
		);
	});

	it("marks a variable with a staged change and offers to revert it", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes({
				...pending([
					{ field: "variables.LEVEL", from: "info", to: "debug" },
					{ field: "settings.port", from: "1", to: "2" },
				]),
				[DISCARD]: { body: make("ServiceView") },
			}),
		);
		await renderTab(
			makeService({
				id: "svc-1",
				pendingChangeCounts: { variables: 1, settings: 1 },
			}),
		);
		await waitFor(async () =>
			expect(await rowFor("LEVEL")).toHaveTextContent("staged"),
		);
		await openActions(user, "LEVEL");
		await user.click(
			await screen.findByRole("menuitem", { name: "Revert staged change" }),
		);
		expect(await screen.findByText("Reverted LEVEL")).toBeInTheDocument();
		const discard = api.calls.find((c) => c.method === "DELETE");
		expect(discard?.path).toBe(
			"/services/svc-1/pending-changes/variables.LEVEL",
		);
	});

	it("offers no revert on a variable without a staged change", async () => {
		const user = userEvent.setup();
		stubApi(
			routes(pending([{ field: "variables.OTHER", from: "a", to: "b" }])),
		);
		await renderTab(
			makeService({
				id: "svc-1",
				pendingChangeCounts: { variables: 1, settings: 0 },
			}),
		);
		await openActions(user, "LEVEL");
		await screen.findByRole("menuitem", { name: "Edit" });
		expect(
			screen.queryByRole("menuitem", { name: "Revert staged change" }),
		).toBeNull();
		expect(await rowFor("LEVEL")).not.toHaveTextContent("staged");
	});

	it("says a restaged variable changed through a reference and cannot be reverted by itself", async () => {
		const user = userEvent.setup();
		stubApi(
			routes({
				[LIST]: list({
					items: [
						variable({ key: "URL", value: ref("{db.HOST}") }),
						variable({ key: "VIA_UNKNOWN", value: ref("{x.Y}") }),
					],
				}),
				...pending([
					{
						field: "variables.URL",
						from: "same",
						to: "same",
						origin: "inherited",
						via: "db.HOST",
					},
					{
						field: "variables.VIA_UNKNOWN",
						from: "s",
						to: "s",
						origin: "inherited",
					},
				]),
			}),
		);
		await renderTab(
			makeService({
				id: "svc-1",
				pendingChangeCounts: { variables: 2, settings: 0 },
			}),
		);
		await waitFor(async () =>
			expect(await rowFor("URL")).toHaveTextContent(
				"restaged: db.HOST changed",
			),
		);
		expect(await rowFor("VIA_UNKNOWN")).toHaveTextContent(
			"restaged: a referenced variable changed",
		);
		await openActions(user, "URL");
		await screen.findByRole("menuitem", { name: "Edit" });
		expect(
			screen.queryByRole("menuitem", { name: "Revert staged change" }),
		).toBeNull();
	});

	it("lists a staged removal and restores it", async () => {
		const user = userEvent.setup();
		const api = stubApi(
			routes({
				...pending([
					{ field: "variables.ZED", from: "old", to: "" },
					{ field: "variables.ALPHA", from: "old", to: "" },
				]),
				[DISCARD]: { body: make("ServiceView") },
			}),
		);
		await renderTab(
			makeService({
				id: "svc-1",
				pendingChangeCounts: { variables: 2, settings: 0 },
			}),
		);
		await waitFor(async () => expect(await rowsOf()).toHaveLength(3));
		const rows = await rowsOf();
		// Removed ones follow the live ones, alphabetically.
		expect(rows[1]).toHaveTextContent("ALPHA");
		expect(rows[2]).toHaveTextContent("ZED");
		expect(rows[1]).toHaveTextContent("Removed on next deploy");
		expect(rows[1]).toHaveTextContent("staged");
		await user.click(
			within(rows[2] as HTMLElement).getByRole("button", {
				name: "Restore ZED",
			}),
		);
		expect(await screen.findByText("Reverted ZED")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/services/svc-1/pending-changes/variables.ZED",
		);
	});

	it("shows a staged removal even when no variable is left", async () => {
		stubApi(
			routes({
				[LIST]: list(),
				...pending([{ field: "variables.GONE", from: "old", to: "" }]),
			}),
		);
		await renderTab(
			makeService({
				id: "svc-1",
				pendingChangeCounts: { variables: 1, settings: 0 },
			}),
		);
		expect(
			await screen.findByText("Removed on next deploy"),
		).toBeInTheDocument();
		expect(screen.queryByText("No variables yet")).toBeNull();
	});

	it("does not list a removal for a key that exists again", async () => {
		stubApi(
			routes(pending([{ field: "variables.LEVEL", from: "info", to: "" }])),
		);
		await renderTab(
			makeService({
				id: "svc-1",
				pendingChangeCounts: { variables: 1, settings: 0 },
			}),
		);
		await waitFor(async () =>
			expect(await rowFor("LEVEL")).toHaveTextContent("staged"),
		);
		expect(screen.queryByText("Removed on next deploy")).toBeNull();
	});

	it("hides staged removals while a filter is typed", async () => {
		const user = userEvent.setup();
		const items = Array.from({ length: 9 }, (_, i) =>
			variable({ key: `K_${i}`, value: "v" }),
		);
		stubApi(
			routes({
				[LIST]: list({ items }),
				...pending([{ field: "variables.GONE", from: "old", to: "" }]),
			}),
		);
		await renderTab(
			makeService({
				id: "svc-1",
				pendingChangeCounts: { variables: 1, settings: 0 },
			}),
		);
		await screen.findByText("Removed on next deploy");
		await user.type(
			screen.getByRole("searchbox", { name: "Filter variables" }),
			"K_1",
		);
		await waitFor(() =>
			expect(screen.queryByText("Removed on next deploy")).toBeNull(),
		);
	});

	it("tells why a revert failed and keeps the row", async () => {
		const user = userEvent.setup();
		stubApi(
			routes({
				...pending([{ field: "variables.LEVEL", from: "info", to: "debug" }]),
				[DISCARD]: problem(409, {
					code: "conflict",
					title: "Conflict",
					detail: "already deployed",
				}),
			}),
		);
		await renderTab(
			makeService({
				id: "svc-1",
				pendingChangeCounts: { variables: 1, settings: 0 },
			}),
		);
		await waitFor(async () =>
			expect(await rowFor("LEVEL")).toHaveTextContent("staged"),
		);
		await openActions(user, "LEVEL");
		await user.click(
			await screen.findByRole("menuitem", { name: "Revert staged change" }),
		);
		expect(
			await screen.findByText("Could not revert LEVEL"),
		).toBeInTheDocument();
		expect(screen.getByText("already deployed")).toBeInTheDocument();
		expect(screen.queryByText("Reverted LEVEL")).toBeNull();
	});
});

describe("VariablesTab: what the platform provides", () => {
	const platform: VariableList["platform"] = [
		{
			key: "NEBULA_RELEASE_ID",
			value: "rel-1",
			description: "The release.",
			category: "release",
			referenceable: false,
		},
		{
			key: "NEBULA_PRIVATE_HOST",
			value: "",
			description: "Private host.",
			category: "container",
			referenceable: true,
		},
	];

	it("is collapsed to a count and opens to list each variable", async () => {
		const user = userEvent.setup();
		stubApi(routes({ [LIST]: list({ items: [], platform }) }));
		await renderTab();
		const toggle = await screen.findByRole("button", {
			name: /2 variables provided by NebulaCtrl/,
		});
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "true");
		const listbox = await screen.findByRole("list", {
			name: "Variables provided by NebulaCtrl",
		});
		const rows = within(listbox).getAllByRole("listitem");
		expect(rows[0]).toHaveTextContent("NEBULA_RELEASE_ID");
		expect(rows[0]).toHaveTextContent("rel-1");
		expect(rows[0]).toHaveTextContent(
			"The release. Set with the release; other services cannot reference it.",
		);
		// A referenceable variable has no such note, and an empty value says so.
		expect(rows[1]).toHaveTextContent("empty");
		expect(rows[1]).not.toHaveTextContent("cannot reference");
	});

	it("uses the singular and counts exports of a service that is not a database", async () => {
		const user = userEvent.setup();
		stubApi(
			routes({
				[LIST]: list({
					items: [],
					platform: platform.slice(1),
					exports: [
						{ key: "URL", description: "Public URL", secret: false },
						{ key: "TOKEN", description: "Access token", secret: true },
					],
				}),
			}),
		);
		await renderTab();
		const toggle = await screen.findByRole("button", {
			name: /1 variable provided by NebulaCtrl, 2 exports/,
		});
		await user.click(toggle);
		const rows = within(
			await screen.findByRole("list", {
				name: "Variables provided by NebulaCtrl",
			}),
		).getAllByRole("listitem");
		expect(rows).toHaveLength(3);
		expect(rows[1]).toHaveTextContent("URL");
		expect(rows[1]).toHaveTextContent("computed");
		expect(rows[1]).toHaveTextContent("export");
		expect(rows[2]).toHaveTextContent("TOKEN");
		expect(rows[2]).toHaveTextContent("••••");
		expect(rows[2]).not.toHaveTextContent("computed");
	});

	it("says one export in the singular", async () => {
		stubApi(
			routes({
				[LIST]: list({
					items: [],
					platform: [],
					exports: [{ key: "URL", description: "Public URL", secret: false }],
				}),
			}),
		);
		await renderTab();
		expect(
			await screen.findByRole("button", {
				name: /0 variables provided by NebulaCtrl, 1 export(?!s)/,
			}),
		).toBeInTheDocument();
	});

	it("is absent when the platform provides nothing", async () => {
		stubApi(routes());
		await renderTab();
		await rowsOf();
		expect(screen.queryByText(/provided by NebulaCtrl/)).toBeNull();
	});
});

describe("VariablesTab: a managed database", () => {
	const dbService = () =>
		makeService({
			id: "svc-db",
			kind: "database",
			config: {
				...makeService().config,
				database: { engine: "postgres", version: "16" },
			} as Service["config"],
		});

	const databaseView = (overrides: Overrides<DatabaseView>) => ({
		body: make("DatabaseView", {
			serviceId: "svc-db",
			image: "postgres:16",
			deployed: true,
			credentialKeys: ["POSTGRES_PASSWORD"],
			exports: [],
			...overrides,
		}),
	});

	it("locks the credentials of a deployed database: no edit, no delete, and says why", async () => {
		const user = userEvent.setup();
		stubApi(
			routes({
				[LIST]: list({
					items: [
						variable({
							key: "POSTGRES_PASSWORD",
							isSecret: true,
							source: "generated",
						}),
						variable({ key: "TZ", value: "UTC" }),
					],
				}),
				"GET /services/:id/database": databaseView({}),
			}),
		);
		await renderTab(dbService());
		await waitFor(async () =>
			expect(await rowFor("POSTGRES_PASSWORD")).toHaveTextContent("managed"),
		);
		expect(await rowFor("TZ")).not.toHaveTextContent("managed");

		await openActions(user, "POSTGRES_PASSWORD");
		expect(
			await screen.findByRole("menuitem", { name: "Edit" }),
		).toHaveAttribute("aria-disabled", "true");
		expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		// Rotation is the database's job, not the variable's.
		expect(screen.queryByRole("menuitem", { name: /Rotate/ })).toBeNull();
		expect(
			screen.getByText("Managed by the database: use Rotate credentials"),
		).toBeInTheDocument();
	});

	it("leaves the credentials editable before the first deploy", async () => {
		const user = userEvent.setup();
		stubApi(
			routes({
				[LIST]: list({
					items: [
						variable({
							key: "POSTGRES_PASSWORD",
							isSecret: true,
							source: "generated",
						}),
					],
				}),
				"GET /services/:id/database": databaseView({ deployed: false }),
			}),
		);
		await renderTab(dbService());
		await rowsOf();
		await openActions(user, "POSTGRES_PASSWORD");
		const edit = await screen.findByRole("menuitem", { name: "Edit" });
		expect(edit).not.toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByRole("menuitem", { name: /Rotate/ }),
		).toBeInTheDocument();
	});

	it("lists the database's connection variables on top and does not repeat its exports", async () => {
		stubApi(
			routes({
				[LIST]: list({
					items: [variable({ key: "TZ", value: "UTC" })],
					platform: [],
					exports: [{ key: "DATABASE_URL", description: "URL", secret: true }],
				}),
				"GET /services/:id/database": databaseView({
					credentialKeys: [],
					exports: [
						{
							key: "DATABASE_URL",
							description: "Connection URL",
							primary: true,
							reference: ref("{db.DATABASE_URL}"),
							secret: true,
						},
					],
				}),
			}),
		);
		await renderTab(dbService());
		const connection = await screen.findByRole("region", {
			name: "Connection variables",
		});
		expect(
			within(connection).getByText(ref("{db.DATABASE_URL}")),
		).toBeInTheDocument();
		// With connection variables shown above, the platform disclosure has no exports.
		await waitFor(() =>
			expect(
				screen.queryByText(/provided by NebulaCtrl, \d export/),
			).toBeNull(),
		);
	});

	it("does not ask for a database view of an ordinary service", async () => {
		const api = stubApi(routes());
		await renderTab();
		await rowsOf();
		expect(api.calls.some((c) => c.path.endsWith("/database"))).toBe(false);
	});
});

describe("VariablesTab: suggestions", () => {
	it("a suggestion that needs a value opens the form with it filled in", async () => {
		const user = userEvent.setup();
		stubApi(
			routes({
				"GET /services/:id/suggested-variables": {
					body: make("SuggestedVariableList", {
						items: [
							{
								key: "DATABASE_URL",
								value: ref("{db.DATABASE_URL}"),
								needsInput: true,
								proposedSecret: true,
								reason: "Database db is not referenced yet",
								source: "database",
								warnings: [],
							},
						],
					}),
				},
			}),
		);
		await renderTab();
		await user.click(await screen.findByRole("button", { name: "Set…" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByLabelText("Name")).toHaveValue("DATABASE_URL");
		expect(within(dialog).getByLabelText("Value")).toHaveValue(
			ref("{db.DATABASE_URL}"),
		);
		expect(
			within(dialog).getByRole("checkbox", { name: "Secret" }),
		).toBeChecked();
	});

	it("closing the form drops the suggestion draft", async () => {
		const user = userEvent.setup();
		stubApi(
			routes({
				"GET /services/:id/suggested-variables": {
					body: make("SuggestedVariableList", {
						items: [
							{
								key: "DRAFTED",
								value: "",
								needsInput: true,
								proposedSecret: false,
								reason: "r",
								source: "database",
								warnings: [],
							},
						],
					}),
				},
			}),
		);
		await renderTab();
		await user.click(await screen.findByRole("button", { name: "Set…" }));
		await screen.findByRole("dialog");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await user.click(screen.getByRole("button", { name: "New variable" }));
		expect(await screen.findByLabelText("Name")).toHaveValue("");
	});
});
