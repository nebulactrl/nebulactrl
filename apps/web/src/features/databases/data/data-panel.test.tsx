import { describe, expect, test } from "bun:test";
import { EditorView } from "@codemirror/view";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import { type ApiStub, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeService } from "../../canvas/lib/fixtures";
import { DataPanel } from "./data-panel";

const TABLES = "GET /services/:serviceId/database/tables";
const ROWS = "POST /services/:serviceId/database/rows";
const QUERY = "POST /services/:serviceId/database/query";

/** The signed-in principal as an owner or a developer (writes are for owners and admins). */
function me(role: "owner" | "developer") {
	return {
		"GET /me": {
			body: make("MeBody", {
				principal: { type: "user", label: "Ada", scopes: [], role },
			}),
		},
	};
}

const service: Service = makeService({
	id: "svc-db",
	name: "Orders DB",
	slug: "orders-db",
	kind: "database",
	config: {
		...makeService().config,
		database: { engine: "postgres", version: "17" },
	} as never,
});

const tables = (
	...list: {
		schema?: string;
		name: string;
		kind?: "table" | "view";
		rows?: number;
	}[]
) =>
	make("DataTables", {
		tables: list.map((t) => ({
			schema: t.schema ?? "public",
			name: t.name,
			kind: t.kind ?? "table",
			...(t.rows === undefined ? {} : { rowEstimate: t.rows }),
		})),
	});

const set = (
	columns: string[],
	rows: (string | null)[][],
	extra: { truncated?: boolean; commandTag?: string } = {},
) => ({
	columns: columns.map((name) => ({ name, type: "text" })),
	rows,
	rowCount: rows.length,
	...extra,
});

const result = (
	sets: ReturnType<typeof set>[],
	extra: { durationMs?: number; truncatedCells?: boolean } = {},
) => make("DataResult", { durationMs: 12, ...extra, results: sets });

async function open(production = false) {
	return renderWithProviders(
		<DataPanel service={service} production={production} />,
	);
}

/** Writes `text` into the SQL editor the way typing would. */
function typeSql(text: string) {
	const dom = document.querySelector<HTMLElement>(".cm-editor");
	const view = dom ? EditorView.findFromDOM(dom) : null;
	if (!view) throw new Error("no SQL editor on the page");
	act(() => {
		view.dispatch({
			changes: { from: 0, to: view.state.doc.length, insert: text },
		});
	});
}

/** Waits for the SQL console and its editor, which mounts after the region. */
async function consoleShown() {
	await screen.findByRole("region", { name: "SQL console" });
	await waitFor(() =>
		expect(document.querySelector(".cm-editor")).not.toBeNull(),
	);
}

const calls = (api: ApiStub, key: string) =>
	api.calls.filter((call) => `${call.method} ${call.path}`.startsWith(key));

describe("the table list", () => {
	test("lists the tables with their estimates and opens the first one's rows", async () => {
		const api = stubApi({
			[TABLES]: {
				body: tables(
					{ name: "users", rows: 42 },
					{ name: "recent", kind: "view" },
				),
			},
			[ROWS]: { body: result([set(["id", "email"], [["1", "a@x.io"]])]) },
		});
		await open();

		const nav = await screen.findByRole("navigation", { name: "Tables" });
		expect(within(nav).getByText("public.users")).toBeInTheDocument();
		expect(within(nav).getByText("~42 rows")).toBeInTheDocument();
		expect(within(nav).getByText("view")).toBeInTheDocument();

		const grid = await screen.findByRole("region", {
			name: "Rows of public.users",
		});
		expect(within(grid).getByText("a@x.io")).toBeInTheDocument();
		expect(within(grid).getByText("email")).toBeInTheDocument();
		expect(calls(api, "POST /services/svc-db/database/rows")[0]?.body).toEqual({
			schema: "public",
			table: "users",
			offset: 0,
		});
	});

	test("the filter narrows the list and says when nothing matches", async () => {
		stubApi({
			[TABLES]: { body: tables({ name: "users" }, { name: "orders" }) },
			[ROWS]: { body: result([set(["id"], [])]) },
		});
		await open();
		const user = userEvent.setup();
		const nav = await screen.findByRole("navigation", { name: "Tables" });
		await user.type(within(nav).getByLabelText("Filter tables"), "ord");
		expect(within(nav).queryByText("public.users")).not.toBeInTheDocument();
		expect(within(nav).getByText("public.orders")).toBeInTheDocument();
		await user.type(within(nav).getByLabelText("Filter tables"), "zzz");
		expect(within(nav).getByText(/No table matches/)).toBeInTheDocument();
	});

	test("a database without tables opens the SQL console", async () => {
		stubApi({ [TABLES]: { body: tables() } });
		await open();
		expect(
			await screen.findByText("This database has no tables yet."),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("region", { name: "SQL console" }),
		).toBeVisible();
	});
});

