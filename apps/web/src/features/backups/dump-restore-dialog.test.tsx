import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { DumpRestoreView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { type destination, LOCAL, R2, server } from "./destinations.fixture";
import { DumpRestoreDialog } from "./dump-restore-dialog";

/** XMLHttpRequest stand-in: records the upload and lets a test finish it. */
class FakeXhr {
	static all: FakeXhr[] = [];
	method = "";
	url = "";
	headers: Record<string, string> = {};
	withCredentials = false;
	body: unknown;
	aborted = false;
	status = 0;
	responseText = "";
	upload: { onprogress: ((e: unknown) => void) | null } = { onprogress: null };
	onload: (() => void) | null = null;
	onerror: (() => void) | null = null;
	onabort: (() => void) | null = null;
	constructor() {
		FakeXhr.all.push(this);
	}
	open(method: string, url: string) {
		this.method = method;
		this.url = url;
	}
	setRequestHeader(k: string, v: string) {
		this.headers[k] = v;
	}
	send(body: unknown) {
		this.body = body;
	}
	abort() {
		this.aborted = true;
		this.onabort?.();
	}
}

const realXhr = globalThis.XMLHttpRequest;
beforeEach(() => {
	FakeXhr.all = [];
	globalThis.XMLHttpRequest = FakeXhr as unknown as typeof XMLHttpRequest;
});
afterEach(() => {
	globalThis.XMLHttpRequest = realXhr;
});

const PG_DUMP = () => new File([`PGDMP\u0001${"x".repeat(14)}`], "app.dump");
const KB = 1024;

function restoreView(over: Partial<DumpRestoreView>): DumpRestoreView {
	return {
		bytesReceived: 0,
		createdAt: "2026-01-01T00:00:00Z",
		id: "rst-1",
		maxUploadBytes: 1 << 30,
		mode: "new",
		sizeBytes: 20,
		sourceServiceId: "svc-1",
		status: "provisioning",
		...over,
	};
}

interface Opts {
	serverId?: string | null;
	scheduleDestinationId?: string | undefined;
	engine?: string;
	routes?: Parameters<typeof stubApi>[0];
	/** What GET /dump-restores/:id answers. */
	restore?: Partial<DumpRestoreView>;
	destinations?: ReturnType<typeof destination>[];
}

async function open(o: Opts = {}) {
	let closed = 0;
	const stub = stubApi({
		"GET /servers": {
			body: make("PageServer", {
				items: [
					server("srv-a", "Alpha"),
					server("srv-b", "Beta"),
					server("srv-off", "Offline box", "offline"),
				],
			}),
		},
		"GET /backup-destinations": {
			body: { items: o.destinations ?? [R2, LOCAL] },
		},
		"POST /services/:id/dump-restores": () =>
			Response.json(restoreView({ ...o.restore, id: "rst-1" }), {
				status: 201,
			}),
		"GET /dump-restores/:id": () =>
			Response.json(restoreView({ ...o.restore, id: "rst-1" })),
		...o.routes,
	});
	await renderWithProviders(
		<DumpRestoreDialog
			open
			onClose={() => {
				closed++;
			}}
			serviceId="svc-1"
			serviceSlug="pg-main"
			engine={o.engine ?? "postgres"}
			serverId={o.serverId === undefined ? "srv-a" : o.serverId}
			scheduleDestinationId={o.scheduleDestinationId}
		/>,
	);
	const dialog = await screen.findByRole("dialog");
	return {
		stub,
		dialog,
		user: userEvent.setup(),
		closed: () => closed,
	};
}

async function pickFile(user: UserEvent, dialog: HTMLElement, file: File) {
	await user.upload(within(dialog).getByLabelText("Dump file"), file);
}

const button = (dialog: HTMLElement, name: string | RegExp) =>
	within(dialog).getByRole("button", { name });

/** Runs `change` on the XHR the dialog opened, inside act. */
async function onXhr(change: (x: FakeXhr) => void) {
	await waitFor(() => expect(FakeXhr.all).toHaveLength(1));
	const x = FakeXhr.all[FakeXhr.all.length - 1];
	if (!x) throw new Error("no upload started");
	await act(async () => change(x));
	return x;
}

describe("DumpRestoreDialog: choosing the file", () => {
	it("starts with Restore disabled and the safe new-database mode selected", async () => {
		const { dialog } = await open();
		expect(button(dialog, "Restore into new database")).toBeDisabled();
		expect(within(dialog).getByText("Safe by default")).toBeVisible();
		expect(within(dialog).getByLabelText("Name")).toHaveAttribute(
			"placeholder",
			"pg-main-restored-…",
		);
	});

	it("accepts a matching dump, shows its size and detected format and enables Restore", async () => {
		const { dialog, user } = await open();
		await pickFile(
			user,
			dialog,
			new File(["PGDMP\u0001".padEnd(2 * KB, "x")], "app.dump"),
		);
		expect(
			await within(dialog).findByText("2 KiB · PostgreSQL custom-format dump"),
		).toBeVisible();
		expect(button(dialog, "Restore into new database")).toBeEnabled();
	});

	it("refuses a dump of another engine before anything is created", async () => {
		const { stub, dialog, user } = await open();
		await pickFile(
			user,
			dialog,
			new File(["-- MySQL dump 10.13\nCREATE TABLE t (id int);"], "my.sql"),
		);
		expect(
			await within(dialog).findByText(
				/cannot be restored into a PostgreSQL database/,
			),
		).toBeVisible();
		expect(button(dialog, "Restore into new database")).toBeDisabled();
		expect(stub.calls.some((c) => c.method === "POST")).toBe(false);
	});

	it("refuses an empty file", async () => {
		const { dialog, user } = await open();
		await pickFile(user, dialog, new File([""], "empty.sql"));
		expect(await within(dialog).findByText("The file is empty.")).toBeVisible();
		expect(button(dialog, "Restore into new database")).toBeDisabled();
	});

	it("refuses a Redis RDB file for a MongoDB database and accepts a mongo archive", async () => {
		const { dialog, user } = await open({ engine: "mongodb" });
		await pickFile(user, dialog, new File(["REDIS0011"], "dump.rdb"));
		expect(
			await within(dialog).findByText(/Redis\/Valkey RDB file/),
		).toBeVisible();
		await pickFile(
			user,
			dialog,
			new File(
				[new Uint8Array([0x6d, 0xe2, 0x99, 0x81, 1, 2, 3])],
				"a.archive",
			),
		);
		expect(await within(dialog).findByText(/MongoDB archive$/)).toBeVisible();
		expect(within(dialog).queryByText(/Redis\/Valkey RDB file/)).toBeNull();
		expect(button(dialog, "Restore into new database")).toBeEnabled();
	});
});

describe("DumpRestoreDialog: into a new database", () => {
	it("creates the restore on the service's own server with the file's name and size", async () => {
		const { stub, dialog, user } = await open({
			restore: { status: "provisioning" },
		});
		await pickFile(user, dialog, PG_DUMP());
		await user.click(button(dialog, "Restore into new database"));

		await within(dialog).findByText("Starting new database");
		const post = stub.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/services/svc-1/dump-restores");
		// No name typed: the field is omitted so the control plane picks one.
		expect(post?.body).toEqual({
			mode: "new",
			fileName: "app.dump",
			sizeBytes: PG_DUMP().size,
			serverId: "srv-a",
		});
		expect(
			within(dialog).getByText(/Creating and starting the new database/),
		).toBeVisible();
	});

	it("sends the trimmed name and the server the reader chose from the online ones", async () => {
		const { stub, dialog, user } = await open();
		await pickFile(user, dialog, PG_DUMP());
		await user.type(within(dialog).getByLabelText("Name"), "  copy-1  ");
		const combo = within(dialog).getByRole("combobox");
		await waitFor(() => expect(combo).toHaveTextContent("Alpha"));
		await user.click(combo);
		expect(screen.queryByRole("option", { name: "Offline box" })).toBeNull();
		await user.click(await screen.findByRole("option", { name: "Beta" }));
		await user.click(button(dialog, "Restore into new database"));
		await waitFor(() =>
			expect(stub.calls.some((c) => c.method === "POST")).toBe(true),
		);
		expect(stub.calls.find((c) => c.method === "POST")?.body).toMatchObject({
			mode: "new",
			name: "copy-1",
			serverId: "srv-b",
		});
	});

	it("needs a server when the database has none to default to", async () => {
		const { stub, dialog, user } = await open({ serverId: null });
		await pickFile(user, dialog, PG_DUMP());
		await within(dialog)
			.findByText("1 KiB", { exact: false })
			.catch(() => null);
		expect(button(dialog, "Restore into new database")).toBeDisabled();
		const combo = within(dialog).getByRole("combobox");
		await waitFor(() =>
			expect(combo).toHaveTextContent("Choose an online server"),
		);
		await user.click(combo);
		await user.click(await screen.findByRole("option", { name: "Alpha" }));
		expect(button(dialog, "Restore into new database")).toBeEnabled();
		await user.click(button(dialog, "Restore into new database"));
		await waitFor(() =>
			expect(stub.calls.find((c) => c.method === "POST")?.body).toMatchObject({
				serverId: "srv-a",
			}),
		);
	});

	it("shows the control plane's refusal and lets the reader try again", async () => {
		let attempts = 0;
		const { dialog, user } = await open({
			routes: {
				"POST /services/:id/dump-restores": () => {
					attempts++;
					return Response.json(
						{ code: "too_large", detail: "The file is above the 1 GiB limit" },
						{ status: 413 },
					);
				},
			},
		});
		await pickFile(user, dialog, PG_DUMP());
		await user.click(button(dialog, "Restore into new database"));
		expect(
			await within(dialog).findByText("The file is above the 1 GiB limit"),
		).toBeVisible();
		// Still the form, not the progress view.
		expect(within(dialog).getByLabelText("Dump file")).toBeVisible();
		expect(button(dialog, "Restore into new database")).toBeEnabled();
		await user.click(button(dialog, "Restore into new database"));
		await waitFor(() => expect(attempts).toBe(2));
	});
});

describe("DumpRestoreDialog: over this database", () => {
	async function inPlace(o: Opts = {}) {
		const ctx = await open(o);
		await pickFile(ctx.user, ctx.dialog, PG_DUMP());
		await ctx.user.click(
			within(ctx.dialog).getByRole("button", { name: "Over this database" }),
		);
		return ctx;
	}

	it("warns, and keeps Replace disabled until the slug is typed exactly", async () => {
		const { dialog, user } = await inPlace({
			scheduleDestinationId: "dest-local",
		});
		expect(
			within(dialog).getByText("This replaces the database's data"),
		).toBeVisible();
		const replace = button(dialog, "Replace database");
		expect(replace).toBeDisabled();
		const confirm = within(dialog).getByLabelText(/to confirm/);
		await user.type(confirm, "pg-mai");
		expect(replace).toBeDisabled();
		await user.type(confirm, "x");
		expect(replace).toBeDisabled();
		await user.clear(confirm);
		await user.type(confirm, "pg-main");
		expect(replace).toBeEnabled();
		// The new-database controls are gone in this mode.
		expect(within(dialog).queryByLabelText("Name")).toBeNull();
	});

	it("preselects the schedule's destination for the safety backup and sends the confirmation", async () => {
		const { stub, dialog, user } = await inPlace({
			scheduleDestinationId: "dest-local",
			restore: { mode: "in_place", status: "pre_backup" },
		});
		expect(within(dialog).getByRole("combobox")).toHaveTextContent(
			"Server disk",
		);
		await user.type(within(dialog).getByLabelText(/to confirm/), "pg-main");
		await user.click(button(dialog, "Replace database"));
		await within(dialog).findByText("Safety backup");
		const post = stub.calls.find((c) => c.method === "POST");
		expect(post?.body).toEqual({
			mode: "in_place",
			fileName: "app.dump",
			sizeBytes: PG_DUMP().size,
			confirmSlug: "pg-main",
			destinationId: "dest-local",
		});
		expect(
			within(dialog).getByText(/Taking a safety backup of the current data/),
		).toBeVisible();
	});

	it("falls back to the first destination and lets the reader pick another", async () => {
		const { stub, dialog, user } = await inPlace();
		const combo = within(dialog).getByRole("combobox");
		await waitFor(() => expect(combo).toHaveTextContent("R2 nightly"));
		await user.click(combo);
		await user.click(
			await screen.findByRole("option", { name: "Server disk (not off-site)" }),
		);
		await user.type(within(dialog).getByLabelText(/to confirm/), "pg-main");
		await user.click(button(dialog, "Replace database"));
		await waitFor(() =>
			expect(stub.calls.find((c) => c.method === "POST")?.body).toMatchObject({
				destinationId: "dest-local",
			}),
		);
	});

	it("cannot proceed without any backup destination and says where to add one", async () => {
		const { dialog, user } = await inPlace({ destinations: [] });
		expect(
			await within(dialog).findByText(
				"Add a backup destination in Team settings → Backup storage first.",
			),
		).toBeVisible();
		await user.type(within(dialog).getByLabelText(/to confirm/), "pg-main");
		expect(button(dialog, "Replace database")).toBeDisabled();
	});
});

describe("DumpRestoreDialog: progress", () => {
	it("cancels a restore that is still starting and closes", async () => {
		const { stub, dialog, user, closed } = await open({
			restore: { status: "provisioning" },
			routes: { "POST /dump-restores/:id/cancel": { body: restoreView({}) } },
		});
		await pickFile(user, dialog, PG_DUMP());
		await user.click(button(dialog, "Restore into new database"));
		await user.click(
			await within(dialog).findByRole("button", { name: "Cancel restore" }),
		);
		expect(await screen.findByText("Restore canceled")).toBeVisible();
		expect(screen.getByText("The file was not used.")).toBeVisible();
		expect(stub.calls.find((c) => c.path.endsWith("/cancel"))?.path).toBe(
			"/dump-restores/rst-1/cancel",
		);
		await waitFor(() => expect(closed()).toBe(1));
	});

	it("toasts and stays open when cancelling fails", async () => {
		const { dialog, user, closed } = await open({
			restore: { status: "provisioning" },
			routes: {
				"POST /dump-restores/:id/cancel": () =>
					Response.json(
						{ code: "conflict", detail: "The restore already finished" },
						{ status: 409 },
					),
			},
		});
		await pickFile(user, dialog, PG_DUMP());
		await user.click(button(dialog, "Restore into new database"));
		await user.click(
			await within(dialog).findByRole("button", { name: "Cancel restore" }),
		);
		expect(await screen.findByText("Could not cancel")).toBeVisible();
		expect(screen.getByText("The restore already finished")).toBeVisible();
		expect(closed()).toBe(0);
	});

	it("Hide closes the dialog while the restore keeps running", async () => {
		const { stub, dialog, user, closed } = await open({
			restore: { status: "restoring" },
		});
		await pickFile(user, dialog, PG_DUMP());
		await user.click(button(dialog, "Restore into new database"));
		await within(dialog).findByText("Restoring");
		// A restore that is already running cannot be cancelled from here.
		expect(
			within(dialog).queryByRole("button", { name: "Cancel restore" }),
		).toBeNull();
		await user.click(button(dialog, "Hide"));
		expect(closed()).toBe(1);
		expect(stub.calls.some((c) => c.path.endsWith("/cancel"))).toBe(false);
	});

	it("shows the log of a running restore", async () => {
		const { dialog, user } = await open({
			restore: { status: "restoring", log: "pg_restore: creating TABLE users" },
		});
		await pickFile(user, dialog, PG_DUMP());
		await user.click(button(dialog, "Restore into new database"));
		expect(
			await within(dialog).findByRole("log", { name: "Restore log" }),
		).toHaveTextContent("pg_restore: creating TABLE users");
	});

	it("reports a finished restore into a new database and offers Close", async () => {
		const { dialog, user, closed } = await open({
			restore: { status: "succeeded", mode: "new" },
		});
		await pickFile(user, dialog, PG_DUMP());
		await user.click(button(dialog, "Restore into new database"));
		// The status pill and the alert title both say "Restored".
		expect(await within(dialog).findAllByText("Restored")).toHaveLength(2);
		expect(
			within(dialog).getByText(
				/The data is in the new database next to this one/,
			),
		).toBeVisible();
		expect(
			within(dialog).queryByRole("button", { name: "Cancel restore" }),
		).toBeNull();
		await user.click(button(dialog, "Close"));
		expect(closed()).toBe(1);
	});

	it("reports a finished in-place restore differently", async () => {
		const { dialog, user } = await open({
			restore: { status: "succeeded", mode: "in_place" },
		});
		await pickFile(user, dialog, PG_DUMP());
		await user.click(
			within(dialog).getByRole("button", { name: "Over this database" }),
		);
		await user.type(within(dialog).getByLabelText(/to confirm/), "pg-main");
		await user.click(button(dialog, "Replace database"));
		expect(
			await within(dialog).findByText(
				"This database now holds the contents of the file.",
			),
		).toBeVisible();
	});

	it("explains a failed new-database restore and keeps the new database", async () => {
		const { dialog, user } = await open({
			restore: { status: "failed", mode: "new", error: "pg_restore exited 1" },
		});
		await pickFile(user, dialog, PG_DUMP());
		await user.click(button(dialog, "Restore into new database"));
		expect(await within(dialog).findByText("The restore failed")).toBeVisible();
		expect(within(dialog).getByText(/pg_restore exited 1/)).toBeVisible();
		expect(
			within(dialog).getByText(
				/The new database is kept for you to inspect or delete/,
			),
		).toBeVisible();
		expect(button(dialog, "Close")).toBeVisible();
	});

	it("says the live data was not switched over when an in-place restore fails, even with no reason", async () => {
		const { dialog, user } = await open({
			restore: { status: "failed", mode: "in_place" },
		});
		await pickFile(user, dialog, PG_DUMP());
		await user.click(
			within(dialog).getByRole("button", { name: "Over this database" }),
		);
		await user.type(within(dialog).getByLabelText(/to confirm/), "pg-main");
		await user.click(button(dialog, "Replace database"));
		expect(await within(dialog).findByText("The restore failed")).toBeVisible();
		expect(within(dialog).getByText(/No reason was reported\./)).toBeVisible();
		expect(
			within(dialog).getByText(
				/The database's live data was not switched over/,
			),
		).toBeVisible();
	});
});

describe("DumpRestoreDialog: sending the file", () => {
	const awaiting = { status: "awaiting_upload" as const };

	async function startUpload(o: Opts = {}) {
		const ctx = await open({ restore: awaiting, ...o });
		await pickFile(ctx.user, ctx.dialog, PG_DUMP());
		await ctx.user.click(button(ctx.dialog, "Restore into new database"));
		return ctx;
	}

	it("PUTs the file to the restore's upload URL once the target is ready, once", async () => {
		const { dialog } = await startUpload();
		const xhr = await onXhr(() => {});
		expect(xhr.method).toBe("PUT");
		expect(xhr.url).toBe(
			"/api/v1/dump-restores/rst-1/upload?fileName=app.dump",
		);
		expect(xhr.headers["Content-Type"]).toBe("application/octet-stream");
		expect(xhr.withCredentials).toBe(true);
		expect((xhr.body as File).name).toBe("app.dump");
		expect(within(dialog).getByText("Waiting for the file")).toBeVisible();
		expect(FakeXhr.all).toHaveLength(1);
	});

	it("shows progress while sending and offers Cancel upload instead of Cancel restore being usable", async () => {
		const { dialog } = await startUpload();
		await onXhr((x) =>
			x.upload.onprogress?.({ lengthComputable: true, loaded: 5, total: 20 }),
		);
		expect(await within(dialog).findByText("5 B of 20 B (25%)")).toBeVisible();
		// Re-renders from progress must not start a second upload.
		expect(FakeXhr.all).toHaveLength(1);
		expect(
			within(dialog).getByRole("progressbar", { name: "Upload progress" }),
		).toBeVisible();
		expect(button(dialog, "Cancel upload")).toBeVisible();
		expect(button(dialog, "Cancel restore")).toBeDisabled();
	});

	it("ignores progress events that cannot compute a total", async () => {
		const { dialog } = await startUpload();
		await onXhr((x) =>
			x.upload.onprogress?.({ lengthComputable: false, loaded: 5, total: 0 }),
		);
		expect(await within(dialog).findByText("0 B of 20 B (0%)")).toBeVisible();
	});

	it("tells the reader the file is uploaded once the control plane accepted it", async () => {
		const { dialog } = await startUpload();
		await onXhr((x) => {
			x.status = 202;
			x.onload?.();
		});
		expect(
			await within(dialog).findByText(
				/Uploaded\. The database client is restoring the file/,
			),
		).toBeVisible();
		expect(within(dialog).queryByText(/Cancel upload/)).toBeNull();
		expect(button(dialog, "Hide")).toBeVisible();
	});

	it("aborts the transfer when the reader cancels the upload", async () => {
		const { dialog, user, closed } = await startUpload();
		await waitFor(() => expect(FakeXhr.all).toHaveLength(1));
		await user.click(
			await within(dialog).findByRole("button", { name: "Cancel upload" }),
		);
		expect(FakeXhr.all[0]?.aborted).toBe(true);
		expect(closed()).toBe(1);
	});

	it("shows the server's reason when the upload is refused and retries with a new upload", async () => {
		const { dialog, user } = await startUpload();
		await onXhr((x) => {
			x.status = 400;
			x.responseText = JSON.stringify({
				code: "bad_dump",
				detail: "Not a valid dump",
			});
			x.onload?.();
		});
		expect(await within(dialog).findByText("Not a valid dump")).toBeVisible();
		await user.click(
			await within(dialog).findByRole("button", {
				name: "Try the upload again",
			}),
		);
		await waitFor(() => expect(FakeXhr.all).toHaveLength(2));
		expect(FakeXhr.all[1]?.url).toBe(
			"/api/v1/dump-restores/rst-1/upload?fileName=app.dump",
		);
		// The old error goes away while the new attempt runs.
		await waitFor(() =>
			expect(within(dialog).queryByText("Not a valid dump")).toBeNull(),
		);
		await act(async () => {
			const x = FakeXhr.all[1];
			if (!x) return;
			x.status = 202;
			x.onload?.();
		});
		expect(
			await within(dialog).findByText(
				/Uploaded\. The database client is restoring/,
			),
		).toBeVisible();
		expect(
			within(dialog).queryByRole("button", { name: "Try the upload again" }),
		).toBeNull();
	});

	it("explains a broken connection during the upload", async () => {
		const { dialog } = await startUpload();
		await onXhr((x) => x.onerror?.());
		expect(
			await within(dialog).findByText(
				"The connection to the control plane broke during the upload. Nothing was switched over.",
			),
		).toBeVisible();
		expect(button(dialog, "Try the upload again")).toBeVisible();
	});

	it("shows an error the agent reported while still waiting for the file", async () => {
		const { dialog } = await startUpload({
			restore: { ...awaiting, error: "agent could not open the pipe" },
		});
		expect(
			await within(dialog).findByText("agent could not open the pipe"),
		).toBeVisible();
	});

	it("does not send anything before the restore is ready for the file", async () => {
		const { dialog, user } = await open({
			restore: { status: "provisioning" },
		});
		await pickFile(user, dialog, PG_DUMP());
		await user.click(button(dialog, "Restore into new database"));
		await within(dialog).findByText("Starting new database");
		expect(FakeXhr.all).toHaveLength(0);
	});
});
