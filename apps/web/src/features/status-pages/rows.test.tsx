import { describe, expect, it, mock } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	MaintenanceWindowView,
	StatusIncidentView,
} from "#/api/gen/types.gen";
import { renderWithProviders } from "#/test/render";
import { IncidentRows, LocalTime, MaintenanceRows } from "./rows";

function incident(over: Partial<StatusIncidentView> = {}): StatusIncidentView {
	return {
		id: "inc-1",
		title: "API is slow",
		impact: "major",
		status: "investigating",
		pageIds: ["page-1"],
		serviceIds: [],
		createdAt: "2026-10-30T10:00:00Z",
		updatedAt: "2026-10-30T10:05:00Z",
		resolvedAt: null,
		updates: [],
		...over,
	};
}

function win(over: Partial<MaintenanceWindowView> = {}): MaintenanceWindowView {
	return {
		id: "win-1",
		title: "Database upgrade",
		description: "",
		phase: "upcoming",
		startsAt: "2026-11-02T01:00:00Z",
		endsAt: "2026-11-02T02:30:00Z",
		pageIds: ["page-1"],
		serviceIds: ["svc-1", "svc-2"],
		canceledAt: null,
		createdAt: "2026-10-30T10:00:00Z",
		updatedAt: "2026-10-30T10:00:00Z",
		...over,
	};
}

const TITLES = new Map([
	["page-1", "Acme status"],
	["page-2", "Acme API"],
]);

describe("IncidentRows", () => {
	it("lists each incident as a link to its timeline, in the given order", async () => {
		await renderWithProviders(
			<IncidentRows
				label="Open incidents"
				pageTitles={TITLES}
				incidents={[
					incident({ id: "inc-1", title: "API is slow" }),
					incident({
						id: "inc-2",
						title: "Login fails",
						impact: "critical",
						status: "resolved",
					}),
				]}
			/>,
		);
		const list = screen.getByRole("list", { name: "Open incidents" });
		const links = within(list).getAllByRole("link");
		expect(links).toHaveLength(2);
		expect(links[0]).toHaveTextContent("API is slow");
		expect(links[0]).toHaveAttribute("href", "/status-pages/incidents/inc-1");
		expect(links[1]).toHaveTextContent("Login fails");
		expect(links[1]).toHaveAttribute("href", "/status-pages/incidents/inc-2");
	});

	it("shows impact and status in words and when it was last updated", async () => {
		await renderWithProviders(
			<IncidentRows
				label="Incidents"
				pageTitles={TITLES}
				incidents={[incident({ impact: "critical", status: "monitoring" })]}
			/>,
		);
		const row = screen.getByRole("link");
		expect(within(row).getByText("Critical")).toBeInTheDocument();
		expect(within(row).getByText("Monitoring")).toBeInTheDocument();
		expect(row.querySelector("time")).toHaveAttribute(
			"datetime",
			"2026-10-30T10:05:00Z",
		);
	});

	it("names the pages it is posted to and counts affected services", async () => {
		await renderWithProviders(
			<IncidentRows
				label="Incidents"
				pageTitles={TITLES}
				incidents={[
					incident({
						pageIds: ["page-1", "page-2"],
						serviceIds: ["a", "b", "c"],
					}),
				]}
			/>,
		);
		expect(
			screen.getByText("Posted to Acme status, Acme API · 3 affected services"),
		).toBeInTheDocument();
	});

	it("uses the singular for one affected service", async () => {
		await renderWithProviders(
			<IncidentRows
				label="Incidents"
				pageTitles={TITLES}
				incidents={[incident({ serviceIds: ["a"] })]}
			/>,
		);
		expect(
			screen.getByText("Posted to Acme status · 1 affected service"),
		).toBeInTheDocument();
	});

	it.each([
		["no pages", null],
		["an empty page list", []],
		["only pages the viewer cannot name", ["page-unknown"]],
	])("says it is not posted anywhere for %s", async (_name, pageIds) => {
		await renderWithProviders(
			<IncidentRows
				label="Incidents"
				pageTitles={TITLES}
				incidents={[incident({ pageIds, serviceIds: null })]}
			/>,
		);
		expect(screen.getByText("Not posted to any page")).toBeInTheDocument();
	});

	it("renders an empty list for no incidents", async () => {
		await renderWithProviders(
			<IncidentRows label="Incidents" pageTitles={TITLES} incidents={[]} />,
		);
		expect(screen.queryAllByRole("listitem")).toHaveLength(0);
	});
});

const LOCAL = new Intl.DateTimeFormat(undefined, {
	dateStyle: "medium",
	timeStyle: "short",
});

function handlers() {
	return {
		onEdit: mock(() => {}),
		onCancel: mock(() => {}),
		onDelete: mock(() => {}),
	};
}

