import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { TemplateUpdate, TemplateUpdateChange } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { templatesActions, useTemplatesState } from "./store";
import { instance, resetTemplatesStore, update } from "./test-fixtures";
import { UpdateStackDialog } from "./update-dialog";

afterEach(() => act(() => resetTemplatesStore()));

const stack = instance({ id: "i1", name: "Umami" });

function Probe() {
	const { updating } = useTemplatesState();
	return <output aria-label="updating">{updating?.id ?? "none"}</output>;
}

function change(
	over: Partial<TemplateUpdateChange> & { field: string },
): TemplateUpdateChange {
	return {
		from: "",
		to: "",
		secret: false,
		service: "web",
		status: "apply",
		...over,
	};
}

const problem = (status: number, detail: string) => ({
	status,
	body: { status, title: "x", detail, code: "err" },
});

async function open(
	body: TemplateUpdate | { status: number; body: unknown },
	extra: Parameters<typeof stubApi>[0] = {},
) {
	const api = stubApi({
		"GET /template-instances/:instanceId/update":
			"status" in body && "body" in body ? body : { body },
		...extra,
	});
	await renderWithProviders(
		<>
			<UpdateStackDialog />
			<Probe />
		</>,
	);
	act(() => templatesActions.requestUpdate(stack));
	return api;
}

