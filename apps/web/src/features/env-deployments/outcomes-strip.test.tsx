import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import { getEnvironmentDeploymentsDailyOptions } from "#/api/gen/@tanstack/react-query.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { DeploymentOutcomes } from "./outcomes-strip";

const day = (date: string, succeeded = 0, failed = 0, other = 0) => ({
	date,
	succeeded,
	failed,
	other,
});

const render = () =>
	renderWithProviders(<DeploymentOutcomes environmentId="env1" />);

describe("DeploymentOutcomes", () => {
	test("asks for the last 30 days of the environment", async () => {
		const api = stubApi({
			"GET /environments/:id/deployments/daily": {
				body: { days: [day("2026-10-30", 1)] },
			},
		});
		await render();
		await screen.findByRole("table", { name: /Deployments, last 30 days/ });
		expect(api.calls[0]?.path).toBe("/environments/env1/deployments/daily");
		expect(api.calls[0]?.query.get("days")).toBe("30");
		expect(api.unmatched).toEqual([]);
	});

	test("shows the success share and a row per day with counts", async () => {
		stubApi({
			"GET /environments/:id/deployments/daily": {
				body: {
					days: [
						day("2026-10-29", 3, 1, 0),
						day("2026-10-30", 0, 0, 0),
						day("2026-10-31", 0, 0, 2),
					],
				},
			},
		});
		await render();
		const table = await screen.findByRole("table", {
			name: /Deployments, last 30 days/,
		});
		// 3 succeeded of 6 deployments.
		expect(table).toHaveTextContent("50.00% succeeded");
		expect(table).toHaveTextContent("3 succeeded, 1 failed");
		expect(table).toHaveTextContent("Failed");
		expect(table).toHaveTextContent("No deployments");
		expect(table).toHaveTextContent("2 other");
		expect(table).toHaveTextContent("Canceled or rolled back");
	});

	test("is hidden for an environment that never deployed", async () => {
		const api = stubApi({
			"GET /environments/:id/deployments/daily": {
				body: { days: [day("2026-10-30"), day("2026-10-31")] },
			},
		});
		const { container, queryClient } = await render();
		await waitFor(() =>
			expect(
				queryClient.getQueryState(
					getEnvironmentDeploymentsDailyOptions({
						path: { environmentId: "env1" },
						query: { days: 30 },
					}).queryKey,
				)?.status,
			).toBe("success"),
		);
		expect(api.calls).toHaveLength(1);
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
		expect(container).not.toHaveTextContent("did not load");
	});

	test("says the history did not load without blocking the page", async () => {
		stubApi({
			"GET /environments/:id/deployments/daily": {
				status: 500,
				body: { status: 500, title: "x", detail: "boom", code: "internal" },
			},
		});
		await render();
		expect(
			await screen.findByText(
				"The 30-day history did not load. The list below is unaffected.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
	});

	test("shows a loading placeholder until the aggregate arrives", async () => {
		stubApi({
			"GET /environments/:id/deployments/daily": () =>
				new Promise<Response>(() => undefined),
		});
		await render();
		expect(
			screen.getByRole("status", { name: "Loading deployment history" }),
		).toBeInTheDocument();
	});
});