describe("blocked states", () => {
	const blocked: [string, string][] = [
		["service_stopped", "Start the database to browse its data."],
		["not_deployed", "Deploy the database first."],
		["database_fenced", "This database is fenced."],
	];
	for (const [code, message] of blocked) {
		test(`${code} says "${message}"`, async () => {
			stubApi({ [TABLES]: problem(409, { code, detail: "refused" }) });
			await open();
			expect(await screen.findByText(message)).toBeInTheDocument();
			expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
		});
	}

	test("another failure names itself and offers a retry", async () => {
		stubApi({ [TABLES]: problem(500, { code: "internal", detail: "kaput" }) });
		await open();
		expect(await screen.findByText("kaput")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Try again" }),
		).toBeInTheDocument();
	});
});

describe("a table's rows", () => {
	test("NULL is its own muted word and an empty string stays empty", async () => {
		stubApi({
			[TABLES]: { body: tables({ name: "users" }) },
			[ROWS]: {
				body: result([
					set(
						["a", "b"],
						[
							["x", null],
							["", "NULL"],
						],
					),
				]),
			},
		});
		await open();
		const grid = await screen.findByRole("region", {
			name: "Rows of public.users",
		});
		await within(grid).findByText("x");
		const nulls = grid.querySelectorAll("[data-null]");
		expect(nulls).toHaveLength(1);
		expect(nulls[0]).toHaveTextContent("NULL");
		// The literal text "NULL" is a value, not the null marker.
		expect(grid.querySelectorAll("td")[3]).toHaveTextContent("NULL");
		expect(
			grid.querySelectorAll("td")[3]?.querySelector("[data-null]"),
		).toBeNull();
	});

	test("pages by offset: Next asks for the next 100 rows, Previous returns", async () => {
		const api = stubApi({
			[TABLES]: { body: tables({ name: "users" }) },
			[ROWS]: (request) =>
				request
					.clone()
					.json()
					.then((body: { offset: number }) =>
						Response.json(
							result(
								[
									set(["n"], [[String(body.offset)]], {
										truncated: body.offset === 0,
									}),
								],
								{},
							),
						),
					),
		});
		await open();
		const user = userEvent.setup();
		await screen.findByText(/Rows 1–1/);
		expect(screen.getByRole("button", { name: "Previous" })).toBeDisabled();

		await user.click(screen.getByRole("button", { name: "Next" }));
		await screen.findByText(/Rows 101–101/);
		expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
		expect(
			calls(api, "POST /services/svc-db/database/rows").map(
				(call) => (call.body as { offset: number }).offset,
			),
		).toEqual([0, 100]);

		await user.click(screen.getByRole("button", { name: "Previous" }));
		await waitFor(() =>
			expect(screen.getByText(/Rows 1–1/)).toBeInTheDocument(),
		);
	});

	test("a failed page reports the database's error", async () => {
		stubApi({
			[TABLES]: { body: tables({ name: "users" }) },
			[ROWS]: problem(422, {
				code: "query_failed",
				detail: 'relation "public.users" does not exist',
			}),
		});
		await open();
		expect(
			await screen.findByText('relation "public.users" does not exist'),
		).toBeInTheDocument();
	});

	test("a table of a thousand rows renders only the rows in view", async () => {
		const many = Array.from({ length: 1000 }, (_, i) => [String(i)]);
		stubApi({
			[TABLES]: { body: tables({ name: "big" }) },
			[ROWS]: { body: result([set(["n"], many)]) },
		});
		await open();
		const grid = await screen.findByRole("region", {
			name: "Rows of public.big",
		});
		await within(grid).findByText("0");
		const rendered = grid.querySelectorAll("tbody tr[data-index]").length;
		expect(rendered).toBeGreaterThan(0);
		expect(rendered).toBeLessThan(100);
	});
});

