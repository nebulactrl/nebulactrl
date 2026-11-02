import { describe, expect, it } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CreatePageDialog } from "./create-page-dialog";
import { pageView } from "./sp-pages-fixtures";

const PROJECTS = {
	"GET /projects": {
		body: make("PageProject", {
			items: [
				make("Project", { id: "p-web", name: "Web", system: false }),
				make("Project", {
					id: "p-sys",
					name: "Platform internals",
					system: true,
				}),
				make("Project", { id: "p-ops", name: "Ops", system: false }),
			],
		}),
	},
};
const LIST = { "GET /status-pages": { body: { items: [] } } };

function Harness({
	onChange,
	startOpen = true,
}: {
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
			<CreatePageDialog
				open={open}
				onOpenChange={(next) => {
					setOpen(next);
					onChange?.(next);
				}}
			/>
		</>
	);
}

/** Fills a field in one go: the text is pasted, not typed key by key. */
async function fill(
	user: ReturnType<typeof userEvent.setup>,
	field: HTMLElement,
	text: string,
) {
	await user.click(field);
	await user.paste(text);
}

const create = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((c) => c.method === "POST");

describe("CreatePageDialog", () => {
	it("does not read projects while closed", async () => {
		const api = stubApi({ ...PROJECTS, ...LIST });
		await renderWithProviders(<Harness startOpen={false} />, {
			path: "/status-pages",
		});
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(api.calls).toEqual([]);
	});

	it("asks for a title and sends nothing when it is empty", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...PROJECTS, ...LIST });
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		const title = await screen.findByLabelText("Title");
		expect(title).not.toHaveAttribute("aria-invalid", "true");
		await user.click(screen.getByRole("button", { name: "Create page" }));
		expect(await screen.findByText("Enter a title.")).toBeVisible();
		expect(title).toHaveAttribute("aria-invalid", "true");
		expect(create(api)).toEqual([]);
	});

	it("derives the short-name hint from the title", async () => {
		const user = userEvent.setup();
		stubApi({ ...PROJECTS, ...LIST });
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		await user.type(await screen.findByLabelText("Title"), "API Status!");
		expect(screen.getByLabelText("Short name (optional)")).toHaveAttribute(
			"placeholder",
			"api-status",
		);
	});

	it("creates a draft from the title alone, toasts and opens the editor", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		const api = stubApi({
			"POST /status-pages": {
				status: 201,
				body: pageView({ id: "page-9", title: "Acme" }),
			},
			...PROJECTS,
			...LIST,
		});
		await renderWithProviders(<Harness onChange={(o) => changes.push(o)} />, {
			path: "/status-pages",
		});
		await fill(user, await screen.findByLabelText("Title"), "  Acme  ");
		await user.click(screen.getByRole("button", { name: "Create page" }));

		expect(await screen.findByText("Status page created")).toBeVisible();
		expect(
			screen.getByText(
				"It is a draft. Add components and a hostname, then publish.",
			),
		).toBeVisible();
		expect(create(api)).toHaveLength(1);
		expect(create(api)[0]?.path).toBe("/status-pages");
		expect(create(api)[0]?.body).toEqual({
			title: "Acme",
			theme: "auto",
			showUptime: true,
		});
		expect(changes).toContain(false);
		await waitFor(() =>
			expect(screen.getByLabelText("location")).toHaveTextContent(
				"/status-pages/page-9",
			),
		);
		expect(api.unmatched).toEqual([]);
	});

	it("sends a normalised hostname, the short name and the chosen project", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /status-pages": { status: 201, body: pageView({ id: "page-2" }) },
			...PROJECTS,
			...LIST,
		});
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		await fill(user, await screen.findByLabelText("Title"), "Acme status");
		await fill(user, screen.getByLabelText("Short name (optional)"), "acme");
		await fill(
			user,
			screen.getByLabelText("Hostname (optional)"),
			"  Status.Acme.Test.  ",
		);
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: "Ops" }));
		await user.click(screen.getByRole("button", { name: "Create page" }));

		await screen.findByText("Status page created");
		expect(create(api)[0]?.body).toEqual({
			title: "Acme status",
			slug: "acme",
			hostname: "status.acme.test",
			theme: "auto",
			showUptime: true,
			fromProjectId: "p-ops",
		});
	});

	it("offers only the team's own projects and None", async () => {
		const user = userEvent.setup();
		stubApi({ ...PROJECTS, ...LIST });
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		await screen.findByLabelText("Title");
		await user.click(screen.getByRole("combobox"));
		const options = await screen.findAllByRole("option");
		expect(options.map((o) => o.textContent)).toEqual(["None", "Web", "Ops"]);
	});

	it("drops the project again when None is picked", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /status-pages": { status: 201, body: pageView() },
			...PROJECTS,
			...LIST,
		});
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		await user.type(await screen.findByLabelText("Title"), "Acme");
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: "Web" }));
		expect(screen.getByRole("combobox")).toHaveTextContent("Web");
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: "None" }));
		await user.click(screen.getByRole("button", { name: "Create page" }));
		await screen.findByText("Status page created");
		expect(create(api)[0]?.body).not.toHaveProperty("fromProjectId");
	});

	it("blocks a bad hostname and a bad short name before sending", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...PROJECTS, ...LIST });
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		await user.type(await screen.findByLabelText("Title"), "Acme");
		await fill(
			user,
			screen.getByLabelText("Hostname (optional)"),
			"https://x.test",
		);
		await fill(
			user,
			screen.getByLabelText("Short name (optional)"),
			"Not Valid",
		);
		await user.click(screen.getByRole("button", { name: "Create page" }));
		expect(
			await screen.findByText(
				"Enter the hostname only, without https://, a port or a path.",
			),
		).toBeVisible();
		expect(
			screen.getByText(/Use lowercase letters and digits, with single hyphens/),
		).toBeVisible();
		expect(create(api)).toEqual([]);
	});

	it("shows a taken short name at the field and clears it on the next edit", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /status-pages": {
				status: 409,
				body: { status: 409, code: "slug_conflict", detail: "conflict" },
			},
			...PROJECTS,
			...LIST,
		});
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		await user.type(await screen.findByLabelText("Title"), "Acme");
		await user.click(screen.getByRole("button", { name: "Create page" }));
		expect(
			await screen.findByText(
				"Another status page already uses this short name.",
			),
		).toBeVisible();
		expect(screen.queryByText("conflict")).toBeNull();
		expect(screen.queryByText("Status page created")).toBeNull();

		await user.type(screen.getByLabelText("Short name (optional)"), "acme-2");
		expect(
			screen.queryByText("Another status page already uses this short name."),
		).toBeNull();
		expect(create(api)).toHaveLength(1);
	});

	it("shows a taken hostname at the hostname field", async () => {
		const user = userEvent.setup();
		stubApi({
			"POST /status-pages": {
				status: 409,
				body: { status: 409, code: "hostname_conflict", detail: "x" },
			},
			...PROJECTS,
			...LIST,
		});
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		await user.type(await screen.findByLabelText("Title"), "Acme");
		await fill(
			user,
			screen.getByLabelText("Hostname (optional)"),
			"status.acme.test",
		);
		await user.click(screen.getByRole("button", { name: "Create page" }));
		expect(
			await screen.findByText(
				"Another status page or a service domain already serves this hostname.",
			),
		).toBeVisible();
		expect(screen.getByLabelText("Hostname (optional)")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});

	it("explains an unknown project at the bottom of the form", async () => {
		const user = userEvent.setup();
		stubApi({
			"POST /status-pages": {
				status: 422,
				body: { status: 422, code: "unknown_project", detail: "x" },
			},
			...PROJECTS,
			...LIST,
		});
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		await user.type(await screen.findByLabelText("Title"), "Acme");
		await user.click(screen.getByRole("button", { name: "Create page" }));
		expect(
			await screen.findByText(
				"That project is not in this team. Pick another one.",
			),
		).toBeVisible();
	});

	it("falls back to the server's message for an unknown failure and stays open", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		stubApi({
			"POST /status-pages": {
				status: 500,
				body: { status: 500, code: "boom", detail: "The database is down." },
			},
			...PROJECTS,
			...LIST,
		});
		await renderWithProviders(<Harness onChange={(o) => changes.push(o)} />, {
			path: "/status-pages",
		});
		await user.type(await screen.findByLabelText("Title"), "Acme");
		await user.click(screen.getByRole("button", { name: "Create page" }));
		expect(await screen.findByText("The database is down.")).toBeVisible();
		expect(screen.getByRole("dialog")).toBeVisible();
		expect(changes).not.toContain(false);
		expect(screen.getByLabelText("location")).toHaveTextContent(
			/^\/status-pages$/,
		);
	});

	it("sends only one request while the first is pending", async () => {
		const user = userEvent.setup();
		let release: (() => void) | undefined;
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		const api = stubApi({
			"POST /status-pages": async () => {
				await gate;
				return Response.json(pageView(), { status: 201 });
			},
			...PROJECTS,
			...LIST,
		});
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		await user.type(await screen.findByLabelText("Title"), "Acme");
		const submit = screen.getByRole("button", { name: "Create page" });
		await user.click(submit);
		await waitFor(() => expect(submit).toBeDisabled());
		await user.click(submit);
		release?.();
		await screen.findByText("Status page created");
		expect(create(api)).toHaveLength(1);
	});

	it("closes from Cancel without sending", async () => {
		const user = userEvent.setup();
		const changes: boolean[] = [];
		const api = stubApi({ ...PROJECTS, ...LIST });
		await renderWithProviders(<Harness onChange={(o) => changes.push(o)} />, {
			path: "/status-pages",
		});
		await screen.findByLabelText("Title");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(changes).toEqual([false]);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		expect(create(api)).toEqual([]);
	});

	it("starts empty again each time it opens", async () => {
		const user = userEvent.setup();
		stubApi({ ...PROJECTS, ...LIST });
		await renderWithProviders(<Harness />, { path: "/status-pages" });
		await user.type(await screen.findByLabelText("Title"), "Half typed");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await user.click(screen.getByRole("button", { name: "reopen" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByLabelText("Title")).toHaveValue("");
		expect(within(dialog).queryByText("Enter a title.")).toBeNull();
	});
});
