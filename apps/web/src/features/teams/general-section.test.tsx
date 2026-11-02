import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Team } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { GeneralSection } from "./general-section";

function team(over: Overrides<Team> = {}): Team {
	return make("Team", {
		id: "t-1",
		name: "Acme",
		slug: "acme",
		role: "owner",
		current: true,
		memberCount: 3,
		createdAt: "2026-01-01T00:00:00Z",
		...over,
	});
}

const ME = make("MeBody", {
	id: "u-1",
	email: "me@example.com",
	name: "Me",
	createdAt: "2026-01-01T00:00:00Z",
	instanceAdmin: false,
	workspaceId: "w-1",
});

const MY_SECURITY = make("SecurityBody", {
	totpEnabled: true,
	totpPending: false,
	passkeys: [],
	sessionMfa: false,
	hasPassword: true,
	identities: [],
	isInstanceAdmin: false,
	enrollmentNeeded: false,
	recoveryCodesLeft: 8,
	requiredByTeams: [],
});

const PLAN = make("TeamDeletionPlan", {
	backupDestinationsKept: true,
	databases: 0,
	domains: 0,
	hostsControlPlane: false,
	managedDnsRecords: 0,
	projects: 0,
	servers: { offline: 0, online: 0 },
	services: 0,
	statusPages: 0,
	tailnetDevices: 0,
	team: { id: "t-1", name: "Acme", slug: "acme" },
	volumes: 0,
});

function baseRoutes(current: Team | null) {
	return {
		"GET /teams": {
			body: make("ListTeamsOutputBody", { items: current ? [current] : [] }),
		},
		"GET /me": { body: ME },
		"GET /teams/:teamId/security": {
			body: make("TeamSecurityBody", {
				require2fa: false,
				enrolled: 1,
				members: 3,
			}),
		},
		"GET /me/security": { body: MY_SECURITY },
		"GET /teams/:teamId/deletion-plan": { body: PLAN },
	};
}

