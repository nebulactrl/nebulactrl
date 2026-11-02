import { describe, expect, it } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { slice, view } from "./fixtures";
import { GIB } from "./model";
import { TeamsPlate } from "./teams-plate";

const DAY = 86_400_000;

function rowFor(name: string): HTMLElement {
	const row = screen.getByRole("row", { name: new RegExp(name) });
	return row;
}

describe("TeamsPlate", () => {
	it("explains the empty state when no team has a slice", async () => {
		await renderWithProviders(<TeamsPlate view={view({ slices: [] })} />);
		expect(screen.getByText(/No team has a slice yet/)).toBeInTheDocument();
		expect(screen.queryByRole("table")).toBeNull();
	});

	it("treats a null slice list as empty", async () => {
		await renderWithProviders(<TeamsPlate view={view({ slices: null })} />);
		expect(screen.getByText(/No team has a slice yet/)).toBeInTheDocument();
	});

	it("shows each live team's use, quota source and state, sorted by name", async () => {
		await renderWithProviders(
			<TeamsPlate
				view={view({
					slices: [
						slice({
							id: "s-b",
							teamId: "t-b",
							teamName: "Beta",
							usedBytes: 45 * GIB,
							effectiveQuotaBytes: 50 * GIB,
						}),
						slice({
							id: "s-a",
							teamId: "t-a",
							teamName: "Alpha",
							usedBytes: 5 * GIB,
							effectiveQuotaBytes: 20 * GIB,
							quotaBytes: 20 * GIB,
						}),
					],
				})}
			/>,
		);
		const rows = screen.getAllByRole("row").slice(1);
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("Alpha");
		expect(rows[0]).toHaveTextContent("Quota: Set for this team");
		expect(rows[0]).toHaveTextContent("5 GiB of 20 GiB");
		expect(rows[0]).toHaveTextContent("25%");
		expect(rows[0]).toHaveTextContent("Ready");
		expect(rows[0]).not.toHaveTextContent("Nearly full");
		expect(rows[1]).toHaveTextContent("Beta");
		expect(rows[1]).toHaveTextContent("Quota: Default");
		expect(rows[1]).toHaveTextContent("45 GiB of 50 GiB");
		expect(rows[1]).toHaveTextContent("90%");
		expect(rows[1]).toHaveTextContent(
			"Nearly full: the team was warned at 80%.",
		);
		expect(
			screen.getByRole("progressbar", {
				name: "Backup storage used by Beta",
			}),
		).toBeInTheDocument();
		expect(screen.getByText("2 teams · 50 GiB of backups")).toBeInTheDocument();
	});

	it("tells a full team that new backups fail, and a failed slice why", async () => {
		await renderWithProviders(
			<TeamsPlate
				view={view({
					slices: [
						slice({
							teamId: "t-1",
							teamName: "Full Co",
							state: "full",
							usedBytes: 50 * GIB,
						}),
						slice({
							id: "s-l",
							teamId: "t-2",
							teamName: "Broken Co",
							kind: "loki",
							state: "failed",
							message: "tunnel closed",
						}),
					],
				})}
			/>,
		);
		expect(rowFor("Full Co")).toHaveTextContent("Full");
		expect(rowFor("Full Co")).toHaveTextContent(
			"New backups fail until space is freed or the quota is raised.",
		);
		expect(rowFor("Broken Co")).toHaveTextContent("Not working");
		expect(rowFor("Broken Co")).toHaveTextContent(
			"Central logs: tunnel closed",
		);
		// A team whose backup slice does not exist yet has no usage to show.
		expect(rowFor("Broken Co")).toHaveTextContent("None yet");
		expect(
			within(rowFor("Broken Co")).queryByRole("button", { name: "Edit quota" }),
		).toBeNull();
		expect(
			within(rowFor("Full Co")).getByRole("button", { name: "Edit quota" }),
		).toBeInTheDocument();
	});

	it("lists a deleted team after the live ones with its purge date", async () => {
		const purgeAt = new Date(Date.now() + 12 * DAY + 1000).toISOString();
		await renderWithProviders(
			<TeamsPlate
				view={view({
					slices: [
						slice({
							id: "s-d",
							teamId: "t-d",
							teamName: "Aaa Gone",
							usedBytes: 4 * GIB,
							deletedAt: new Date().toISOString(),
							purgeAt,
						}),
						slice({ id: "s-z", teamId: "t-z", teamName: "Zed Live" }),
						slice({
							id: "s-n",
							teamId: "t-n",
							teamName: "Nodate",
							deletedAt: new Date().toISOString(),
						}),
					],
				})}
			/>,
		);
		const rows = screen.getAllByRole("row").slice(1);
		expect(rows[0]).toHaveTextContent("Zed Live");
		// An unknown purge date sorts before the dated ones.
		expect(rows[1]).toHaveTextContent("Nodate");
		expect(rows[1]).toHaveTextContent("Purge date unknown");
		expect(rows[2]).toHaveTextContent("Aaa Gone");
		expect(rows[2]).toHaveTextContent("Team deleted");
		expect(rows[2]).toHaveTextContent("4 GiB kept");
		expect(rows[2]).toHaveTextContent(/Purged .* \(in 13 days\)/);
		// A deleted team's quota cannot be edited; a live one's cannot be purged.
		expect(
			within(rows[2] as HTMLElement).queryByRole("button", {
				name: "Edit quota",
			}),
		).toBeNull();
		expect(
			within(rows[0] as HTMLElement).queryByRole("button", {
				name: "Delete now",
			}),
		).toBeNull();
		expect(
			screen.getByText(
				"1 team · 24 GiB of backups · 2 deleted, waiting to be purged",
			),
		).toBeInTheDocument();
	});

	it("Edit quota opens the quota dialog for that team", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<TeamsPlate
				view={view({
					slices: [
						slice({ teamId: "t-a", teamName: "Alpha" }),
						slice({
							id: "s-b",
							teamId: "t-b",
							teamName: "Beta",
							effectiveQuotaBytes: 30 * GIB,
						}),
					],
				})}
			/>,
		);
		await user.click(
			within(rowFor("Beta")).getByRole("button", { name: "Edit quota" }),
		);
		const dialog = await screen.findByRole("dialog", {
			name: "Backup quota for Beta",
		});
		expect(within(dialog).getByLabelText("Quota (GiB)")).toHaveValue("30");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await screen.findByRole("table");
		expect(screen.queryByRole("dialog", { name: /Backup quota/ })).toBeNull();
	});

	describe("Delete now", () => {
		const gone = () =>
			view({
				slices: [
					slice({
						teamId: "t-d",
						teamName: "Gone Co",
						usedBytes: 4 * GIB,
						deletedAt: new Date().toISOString(),
						purgeAt: new Date(Date.now() + 5 * DAY).toISOString(),
					}),
				],
			});

		it("is locked until the team's name is typed, then purges that team", async () => {
			const user = userEvent.setup();
			const api = stubApi({
				"DELETE /admin/team-slices/:teamId": { status: 204 },
			});
			await renderWithProviders(<TeamsPlate view={gone()} />);
			await user.click(screen.getByRole("button", { name: "Delete now" }));
			const dialog = await screen.findByRole("alertdialog");
			expect(dialog).toHaveTextContent("Delete Gone Co's data now?");
			expect(dialog).toHaveTextContent("(4 GiB of backups)");
			const confirm = within(dialog).getByRole("button", {
				name: "Delete now",
			});
			await user.click(confirm);
			expect(api.calls).toEqual([]);
			await user.type(within(dialog).getByRole("textbox"), "Gone");
			await user.click(confirm);
			expect(api.calls).toEqual([]);
			await user.type(within(dialog).getByRole("textbox"), " Co");
			await user.click(confirm);
			expect(
				await screen.findByText("Deleted Gone Co's built-in storage"),
			).toBeInTheDocument();
			expect(api.calls).toHaveLength(1);
			expect(api.calls[0]).toMatchObject({
				method: "DELETE",
				path: "/admin/team-slices/t-d",
			});
			expect(api.calls[0]?.query.get("confirm")).toBe("Gone Co");
			expect(screen.queryByRole("alertdialog")).toBeNull();
		});

		it("keeps the dialog open with the server's reason when the purge fails", async () => {
			const user = userEvent.setup();
			stubApi({
				"DELETE /admin/team-slices/:teamId": problem(409, {
					detail: "A purge is already running.",
				}),
			});
			await renderWithProviders(<TeamsPlate view={gone()} />);
			await user.click(screen.getByRole("button", { name: "Delete now" }));
			const dialog = await screen.findByRole("alertdialog");
			await user.type(within(dialog).getByRole("textbox"), "Gone Co");
			await user.click(
				within(dialog).getByRole("button", { name: "Delete now" }),
			);
			expect(
				await within(dialog).findByText("A purge is already running."),
			).toBeInTheDocument();
			expect(screen.getByRole("alertdialog")).toBeInTheDocument();
			expect(screen.queryByText(/built-in storage$/)).toBeNull();
		});

		it("words the dialog without a size when the team never had a backup slice", async () => {
			const user = userEvent.setup();
			await renderWithProviders(
				<TeamsPlate
					view={view({
						slices: [
							slice({
								kind: "loki",
								teamName: "Logs Only",
								deletedAt: new Date().toISOString(),
							}),
						],
					})}
				/>,
			);
			await user.click(screen.getByRole("button", { name: "Delete now" }));
			const dialog = await screen.findByRole("alertdialog");
			expect(dialog).not.toHaveTextContent("of backups)");
			expect(dialog).toHaveTextContent("its purge date");
		});
	});
});
