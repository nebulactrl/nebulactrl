import { describe, expect, it } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import type { TeamSliceView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { BuiltinStorageCard } from "./builtin-storage-card";
import { GIB } from "./model";

function teamSlice(
	backup: Partial<TeamSliceView["backup"]> = {},
): TeamSliceView {
	return {
		backup: {
			percent: 24,
			quotaBytes: 50 * GIB,
			usedBytes: 12 * GIB,
			state: "ready",
			...backup,
		},
		logs: { retentionDays: 7 },
		registry: { usedBytes: 0 },
	};
}

describe("BuiltinStorageCard", () => {
	it("shows the team's use of its quota, its state and the actions", async () => {
		stubApi({ "GET /team-slice": { body: teamSlice() } });
		await renderWithProviders(
			<BuiltinStorageCard actions={<button type="button">Manage</button>} />,
		);
		const card = await screen.findByRole("article", {
			name: "Built-in backup storage",
		});
		expect(card).toHaveTextContent("12 GiB of 50 GiB");
		expect(card).toHaveTextContent("24%");
		expect(card).toHaveTextContent("Ready");
		expect(card).toHaveTextContent("not off-site");
		expect(card).not.toHaveTextContent("Nearly full");
		expect(
			screen.getByRole("progressbar", { name: "Built-in backup storage used" }),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Manage" })).toBeInTheDocument();
	});

	it("warns when the quota is nearly used up and explains a full slice", async () => {
		stubApi({
			"GET /team-slice": {
				body: teamSlice({ usedBytes: 45 * GIB }),
			},
		});
		const { unmount } = await renderWithProviders(<BuiltinStorageCard />);
		const card = await screen.findByRole("article");
		expect(card).toHaveTextContent("90%");
		expect(card).toHaveTextContent(
			"Nearly full: you are alerted at 80% and at 100%.",
		);
		expect(screen.queryByRole("contentinfo")).toBeNull();
		unmount();

		stubApi({
			"GET /team-slice": {
				body: teamSlice({ usedBytes: 50 * GIB, state: "full" }),
			},
		});
		await renderWithProviders(<BuiltinStorageCard />);
		const full = await screen.findByRole("article");
		expect(full).toHaveTextContent("Full");
		expect(full).toHaveTextContent("New backups fail until space is freed");
		expect(full).toHaveTextContent("instance admin raises the quota");
	});

	it("says what a failed slice means for the team", async () => {
		stubApi({
			"GET /team-slice": {
				body: teamSlice({ state: "failed" }),
			},
		});
		await renderWithProviders(<BuiltinStorageCard />);
		const card = await screen.findByRole("article");
		expect(card).toHaveTextContent("Not working");
		expect(card).toHaveTextContent("Ask your instance admin");
	});

	it("renders nothing on an installation without built-in storage", async () => {
		const api = stubApi({
			"GET /team-slice": {
				body: teamSlice({ state: "unavailable", quotaBytes: 0, usedBytes: 0 }),
			},
		});
		const { queryClient } = await renderWithProviders(<BuiltinStorageCard />);
		await waitFor(() =>
			expect(queryClient.getQueryCache().getAll()[0]?.state.status).toBe(
				"success",
			),
		);
		expect(api.calls).toHaveLength(1);
		expect(screen.queryByRole("article")).toBeNull();
	});

	it("renders nothing when the slice cannot be read", async () => {
		const api = stubApi({
			"GET /team-slice": problem(500, { detail: "boom" }),
		});
		const { queryClient } = await renderWithProviders(<BuiltinStorageCard />);
		await waitFor(() =>
			expect(queryClient.getQueryCache().getAll()[0]?.state.status).toBe(
				"error",
			),
		);
		expect(api.calls).toHaveLength(1);
		expect(screen.queryByRole("article")).toBeNull();
	});
});
