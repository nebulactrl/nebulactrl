import { describe, expect, test } from "bun:test";
import type { RegisteredRouter } from "@tanstack/react-router";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { InstanceSettings, Team } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { getTeamScope, resetTeamScope } from "./team-scope";
import { TeamSwitcher } from "./team-switcher";

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

function instance(over: Overrides<InstanceSettings> = {}): InstanceSettings {
	return make("InstanceSettings", {
		allowTeamCreation: false,
		canManage: false,
		smtpConfigured: false,
		...over,
	});
}

function project(id: string) {
	return make("Project", {
		id,
		name: id,
		slug: id,
		description: "",
		system: false,
		createdAt: "2026-01-01T00:00:00Z",
		updatedAt: "2026-01-01T00:00:00Z",
	});
}

function routes(opts: {
	teams?: Team[];
	instance?: InstanceSettings;
	projects?: number;
	servers?: number;
}) {
	return {
		"GET /teams": {
			body: make("ListTeamsOutputBody", { items: opts.teams ?? [team()] }),
		},
		"GET /instance": { body: opts.instance ?? instance() },
		"GET /projects": {
			body: make("PageProject", {
				items: Array.from({ length: opts.projects ?? 2 }, (_, i) =>
					project(`p-${i}`),
				),
			}),
		},
		"GET /servers": {
			body: make("PageServer", {
				items: Array.from({ length: opts.servers ?? 1 }, (_, i) =>
					make("Server", { id: `s-${i}`, name: `srv-${i}` }),
				),
			}),
		},
	};
}

const trigger = (name: string | RegExp) =>
	screen.findByRole("button", { name });

