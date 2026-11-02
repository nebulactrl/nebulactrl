import { describe, expect, it } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { StatusIncidentView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { IncidentDialog } from "./incident-dialog";
import {
	incidentView,
	pageView,
	TEAM_SERVICES_ROUTES,
} from "./sp-pages-fixtures";

const PAGES = {
	"GET /status-pages": {
		body: {
			items: [
				pageView({ id: "page-1", title: "Acme status" }),
				pageView({
					id: "page-2",
					title: "Beta status",
					hostname: null,
					published: false,
				}),
			],
		},
	},
};
const BASE = { ...PAGES, ...TEAM_SERVICES_ROUTES };

function Harness({
	incident = null,
	defaultPageIds,
	onChange,
	startOpen = true,
}: {
	incident?: StatusIncidentView | null;
	defaultPageIds?: string[];
	onChange?: (open: boolean) => void;
	startOpen?: boolean;
}) {
	const [open, setOpen] = useState(startOpen);
	const pathname = useRouterState({ select: (s) => s.location.pathname });
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				reopen
			</button>
			<output aria-label="location">{pathname}</output>
			<IncidentDialog
				open={open}
				onOpenChange={(next) => {
					setOpen(next);
					onChange?.(next);
				}}
				incident={incident}
				{...(defaultPageIds ? { defaultPageIds } : {})}
			/>
		</>
	);
}

const render = (ui: React.ReactNode) =>
	renderWithProviders(ui, { path: "/status-pages" });

/** Fills a field in one go: the text is pasted, not typed key by key. */
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