describe("MaintenanceRows", () => {
	it("shows the window in local time with its length, and in UTC beneath", async () => {
		await renderWithProviders(
			<MaintenanceRows
				label="Maintenance"
				windows={[win()]}
				pageTitles={TITLES}
				canManage={false}
				{...handlers()}
			/>,
		);
		const row = screen.getByRole("listitem");
		expect(within(row).getByText("Database upgrade")).toBeInTheDocument();
		expect(within(row).getByText("Upcoming")).toBeInTheDocument();
		expect(
			within(row).getByText(
				`${LOCAL.format(new Date("2026-11-02T01:00:00Z"))} to ${LOCAL.format(new Date("2026-11-02T02:30:00Z"))} (1 h 30 min)`,
			),
		).toBeInTheDocument();
		expect(
			within(row).getByText("2026-11-02 01:00 UTC to 2026-11-02 02:30 UTC"),
		).toBeInTheDocument();
		expect(
			within(row).getByText(
				"Posted to Acme status · 2 services in maintenance",
			),
		).toBeInTheDocument();
	});

	it("omits the length of a window that ends before it starts", async () => {
		await renderWithProviders(
			<MaintenanceRows
				label="Maintenance"
				windows={[
					win({
						startsAt: "2026-11-02T02:00:00Z",
						endsAt: "2026-11-02T01:00:00Z",
					}),
				]}
				pageTitles={TITLES}
				canManage={false}
				{...handlers()}
			/>,
		);
		expect(screen.queryByText(/\(.*\)/)).not.toBeInTheDocument();
	});

	it("says so when no service is in maintenance and no page announces it", async () => {
		await renderWithProviders(
			<MaintenanceRows
				label="Maintenance"
				windows={[win({ serviceIds: null, pageIds: null })]}
				pageTitles={TITLES}
				canManage={false}
				{...handlers()}
			/>,
		);
		expect(
			screen.getByText("Not posted to any page · no services in maintenance"),
		).toBeInTheDocument();
	});

	it("counts one service in the singular", async () => {
		await renderWithProviders(
			<MaintenanceRows
				label="Maintenance"
				windows={[win({ serviceIds: ["svc-1"] })]}
				pageTitles={TITLES}
				canManage={false}
				{...handlers()}
			/>,
		);
		expect(
			screen.getByText("Posted to Acme status · 1 service in maintenance"),
		).toBeInTheDocument();
	});

	it("offers no actions to someone who cannot manage", async () => {
		await renderWithProviders(
			<MaintenanceRows
				label="Maintenance"
				windows={[win(), win({ id: "win-2", phase: "active" })]}
				pageTitles={TITLES}
				canManage={false}
				{...handlers()}
			/>,
		);
		expect(screen.queryByRole("button")).not.toBeInTheDocument();
	});

	it("lets a manager edit, cancel and delete an upcoming window", async () => {
		const user = userEvent.setup();
		const h = handlers();
		const upcoming = win();
		await renderWithProviders(
			<MaintenanceRows
				label="Maintenance"
				windows={[upcoming]}
				pageTitles={TITLES}
				canManage
				{...h}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Edit" }));
		expect(h.onEdit).toHaveBeenCalledWith(upcoming);
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(h.onCancel).toHaveBeenCalledWith(upcoming);
		await user.click(screen.getByRole("button", { name: "Delete" }));
		expect(h.onDelete).toHaveBeenCalledWith(upcoming);
		expect(
			screen.queryByRole("button", { name: "End now" }),
		).not.toBeInTheDocument();
	});

	it("calls cancelling a running window 'End now'", async () => {
		const user = userEvent.setup();
		const h = handlers();
		const active = win({ phase: "active" });
		await renderWithProviders(
			<MaintenanceRows
				label="Maintenance"
				windows={[active]}
				pageTitles={TITLES}
				canManage
				{...h}
			/>,
		);
		expect(
			screen.queryByRole("button", { name: "Cancel" }),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "End now" }));
		expect(h.onCancel).toHaveBeenCalledWith(active);
	});

	it.each(["ended", "canceled"] as const)(
		"leaves only Delete on a %s window",
		async (phase) => {
			await renderWithProviders(
				<MaintenanceRows
					label="Maintenance"
					windows={[win({ phase })]}
					pageTitles={TITLES}
					canManage
					{...handlers()}
				/>,
			);
			expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual([
				"Delete",
			]);
		},
	);
});

describe("LocalTime", () => {
	it("reads the instant in local time with UTC as its tooltip", async () => {
		await renderWithProviders(<LocalTime iso="2026-11-02T01:00:00Z" />);
		const time = document.querySelector("time");
		expect(time).toHaveAttribute("datetime", "2026-11-02T01:00:00Z");
		expect(time).toHaveAttribute("title", "2026-11-02 01:00 UTC");
		expect(time).toHaveTextContent(
			LOCAL.format(new Date("2026-11-02T01:00:00Z")),
		);
	});
});