describe("GeneralSection", () => {
	test("owner sees the editable name, the sign-in rule and both danger zones", async () => {
		stubApi(baseRoutes(team()));
		await renderWithProviders(<GeneralSection />);

		expect(await screen.findByLabelText("Name")).toHaveValue("Acme");
		expect(screen.getByText("acme")).toBeInTheDocument();
		expect(screen.getByText("Owner")).toBeInTheDocument();
		expect(
			screen.queryByText("Owners and admins rename the team."),
		).not.toBeInTheDocument();
		expect(
			await screen.findByRole("group", {
				name: "Require two-step verification",
			}),
		).toBeInTheDocument();
		expect(
			screen.getByRole("group", { name: "Leave this team" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("group", { name: "Delete this team" }),
		).toBeInTheDocument();
	});

	test("admin can rename and set the rule but cannot delete the team", async () => {
		stubApi(baseRoutes(team({ role: "admin" })));
		await renderWithProviders(<GeneralSection />);

		expect(await screen.findByLabelText("Name")).toBeInTheDocument();
		expect(
			await screen.findByRole("group", {
				name: "Require two-step verification",
			}),
		).toBeInTheDocument();
		expect(
			screen.getByRole("group", { name: "Leave this team" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("group", { name: "Delete this team" }),
		).not.toBeInTheDocument();
	});

	test("a developer sees the name as text and a notice instead of the sign-in rule", async () => {
		const api = stubApi(baseRoutes(team({ role: "developer" })));
		await renderWithProviders(<GeneralSection />);

		expect(
			await screen.findByText("Owners and admins rename the team."),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
		expect(screen.getByText("Developer")).toBeInTheDocument();
		expect(
			screen.getByText("Owners and admins manage the team's sign-in rules."),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("group", { name: "Require two-step verification" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("group", { name: "Delete this team" }),
		).not.toBeInTheDocument();
		// No requests for data a developer must not manage.
		expect(api.calls.map((c) => c.path)).not.toContain("/teams/t-1/security");
	});

	test("shows a skeleton-free error state when the team did not load", async () => {
		stubApi({
			"GET /teams": {
				status: 500,
				body: { status: 500, code: "internal", title: "Boom" },
			},
		});
		await renderWithProviders(<GeneralSection />);
		expect(
			await screen.findByText("The team did not load"),
		).toBeInTheDocument();
	});

	test("shows the error state when no team is current", async () => {
		stubApi(baseRoutes(team({ current: false })));
		await renderWithProviders(<GeneralSection />);
		expect(
			await screen.findByText("The team did not load"),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
	});

	test("Save name is only enabled for a changed, non-blank name and PATCHes the trimmed name", async () => {
		const api = stubApi({
			...baseRoutes(team()),
			"PATCH /teams/:teamId": { body: team({ name: "Acme Labs" }) },
		});
		const user = userEvent.setup();
		await renderWithProviders(<GeneralSection />);

		const input = await screen.findByLabelText("Name");
		const save = screen.getByRole("button", { name: "Save name" });
		expect(save).toBeDisabled();

		await user.clear(input);
		expect(save).toBeDisabled();
		await user.type(input, "Acme");
		expect(save).toBeDisabled();
		await user.clear(input);
		await user.type(input, "  Acme Labs ");
		expect(save).toBeEnabled();
		await user.click(save);

		expect(await screen.findByText("Team renamed")).toBeInTheDocument();
		const patch = api.calls.find((c) => c.method === "PATCH");
		expect(patch).toMatchObject({
			path: "/teams/t-1",
			body: { name: "Acme Labs" },
		});
	});

	test("a refused rename toasts the reason", async () => {
		stubApi({
			...baseRoutes(team()),
			"PATCH /teams/:teamId": {
				status: 409,
				body: {
					status: 409,
					code: "name_taken",
					title: "Conflict",
					detail: "a team with that name exists",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<GeneralSection />);
		const input = await screen.findByLabelText("Name");
		await user.clear(input);
		await user.type(input, "Other");
		await user.click(screen.getByRole("button", { name: "Save name" }));

		expect(await screen.findByText("Could not rename")).toBeInTheDocument();
		expect(
			screen.getByText("a team with that name exists"),
		).toBeInTheDocument();
	});

	test("leaving removes you from the team, then reloads the team's state", async () => {
		const api = stubApi({
			...baseRoutes(team({ role: "developer" })),
			"DELETE /teams/:teamId/members/:userId": { status: 204 },
			"GET /projects": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<GeneralSection />);

		await screen.findByText("Owners and admins rename the team.");
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/me")).toBe(true),
		);
		await user.click(
			within(screen.getByRole("group", { name: "Leave this team" })).getByRole(
				"button",
				{ name: "Leave team" },
			),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Leave Acme?")).toBeInTheDocument();
		await user.click(
			within(dialog).getByRole("button", { name: "Leave team" }),
		);

		expect(await screen.findByText("You left the team")).toBeInTheDocument();
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.path).toBe("/teams/t-1/members/u-1");
		// The new team's lists are fetched after the cache is emptied.
		await waitFor(() => {
			expect(api.calls.filter((c) => c.path === "/projects")).toHaveLength(1);
		});
		expect(api.unmatched).toEqual([]);
	});

	test("a refused leave (last owner) shows the reason in the dialog and stays", async () => {
		const api = stubApi({
			...baseRoutes(team()),
			"DELETE /teams/:teamId/members/:userId": {
				status: 409,
				body: {
					status: 409,
					code: "last_owner",
					title: "Conflict",
					detail: "a team needs at least one owner",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<GeneralSection />);
		await screen.findByLabelText("Name");
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/me")).toBe(true),
		);
		await user.click(
			within(screen.getByRole("group", { name: "Leave this team" })).getByRole(
				"button",
				{ name: "Leave team" },
			),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Leave team" }),
		);

		expect(
			await within(dialog).findByText("a team needs at least one owner"),
		).toBeInTheDocument();
		expect(await screen.findByText("Could not leave")).toBeInTheDocument();
	});
});