describe("IncidentDialog (new incident)", () => {
	it("starts with a minor, investigating incident and nothing ticked", async () => {
		stubApi(BASE);
		await render(<Harness />);
		expect(await screen.findByText("Post an incident")).toBeVisible();
		expect(screen.getByRole("button", { name: "Minor" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(
			screen.getByRole("button", { name: "Investigating" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(
			await screen.findByRole("checkbox", { name: /Acme status/ }),
		).not.toBeChecked();
		expect(
			screen.getByRole("checkbox", { name: /Beta status/ }),
		).not.toBeChecked();
	});

	it("lists pages with their hostname or draft state", async () => {
		stubApi(BASE);
		await render(<Harness />);
		const acme = (
			await screen.findByRole("checkbox", { name: /Acme status/ })
		).closest("div");
		expect(
			within(acme as HTMLElement).getByText("status.acme.test"),
		).toBeVisible();
		const beta = screen
			.getByRole("checkbox", { name: /Beta status/ })
			.closest("div");
		expect(
			within(beta as HTMLElement).getByText(/no hostname yet · draft/),
		).toBeVisible();
	});

	it("ticks the default pages", async () => {
		stubApi(BASE);
		await render(<Harness defaultPageIds={["page-2"]} />);
		expect(
			await screen.findByRole("checkbox", { name: /Beta status/ }),
		).toBeChecked();
		expect(
			screen.getByRole("checkbox", { name: /Acme status/ }),
		).not.toBeChecked();
	});

	it("explains what the chosen impact and status do", async () => {
		const user = userEvent.setup();
		stubApi(BASE);
		await render(<Harness />);
		expect(
			await screen.findByText(
				/The page shows degraded performance\. While the incident is open\./,
			),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Critical" }));
		expect(
			screen.getByText(
				/The page shows a major outage\. While the incident is open\./,
			),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Monitoring" }));
		expect(
			screen.getByText("A fix is in place; we are watching to be sure."),
		).toBeVisible();
	});

	it("asks for a title and a first update, and sends nothing", async () => {
		const user = userEvent.setup();
		const api = stubApi(BASE);
		await render(<Harness />);
		await screen.findByLabelText("Title");
		expect(screen.queryByText("Enter a title.")).toBeNull();
		await user.click(screen.getByRole("button", { name: "Post incident" }));
		expect(await screen.findByText("Enter a title.")).toBeVisible();
		expect(screen.getByText("Write what people should know.")).toBeVisible();
		expect(screen.getByLabelText("Title")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(sent(api, "POST")).toEqual([]);
	});

	it("rejects a title longer than 120 characters", async () => {
		const user = userEvent.setup();
		const api = stubApi(BASE);
		await render(<Harness />);
		await fill(user, await screen.findByLabelText("Title"), "x".repeat(121));
		await fill(user, screen.getByLabelText("First update"), "Looking into it");
		await user.click(screen.getByRole("button", { name: "Post incident" }));
		expect(
			await screen.findByText("A title is at most 120 characters."),
		).toBeVisible();
		expect(sent(api, "POST")).toEqual([]);
	});

	it("posts the incident with pages and services, toasts and opens its page", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		const api = stubApi({
			"POST /status-incidents": {
				status: 201,
				body: incidentView({ id: "inc-9" }),
			},
			...BASE,
		});
		await render(<Harness onChange={(o) => changes.push(o)} />);
		await fill(
			user,
			await screen.findByLabelText("Title"),
			"  Elevated API errors ",
		);
		await user.click(screen.getByRole("button", { name: "Major" }));
		await user.click(screen.getByRole("button", { name: "Identified" }));
		await fill(user, screen.getByLabelText("First update"), "  Cause found.  ");
		await user.click(
			await screen.findByRole("checkbox", { name: /Acme status/ }),
		);
		await user.click(await screen.findByRole("checkbox", { name: "api" }));
		await user.click(screen.getByRole("button", { name: "Post incident" }));

		expect(await screen.findByText("Incident opened")).toBeVisible();
		expect(
			screen.getByText(
				"It is on the pages you picked now. Post updates as things change.",
			),
		).toBeVisible();
		expect(sent(api, "POST")).toHaveLength(1);
		expect(sent(api, "POST")[0]?.path).toBe("/status-incidents");
		expect(sent(api, "POST")[0]?.body).toEqual({
			title: "Elevated API errors",
			impact: "major",
			status: "identified",
			body: "Cause found.",
			pageIds: ["page-1"],
			serviceIds: ["svc-api"],
		});
		expect(changes).toContain(false);
		await waitFor(() =>
			expect(screen.getByLabelText("location")).toHaveTextContent(
				"/status-pages/incidents/inc-9",
			),
		);
	});

	it("says the incident is private when no page is ticked", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /status-incidents": {
				status: 201,
				body: incidentView({ id: "inc-3" }),
			},
			...BASE,
		});
		await render(<Harness />);
		await fill(user, await screen.findByLabelText("Title"), "Disk full");
		await fill(user, screen.getByLabelText("First update"), "Cleaning up.");
		await user.click(screen.getByRole("button", { name: "Post incident" }));
		expect(
			await screen.findByText(
				"It is not posted to any page yet. Edit it to choose pages.",
			),
		).toBeVisible();
		expect(sent(api, "POST")[0]?.body).toMatchObject({
			pageIds: [],
			serviceIds: [],
		});
	});

	it("unticks a page again", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /status-incidents": { status: 201, body: incidentView() },
			...BASE,
		});
		await render(<Harness defaultPageIds={["page-1", "page-2"]} />);
		await user.click(
			await screen.findByRole("checkbox", { name: /Acme status/ }),
		);
		await fill(user, screen.getByLabelText("Title"), "T");
		await fill(user, screen.getByLabelText("First update"), "B");
		await user.click(screen.getByRole("button", { name: "Post incident" }));
		await screen.findByText("Incident opened");
		expect(sent(api, "POST")[0]?.body).toMatchObject({ pageIds: ["page-2"] });
	});

	it("shows the server's message and stays open when posting fails", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		stubApi({
			"POST /status-incidents": {
				status: 422,
				body: { status: 422, code: "unknown_page", detail: "A page is gone." },
			},
			...BASE,
		});
		await render(<Harness onChange={(o) => changes.push(o)} />);
		await fill(user, await screen.findByLabelText("Title"), "T");
		await fill(user, screen.getByLabelText("First update"), "B");
		await user.click(screen.getByRole("button", { name: "Post incident" }));
		expect(await screen.findByText("A page is gone.")).toBeVisible();
		expect(screen.getByRole("dialog")).toBeVisible();
		expect(changes).not.toContain(false);
		expect(screen.queryByText("Incident opened")).toBeNull();
	});

	it("starts over each time it opens", async () => {
		const user = userEvent.setup();
		stubApi(BASE);
		await render(<Harness />);
		await fill(user, await screen.findByLabelText("Title"), "Half typed");
		await user.click(screen.getByRole("button", { name: "Post incident" }));
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await user.click(screen.getByRole("button", { name: "reopen" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByLabelText("Title")).toHaveValue("");
		expect(
			within(dialog).queryByText("Write what people should know."),
		).toBeNull();
	});

	it("reads nothing while closed", async () => {
		const api = stubApi(BASE);
		await render(<Harness startOpen={false} />);
		expect(api.calls).toEqual([]);
	});
});

describe("IncidentDialog (edit)", () => {
	const incident = incidentView({
		id: "inc-5",
		title: "Slow builds",
		impact: "minor",
		pageIds: ["page-1"],
		serviceIds: ["svc-web"],
	});

	it("shows the incident's own values without status or first update", async () => {
		stubApi(BASE);
		await render(<Harness incident={incident} />);
		expect(await screen.findByText("Edit incident")).toBeVisible();
		expect(screen.getByLabelText("Title")).toHaveValue("Slow builds");
		expect(screen.queryByLabelText("First update")).toBeNull();
		expect(screen.queryByRole("button", { name: "Investigating" })).toBeNull();
		expect(
			await screen.findByRole("checkbox", { name: /Acme status/ }),
		).toBeChecked();
		expect(await screen.findByRole("checkbox", { name: "web" })).toBeChecked();
		expect(screen.getByRole("checkbox", { name: "api" })).not.toBeChecked();
	});

	it("keeps Save locked until something changes", async () => {
		const user = userEvent.setup();
		const api = stubApi(BASE);
		await render(<Harness incident={incident} />);
		const save = await screen.findByRole("button", { name: "Save incident" });
		expect(save).toBeDisabled();
		await user.click(screen.getByRole("button", { name: "Major" }));
		expect(save).toBeEnabled();
		await user.click(screen.getByRole("button", { name: "Minor" }));
		expect(save).toBeDisabled();
		expect(sent(api, "PATCH")).toEqual([]);
	});

	it("patches only what changed and does not leave the page", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		const api = stubApi({
			"PATCH /status-incidents/:id": { body: incident },
			...BASE,
		});
		await render(
			<Harness incident={incident} onChange={(o) => changes.push(o)} />,
		);
		const title = await screen.findByLabelText("Title");
		await user.clear(title);
		await user.paste("Slow builds and deploys");
		await user.click(screen.getByRole("button", { name: "Critical" }));
		await user.click(
			await screen.findByRole("checkbox", { name: /Beta status/ }),
		);
		await user.click(screen.getByRole("button", { name: "Save incident" }));

		expect(await screen.findByText("Incident saved")).toBeVisible();
		expect(screen.getByText("The pages show the change now.")).toBeVisible();
		expect(sent(api, "PATCH")).toHaveLength(1);
		expect(sent(api, "PATCH")[0]?.path).toBe("/status-incidents/inc-5");
		expect(sent(api, "PATCH")[0]?.body).toEqual({
			title: "Slow builds and deploys",
			impact: "critical",
			pageIds: ["page-1", "page-2"],
		});
		expect(changes).toContain(false);
		expect(screen.getByLabelText("location")).toHaveTextContent(
			/^\/status-pages$/,
		);
	});

	it("refuses an emptied title", async () => {
		const user = userEvent.setup();
		const api = stubApi(BASE);
		await render(<Harness incident={incident} />);
		await user.clear(await screen.findByLabelText("Title"));
		await user.click(screen.getByRole("button", { name: "Save incident" }));
		expect(await screen.findByText("Enter a title.")).toBeVisible();
		expect(sent(api, "PATCH")).toEqual([]);
	});

	it("shows the server's message when saving fails", async () => {
		const user = userEvent.setup();
		stubApi({
			"PATCH /status-incidents/:id": {
				status: 404,
				body: {
					status: 404,
					code: "not_found",
					detail: "Incident was deleted.",
				},
			},
			...BASE,
		});
		await render(<Harness incident={incident} />);
		await user.click(await screen.findByRole("button", { name: "Major" }));
		await user.click(screen.getByRole("button", { name: "Save incident" }));
		expect(await screen.findByText("Incident was deleted.")).toBeVisible();
		expect(screen.queryByText("Incident saved")).toBeNull();
	});
});
