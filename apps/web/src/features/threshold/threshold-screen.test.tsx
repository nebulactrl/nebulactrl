import { describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import type { Team, UpdateView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { ThresholdGate } from "./threshold-screen";

const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

function update(over: Partial<UpdateView> = {}): UpdateView {
	return {
		available: false,
		canUpdate: true,
		checkEnabled: true,
		current: "v1.1.0",
		isAdmin: true,
		manualCommand: "",
		...over,
	};
}

function running(startedAt = ago(65_000)): UpdateView {
	return update({
		updating: {
			state: "running",
			fromVersion: "v1.1.0",
			targetVersion: "v1.2.0",
			startedAt,
		},
	});
}

function team(over: Partial<Team> = {}): Team {
	return {
		id: "t1",
		name: "Acme",
		slug: "acme",
		role: "owner",
		current: true,
		memberCount: 3,
		createdAt: "2026-01-01T00:00:00Z",
		...over,
	};
}

const deleting = (step?: "projects" | "servers" | "integrations" | "team") =>
	team({
		deletion: {
			state: "deleting",
			forgottenServers: null,
			keepDnsRecords: false,
			...(step ? { step } : {}),
		},
	});

/** Both reads have been answered and rendered, so an absent screen is a real absence. */
async function settled(api: ReturnType<typeof stubApi>) {
	await waitFor(() =>
		expect(api.calls.map((c) => c.path).sort()).toEqual([
			"/system/update",
			"/teams",
		]),
	);
	await act(async () => {});
}

const mount = (queryClient?: Parameters<typeof renderWithProviders>[1]) =>
	renderWithProviders(
		<ThresholdGate>
			<p>App content</p>
		</ThresholdGate>,
		queryClient,
	);

describe("ThresholdGate", () => {
	test("shows only the app when nothing is updating or being deleted", async () => {
		const api = stubApi({
			"GET /system/update": { body: update() },
			"GET /teams": { body: { items: [team()] } },
		});
		await mount();
		await settled(api);
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(screen.getByText("App content").parentElement).not.toHaveAttribute(
			"inert",
		);
	});

	test("a running update takes the screen and makes the app inert", async () => {
		stubApi({
			"GET /system/update": { body: running() },
			"GET /teams": { body: { items: [team()] } },
		});
		await mount();
		const dialog = await screen.findByRole("dialog", {
			name: "Updating to v1.2.0",
		});
		expect(dialog).toHaveAttribute("aria-modal", "true");
		expect(
			within(dialog).getByText(/unavailable for about a minute/),
		).toBeInTheDocument();
		expect(within(dialog).getByRole("status")).toHaveTextContent(
			"Installing the new version",
		);
		expect(
			within(dialog).getByText(/Running for 1m 0\d+s, from v1\.1\.0/),
		).toBeInTheDocument();
		expect(screen.getByText("App content").parentElement).toHaveAttribute(
			"inert",
		);
	});

	test("while the control plane does not answer it says it is restarting", async () => {
		let reads = 0;
		const { queryClient } = await (async () => {
			stubApi({
				"GET /system/update": () => {
					reads++;
					return reads === 1
						? Response.json(running())
						: Response.json({ title: "bad gateway" }, { status: 503 });
				},
				"GET /teams": { body: { items: [team()] } },
			});
			return mount();
		})();
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByRole("status")).toHaveTextContent(
			"Installing the new version",
		);
		await act(async () => {
			await queryClient.invalidateQueries();
		});
		await waitFor(() =>
			expect(screen.getByRole("dialog")).toHaveTextContent(
				"Restarting the control plane",
			),
		);
	});

	test("an update that has run for over half an hour hands the screen back", async () => {
		const api = stubApi({
			"GET /system/update": { body: running(ago(31 * 60_000)) },
			"GET /teams": { body: { items: [team()] } },
		});
		await mount();
		await settled(api);
		expect(screen.getByText("App content")).toBeInTheDocument();
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("a finished update shows no screen", async () => {
		const api = stubApi({
			"GET /system/update": {
				body: update({
					updating: {
						state: "succeeded",
						fromVersion: "v1.1.0",
						targetVersion: "v1.2.0",
						startedAt: ago(60_000),
						finishedAt: ago(1_000),
					},
				}),
			},
			"GET /teams": { body: { items: [team()] } },
		});
		await mount();
		await settled(api);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("deleting the current team lists the steps with the current one marked", async () => {
		stubApi({
			"GET /system/update": { body: update() },
			"GET /teams": { body: { items: [deleting("servers")] } },
		});
		await mount();
		const dialog = await screen.findByRole("dialog", { name: "Deleting Acme" });
		expect(
			within(dialog).getByText(
				/The deletion\s+continues on the server if you close this tab/,
			),
		).toBeInTheDocument();
		const rows = within(dialog).getAllByRole("listitem");
		expect(rows.map((r) => r.textContent)).toEqual([
			"Deleting projects and their services (done)",
			"Emptying and removing servers",
			"Removing status pages, Cloudflare and Tailscale",
			"Deleting the team",
		]);
		expect(rows.map((r) => r.getAttribute("aria-current"))).toEqual([
			null,
			"step",
			null,
			null,
		]);
		expect(screen.getByText("App content").parentElement).toHaveAttribute(
			"inert",
		);
	});

	test("before the job names a step the first one is current", async () => {
		stubApi({
			"GET /system/update": { body: update() },
			"GET /teams": { body: { items: [deleting()] } },
		});
		await mount();
		const rows = within(await screen.findByRole("dialog")).getAllByRole(
			"listitem",
		);
		expect(rows.map((r) => r.getAttribute("aria-current"))).toEqual([
			"step",
			null,
			null,
			null,
		]);
	});

	test("another team's deletion and a failed deletion stay inline", async () => {
		const api = stubApi({
			"GET /system/update": { body: update() },
			"GET /teams": {
				body: {
					items: [
						team(),
						{
							...deleting("projects"),
							id: "t2",
							name: "Other",
							current: false,
						},
						team({
							id: "t3",
							name: "Broken",
							current: false,
							deletion: {
								state: "failed",
								forgottenServers: null,
								keepDnsRecords: false,
								error: "x",
							},
						}),
					],
				},
			},
		});
		await mount();
		await settled(api);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("the update screen wins over a team deletion", async () => {
		stubApi({
			"GET /system/update": { body: running() },
			"GET /teams": { body: { items: [deleting("team")] } },
		});
		await mount();
		expect(
			await screen.findByRole("dialog", { name: "Updating to v1.2.0" }),
		).toBeInTheDocument();
		expect(screen.queryByText("Deleting Acme")).toBeNull();
	});

	test("when the deleted team leaves the list the reader is told and the team state is reset", async () => {
		let teamReads = 0;
		const api = stubApi({
			"GET /system/update": { body: update() },
			"GET /teams": () => {
				teamReads++;
				return Response.json({
					items:
						teamReads === 1
							? [deleting("team")]
							: [team({ id: "t9", name: "Home" })],
				});
			},
			"GET /projects": { body: { items: [] } },
		});
		const { queryClient } = await mount();
		await screen.findByRole("dialog", { name: "Deleting Acme" });
		await act(async () => {
			await queryClient.invalidateQueries({
				queryKey: [{ _id: "listTeams" }] as never,
				predicate: (q) => JSON.stringify(q.queryKey).includes("listTeams"),
			});
		});
		expect(await screen.findByText("Acme was deleted")).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.queryByRole("dialog", { name: /Deleting/ }) === null).toBe(
				true,
			),
		);
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/projects")).toBe(true),
		);
	});

	test("a deletion that stops without the team going away raises no 'deleted' toast", async () => {
		let teamReads = 0;
		stubApi({
			"GET /system/update": { body: update() },
			"GET /teams": () => {
				teamReads++;
				return Response.json({
					items: [
						teamReads === 1
							? deleting("servers")
							: team({
									deletion: {
										state: "failed",
										forgottenServers: null,
										keepDnsRecords: false,
										error: "agent offline",
									},
								}),
					],
				});
			},
		});
		const { queryClient } = await mount();
		await screen.findByRole("dialog", { name: "Deleting Acme" });
		await act(async () => {
			await queryClient.invalidateQueries({
				predicate: (q) => JSON.stringify(q.queryKey).includes("listTeams"),
			});
		});
		await waitFor(() =>
			expect(screen.queryByRole("dialog") === null).toBe(true),
		);
		expect(screen.queryByText("Acme was deleted")).toBeNull();
	});
});
