import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { DestinationView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	destination,
	LOCAL,
	R2,
	server,
	teamSlice,
} from "./destinations.fixture";
import { DestinationsSection } from "./destinations-section";

/** Routes every test needs: the list, the built-in slice (absent by default). */
function api(
	items: DestinationView[],
	extra: Parameters<typeof stubApi>[0] = {},
) {
	return stubApi({
		"GET /backup-destinations": { body: { items } },
		"GET /team-slice": { body: teamSlice("unavailable") },
		...extra,
	});
}

describe("DestinationsSection: list", () => {
	it("shows the empty state with an add button when nothing is configured", async () => {
		const stub = api([]);
		await renderWithProviders(<DestinationsSection />);
		expect(await screen.findByText("Add a place for backups")).toBeVisible();
		expect(
			screen.getByRole("button", { name: "Add destination" }),
		).toBeEnabled();
		expect(stub.unmatched).toEqual([]);
	});

	it("shows an error with a retry that loads the list once the control plane answers", async () => {
		let healthy = false;
		api([], {
			"GET /backup-destinations": () =>
				healthy
					? Response.json({ items: [R2] })
					: Response.json({ title: "boom" }, { status: 500 }),
		});
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		expect(await screen.findByText("Destinations did not load")).toBeVisible();
		expect(
			screen.getByText(
				"The control plane did not answer, so nothing was changed.",
			),
		).toBeVisible();
		healthy = true;
		await user.click(screen.getByRole("button", { name: /retry|try again/i }));
		expect(await screen.findByText("R2 nightly")).toBeVisible();
		expect(screen.queryByText("Destinations did not load")).toBeNull();
	});

	it("lists each destination with where it writes, its test state and its use", async () => {
		api([
			{
				...R2,
				lastTestOk: true,
				lastTestAt: "2026-01-02T00:00:00Z",
				inUseBySchedules: 1,
				storedBackups: 3,
			},
			{
				...LOCAL,
				lastTestOk: false,
				lastTestMessage: "permission denied",
				inUseBySchedules: 2,
				storedBackups: 1,
			},
			destination({
				id: "dest-aws",
				name: "AWS cold",
				bucket: "cold",
				// untested, no endpoint: Amazon S3 itself
			}),
		]);
		await renderWithProviders(<DestinationsSection />);
		await screen.findByText("R2 nightly");

		const rows = screen.getAllByRole("row");
		const r2 = rows.find((r) => within(r).queryByText("R2 nightly"));
		const local = rows.find((r) => within(r).queryByText("Server disk"));
		const aws = rows.find((r) => within(r).queryByText("AWS cold"));
		if (!r2 || !local || !aws) throw new Error("rows missing");

		expect(
			within(r2).getByText("acct.r2.cloudflarestorage.com · backups/prod"),
		).toBeVisible();
		expect(within(r2).getByText("Works")).toBeVisible();
		expect(within(r2).getByText(/1 schedule ·/)).toBeVisible();
		expect(within(r2).getByText(/3 stored backups/)).toBeVisible();
		expect(within(r2).queryByText("not off-site")).toBeNull();

		expect(
			within(local).getByText("Agent backup directory/nightly"),
		).toBeVisible();
		expect(within(local).getByText("Failed")).toBeVisible();
		expect(within(local).getByText("permission denied")).toBeVisible();
		expect(within(local).getByText("not off-site")).toBeVisible();
		expect(within(local).getByText(/2 schedules/)).toBeVisible();
		expect(within(local).getByText(/1 stored backup$/)).toBeVisible();

		expect(within(aws).getByText("AWS S3 · cold")).toBeVisible();
		expect(within(aws).getByText("Not tested")).toBeVisible();
		expect(
			within(aws).getByText(/0 schedules · 0 stored backups/),
		).toBeVisible();
	});

	it("keeps the built-in storage out of the table and shows its own card with Test and use counts", async () => {
		api(
			[
				destination({
					id: "dest-builtin",
					name: "Built-in",
					kind: "builtin",
					offSite: false,
					inUseBySchedules: 1,
					storedBackups: 1,
				}),
			],
			{ "GET /team-slice": { body: teamSlice("ready") } },
		);
		await renderWithProviders(<DestinationsSection />);
		const card = await screen.findByRole("article", {
			name: "Built-in backup storage",
		});
		expect(within(card).getByRole("button", { name: "Test" })).toBeVisible();
		expect(
			within(card).getByText("1 schedule · 1 stored backup"),
		).toBeVisible();
		// No external destination yet: the hint offers adding one, there is no table.
		expect(screen.getByText(/Want a copy somewhere else\?/)).toBeVisible();
		expect(screen.queryByRole("table")).toBeNull();
		expect(screen.queryByText("Add a place for backups")).toBeNull();
		expect(
			screen.getByRole("button", { name: "Add destination" }),
		).toBeVisible();
	});

	it("shows the table next to the built-in card once an external destination exists", async () => {
		api(
			[
				destination({ id: "dest-builtin", name: "Built-in", kind: "builtin" }),
				R2,
			],
			{ "GET /team-slice": { body: teamSlice("ready") } },
		);
		await renderWithProviders(<DestinationsSection />);
		expect(await screen.findByText("R2 nightly")).toBeVisible();
		expect(
			screen.getByRole("article", { name: "Built-in backup storage" }),
		).toBeVisible();
		// The built-in storage is never a table row.
		expect(
			screen.queryByText("Built-in backup storage", { selector: "td *" }),
		).toBeNull();
		expect(screen.getAllByRole("row")).toHaveLength(2); // header + R2
		expect(screen.queryByText(/Want a copy somewhere else\?/)).toBeNull();
	});
});

