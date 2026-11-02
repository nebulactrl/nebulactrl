import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { CreatedInvite } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { InviteDialog } from "./invite-dialog";
import type { Role } from "./roles";

function project(id: string, name: string) {
	return {
		id,
		name,
		slug: name.toLowerCase(),
		description: "",
		system: false,
		buildLocation: "inherit",
		buildServerId: "",
		registryId: "",
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
	};
}

const PROJECTS = {
	items: [project("p-1", "Storefront"), project("p-2", "Billing")],
};

function created(over: Partial<CreatedInvite> = {}): CreatedInvite {
	return {
		delivery: "link_only",
		link: "/invite/nci_abc",
		token: "nci_abc",
		invite: {
			id: "i-1",
			prefix: "abc",
			role: "developer",
			maxUses: 1,
			useCount: 0,
			emailSent: false,
			exhausted: false,
			expired: false,
			createdAt: "2026-01-01T00:00:00Z",
			expiresAt: "2026-01-08T00:00:00Z",
		},
		...over,
	};
}

function Harness({ actorRole = "owner" }: { actorRole?: Role }) {
	const [open, setOpen] = useState(true);
	return (
		<>
			<span data-testid="state">{open ? "open" : "closed"}</span>
			<button type="button" onClick={() => setOpen(true)}>
				reopen
			</button>
			<InviteDialog
				open={open}
				onOpenChange={setOpen}
				teamId="t-1"
				actorRole={actorRole}
			/>
		</>
	);
}

const routes = (reply: object) => ({
	"GET /projects": { body: PROJECTS },
	"POST /teams/:teamId/invites": { status: 201, ...reply },
	"GET /teams/:teamId/invites": { body: { items: [] } },
});

const postBody = (api: ReturnType<typeof stubApi>) =>
	api.calls.find((c) => c.method === "POST")?.body;

