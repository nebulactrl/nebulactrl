import { describe, expect, it } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { MaintenanceWindowView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { MaintenanceDialog } from "./maintenance-dialog";
import {
	pageView,
	TEAM_SERVICES_ROUTES,
	windowView,
} from "./sp-pages-fixtures";

const PAGES = {
	"GET /status-pages": {
		body: {
			items: [
				pageView({ id: "page-1", title: "Acme status" }),
				pageView({ id: "page-2", title: "Beta status" }),
			],
		},
	},
};
const BASE = { ...PAGES, ...TEAM_SERVICES_ROUTES };

function Harness({
	maintenance = null,
	defaultPageIds,
	onChange,
	startOpen = true,
}: {
	maintenance?: MaintenanceWindowView | null;
	defaultPageIds?: string[];
	onChange?: (open: boolean) => void;
	startOpen?: boolean;
}) {
	const [open, setOpen] = useState(startOpen);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				reopen
			</button>
			<MaintenanceDialog
				open={open}
				onOpenChange={(next) => {
					setOpen(next);
					onChange?.(next);
				}}
				maintenance={maintenance}
				{...(defaultPageIds ? { defaultPageIds } : {})}
			/>
		</>
	);
}

/** Sets a datetime-local picker: Testing Library cannot type into one. */
function pick(label: string, value: string) {
	fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

/** The value a datetime-local picker holds for an instant, in the browser's zone. */
function localValue(iso: string): string {
	const at = new Date(iso);
	const pad = (n: number) => String(n).padStart(2, "0");
	return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}`;
}

async function fill(
	user: ReturnType<typeof userEvent.setup>,
	field: HTMLElement,
	text: string,
) {
	await user.click(field);
	await user.paste(text);
}

const sent = (api: ReturnType<typeof stubApi>, method: string) =>
	api.calls.filter((c) => c.method === method);

const FUTURE_START = "2031-03-04T10:00";
const FUTURE_END = "2031-03-04T11:30";

describe("MaintenanceDialog (new window)", () => {
	it("starts on the next full hour for one hour, with UTC readings", async () => {
		stubApi(BASE);
		await renderWithProviders(<Harness />);
		const start = (await screen.findByLabelText("Starts")) as HTMLInputElement;
		const end = screen.getByLabelText("Ends") as HTMLInputElement;
		const startAt = new Date(start.value).getTime();
		const endAt = new Date(end.value).getTime();
		expect(endAt - startAt).toBe(3_600_000);
		expect(new Date(start.value).getMinutes()).toBe(0);
		expect(startAt).toBeGreaterThan(Date.now());
		expect(startAt - Date.now()).toBeLessThanOrEqual(3_600_000);
		expect(screen.getByText(/The window lasts 1 h\./)).toBeVisible();
		expect(document.getElementById("window-start-utc")?.textContent).toMatch(
			/^\d{4}-\d{2}-\d{2} \d{2}:00 UTC$/,
		);
	});

	it("shows the picked times in UTC and the length of the window", async () => {
		stubApi(BASE);
		await renderWithProviders(<Harness />);
		await screen.findByLabelText("Starts");
		pick("Starts", FUTURE_START);
		pick("Ends", FUTURE_END);
		const utc = (value: string) =>
			`${new Date(value).toISOString().slice(0, 16).replace("T", " ")} UTC`;
		expect(document.getElementById("window-start-utc")).toHaveTextContent(
			utc(FUTURE_START),
		);
		expect(document.getElementById("window-end-utc")).toHaveTextContent(
			utc(FUTURE_END),
		);
		expect(screen.getByText(/The window lasts 1 h 30 min\./)).toBeVisible();
	});

	it("asks for a pick while a picker is empty and hides the length", async () => {
		stubApi(BASE);
		await renderWithProviders(<Harness />);
		await screen.findByLabelText("Starts");
		pick("Ends", "");
		expect(screen.getByText("Pick a date and time")).toBeVisible();
		expect(screen.getByText("Pick an end date and time.")).toBeVisible();
		expect(screen.queryByText(/The window lasts/)).toBeNull();
	});

	it("asks for a title and sends nothing", async () => {
		const user = userEvent.setup();
		const api = stubApi(BASE);
		await renderWithProviders(<Harness />);
		await screen.findByLabelText("Title");
		expect(screen.queryByText("Enter a title.")).toBeNull();
		await user.click(
			screen.getByRole("button", { name: "Schedule maintenance" }),
		);
		expect(await screen.findByText("Enter a title.")).toBeVisible();
		expect(screen.getByLabelText("Title")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(sent(api, "POST")).toEqual([]);
	});

	it("refuses an end that is not after the start", async () => {
		const user = userEvent.setup();
		const api = stubApi(BASE);
		await renderWithProviders(<Harness />);
		await fill(user, await screen.findByLabelText("Title"), "Upgrade");
		pick("Starts", FUTURE_START);
		pick("Ends", FUTURE_START);
		expect(screen.getByText("The end must be after the start.")).toBeVisible();
		await user.click(
			screen.getByRole("button", { name: "Schedule maintenance" }),
		);
		expect(sent(api, "POST")).toEqual([]);
	});

	it("refuses a window that already ended", async () => {
		const user = userEvent.setup();
		const api = stubApi(BASE);
		await renderWithProviders(<Harness />);
		await fill(user, await screen.findByLabelText("Title"), "Upgrade");
		pick("Starts", "2020-01-01T10:00");
		pick("Ends", "2020-01-01T11:00");
		expect(
			screen.getByText("The window must end in the future."),
		).toBeVisible();
		await user.click(
			screen.getByRole("button", { name: "Schedule maintenance" }),
		);
		expect(sent(api, "POST")).toEqual([]);
	});

	it("refuses a description over 2000 characters", async () => {
		const user = userEvent.setup();
		const api = stubApi(BASE);
		await renderWithProviders(<Harness />);
		await fill(user, await screen.findByLabelText("Title"), "Upgrade");
		await fill(
			user,
			screen.getByLabelText("Description (optional)"),
			"d".repeat(2001),
		);
		expect(
			screen.getByText("A description is at most 2000 characters."),
		).toBeVisible();
		await user.click(
			screen.getByRole("button", { name: "Schedule maintenance" }),
		);
		expect(sent(api, "POST")).toEqual([]);
	});

	it("schedules the window with pages and services and closes", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		const api = stubApi({
			"POST /maintenance-windows": { status: 201, body: windowView() },
			...BASE,
		});
		await renderWithProviders(<Harness onChange={(o) => changes.push(o)} />, {
			path: "/status-pages",
		});
		await fill(
			user,
			await screen.findByLabelText("Title"),
			"  Database upgrade ",
		);
		await fill(
			user,
			screen.getByLabelText("Description (optional)"),
			" Read-only. ",
		);
		pick("Starts", FUTURE_START);
		pick("Ends", FUTURE_END);
		await user.click(
			await screen.findByRole("checkbox", { name: /Beta status/ }),
		);
		await user.click(await screen.findByRole("checkbox", { name: "api" }));
		await user.click(
			screen.getByRole("button", { name: "Schedule maintenance" }),
		);

		expect(await screen.findByText("Maintenance scheduled")).toBeVisible();
		expect(
			screen.getByText("The pages you picked announce it up to 14 days ahead."),
		).toBeVisible();
		expect(sent(api, "POST")).toHaveLength(1);
		expect(sent(api, "POST")[0]?.path).toBe("/maintenance-windows");
		expect(sent(api, "POST")[0]?.body).toEqual({
			title: "Database upgrade",
			description: "Read-only.",
			startsAt: new Date(FUTURE_START).toISOString(),
			endsAt: new Date(FUTURE_END).toISOString(),
			pageIds: ["page-2"],
			serviceIds: ["svc-api"],
		});
		expect(changes).toContain(false);
	});

	it("says no page announces the window when none is picked", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /maintenance-windows": { status: 201, body: windowView() },
			...BASE,
		});
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		await fill(user, await screen.findByLabelText("Title"), "Upgrade");
		await user.click(
			screen.getByRole("button", { name: "Schedule maintenance" }),
		);
		expect(
			await screen.findByText(
				"No page announces it: pick pages to tell visitors.",
			),
		).toBeVisible();
		expect(sent(api, "POST")[0]?.body).toMatchObject({ pageIds: [] });
	});

	it("ticks the default pages", async () => {
		stubApi(BASE);
		await renderWithProviders(<Harness defaultPageIds={["page-1"]} />);
		expect(
			await screen.findByRole("checkbox", { name: /Acme status/ }),
		).toBeChecked();
		expect(
			screen.getByRole("checkbox", { name: /Beta status/ }),
		).not.toBeChecked();
	});

	it("shows the server's message and stays open when scheduling fails", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		stubApi({
			"POST /maintenance-windows": {
				status: 422,
				body: { status: 422, code: "invalid", detail: "Too far ahead." },
			},
			...BASE,
		});
		await renderWithProviders(<Harness onChange={(o) => changes.push(o)} />);
		await fill(user, await screen.findByLabelText("Title"), "Upgrade");
		await user.click(
			screen.getByRole("button", { name: "Schedule maintenance" }),
		);
		expect(await screen.findByText("Too far ahead.")).toBeVisible();
		expect(screen.getByRole("dialog")).toBeVisible();
		expect(changes).not.toContain(false);
		expect(screen.queryByText("Maintenance scheduled")).toBeNull();
	});

	it("starts over each time it opens", async () => {
		const user = userEvent.setup();
		stubApi(BASE);
		await renderWithProviders(<Harness />);
		await fill(user, await screen.findByLabelText("Title"), "Half typed");
		await user.click(
			screen.getByRole("button", { name: "Schedule maintenance" }),
		);
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await user.click(screen.getByRole("button", { name: "reopen" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByLabelText("Title")).toHaveValue("");
		expect(within(dialog).queryByText("Enter a title.")).toBeNull();
	});

	it("reads nothing while closed", async () => {
		const api = stubApi(BASE);
		await renderWithProviders(<Harness startOpen={false} />);
		expect(api.calls).toEqual([]);
	});
});

describe("MaintenanceDialog (edit)", () => {
	const upcoming = windowView({
		id: "win-7",
		title: "Network move",
		description: "Short blip.",
		startsAt: "2031-06-01T08:00:00Z",
		endsAt: "2031-06-01T09:00:00Z",
		pageIds: ["page-1"],
		serviceIds: ["svc-web"],
	});

	it("shows the window's own values in local time", async () => {
		stubApi(BASE);
		await renderWithProviders(<Harness maintenance={upcoming} />);
		expect(await screen.findByText("Edit maintenance")).toBeVisible();
		expect(screen.getByLabelText("Title")).toHaveValue("Network move");
		expect(screen.getByLabelText("Description (optional)")).toHaveValue(
			"Short blip.",
		);
		expect(screen.getByLabelText("Starts")).toHaveValue(
			localValue(upcoming.startsAt),
		);
		expect(screen.getByLabelText("Ends")).toHaveValue(
			localValue(upcoming.endsAt),
		);
		expect(document.getElementById("window-start-utc")).toHaveTextContent(
			"2031-06-01 08:00 UTC",
		);
		expect(
			await screen.findByRole("checkbox", { name: /Acme status/ }),
		).toBeChecked();
		expect(await screen.findByRole("checkbox", { name: "web" })).toBeChecked();
	});

	it("keeps Save locked until something changes", async () => {
		const user = userEvent.setup();
		const api = stubApi(BASE);
		await renderWithProviders(<Harness maintenance={upcoming} />);
		const save = await screen.findByRole("button", {
			name: "Save maintenance",
		});
		expect(save).toBeDisabled();
		await fill(user, screen.getByLabelText("Description (optional)"), " more");
		expect(save).toBeEnabled();
		expect(sent(api, "PATCH")).toEqual([]);
	});

	it("patches only what changed and closes", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		const api = stubApi({
			"PATCH /maintenance-windows/:id": { body: upcoming },
			...BASE,
		});
		await renderWithProviders(
			<Harness maintenance={upcoming} onChange={(o) => changes.push(o)} />,
			{ path: "/status-pages" },
		);
		const title = await screen.findByLabelText("Title");
		await user.clear(title);
		await user.paste("Network move, part 2");
		pick("Ends", "2031-06-01T12:00");
		await user.click(await screen.findByRole("checkbox", { name: "api" }));
		await user.click(screen.getByRole("button", { name: "Save maintenance" }));

		expect(await screen.findByText("Maintenance saved")).toBeVisible();
		expect(sent(api, "PATCH")).toHaveLength(1);
		expect(sent(api, "PATCH")[0]?.path).toBe("/maintenance-windows/win-7");
		expect(sent(api, "PATCH")[0]?.body).toEqual({
			title: "Network move, part 2",
			endsAt: new Date("2031-06-01T12:00").toISOString(),
			serviceIds: ["svc-web", "svc-api"],
		});
		expect(changes).toContain(false);
	});

	it("lets a window that already started keep its start", async () => {
		const user = userEvent.setup();
		const running = windowView({
			id: "win-8",
			title: "Rolling restart",
			phase: "active",
			startsAt: "2020-01-01T00:00:00Z",
			endsAt: "2099-01-01T00:00:00Z",
		});
		const api = stubApi({
			"PATCH /maintenance-windows/:id": { body: running },
			...BASE,
		});
		await renderWithProviders(<Harness maintenance={running} />, {
			path: "/status-pages",
		});
		const title = await screen.findByLabelText("Title");
		await user.clear(title);
		await user.paste("Rolling restart, extended");
		await user.click(screen.getByRole("button", { name: "Save maintenance" }));
		expect(await screen.findByText("Maintenance saved")).toBeVisible();
		expect(sent(api, "PATCH")[0]?.body).toEqual({
			title: "Rolling restart, extended",
		});
	});
});
