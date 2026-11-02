import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Team, TeamInvite, TeamMember } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { MembersSection } from "./members-section";

function team(over: Partial<Team> = {}): Team {
	return {
		id: "t-1",
		name: "Acme",
		slug: "acme",
		role: "owner",
		current: true,
		memberCount: 4,
		createdAt: "2026-01-01T00:00:00Z",
		...over,
	};
}

function member(over: Partial<TeamMember> = {}): TeamMember {
	return {
		userId: "u-x",
		name: "Someone",
		email: "someone@example.com",
		role: "developer",
		joinedAt: "2026-01-01T00:00:00Z",
		you: false,
		...over,
	};
}

const OWNER_ME = member({
	userId: "u-1",
	name: "Olive Owner",
	email: "olive@example.com",
	role: "owner",
	you: true,
});
const ADMIN = member({
	userId: "u-2",
	name: "Adam Admin",
	email: "adam@example.com",
	role: "admin",
});
const DEV = member({
	userId: "u-3",
	name: "Dana Dev",
	email: "dana@example.com",
	role: "developer",
	projectIds: ["p-1", "p-2"],
});
const VIEWER = member({
	userId: "u-4",
	name: "Vic Viewer",
	email: "vic@example.com",
	role: "viewer",
	projectIds: [],
});
const OTHER_OWNER = member({
	userId: "u-5",
	name: "Otto Owner",
	email: "otto@example.com",
	role: "owner",
});

function invite(over: Partial<TeamInvite> = {}): TeamInvite {
	return {
		id: "i-1",
		prefix: "abcd",
		role: "developer",
		maxUses: 1,
		useCount: 0,
		emailSent: false,
		exhausted: false,
		expired: false,
		createdAt: "2026-01-01T00:00:00Z",
		expiresAt: "2099-01-01T00:00:00Z",
		...over,
	};
}

function routes(opts: {
	role?: Team["role"];
	members?: TeamMember[];
	invites?: TeamInvite[];
}) {
	return {
		"GET /teams": { body: { items: [team({ role: opts.role ?? "owner" })] } },
		"GET /teams/:teamId/members": {
			body: { items: opts.members ?? [OWNER_ME, ADMIN, DEV, VIEWER] },
		},
		"GET /teams/:teamId/invites": { body: { items: opts.invites ?? [] } },
	};
}

const rowOf = (name: string) => {
	const row = screen
		.getByText(name, { selector: "span.truncate" })
		.closest("tr");
	if (!row) throw new Error(`no row for ${name}`);
	return within(row);
};

