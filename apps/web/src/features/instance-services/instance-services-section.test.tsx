import { describe, expect, it } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { slice, view } from "./fixtures";
import { InstanceServicesSection } from "./instance-services-section";
import { GIB } from "./model";

describe("InstanceServicesSection", () => {
	it("shows the services, the default quota and the teams from one read", async () => {
		const api = stubApi({
			"GET /admin/instance-services": {
				body: view({
					defaultQuotaBytes: 40 * GIB,
					slices: [slice({ teamName: "Acme" })],
				}),
			},
		});
		await renderWithProviders(<InstanceServicesSection />);
		expect(
			await screen.findByRole("group", { name: "Services" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Backup storage per team (GiB)")).toHaveValue(
			"40",
		);
		const teams = screen.getByRole("group", { name: "Teams" });
		expect(within(teams).getByText("Acme")).toBeInTheDocument();
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"GET /admin/instance-services",
		]);
	});

	it("says what the control plane answered when the read fails, and retries", async () => {
		const user = userEvent.setup();
		let calls = 0;
		stubApi({
			"GET /admin/instance-services": () => {
				calls++;
				return calls === 1
					? Response.json(
							{ detail: "Not an admin." },
							{
								status: 403,
								headers: { "Content-Type": "application/problem+json" },
							},
						)
					: Response.json(view());
			},
		});
		await renderWithProviders(<InstanceServicesSection />);
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Built-in services did not load");
		expect(alert).toHaveTextContent(
			"The control plane answered 403: Not an admin. Nothing was changed.",
		);
		await user.click(
			within(alert).getByRole("button", { name: /retry|try again/i }),
		);
		expect(
			await screen.findByRole("group", { name: "Services" }),
		).toBeInTheDocument();
		expect(calls).toBe(2);
	});
});
