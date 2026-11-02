import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import type { IncidentView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeService } from "../../lib/fixtures";
import { Incidents } from "./incidents";

function incident(overrides: Partial<IncidentView>): IncidentView {
	return {
		id: "inc-1",
		kind: "crashed",
		flapping: false,
		detail: "The container exited and has been down for 30s.",
		restarts: 0,
		openedAt: "2026-10-28T12:00:00Z",
		resolvedAt: null,
		resolution: "",
		...overrides,
	};
}

describe("Incidents", () => {
	test("lists the service's recent incidents under a Health incidents heading, asking for five", async () => {
		const service = makeService({ id: "svc-inc" });
		const api = stubApi({
			"GET /services/:serviceId/incidents": {
				body: make("ListIncidentsOutputBody", {
					items: [
						incident({ id: "a", detail: "Exited with code 137" }),
						incident({
							id: "b",
							kind: "unhealthy",
							detail: "HTTP 503 from /healthz",
							resolvedAt: "2026-10-28T12:05:00Z",
							resolution: "recovered",
						}),
						incident({
							id: "c",
							kind: "restart_loop",
							flapping: true,
							detail: "Restarted 6 times",
						}),
					],
				}),
			},
		});
		await renderWithProviders(<Incidents service={service} />);
		const section = await screen.findByRole("region", {
			name: "Health incidents",
		});
		const rows = within(section).getAllByRole("listitem");
		expect(rows.map((row) => row.textContent)).toEqual([
			expect.stringContaining("CrashedExited with code 137"),
			expect.stringContaining("Unhealthy · recoveredHTTP 503 from /healthz"),
			expect.stringContaining("Restart loop · alerts pausedRestarted 6 times"),
		]);
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/services/svc-inc/incidents");
		expect(api.calls[0]?.query.get("limit")).toBe("5");
		expect(api.unmatched).toEqual([]);
	});

	test("renders nothing for a service that has never had an incident", async () => {
		const api = stubApi({
			"GET /services/:serviceId/incidents": {
				body: make("ListIncidentsOutputBody", { items: [] }),
			},
		});
		const view = await renderWithProviders(
			<Incidents service={makeService()} />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() => expect(view.queryClient.isFetching()).toBe(0));
		expect(
			view.queryByRole("region", { name: "Health incidents" }),
		).not.toBeInTheDocument();
	});

	test("renders nothing when the request fails", async () => {
		const api = stubApi({
			"GET /services/:serviceId/incidents": problem(500, { title: "boom" }),
		});
		const view = await renderWithProviders(
			<Incidents service={makeService()} />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() => expect(view.queryClient.isFetching()).toBe(0));
		expect(screen.queryByRole("region")).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});
});