describe("UpdateStackDialog", () => {
	test("renders nothing and fetches nothing until an update is requested", async () => {
		const api = stubApi({});
		await renderWithProviders(<UpdateStackDialog />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("asks for the stack's update and shows the revision step while comparing", async () => {
		const api = await open(
			update({
				installedRevision: 1,
				currentRevision: 2,
				installedVersion: "3.4.0",
				currentVersion: "3.4.1",
				apply: 1,
			}),
		);
		expect(await screen.findByText("Update Umami")).toBeInTheDocument();
		expect(
			await screen.findByText("Revision 1 → 2 · 3.4.0 → 3.4.1"),
		).toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/template-instances/i1/update");
	});

	test("groups the diff by what the update does and describes each change", async () => {
		await open(
			update({
				apply: 3,
				kept: 1,
				manual: 1,
				applied: 1,
				changes: [
					change({ field: "env.DOMAIN", from: "old.com", to: "new.com" }),
					change({ field: "env.NEW_FLAG", to: "on" }),
					change({ field: "env.GONE", from: "x" }),
					change({
						field: "env.API_KEY",
						from: "a",
						to: "b",
						secret: true,
						status: "kept",
						reason: "You changed it",
					}),
					change({
						field: "volume:data",
						to: "/data",
						status: "manual",
						reason: "Volumes are yours",
					}),
					change({
						field: "file:/etc/app.conf",
						from: "1",
						to: "2",
						status: "applied",
					}),
				],
			}),
		);
		expect(await screen.findByText("Will be staged (3)")).toBeInTheDocument();
		expect(
			screen.getByText("Kept as you changed them (1)"),
		).toBeInTheDocument();
		expect(screen.getByText("Not done by an update (1)")).toBeInTheDocument();
		expect(screen.getByText("Already applied (1)")).toBeInTheDocument();
		expect(screen.getByText(/variable DOMAIN/)).toBeInTheDocument();
		expect(screen.getByText("old.com → new.com")).toBeInTheDocument();
		expect(screen.getByText(/variable NEW_FLAG/)).toBeInTheDocument();
		expect(screen.getByText(/variable GONE/)).toBeInTheDocument();
		expect(screen.getByText("secret")).toBeInTheDocument();
		expect(screen.queryByText("a → b")).not.toBeInTheDocument();
		expect(screen.getByText(/volume data/)).toBeInTheDocument();
		expect(screen.getByText(/file \/etc\/app\.conf/)).toBeInTheDocument();
		expect(screen.getByText("You changed it")).toBeInTheDocument();
		expect(screen.getByText("Volumes are yours")).toBeInTheDocument();
		expect(
			screen.getByText(
				"3 changes will be staged, 1 kept as you changed them, 1 left for you. Nothing is deployed until you press Deploy.",
			),
		).toBeInTheDocument();
	});

	test("a group with no changes is not shown", async () => {
		await open(
			update({ apply: 1, changes: [change({ field: "env.A", to: "1" })] }),
		);
		await screen.findByText("Will be staged (1)");
		expect(
			screen.queryByText(/Kept as you changed them/),
		).not.toBeInTheDocument();
		expect(screen.queryByText(/Already applied/)).not.toBeInTheDocument();
	});

	test("lists the notes of the new revision", async () => {
		await open(update({ apply: 1, notes: ["Run migrations after deploying"] }));
		expect(
			await screen.findByText("Notes for this revision"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Run migrations after deploying"),
		).toBeInTheDocument();
	});

	test("a blocked update says why and offers no way to stage it", async () => {
		await open(
			update({ blocked: "The template was removed from the catalog." }),
		);
		expect(
			await screen.findByText("The template was removed from the catalog."),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Stage|Mark as reviewed/ }),
		).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument();
	});

	test("a stack that is up to date says there is nothing to update", async () => {
		await open(update({ available: false }));
		expect(
			await screen.findByText(/matches the template's current revision/),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Stage|Mark as reviewed/ }),
		).not.toBeInTheDocument();
	});

	test("staging posts the current revision, confirms, closes and refetches the stack data", async () => {
		const user = userEvent.setup();
		const api = await open(
			update({
				currentRevision: 7,
				apply: 2,
				changes: [
					change({ field: "env.A", to: "1" }),
					change({ field: "env.B", to: "2" }),
				],
			}),
			{
				"POST /template-instances/:instanceId/update": {
					body: {
						staged: 2,
						changes: [],
						instance: stack,
						services: [],
						warnings: [],
					},
				},
			},
		);
		await user.click(
			await screen.findByRole("button", { name: "Stage 2 changes" }),
		);
		expect(
			await screen.findByText("Staged the Umami update"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/2 changes staged\. Review and deploy them from the staged bar\./,
			),
		).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/template-instances/i1/update");
		expect(post?.body).toEqual({ revision: 7, inputs: {} });
		await waitFor(() =>
			expect(screen.getByLabelText("updating")).toHaveTextContent("none"),
		);
		expect(screen.queryByText("Update Umami")).not.toBeInTheDocument();
	});

	test("a single staged change is not pluralised", async () => {
		const user = userEvent.setup();
		await open(
			update({ apply: 1, changes: [change({ field: "env.A", to: "1" })] }),
			{
				"POST /template-instances/:instanceId/update": {
					body: {
						staged: 1,
						changes: [],
						instance: stack,
						services: [],
						warnings: [],
					},
				},
			},
		);
		await user.click(
			await screen.findByRole("button", { name: "Stage 1 change" }),
		);
		expect(await screen.findByText(/1 change staged\./)).toBeInTheDocument();
	});

	test("an update with nothing to stage is marked reviewed instead", async () => {
		const user = userEvent.setup();
		const api = await open(
			update({
				apply: 0,
				manual: 1,
				changes: [change({ field: "volume:x", status: "manual" })],
			}),
			{
				"POST /template-instances/:instanceId/update": {
					body: {
						staged: 0,
						changes: [],
						instance: stack,
						services: [],
						warnings: [],
					},
				},
			},
		);
		await user.click(
			await screen.findByRole("button", { name: "Mark as reviewed" }),
		);
		expect(await screen.findByText(/0 changes staged/)).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "POST")).toBe(true);
	});

	test("a required new input must be filled before staging and is sent with the request", async () => {
		const user = userEvent.setup();
		const api = await open(
			update({
				apply: 1,
				inputs: [
					{
						key: "SMTP_HOST",
						label: "SMTP host",
						required: true,
						secret: false,
						generated: false,
					},
					{
						key: "APP_KEY",
						label: "App key",
						required: true,
						secret: true,
						generated: true,
					},
				],
			}),
			{
				"POST /template-instances/:instanceId/update": {
					body: {
						staged: 1,
						changes: [],
						instance: stack,
						services: [],
						warnings: [],
					},
				},
			},
		);
		expect(await screen.findByText("New inputs")).toBeInTheDocument();
		const stage = screen.getByRole("button", { name: "Stage 1 change" });
		expect(stage).toBeDisabled();
		await user.type(screen.getByLabelText(/SMTP host/), " mail.example.com ");
		expect(stage).toBeEnabled();
		await user.click(stage);
		await screen.findByText("Staged the Umami update");
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			revision: 2,
			inputs: { SMTP_HOST: "mail.example.com" },
		});
	});

	test("a failed update shows the server's reason, stays open and can be retried", async () => {
		const user = userEvent.setup();
		let attempts = 0;
		const api = await open(
			update({ apply: 1, changes: [change({ field: "env.A", to: "1" })] }),
			{
				"POST /template-instances/:instanceId/update": () => {
					attempts += 1;
					return attempts === 1
						? Response.json(
								{
									status: 409,
									title: "x",
									detail: "the stack changed meanwhile",
									code: "conflict",
								},
								{ status: 409 },
							)
						: Response.json({
								staged: 1,
								changes: [],
								instance: stack,
								services: [],
								warnings: [],
							});
				},
			},
		);
		await user.click(
			await screen.findByRole("button", { name: "Stage 1 change" }),
		);
		expect(
			await screen.findByText("The update was not staged"),
		).toBeInTheDocument();
		expect(screen.getByText("the stack changed meanwhile")).toBeInTheDocument();
		expect(screen.getByLabelText("updating")).toHaveTextContent("i1");
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Stage 1 change" }),
			).toBeEnabled(),
		);
		await user.click(screen.getByRole("button", { name: "Stage 1 change" }));
		expect(
			await screen.findByText("Staged the Umami update"),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(2);
	});

	test("a failed load says so, can be retried, and cannot be staged", async () => {
		const user = userEvent.setup();
		let attempts = 0;
		stubApi({
			"GET /template-instances/:instanceId/update": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(problem(500, "catalog unreadable").body, {
							status: 500,
						})
					: Response.json(update({ apply: 1 }));
			},
		});
		await renderWithProviders(<UpdateStackDialog />);
		act(() => templatesActions.requestUpdate(stack));
		expect(
			await screen.findByText("The update did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/catalog unreadable Nothing was changed\./),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Stage/ }),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(
			await screen.findByRole("button", { name: "Stage 1 change" }),
		).toBeInTheDocument();
	});

	test("the dismiss button reads Cancel when staging is possible and Close otherwise; it closes the dialog", async () => {
		const user = userEvent.setup();
		await open(update({ apply: 1 }));
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.getByLabelText("updating")).toHaveTextContent("none"),
		);
	});

	test("shows Close while the diff is still loading", async () => {
		stubApi({
			"GET /template-instances/:instanceId/update": () => new Promise(() => {}),
		});
		await renderWithProviders(<UpdateStackDialog />);
		act(() => templatesActions.requestUpdate(stack));
		expect(
			await screen.findByText("Comparing with the catalog…"),
		).toBeInTheDocument();
		const footer = screen.getByRole("button", { name: "Close", hidden: false });
		expect(
			within(footer.parentElement as HTMLElement).queryByRole("button", {
				name: /Stage/,
			}),
		).toBeNull();
	});
});
