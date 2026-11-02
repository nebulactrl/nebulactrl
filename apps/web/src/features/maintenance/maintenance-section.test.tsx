import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { renderWithProviders } from "#/test/render";
import { MaintenanceSection } from "./maintenance-section";

const domain = {
	id: "d1",
	hostname: "shop.example.com",
} as unknown as Service["domains"][number];

describe("MaintenanceSection", () => {
	test("renders nothing for a service without a domain", async () => {
		const { container } = await renderWithProviders(
			<MaintenanceSection service={makeService({ domains: [] })} />,
		);
		expect(container).toBeEmptyDOMElement();
	});

	test("shows the form, with the summary Off, for a service that serves a domain", async () => {
		await renderWithProviders(
			<MaintenanceSection service={makeService({ domains: [domain] })} />,
		);
		expect(screen.getByText("Maintenance")).toBeInTheDocument();
		expect(screen.getByText("Off")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Turn on maintenance" }),
		).toBeInTheDocument();
	});

	test("a service in maintenance summarises since when and offers Turn off", async () => {
		await renderWithProviders(
			<MaintenanceSection
				service={makeService({
					domains: [domain],
					maintenance: {
						enabled: true,
						title: "Upgrade",
						message: "",
						eta: null,
						since: new Date(Date.now() - 2 * 3600_000).toISOString(),
					},
				})}
			/>,
		);
		expect(screen.getByText(/^On since /)).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Turn off" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Title")).toHaveValue("Upgrade");
	});

	test("a TCP route cannot show a maintenance page, so there is no section", async () => {
		const service = makeService({ kind: "route", domains: [domain] });
		(service.config as { route?: { protocol: string } }).route = {
			protocol: "tcp",
		};
		const { container } = await renderWithProviders(
			<MaintenanceSection service={service} />,
		);
		expect(container).toBeEmptyDOMElement();
	});
});