describe("InviteDialog", () => {
	test("email invite needs an address, then posts role developer and the default 7-day expiry", async () => {
		const api = stubApi(
			routes({
				status: 201,
				body: created({
					delivery: "sent",
					invite: { ...created().invite, email: "new@example.com" },
				}),
			}),
		);
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);

		const send = await screen.findByRole("button", { name: "Send invite" });
		expect(send).toBeDisabled();
		await user.type(
			screen.getByLabelText("Email address"),
			"  new@example.com ",
		);
		expect(send).toBeEnabled();
		await user.click(send);

		expect(
			await screen.findByText("Invite for new@example.com"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Invite emailed to new@example.com. The link below works too.",
			),
		).toBeInTheDocument();
		expect(screen.getByTestId("invite-link")).toHaveTextContent(
			`${window.location.origin}/invite/nci_abc`,
		);
		expect(
			screen.getByText(/It works once, for that address only\./),
		).toBeInTheDocument();
		expect(postBody(api)).toEqual({
			role: "developer",
			email: "new@example.com",
			expiresInDays: 7,
		});
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/teams/t-1/invites",
		);
		expect(api.unmatched).toEqual([]);
	});

	test("link invite posts maxUses, chosen expiry, role and restricted projects", async () => {
		const api = stubApi(
			routes({
				status: 201,
				body: created({
					invite: { ...created().invite, role: "viewer", maxUses: 5 },
				}),
			}),
		);
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);

		await user.click(
			await screen.findByRole("button", { name: "Share a link" }),
		);
		const uses = screen.getByLabelText("People who can use it");
		await user.clear(uses);
		// Non-digits are dropped as typed.
		await user.type(uses, "5x");
		expect(uses).toHaveValue("5");
		await user.click(screen.getByRole("button", { name: "30 days" }));
		await user.click(screen.getByRole("radio", { name: /Viewer/ }));
		await user.click(await screen.findByRole("checkbox", { name: "Billing" }));
		expect(
			screen.getByText("Limited to 1 project; the rest answer 404."),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create link" }));

		expect(await screen.findByText("Invite link ready")).toBeInTheDocument();
		expect(
			screen.getByText("Share this link with the person you are inviting."),
		).toBeInTheDocument();
		expect(
			screen.getByText(/It works 5 times and expires/),
		).toBeInTheDocument();
		expect(postBody(api)).toEqual({
			role: "viewer",
			maxUses: 5,
			expiresInDays: 30,
			projectIds: ["p-2"],
		});
	});

	test("link invite with an emptied use count omits maxUses; admin roles carry no project list", async () => {
		const api = stubApi(routes({ status: 201, body: created() }));
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);

		await user.click(
			await screen.findByRole("button", { name: "Share a link" }),
		);
		await user.clear(screen.getByLabelText("People who can use it"));
		await user.click(
			await screen.findByRole("checkbox", { name: "Storefront" }),
		);
		await user.click(screen.getByRole("radio", { name: /^Admin/ }));
		expect(
			screen.queryByRole("checkbox", { name: "Storefront" }),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Create link" }));

		await screen.findByText("Invite link ready");
		expect(postBody(api)).toEqual({ role: "admin", expiresInDays: 7 });
	});

	test("an admin can hand out admin, developer and viewer but not owner", async () => {
		stubApi(routes({ body: created() }));
		await renderWithProviders(<Harness actorRole="admin" />);

		const group = await screen.findByRole("radiogroup");
		const names = within(group)
			.getAllByRole("radio")
			.map((r) => r.textContent ?? "");
		expect(names.some((n) => n.startsWith("Owner"))).toBe(false);
		expect(names.map((n) => n.slice(0, 5))).toEqual([
			"Admin",
			"Devel",
			"Viewe",
		]);
	});

	test("an owner can invite another owner", async () => {
		const api = stubApi(routes({ status: 201, body: created() }));
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);

		await user.type(
			await screen.findByLabelText("Email address"),
			"o@example.com",
		);
		await user.click(screen.getByRole("radio", { name: /^Owner/ }));
		await user.click(screen.getByRole("button", { name: "Send invite" }));

		await screen.findByText("Invite link ready");
		expect(postBody(api)).toMatchObject({
			role: "owner",
			email: "o@example.com",
		});
	});

	test.each([
		[
			"not_configured",
			undefined,
			/No email was sent: this team has no email channel and the instance has no SMTP relay/,
		],
		[
			"not_configured",
			"smtp host unset",
			/No email was sent: smtp host unset\. Share the link below\./,
		],
		[
			"failed",
			"mailbox full",
			/The email could not be sent: mailbox full\. Share the link below instead\./,
		],
	] as const)(
		"reports a %s delivery (%s)",
		async (delivery, emailError, text) => {
			stubApi(
				routes({
					status: 201,
					body: created({
						delivery,
						...(emailError ? { emailError } : {}),
						invite: { ...created().invite, email: "x@example.com" },
					}),
				}),
			);
			const user = userEvent.setup();
			await renderWithProviders(<Harness />);
			await user.type(
				await screen.findByLabelText("Email address"),
				"x@example.com",
			);
			await user.click(screen.getByRole("button", { name: "Send invite" }));
			expect(await screen.findByText(text)).toBeInTheDocument();
		},
	);

	test("shows the server's reason and keeps the form on failure", async () => {
		stubApi(
			routes({
				status: 409,
				body: {
					status: 409,
					code: "already_member",
					title: "Conflict",
					detail: "x@example.com is already a member",
				},
			}),
		);
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await user.type(
			await screen.findByLabelText("Email address"),
			"x@example.com",
		);
		await user.click(screen.getByRole("button", { name: "Send invite" }));

		expect(
			await screen.findByText("x@example.com is already a member"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Email address")).toHaveValue("x@example.com");
	});

	test("with no projects the restriction list says so", async () => {
		stubApi({ "GET /projects": { body: { items: [] } } });
		await renderWithProviders(<Harness />);
		expect(await screen.findByText("No projects yet.")).toBeInTheDocument();
		expect(
			screen.getByText("No project selected: they can see every project."),
		).toBeInTheDocument();
	});

	test("Done closes, and reopening gives a fresh form", async () => {
		stubApi(routes({ status: 201, body: created() }));
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);

		await user.click(
			await screen.findByRole("button", { name: "Share a link" }),
		);
		await user.click(screen.getByRole("button", { name: "Create link" }));
		await user.click(await screen.findByRole("button", { name: "Done" }));
		expect(screen.getByTestId("state")).toHaveTextContent("closed");

		await user.click(screen.getByRole("button", { name: "reopen" }));
		expect(await screen.findByLabelText("Email address")).toHaveValue("");
		expect(screen.queryByTestId("invite-link")).not.toBeInTheDocument();
	});

	test("Cancel closes without a request", async () => {
		const api = stubApi({ "GET /projects": { body: PROJECTS } });
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(screen.getByTestId("state")).toHaveTextContent("closed");
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});
});