describe("MembersSection", () => {
	test("an owner sees every member with role, project access and the invite controls", async () => {
		stubApi(routes({}));
		await renderWithProviders(<MembersSection />);

		expect(await screen.findByText("4 members")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Invite people" }),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Owners and admins manage members and invites."),
		).not.toBeInTheDocument();

		const me = rowOf("Olive Owner");
		expect(me.getByText("you")).toBeInTheDocument();
		expect(me.getByText("olive@example.com")).toBeInTheDocument();
		// Owners and admins always see all projects, and you cannot remove yourself.
		expect(me.getByText("All projects")).toBeInTheDocument();
		expect(
			me.queryByRole("button", { name: /Remove/ }),
		).not.toBeInTheDocument();
		expect(me.queryByRole("button", { name: /Reset/ })).not.toBeInTheDocument();

		expect(
			rowOf("Dana Dev").getByRole("button", {
				name: "Project access of Dana Dev: 2 projects",
			}),
		).toBeInTheDocument();
		expect(
			rowOf("Vic Viewer").getByRole("button", {
				name: "Project access of Vic Viewer: No projects",
			}),
		).toBeInTheDocument();
		expect(rowOf("Adam Admin").getByText("All projects")).toBeInTheDocument();
		expect(
			rowOf("Adam Admin").getByRole("combobox", { name: "Role of Adam Admin" }),
		).toBeInTheDocument();
	});

	test("one member reads in the singular", async () => {
		stubApi(routes({ members: [OWNER_ME] }));
		await renderWithProviders(<MembersSection />);
		expect(await screen.findByText("1 member")).toBeInTheDocument();
	});

	test("a developer sees the roster read-only and never asks for invites", async () => {
		const api = stubApi(
			routes({ role: "developer", members: [OWNER_ME, DEV] }),
		);
		await renderWithProviders(<MembersSection />);

		expect(await screen.findByText("2 members")).toBeInTheDocument();
		expect(
			screen.getByText("Owners and admins manage members and invites."),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Invite people" }),
		).not.toBeInTheDocument();
		expect(screen.queryByText("Pending invites")).not.toBeInTheDocument();
		expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Remove" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Reset the password/ }),
		).not.toBeInTheDocument();
		expect(rowOf("Olive Owner").getByText("Owner")).toBeInTheDocument();
		expect(api.calls.some((c) => c.path.endsWith("/invites"))).toBe(false);
	});

	test("an admin manages others but not owners, and cannot hand out owner", async () => {
		stubApi(
			routes({ role: "admin", members: [OWNER_ME, ADMIN, OTHER_OWNER, DEV] }),
		);
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);

		await screen.findByText("4 members");
		const otto = rowOf("Otto Owner");
		expect(otto.queryByRole("combobox")).not.toBeInTheDocument();
		expect(otto.getByText("Owner")).toBeInTheDocument();
		expect(
			otto.queryByRole("button", { name: "Remove" }),
		).not.toBeInTheDocument();
		expect(
			otto.queryByRole("button", { name: /Reset the password/ }),
		).not.toBeInTheDocument();

		expect(
			rowOf("Dana Dev").getByRole("button", { name: "Remove" }),
		).toBeInTheDocument();
		await user.click(
			rowOf("Dana Dev").getByRole("combobox", { name: "Role of Dana Dev" }),
		);
		const options = (await screen.findAllByRole("option")).map((o) =>
			(o.textContent ?? "").slice(0, 5),
		);
		expect(options).toEqual(["Admin", "Devel", "Viewe"]);
	});

	test("changing a role PATCHes it, confirms and reloads the roster", async () => {
		let members = [OWNER_ME, DEV];
		const api = stubApi({
			...routes({}),
			"GET /teams/:teamId/members": () => Response.json({ items: members }),
			"PATCH /teams/:teamId/members/:userId": () => {
				members = [OWNER_ME, { ...DEV, role: "admin" }];
				return Response.json({});
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);

		await screen.findByText("2 members");
		await user.click(
			rowOf("Dana Dev").getByRole("combobox", { name: "Role of Dana Dev" }),
		);
		await user.click(await screen.findByRole("option", { name: /^Admin/ }));

		expect(
			await screen.findByText("Dana Dev is now Admin"),
		).toBeInTheDocument();
		const patch = api.calls.find((c) => c.method === "PATCH");
		expect(patch).toMatchObject({
			path: "/teams/t-1/members/u-3",
			body: { role: "admin" },
		});
		// The reloaded roster shows the new role: admins have no project button.
		await waitFor(() =>
			expect(
				screen.queryByRole("button", { name: /Project access of Dana Dev/ }),
			).not.toBeInTheDocument(),
		);
	});

	test("a refused role change toasts the reason", async () => {
		stubApi({
			...routes({ members: [OWNER_ME, DEV] }),
			"PATCH /teams/:teamId/members/:userId": {
				status: 409,
				body: {
					status: 409,
					code: "last_owner",
					title: "Conflict",
					detail: "a team needs an owner",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);
		await screen.findByText("2 members");
		await user.click(
			rowOf("Dana Dev").getByRole("combobox", { name: "Role of Dana Dev" }),
		);
		await user.click(await screen.findByRole("option", { name: /^Viewer/ }));

		expect(
			await screen.findByText("Could not change the role"),
		).toBeInTheDocument();
		expect(screen.getByText("a team needs an owner")).toBeInTheDocument();
	});

	test("choosing the member's current role sends nothing", async () => {
		const api = stubApi(routes({ members: [OWNER_ME, DEV] }));
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);
		await screen.findByText("2 members");
		await user.click(
			rowOf("Dana Dev").getByRole("combobox", { name: "Role of Dana Dev" }),
		);
		await user.click(await screen.findByRole("option", { name: /^Developer/ }));
		expect(api.calls.filter((c) => c.method === "PATCH")).toEqual([]);
	});

	test("removing a member asks first, then DELETEs and says their tokens stopped", async () => {
		let members = [OWNER_ME, DEV];
		const api = stubApi({
			...routes({}),
			"GET /teams/:teamId/members": () => Response.json({ items: members }),
			"DELETE /teams/:teamId/members/:userId": () => {
				members = [OWNER_ME];
				return new Response(null, { status: 204 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);
		await screen.findByText("2 members");

		await user.click(rowOf("Dana Dev").getByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Remove Dana Dev?")).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);

		await user.click(
			within(dialog).getByRole("button", { name: "Remove member" }),
		);

		expect(await screen.findByText("Removed Dana Dev")).toBeInTheDocument();
		expect(
			screen.getByText("Their API tokens in this team stopped working."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/teams/t-1/members/u-3",
		);
		expect(await screen.findByText("1 member")).toBeInTheDocument();
	});

	test("cancelling the removal sends nothing", async () => {
		const api = stubApi(routes({ members: [OWNER_ME, DEV] }));
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);
		await screen.findByText("2 members");
		await user.click(rowOf("Dana Dev").getByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
	});

	test("a refused removal keeps the dialog open with the reason", async () => {
		stubApi({
			...routes({ members: [OWNER_ME, DEV] }),
			"DELETE /teams/:teamId/members/:userId": {
				status: 409,
				body: {
					status: 409,
					code: "conflict",
					title: "Conflict",
					detail: "member owns a running deployment",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);
		await screen.findByText("2 members");
		await user.click(rowOf("Dana Dev").getByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Remove member" }),
		);
		expect(
			await within(dialog).findByText("member owns a running deployment"),
		).toBeInTheDocument();
	});

	test("Project access opens the dialog for that member", async () => {
		stubApi({ ...routes({}), "GET /projects": { body: { items: [] } } });
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);
		await screen.findByText("4 members");
		await user.click(
			rowOf("Dana Dev").getByRole("button", {
				name: /Project access of Dana Dev/,
			}),
		);
		expect(
			await screen.findByText("Project access for Dana Dev"),
		).toBeInTheDocument();
	});

	test("Reset password opens the reset dialog for that member", async () => {
		stubApi(routes({}));
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);
		await screen.findByText("4 members");
		await user.click(
			rowOf("Adam Admin").getByRole("button", {
				name: "Reset the password of Adam Admin",
			}),
		);
		expect(
			await screen.findByText("Reset password for Adam Admin"),
		).toBeInTheDocument();
	});

	test("Invite people opens the invite dialog", async () => {
		stubApi({ ...routes({}), "GET /projects": { body: { items: [] } } });
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);
		await user.click(
			await screen.findByRole("button", { name: "Invite people" }),
		);
		expect(
			await screen.findByRole("heading", { name: "Invite people" }),
		).toBeInTheDocument();
	});

	test("an empty roster invites people", async () => {
		stubApi(routes({ members: [] }));
		await renderWithProviders(<MembersSection />);
		expect(await screen.findByText("Nobody here yet")).toBeInTheDocument();
		expect(screen.getByText("0 members")).toBeInTheDocument();
	});

	test("lists pending invites with use count, expiry state and delivery", async () => {
		stubApi(
			routes({
				invites: [
					invite({
						id: "i-1",
						prefix: "aaaa",
						email: "new@example.com",
						emailSent: true,
					}),
					invite({
						id: "i-2",
						prefix: "bbbb",
						role: "viewer",
						maxUses: 5,
						useCount: 2,
						expired: true,
						projectIds: ["p-1"],
					}),
					invite({ id: "i-3", prefix: "cccc", email: "x@example.com" }),
				],
			}),
		);
		await renderWithProviders(<MembersSection />);

		const pending = within(
			await screen.findByRole("group", { name: "Pending invites" }),
		);
		await pending.findByText("new@example.com");
		const mailed = pending.getByText("new@example.com").closest("tr");
		expect(within(mailed as HTMLElement).getByText("sent")).toBeInTheDocument();
		expect(
			within(mailed as HTMLElement).getByText("0 / 1"),
		).toBeInTheDocument();
		expect(
			within(mailed as HTMLElement).getByText("nci_aaaa…"),
		).toBeInTheDocument();

		const link = pending.getByText("Link invite").closest("tr") as HTMLElement;
		expect(within(link).getByText("expired")).toBeInTheDocument();
		expect(within(link).getByText("2 / 5")).toBeInTheDocument();
		expect(within(link).getByText(/Viewer/)).toHaveTextContent("· 1 project");
		expect(within(link).getByText("–")).toBeInTheDocument();

		const unsent = pending
			.getByText("x@example.com")
			.closest("tr") as HTMLElement;
		expect(within(unsent).getByText("not sent")).toBeInTheDocument();
	});

	test("with no invites it explains how they work", async () => {
		stubApi(routes({}));
		await renderWithProviders(<MembersSection />);
		expect(
			await screen.findByText(
				/No pending invites\. A link works until it expires/,
			),
		).toBeInTheDocument();
	});

	test("revoking an invite DELETEs it and confirms", async () => {
		let invites = [
			invite({ id: "i-9", prefix: "zzzz", email: "r@example.com" }),
		];
		const api = stubApi({
			...routes({}),
			"GET /teams/:teamId/invites": () => Response.json({ items: invites }),
			"DELETE /teams/:teamId/invites/:inviteId": () => {
				invites = [];
				return new Response(null, { status: 204 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);
		await user.click(await screen.findByRole("button", { name: "Revoke" }));

		expect(await screen.findByText("Invite revoked")).toBeInTheDocument();
		expect(screen.getByText("Its link no longer works.")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/teams/t-1/invites/i-9",
		);
		expect(await screen.findByText(/No pending invites/)).toBeInTheDocument();
	});

	test("a refused revoke toasts the reason", async () => {
		stubApi({
			...routes({ invites: [invite()] }),
			"DELETE /teams/:teamId/invites/:inviteId": {
				status: 404,
				body: {
					status: 404,
					code: "not_found",
					title: "Not found",
					detail: "invite already used",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);
		await user.click(await screen.findByRole("button", { name: "Revoke" }));
		expect(
			await screen.findByText("Could not revoke the invite"),
		).toBeInTheDocument();
		expect(screen.getByText("invite already used")).toBeInTheDocument();
	});

	test("a roster that fails to load offers a retry", async () => {
		let attempts = 0;
		stubApi({
			...routes({}),
			"GET /teams/:teamId/members": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, code: "internal", title: "Boom" },
							{ status: 500 },
						)
					: Response.json({ items: [OWNER_ME] });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<MembersSection />);

		expect(await screen.findByText("Members did not load")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /retry|try again/i }));
		expect(await screen.findByText("1 member")).toBeInTheDocument();
	});
});