describe("TeamSwitcher", () => {
	test("the header names the team with role, project and server counts", async () => {
		stubApi(routes({ projects: 4, servers: 2 }));
		await renderWithProviders(<TeamSwitcher collapsed={false} />);

		const button = await trigger("Team: Acme. Switch team");
		await waitFor(() =>
			expect(button).toHaveTextContent("Owner · 4 projects · 2 servers"),
		);
		expect(button).toHaveTextContent("Acme");
	});

	test("a team without servers leaves them out of the caption", async () => {
		stubApi(routes({ projects: 1, servers: 0 }));
		await renderWithProviders(<TeamSwitcher collapsed={false} />);
		const button = await trigger("Team: Acme. Switch team");
		await waitFor(() => expect(button).toHaveTextContent("Owner · 1 project"));
		expect(button).not.toHaveTextContent("server");
	});

	test("collapsed it keeps only the avatar, with the name as its title", async () => {
		stubApi(routes({}));
		await renderWithProviders(<TeamSwitcher collapsed />);
		const button = await trigger("Team: Acme. Switch team");
		await waitFor(() => expect(button).toHaveAttribute("title", "Acme"));
		expect(button).not.toHaveTextContent("Owner");
		expect(button).toHaveTextContent("A");
	});

	test("before the teams load the header shows a placeholder name", async () => {
		stubApi({
			"GET /teams": () => new Promise<Response>(() => undefined),
			"GET /instance": { body: instance() },
			"GET /projects": { body: { items: [] } },
			"GET /servers": { body: make("PageServer", { items: [] }) },
		});
		await renderWithProviders(<TeamSwitcher collapsed={false} />);
		const button = await trigger("Team: Team. Switch team");
		expect(button).toHaveTextContent("…");
	});

	test("lists every team with role and member count, marking the current one", async () => {
		stubApi(
			routes({
				teams: [
					team(),
					team({
						id: "t-2",
						name: "Beta",
						current: false,
						role: "admin",
						memberCount: 1,
					}),
					team({
						id: "t-3",
						name: "Gone",
						current: false,
						deletion: {
							state: "deleting",
							forgottenServers: null,
							keepDnsRecords: false,
						},
					}),
					team({
						id: "t-4",
						name: "Stuck",
						current: false,
						deletion: {
							state: "failed",
							forgottenServers: null,
							keepDnsRecords: false,
						},
					}),
				],
			}),
		);
		const user = userEvent.setup();
		await renderWithProviders(<TeamSwitcher collapsed={false} />);
		await user.click(await trigger("Team: Acme. Switch team"));

		const acme = await screen.findByRole("menuitem", { name: /Acme/ });
		expect(acme).toHaveAttribute("aria-current", "true");
		expect(acme).toHaveTextContent("Owner · 3 members");
		const beta = screen.getByRole("menuitem", { name: /Beta/ });
		expect(beta).not.toHaveAttribute("aria-current");
		expect(beta).toHaveTextContent("Admin · 1 member");
		expect(beta).not.toHaveTextContent("1 members");
		expect(screen.getByRole("menuitem", { name: /Gone/ })).toHaveTextContent(
			"Deleting…",
		);
		expect(screen.getByRole("menuitem", { name: /Stuck/ })).toHaveTextContent(
			"Deletion failed",
		);
	});

	test("choosing another team switches to it, reloads and confirms", async () => {
		const api = stubApi({
			...routes({
				teams: [
					team(),
					team({ id: "t-2", name: "Beta", current: false, role: "viewer" }),
				],
			}),
			"POST /teams/:teamId/switch": {
				body: team({ id: "t-2", name: "Beta", role: "viewer" }),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<TeamSwitcher collapsed={false} />);
		await user.click(await trigger("Team: Acme. Switch team"));
		await user.click(await screen.findByRole("menuitem", { name: /Beta/ }));

		expect(await screen.findByText("Switched to Beta")).toBeInTheDocument();
		const switches = api.calls.filter((c) => c.method === "POST");
		expect(switches.map((c) => c.path)).toEqual(["/teams/t-2/switch"]);
		// The lists of the new team are fetched after the cache was emptied.
		const teamLists = api.calls.filter((c) => c.path === "/teams");
		expect(teamLists.length).toBeGreaterThanOrEqual(2);
		expect(api.unmatched).toEqual([]);
	});

	test("choosing the current team does nothing", async () => {
		const api = stubApi({
			...routes({}),
			"POST /teams/:teamId/switch": { body: team() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<TeamSwitcher collapsed={false} />);
		await user.click(await trigger("Team: Acme. Switch team"));
		await user.click(await screen.findByRole("menuitem", { name: /Acme/ }));

		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});

	test("a refused switch toasts the reason and stays on the team", async () => {
		stubApi({
			...routes({
				teams: [team(), team({ id: "t-2", name: "Beta", current: false })],
			}),
			"POST /teams/:teamId/switch": problem(403, {
				code: "two_factor_required",
				title: "Forbidden",
				detail: "Beta requires two-step verification",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<TeamSwitcher collapsed={false} />);
		await user.click(await trigger("Team: Acme. Switch team"));
		await user.click(await screen.findByRole("menuitem", { name: /Beta/ }));

		expect(
			await screen.findByText("Could not switch team"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Beta requires two-step verification"),
		).toBeInTheDocument();
		expect(screen.queryByText("Switched to Beta")).not.toBeInTheDocument();
	});

	test("Create team is offered when anyone may create teams and opens the dialog", async () => {
		stubApi(routes({ instance: instance({ allowTeamCreation: true }) }));
		const user = userEvent.setup();
		await renderWithProviders(<TeamSwitcher collapsed={false} />);
		await user.click(await trigger("Team: Acme. Switch team"));
		await user.click(
			await screen.findByRole("menuitem", { name: "Create team" }),
		);

		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Create a team")).toBeInTheDocument();
	});

	test("the instance admin can create teams even when creation is closed", async () => {
		stubApi(
			routes({
				instance: instance({ allowTeamCreation: false, canManage: true }),
			}),
		);
		const user = userEvent.setup();
		await renderWithProviders(<TeamSwitcher collapsed={false} />);
		await user.click(await trigger("Team: Acme. Switch team"));
		expect(
			await screen.findByRole("menuitem", { name: "Create team" }),
		).toBeInTheDocument();
	});

	test("Create team is hidden when creation is closed to this user", async () => {
		const api = stubApi(routes({ instance: instance() }));
		const user = userEvent.setup();
		await renderWithProviders(<TeamSwitcher collapsed={false} />);
		await user.click(await trigger("Team: Acme. Switch team"));
		await screen.findByRole("menuitem", { name: /Acme/ });
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/instance")).toBe(true),
		);
		expect(
			screen.queryByRole("menuitem", { name: "Create team" }),
		).not.toBeInTheDocument();
	});

	test("while a team change is under way the header hides the previous team", async () => {
		stubApi(routes({}));
		const { queryClient } = await renderWithProviders(
			<TeamSwitcher collapsed={false} />,
		);
		const button = await trigger("Team: Acme. Switch team");
		await waitFor(() => expect(button).toHaveTextContent("Owner"));

		let release: () => void = () => undefined;
		const warm = new Promise<void>((resolve) => {
			release = resolve;
		});
		const router = {
			invalidate: async () => undefined,
			navigate: async () => undefined,
		} as unknown as RegisteredRouter;
		let done: Promise<void> | undefined;
		act(() => {
			done = resetTeamScope(queryClient, router, () => warm);
		});
		await waitFor(() => expect(button).toHaveTextContent("…"));
		expect(button).not.toHaveTextContent("Owner");

		await act(async () => {
			release();
			await done;
		});
		expect(getTeamScope().switching).toBe(false);
	});
});