describe("the SQL console", () => {
	async function consoleReady(routes: Parameters<typeof stubApi>[0]) {
		const api = stubApi({
			[TABLES]: { body: tables() },
			...me("owner"),
			...routes,
		});
		const view = await open(false);
		await consoleShown();
		return { api, view };
	}

	test("runs the statement read-only and shows each result with a footer", async () => {
		const { api } = await consoleReady({
			[QUERY]: {
				body: result(
					[
						set(["a"], [["1"], [null]]),
						set([], [], { commandTag: "INSERT 0 1" }),
					],
					{ durationMs: 7 },
				),
			},
		});
		const user = userEvent.setup();
		typeSql("SELECT 1; INSERT INTO t VALUES (1)");
		await user.click(screen.getByRole("button", { name: /Run/ }));

		expect(await screen.findByText("INSERT 0 1")).toBeInTheDocument();
		expect(screen.getByText("Result 1 of 2")).toBeInTheDocument();
		expect(screen.getByText("2 rows · 7 ms")).toBeInTheDocument();
		expect(calls(api, "POST /services/svc-db/database/query")[0]?.body).toEqual(
			{
				sql: "SELECT 1; INSERT INTO t VALUES (1)",
			},
		);
	});

	test("a developer gets no Allow writes switch and is told why", async () => {
		await consoleReady({ ...me("developer"), [QUERY]: { body: result([]) } });
		expect(
			await screen.findByText(
				"Read-only: only owners and admins can change data here.",
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("switch", { name: "Allow writes" }),
		).not.toBeInTheDocument();
	});

	test("says when a set was cut at the row limit", async () => {
		await consoleReady({
			[QUERY]: {
				body: result([set(["n"], [["1"]], { truncated: true })], {
					truncatedCells: true,
				}),
			},
		});
		typeSql("SELECT n FROM big");
		await userEvent.setup().click(screen.getByRole("button", { name: /Run/ }));
		expect(
			await screen.findByText("Showing the first 1 row"),
		).toBeInTheDocument();
		expect(screen.getByText(/long values are clipped/)).toBeInTheDocument();
	});

	test("Run waits for a statement", async () => {
		await consoleReady({ [QUERY]: { body: result([]) } });
		expect(screen.getByRole("button", { name: /Run/ })).toBeDisabled();
		typeSql("SELECT 1");
		await waitFor(() =>
			expect(screen.getByRole("button", { name: /Run/ })).toBeEnabled(),
		);
	});

	test("a failed statement shows the database's error under the editor", async () => {
		await consoleReady({
			[QUERY]: problem(422, {
				code: "query_failed",
				detail: "ERROR: cannot execute INSERT in a read-only transaction",
			}),
		});
		typeSql("INSERT INTO t VALUES (1)");
		await userEvent.setup().click(screen.getByRole("button", { name: /Run/ }));
		expect(
			await screen.findByText(
				"ERROR: cannot execute INSERT in a read-only transaction",
			),
		).toBeInTheDocument();
		expect(screen.getByText("The statement failed")).toBeInTheDocument();
	});

	test("a stopped database explains itself instead of failing", async () => {
		await consoleReady({
			[QUERY]: problem(409, { code: "service_stopped", detail: "stopped" }),
		});
		typeSql("SELECT 1");
		await userEvent.setup().click(screen.getByRole("button", { name: /Run/ }));
		expect(
			await screen.findByText("Start the database to browse its data."),
		).toBeInTheDocument();
	});

	test("Allow writes is off by default, is sent when on, and resets after the run", async () => {
		const { api } = await consoleReady({
			[QUERY]: { body: result([set([], [], { commandTag: "DELETE 1" })]) },
		});
		const user = userEvent.setup();
		const writes = screen.getByRole("switch", { name: "Allow writes" });
		expect(writes).not.toBeChecked();
		expect(screen.getByText(/Read-only/)).toBeInTheDocument();

		typeSql("DELETE FROM t");
		await user.click(writes);
		expect(writes).toBeChecked();
		await user.click(screen.getByRole("button", { name: /Run/ }));

		await screen.findByText("DELETE 1");
		expect(calls(api, "POST /services/svc-db/database/query")[0]?.body).toEqual(
			{
				sql: "DELETE FROM t",
				allowWrites: true,
			},
		);
		expect(writes).not.toBeChecked();
	});

	test("a write in production asks for the service name before it runs", async () => {
		const api = stubApi({
			[TABLES]: { body: tables() },
			...me("owner"),
			[QUERY]: { body: result([set([], [], { commandTag: "DELETE 1" })]) },
		});
		await open(true);
		await consoleShown();
		const user = userEvent.setup();
		typeSql("DELETE FROM t");
		await user.click(screen.getByRole("switch", { name: "Allow writes" }));
		await user.click(screen.getByRole("button", { name: /Run/ }));

		const dialog = await screen.findByRole("alertdialog");
		expect(calls(api, "POST /services/svc-db/database/query")).toHaveLength(0);
		const confirm = within(dialog).getByRole("button", {
			name: "Run statement",
		});
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByRole("textbox"), "orders-db");
		await waitFor(() => expect(confirm).toBeEnabled());
		await user.click(confirm);

		await screen.findByText("DELETE 1");
		expect(calls(api, "POST /services/svc-db/database/query")[0]?.body).toEqual(
			{
				sql: "DELETE FROM t",
				allowWrites: true,
				confirm: "orders-db",
			},
		);
	});

	test("a read in production runs without a question", async () => {
		const api = stubApi({
			[TABLES]: { body: tables() },
			...me("owner"),
			[QUERY]: { body: result([set(["a"], [["1"]])]) },
		});
		await open(true);
		await consoleShown();
		typeSql("SELECT 1");
		await userEvent.setup().click(screen.getByRole("button", { name: /Run/ }));
		await screen.findByText("1 row · 12 ms");
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
		expect(calls(api, "POST /services/svc-db/database/query")).toHaveLength(1);
	});

	test("when the server asks for the confirmation the page did not know about, the dialog opens", async () => {
		let answered = 0;
		stubApi({
			[TABLES]: { body: tables() },
			...me("owner"),
			[QUERY]: async () => {
				answered += 1;
				return answered === 1
					? Response.json(
							make("Error", {
								status: 409,
								code: "confirmation_required",
								detail: "type the service name",
							}),
							{
								status: 409,
								headers: { "content-type": "application/problem+json" },
							},
						)
					: Response.json(result([set([], [], { commandTag: "DELETE 1" })]));
			},
		});
		await open(false);
		await consoleShown();
		const user = userEvent.setup();
		typeSql("DELETE FROM t");
		await user.click(screen.getByRole("switch", { name: "Allow writes" }));
		await user.click(screen.getByRole("button", { name: /Run/ }));
		const dialog = await screen.findByRole("alertdialog");
		await user.type(within(dialog).getByRole("textbox"), "orders-db");
		await user.click(
			within(dialog).getByRole("button", { name: "Run statement" }),
		);
		await screen.findByText("DELETE 1");
	});
});