describe("DestinationsSection: remove", () => {
	it("asks first, then deletes the destination and reloads the list", async () => {
		let removed = false;
		const stub = api([], {
			"GET /backup-destinations": () =>
				Response.json({ items: removed ? [] : [R2] }),
			"DELETE /backup-destinations/:id": () => {
				removed = true;
				return new Response(null, { status: 204 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		await screen.findByText("R2 nightly");
		await user.click(screen.getByRole("button", { name: "Remove" }));

		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Remove R2 nightly?")).toBeVisible();
		expect(
			within(dialog).getByText(
				"Nothing is deleted from the bucket or directory.",
			),
		).toBeVisible();
		// Nothing is sent until the reader confirms.
		expect(stub.calls.some((c) => c.method === "DELETE")).toBe(false);

		await user.click(
			within(dialog).getByRole("button", { name: "Remove destination" }),
		);
		expect(await screen.findByText("Removed R2 nightly")).toBeVisible();
		expect(
			screen.getByText("Objects already stored there are not touched."),
		).toBeVisible();
		const del = stub.calls.find((c) => c.method === "DELETE");
		expect(del?.path).toBe("/backup-destinations/dest-r2");
		await waitFor(() => expect(screen.queryByText("R2 nightly")).toBeNull());
		expect(await screen.findByText("Add a place for backups")).toBeVisible();
		expect(stub.unmatched).toEqual([]);
	});

	it("warns when schedules or stored backups still use the destination", async () => {
		api([{ ...R2, inUseBySchedules: 1, storedBackups: 0 }]);
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		await screen.findByText("R2 nightly");
		await user.click(screen.getByRole("button", { name: "Remove" }));
		expect(
			await screen.findByText(
				"A destination that schedules or stored backups use can not be removed. Delete those first.",
			),
		).toBeVisible();
	});

	it("does not delete when the reader cancels", async () => {
		const stub = api([R2]);
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		await screen.findByText("R2 nightly");
		await user.click(screen.getByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(stub.calls.some((c) => c.method === "DELETE")).toBe(false);
		expect(screen.getByText("R2 nightly")).toBeVisible();
	});

	it("keeps the dialog open and shows why when the control plane refuses", async () => {
		api([{ ...R2, storedBackups: 2 }], {
			"DELETE /backup-destinations/:id": () =>
				Response.json(
					{ code: "in_use", detail: "2 backups are stored here" },
					{ status: 409 },
				),
		});
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		await screen.findByText("R2 nightly");
		await user.click(screen.getByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Remove destination" }),
		);
		expect(
			await within(dialog).findByText("2 backups are stored here"),
		).toBeVisible();
		expect(screen.getByRole("alertdialog")).toBeVisible();
		expect(screen.queryByText("Removed R2 nightly")).toBeNull();
		expect(
			await screen.findAllByText("Could not remove the destination"),
		).not.toHaveLength(0);
	});
});

describe("DestinationsSection: test connection", () => {
	const servers = {
		"GET /servers": {
			body: make("PageServer", {
				items: [
					server("srv-off", "Offline box", "offline"),
					server("srv-a", "Alpha"),
					server("srv-b", "Beta"),
				],
			}),
		},
	};

	async function openTest(user: UserEvent) {
		await screen.findByText("R2 nightly");
		await user.click(screen.getByRole("button", { name: "Test" }));
		const dialog = await screen.findByRole("dialog", {
			name: "Test R2 nightly",
		});
		return dialog;
	}

	it("runs the probe on the first online server and reports a success", async () => {
		const stub = api([R2], {
			...servers,
			"POST /backup-destinations/:id/test": {
				body: { ...R2, lastTestOk: true, lastTestMessage: "" },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		const dialog = await openTest(user);
		await waitFor(() =>
			expect(within(dialog).getByRole("combobox")).toHaveTextContent("Alpha"),
		);
		await user.click(within(dialog).getByRole("button", { name: "Run test" }));

		expect(await screen.findByText("Destination works")).toBeVisible();
		expect(
			screen.getByText("The agent wrote, read and deleted a probe object."),
		).toBeVisible();
		const post = stub.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/backup-destinations/dest-r2/test");
		expect(post?.body).toEqual({ serverId: "srv-a" });
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	it("sends the server the reader picked instead of the default", async () => {
		const stub = api([R2], {
			...servers,
			"POST /backup-destinations/:id/test": {
				body: { ...R2, lastTestOk: true, lastTestMessage: "all good" },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		const dialog = await openTest(user);
		const combo = within(dialog).getByRole("combobox");
		await waitFor(() => expect(combo).toHaveTextContent("Alpha"));
		await user.click(combo);
		// An offline server is not offered.
		expect(screen.queryByRole("option", { name: "Offline box" })).toBeNull();
		await user.click(await screen.findByRole("option", { name: "Beta" }));
		await user.click(within(dialog).getByRole("button", { name: "Run test" }));
		// The server's own message replaces the generic sentence.
		expect(await screen.findByText("all good")).toBeVisible();
		expect(stub.calls.find((c) => c.method === "POST")?.body).toEqual({
			serverId: "srv-b",
		});
	});

	it("reports a failed probe with the agent's message", async () => {
		api([R2], {
			...servers,
			"POST /backup-destinations/:id/test": {
				body: { ...R2, lastTestOk: false, lastTestMessage: "AccessDenied" },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		const dialog = await openTest(user);
		await waitFor(() =>
			expect(within(dialog).getByRole("combobox")).toHaveTextContent("Alpha"),
		);
		await user.click(within(dialog).getByRole("button", { name: "Run test" }));
		expect(await screen.findByText("Destination test failed")).toBeVisible();
		expect(screen.getByText("AccessDenied")).toBeVisible();
		expect(screen.queryByText("Destination works")).toBeNull();
	});

	it("says it is still waiting when the agent has not answered", async () => {
		api([R2], {
			...servers,
			"POST /backup-destinations/:id/test": { body: { ...R2 } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		const dialog = await openTest(user);
		await waitFor(() =>
			expect(within(dialog).getByRole("combobox")).toHaveTextContent("Alpha"),
		);
		await user.click(within(dialog).getByRole("button", { name: "Run test" }));
		expect(
			await screen.findByText("Still waiting for the agent"),
		).toBeVisible();
		expect(
			screen.getByText(
				"The result appears on the destination when it answers.",
			),
		).toBeVisible();
	});

	it("keeps the dialog open and toasts when the request itself fails", async () => {
		api([R2], {
			...servers,
			"POST /backup-destinations/:id/test": () =>
				Response.json(
					{ code: "agent_offline", detail: "That server's agent is offline" },
					{ status: 409 },
				),
		});
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		const dialog = await openTest(user);
		await waitFor(() =>
			expect(within(dialog).getByRole("combobox")).toHaveTextContent("Alpha"),
		);
		await user.click(within(dialog).getByRole("button", { name: "Run test" }));
		expect(
			await screen.findByText("Could not test the destination"),
		).toBeVisible();
		expect(screen.getByText("That server's agent is offline")).toBeVisible();
		expect(
			screen.getByRole("dialog", { name: "Test R2 nightly" }),
		).toBeVisible();
		expect(
			within(screen.getByRole("dialog")).getByRole("button", {
				name: "Run test",
			}),
		).toBeEnabled();
	});

	it("cannot run without an online server", async () => {
		const stub = api([R2], {
			"GET /servers": {
				body: make("PageServer", {
					items: [server("srv-off", "Offline box", "offline")],
				}),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		const dialog = await openTest(user);
		await waitFor(() =>
			expect(within(dialog).getByRole("combobox")).toHaveTextContent(
				"No server is online",
			),
		);
		expect(
			within(dialog).getByRole("button", { name: "Run test" }),
		).toBeDisabled();
		expect(stub.calls.some((c) => c.method === "POST")).toBe(false);
	});

	it("closes on Cancel without testing", async () => {
		const stub = api([R2], servers);
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		const dialog = await openTest(user);
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(stub.calls.some((c) => c.method === "POST")).toBe(false);
	});

	it("tests the built-in storage from its card", async () => {
		const stub = api(
			[destination({ id: "dest-builtin", name: "Built-in", kind: "builtin" })],
			{
				...servers,
				"GET /team-slice": { body: teamSlice("ready") },
				"POST /backup-destinations/:id/test": {
					body: { ...R2, lastTestOk: true, lastTestMessage: "" },
				},
			},
		);
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		const card = await screen.findByRole("article", {
			name: "Built-in backup storage",
		});
		await user.click(within(card).getByRole("button", { name: "Test" }));
		const dialog = await screen.findByRole("dialog", { name: "Test Built-in" });
		await waitFor(() =>
			expect(within(dialog).getByRole("combobox")).toHaveTextContent("Alpha"),
		);
		await user.click(within(dialog).getByRole("button", { name: "Run test" }));
		expect(await screen.findByText("Destination works")).toBeVisible();
		expect(stub.calls.find((c) => c.method === "POST")?.path).toBe(
			"/backup-destinations/dest-builtin/test",
		);
	});
});

describe("DestinationsSection: add and edit", () => {
	it("opens the add dialog empty and the edit dialog on the chosen destination", async () => {
		api([R2]);
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		await screen.findByText("R2 nightly");

		await user.click(screen.getByRole("button", { name: "Add destination" }));
		const add = await screen.findByRole("dialog", {
			name: "Add backup destination",
		});
		expect(within(add).getByLabelText("Name")).toHaveValue("");
		await user.click(within(add).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

		await user.click(screen.getByRole("button", { name: "Edit" }));
		const edit = await screen.findByRole("dialog", { name: "Edit R2 nightly" });
		expect(within(edit).getByLabelText("Name")).toHaveValue("R2 nightly");
		expect(within(edit).getByLabelText("Bucket")).toHaveValue("backups");
	});

	it("reloads the list after a destination is added", async () => {
		let items: DestinationView[] = [];
		const stub = api([], {
			"GET /backup-destinations": () => Response.json({ items }),
			"POST /backup-destinations": () => {
				items = [LOCAL];
				return Response.json(LOCAL, { status: 201 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<DestinationsSection />);
		await user.click(
			await screen.findByRole("button", { name: "Add destination" }),
		);
		const dialog = await screen.findByRole("dialog");
		await user.type(within(dialog).getByLabelText("Name"), "Server disk");
		await user.click(
			within(dialog).getByRole("button", { name: "Directory on the server" }),
		);
		await user.click(
			within(dialog).getByRole("button", { name: "Add destination" }),
		);
		expect(
			await screen.findByText("Agent backup directory/nightly"),
		).toBeVisible();
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(stub.unmatched).toEqual([]);
	});
});
