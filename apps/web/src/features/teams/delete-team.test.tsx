import { describe, expect, jest, test } from "bun:test";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { listTeamsQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import type { Team, TeamDeletion, TeamDeletionPlan } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { DeleteTeamDangerZone } from "./delete-team";

function team(deletion?: TeamDeletion): Team {
	return make("Team", {
		id: "t-1",
		name: "Acme",
		slug: "acme",
		role: "owner",
		current: true,
		memberCount: 3,
		createdAt: "2026-01-01T00:00:00Z",
		deletion,
	});
}

/** The team as `DELETE /teams/:teamId` answers: the job is queued and the servers come first. */
const deleting = () =>
	team({
		state: "deleting",
		step: "servers",
		forgottenServers: [],
		keepDnsRecords: false,
	});

function plan(over: Overrides<TeamDeletionPlan> = {}): TeamDeletionPlan {
	return make("TeamDeletionPlan", {
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
		...over,
	});
}

/** Holds the confirm button past its 1.3s hold without waiting for it. */
function holdToConfirm(button: HTMLElement) {
	jest.useFakeTimers();
	try {
		fireEvent.keyDown(button, { key: " " });
		act(() => {
			jest.advanceTimersByTime(1400);
		});
	} finally {
		jest.useRealTimers();
	}
}

async function openConfirm(user: ReturnType<typeof userEvent.setup>) {
	const open = await screen.findByRole("button", { name: "Delete team" });
	await screen.findByText(/^Deletes Acme/);
	await user.click(open);
	return screen.findByRole("alertdialog");
}

describe("DeleteTeamDangerZone", () => {
	test("lists only the non-zero parts of the plan, singular and plural", async () => {
		const api = stubApi({
			"GET /teams/:teamId/deletion-plan": {
				body: plan({
					projects: 1,
					services: 4,
					volumes: 2,
					servers: { online: 1, offline: 2 },
					statusPages: 1,
				}),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<DeleteTeamDangerZone team={team()} />);
		await waitForPlan();
		const dialog = await openConfirm(user);

		expect(within(dialog).getByText("Delete Acme?")).toBeInTheDocument();
		const items = within(dialog)
			.getAllByRole("listitem")
			.map((li) => li.textContent);
		expect(items).toEqual([
			"1 project",
			"4 services",
			"2 volumes with their data",
			"1 online server: the agent empties the host and removes itself",
			"2 offline servers: removed without touching the host, which keeps its containers",
			"1 status page",
			"every token, integration, secret and the audit log",
		]);
		// No DNS records: no keep-DNS choice.
		expect(within(dialog).queryByRole("checkbox")).not.toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toEqual(["/teams/t-1/deletion-plan"]);
	});

	test("an empty team gets the plain sentence instead of a list", async () => {
		stubApi({ "GET /teams/:teamId/deletion-plan": { body: plan() } });
		const user = userEvent.setup();
		await renderWithProviders(<DeleteTeamDangerZone team={team()} />);
		await waitForPlan();
		const dialog = await openConfirm(user);
		expect(
			within(dialog).getByText(
				/This deletes the team for every member and cannot be undone: every token/,
			),
		).toBeInTheDocument();
		expect(within(dialog).queryByRole("list")).not.toBeInTheDocument();
	});

	test("a team hosting the control plane cannot be deleted", async () => {
		const api = stubApi({
			"GET /teams/:teamId/deletion-plan": {
				body: plan({ hostsControlPlane: true, domains: 2 }),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<DeleteTeamDangerZone team={team()} />);
		await waitForPlan();
		const dialog = await openConfirm(user);

		expect(
			within(dialog).getByText(/so it cannot be deleted\./),
		).toBeInTheDocument();
		expect(within(dialog).getByText("2 domains")).toBeInTheDocument();
		await user.type(within(dialog).getByLabelText(/to confirm/), "acme");
		const hold = within(dialog).getByRole("button", {
			name: /Hold to delete team/,
		});
		expect(hold).toBeDisabled();
		holdToConfirm(hold);
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
	});

	test("typing the slug and holding deletes with confirm=slug and marks the team list stale", async () => {
		const api = stubApi({
			"GET /teams/:teamId/deletion-plan": { body: plan({ projects: 2 }) },
			"DELETE /teams/:teamId": { status: 202, body: deleting() },
		});
		const queryClient = testQueryClient();
		queryClient.setQueryData(listTeamsQueryKey(), { items: [team()] });
		const user = userEvent.setup();
		await renderWithProviders(<DeleteTeamDangerZone team={team()} />, {
			queryClient,
		});
		await waitForPlan();
		const dialog = await openConfirm(user);

		const hold = within(dialog).getByRole("button", {
			name: /Hold to delete team/,
		});
		// A hold without the slug is refused.
		holdToConfirm(hold);
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);

		await user.type(within(dialog).getByLabelText(/to confirm/), "acme");
		holdToConfirm(hold);

		await waitForDeleteCall(api);
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.path).toBe("/teams/t-1");
		expect(del?.query.get("confirm")).toBe("acme");
		expect(del?.query.get("keepDnsRecords")).toBe("false");
		// The team list is invalidated so the page picks up "Deleting…".
		await waitFor(() =>
			expect(
				queryClient.getQueryState(listTeamsQueryKey())?.isInvalidated,
			).toBe(true),
		);
		expect(api.unmatched).toEqual([]);
	});

	test("ticking 'Keep the DNS records' sends keepDnsRecords=true", async () => {
		const api = stubApi({
			"GET /teams/:teamId/deletion-plan": {
				body: plan({ managedDnsRecords: 3 }),
			},
			"DELETE /teams/:teamId": { status: 202, body: deleting() },
			"GET /teams": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<DeleteTeamDangerZone team={team()} />);
		await waitForPlan();
		const dialog = await openConfirm(user);

		expect(
			within(dialog).getByText("3 DNS records managed in Cloudflare"),
		).toBeInTheDocument();
		await user.click(
			within(dialog).getByRole("checkbox", {
				name: "Keep the DNS records in Cloudflare",
			}),
		);
		await user.type(within(dialog).getByLabelText(/to confirm/), "acme");
		holdToConfirm(
			within(dialog).getByRole("button", { name: /Hold to delete team/ }),
		);

		await waitForDeleteCall(api);
		expect(
			api.calls.find((c) => c.method === "DELETE")?.query.get("keepDnsRecords"),
		).toBe("true");
	});

	test("a refused delete keeps the dialog open with the server's reason", async () => {
		stubApi({
			"GET /teams/:teamId/deletion-plan": { body: plan() },
			"DELETE /teams/:teamId": problem(409, {
				code: "team_hosts_control_plane",
				title: "Conflict",
				detail: "this team hosts NebulaCtrl itself",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<DeleteTeamDangerZone team={team()} />);
		await waitForPlan();
		const dialog = await openConfirm(user);
		await user.type(within(dialog).getByLabelText(/to confirm/), "acme");
		holdToConfirm(
			within(dialog).getByRole("button", { name: /Hold to delete team/ }),
		);

		expect(
			await within(dialog).findByText("this team hosts NebulaCtrl itself"),
		).toBeInTheDocument();
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
	});

	test("while the job runs it shows its step, hides the button and skips the plan", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<DeleteTeamDangerZone
				team={team({
					state: "deleting",
					step: "servers",
					forgottenServers: ["edge-1", "edge-2"],
					keepDnsRecords: false,
				})}
			/>,
		);

		expect(await screen.findByText(/Deleting…/)).toBeInTheDocument();
		const progress = screen.getByText(/Emptying and removing servers\./);
		expect(progress).toHaveAttribute("role", "status");
		expect(progress).toHaveTextContent(
			"Emptying and removing servers. Agents can take a few minutes to empty their hosts. You can leave this page.",
		);
		expect(
			screen.queryByRole("button", { name: "Delete team" }),
		).not.toBeInTheDocument();
		expect(
			screen.getByText(/clean them up yourself\): edge-1, edge-2\./),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("a failed job shows why and offers Retry deletion", async () => {
		stubApi({ "GET /teams/:teamId/deletion-plan": { body: plan() } });
		const user = userEvent.setup();
		await renderWithProviders(
			<DeleteTeamDangerZone
				team={team({
					state: "failed",
					error: "agent unreachable",
					forgottenServers: [],
					keepDnsRecords: false,
				})}
			/>,
		);

		expect(
			await screen.findByText(
				"The deletion stopped: agent unreachable. Nothing was undone; delete again to retry.",
			),
		).toBeInTheDocument();
		expect(
			screen.queryByText(/clean them up yourself/),
		).not.toBeInTheDocument();
		await user.click(
			await screen.findByRole("button", { name: "Retry deletion" }),
		);
		expect(await screen.findByText("Delete Acme?")).toBeInTheDocument();
	});

	test("the button waits for the plan: it is disabled while the plan loads", async () => {
		let release: (() => void) | undefined;
		stubApi({
			"GET /teams/:teamId/deletion-plan": () =>
				new Promise<Response>((resolve) => {
					release = () => resolve(Response.json(plan()));
				}),
		});
		await renderWithProviders(<DeleteTeamDangerZone team={team()} />);

		expect(screen.getByRole("button", { name: "Delete team" })).toBeDisabled();
		await act(async () => release?.());
		await waitForPlan();
	});
});

async function waitForPlan() {
	await screen.findByRole("button", { name: "Delete team" });
	await act(async () => {
		await Promise.resolve();
	});
	await waitFor(() =>
		expect(screen.getByRole("button", { name: "Delete team" })).toBeEnabled(),
	);
}

async function waitForDeleteCall(api: ReturnType<typeof stubApi>) {
	await waitFor(() =>
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(true),
	);
}
