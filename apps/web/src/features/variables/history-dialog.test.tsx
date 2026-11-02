import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { VariableHistoryEntry } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { HistoryDialog } from "./history-dialog";

function entry(
	overrides: Overrides<VariableHistoryEntry>,
): VariableHistoryEntry {
	return make("VariableHistoryEntry", {
		version: 1,
		op: "set",
		isSecret: false,
		fingerprint: "ab12cd34",
		restorable: false,
		releaseId: null,
		generated: [],
		actorType: "user",
		createdAt: "2026-01-02T03:04:05Z",
		scope: "runtime",
		source: "manual",
		...overrides,
	});
}

const HISTORY = "GET /services/:id/variables/:key/history";

describe("HistoryDialog", () => {
	it("renders nothing and requests nothing while closed", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<HistoryDialog serviceId="s1" variableKey={null} onClose={() => {}} />,
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	it("shows a loading state, then the versions of the requested variable", async () => {
		const reply = Promise.withResolvers<Response>();
		const api = stubApi({ [HISTORY]: () => reply.promise });
		await renderWithProviders(
			<HistoryDialog serviceId="s1" variableKey="LEVEL" onClose={() => {}} />,
		);
		expect(
			await screen.findByRole("status", { name: "Loading history" }),
		).toBeInTheDocument();
		expect(screen.getByText("History of LEVEL")).toBeInTheDocument();
		reply.resolve(
			Response.json({ items: [entry({ version: 1, value: "info" })] }),
		);
		expect(await screen.findByText("v1")).toBeInTheDocument();
		expect(
			screen.queryByRole("status", { name: "Loading history" }),
		).not.toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/services/s1/variables/LEVEL/history");
	});

	it("says so when no version is recorded yet", async () => {
		stubApi({ [HISTORY]: { body: make("HistoryOutputBody", { items: [] }) } });
		await renderWithProviders(
			<HistoryDialog serviceId="s1" variableKey="LEVEL" onClose={() => {}} />,
		);
		expect(
			await screen.findByText("No versions recorded yet."),
		).toBeInTheDocument();
		expect(screen.queryByRole("list", { name: "Versions" })).toBeNull();
	});

	it("shows the server's reason when the history cannot be loaded", async () => {
		stubApi({
			[HISTORY]: problem(404, {
				code: "not_found",
				title: "Not found",
				detail: "no such variable",
			}),
		});
		await renderWithProviders(
			<HistoryDialog serviceId="s1" variableKey="LEVEL" onClose={() => {}} />,
		);
		expect(await screen.findByText("no such variable")).toBeInTheDocument();
		expect(screen.queryByRole("list", { name: "Versions" })).toBeNull();
	});

	it("describes each operation and shows plain values, masked secrets and deletions", async () => {
		stubApi({
			[HISTORY]: {
				body: make("HistoryOutputBody", {
					items: [
						entry({ version: 5, op: "delete" }),
						entry({
							version: 4,
							op: "rotate",
							isSecret: true,
							fingerprint: "deadbeef",
							generated: [{ fn: "secret", args: ["32"] }],
						}),
						entry({ version: 3, op: "restore", value: "info" }),
						entry({
							version: 2,
							op: "set",
							value: "x",
							generated: [
								{ fn: "secret", args: ["8"] },
								{ fn: "randomInt", args: null },
							],
						}),
						entry({ version: 1, op: "set", value: "debug" }),
					],
				}),
			},
		});
		await renderWithProviders(
			<HistoryDialog serviceId="s1" variableKey="LEVEL" onClose={() => {}} />,
		);
		const rows = within(
			await screen.findByRole("list", { name: "Versions" }),
		).getAllByRole("listitem");
		expect(rows).toHaveLength(5);

		expect(rows[0]).toHaveTextContent("v5");
		expect(within(rows[0] as HTMLElement).getByText("Deleted")).toBeVisible();
		expect(within(rows[0] as HTMLElement).getByText("—")).toBeVisible();

		const secret = rows[1] as HTMLElement;
		expect(secret).toHaveTextContent("Rotated · generated secret(32)");
		expect(secret).toHaveTextContent("•••••• · fingerprint deadbeef");
		expect(within(secret).getByText("secret")).toBeVisible();

		expect(rows[2]).toHaveTextContent("Restored");
		expect(rows[2]).toHaveTextContent("info");

		expect(rows[3]).toHaveTextContent(
			"Set · generated secret(8) + randomInt()",
		);

		expect(rows[4]).toHaveTextContent("debug");
		expect(rows[4]).not.toHaveTextContent("generated");
	});

	it("says whether a version shipped in a release", async () => {
		stubApi({
			[HISTORY]: {
				body: make("HistoryOutputBody", {
					items: [
						entry({ version: 2, value: "b" }),
						entry({ version: 1, value: "a", releaseId: "abcdef1234567890" }),
					],
				}),
			},
		});
		await renderWithProviders(
			<HistoryDialog serviceId="s1" variableKey="LEVEL" onClose={() => {}} />,
		);
		const rows = within(
			await screen.findByRole("list", { name: "Versions" }),
		).getAllByRole("listitem");
		expect(rows[0]).toHaveTextContent("user · not deployed yet");
		expect(rows[1]).toHaveTextContent("user · shipped in release abcdef1");
		expect(rows[1]).not.toHaveTextContent("abcdef12");
	});

	it("offers Restore only for restorable versions other than the newest", async () => {
		stubApi({
			[HISTORY]: {
				body: make("HistoryOutputBody", {
					items: [
						entry({ version: 3, value: "c", restorable: true }),
						entry({ version: 2, value: "b", restorable: false }),
						entry({ version: 1, value: "a", restorable: true }),
					],
				}),
			},
		});
		await renderWithProviders(
			<HistoryDialog serviceId="s1" variableKey="LEVEL" onClose={() => {}} />,
		);
		const rows = within(
			await screen.findByRole("list", { name: "Versions" }),
		).getAllByRole("listitem");
		expect(within(rows[0] as HTMLElement).queryByRole("button")).toBeNull();
		expect(within(rows[1] as HTMLElement).queryByRole("button")).toBeNull();
		expect(
			within(rows[2] as HTMLElement).getByRole("button", { name: "Restore" }),
		).toBeVisible();
	});

	it("restores a version, toasts and closes", async () => {
		const user = userEvent.setup();
		let closed = 0;
		const api = stubApi({
			[HISTORY]: {
				body: make("HistoryOutputBody", {
					items: [
						entry({ version: 2, value: "b" }),
						entry({ version: 1, value: "a", restorable: true }),
					],
				}),
			},
			"POST /services/:id/variables/:key/history/:version/restore": {
				body: make("VariableList"),
			},
			"GET /services/:id/variables": { body: make("VariableList") },
		});
		await renderWithProviders(
			<HistoryDialog
				serviceId="s1"
				variableKey="LEVEL"
				onClose={() => {
					closed += 1;
				}}
			/>,
		);
		await user.click(await screen.findByRole("button", { name: "Restore" }));
		expect(await screen.findByText("Restored v1 of LEVEL")).toBeInTheDocument();
		expect(screen.getByText("Staged as a new version.")).toBeInTheDocument();
		await waitFor(() => expect(closed).toBe(1));
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.path).toBe("/services/s1/variables/LEVEL/history/1/restore");
	});

	it("shows the server's reason and stays open when a restore fails", async () => {
		const user = userEvent.setup();
		let closed = 0;
		stubApi({
			[HISTORY]: {
				body: make("HistoryOutputBody", {
					items: [
						entry({ version: 2, value: "b" }),
						entry({ version: 1, value: "a", restorable: true }),
					],
				}),
			},
			"POST /services/:id/variables/:key/history/:version/restore": problem(
				409,
				{ code: "conflict", title: "Conflict", detail: "key is managed" },
			),
		});
		await renderWithProviders(
			<HistoryDialog
				serviceId="s1"
				variableKey="LEVEL"
				onClose={() => {
					closed += 1;
				}}
			/>,
		);
		await user.click(await screen.findByRole("button", { name: "Restore" }));
		expect(await screen.findByText("key is managed")).toBeInTheDocument();
		expect(closed).toBe(0);
		expect(screen.queryByText("Restored v1 of LEVEL")).toBeNull();
	});

	it("closes from the Close button", async () => {
		const user = userEvent.setup();
		let closed = 0;
		stubApi({ [HISTORY]: { body: make("HistoryOutputBody", { items: [] }) } });
		await renderWithProviders(
			<HistoryDialog
				serviceId="s1"
				variableKey="LEVEL"
				onClose={() => {
					closed += 1;
				}}
			/>,
		);
		await screen.findByText("No versions recorded yet.");
		await user.click(
			within(screen.getByRole("dialog")).getByRole("button", { name: "Close" }),
		);
		expect(closed).toBe(1);
	});

	it("closes on Escape", async () => {
		const user = userEvent.setup();
		let closed = 0;
		stubApi({ [HISTORY]: { body: make("HistoryOutputBody", { items: [] }) } });
		await renderWithProviders(
			<HistoryDialog
				serviceId="s1"
				variableKey="LEVEL"
				onClose={() => {
					closed += 1;
				}}
			/>,
		);
		await screen.findByText("No versions recorded yet.");
		await user.keyboard("{Escape}");
		await waitFor(() => expect(closed).toBe(1));
	});
});
