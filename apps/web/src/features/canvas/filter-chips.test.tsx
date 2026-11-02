import { describe, expect, mock, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Server, Service } from "#/api/models";
import { FilterChips } from "./filter-chips";
import { type CanvasFilter, EMPTY_FILTER, NO_SERVER } from "./lib/filter";
import { makeDeployment, makeService } from "./lib/fixtures";

const SERVER_A = {
	id: "server-a",
	name: "hetzner-01",
	status: "online" as const,
	lastSeenAt: null,
};
const SERVER_B = {
	id: "server-b",
	name: "ovh-02",
	status: "online" as const,
	lastSeenAt: null,
};

const web = makeService({
	id: "web",
	name: "Web",
	kind: "app",
	serverId: SERVER_A.id,
	server: SERVER_A,
	currentDeploymentId: "dep-1",
	latestDeployment: makeDeployment("succeeded", {
		finishedAt: new Date().toISOString(),
	}),
});
const db = makeService({
	id: "db",
	name: "DB",
	kind: "database",
	serverId: SERVER_B.id,
	server: SERVER_B,
	latestDeployment: makeDeployment("failed"),
});
const worker = makeService({
	id: "worker",
	name: "Worker",
	kind: "worker",
	serverId: SERVER_A.id,
	server: SERVER_A,
});

function setup(
	services: Service[],
	filter: CanvasFilter = EMPTY_FILTER,
	servers: Server[] = [],
) {
	const onChange = mock((_filter: CanvasFilter) => {});
	const view = render(
		<FilterChips
			services={services}
			servers={servers}
			filter={filter}
			onChange={onChange}
		/>,
	);
	const last = () => onChange.mock.calls.at(-1)?.[0] as CanvasFilter;
	return { onChange, last, ...view };
}

describe("FilterChips", () => {
	test("renders nothing on an empty canvas", () => {
		const { container } = setup([]);
		expect(container).toBeEmptyDOMElement();
	});

	test("renders nothing when every card looks alike: nothing to filter by", () => {
		const { container } = setup([web, { ...web, id: "web-2" }]);
		expect(container).toBeEmptyDOMElement();
	});

	test("offers status, server and kind chips, each only when it separates cards", () => {
		setup([web, db, worker]);
		const bar = screen.getByRole("group", { name: "Filter services" });
		const names = within(bar)
			.getAllByRole("button")
			.map((b) => b.textContent);
		// Statuses in a fixed order, then servers in first-seen order, then kinds.
		expect(names).toEqual([
			"Deployed",
			"Failed",
			"Not deployed",
			"hetzner-01",
			"ovh-02",
			"App",
			"Worker",
			"Database",
		]);
	});

	test("a row with a single value shows no chips for it", () => {
		const sameServerKinds = [
			{ ...web, kind: "app" as const },
			{ ...web, id: "w2", kind: "worker" as const },
		];
		setup(sameServerKinds);
		const names = screen.getAllByRole("button").map((b) => b.textContent);
		expect(names).toEqual(["App", "Worker"]);
	});

	test("clicking a chip adds it to the filter; the filter object is not mutated", async () => {
		const user = userEvent.setup();
		const { last } = setup([web, db, worker]);
		await user.click(screen.getByRole("button", { name: "Failed" }));
		expect([...last().statuses]).toEqual(["failed"]);
		expect(EMPTY_FILTER.statuses.size).toBe(0);
		await user.click(screen.getByRole("button", { name: "ovh-02" }));
		expect([...last().serverIds]).toEqual(["server-b"]);
		await user.click(screen.getByRole("button", { name: "Database" }));
		expect([...last().kinds]).toEqual(["database"]);
	});

	test("a pressed chip is aria-pressed and clicking it again removes it", async () => {
		const user = userEvent.setup();
		const filter: CanvasFilter = {
			...EMPTY_FILTER,
			statuses: new Set(["failed", "deployed"]),
		};
		const { last } = setup([web, db, worker], filter);
		expect(screen.getByRole("button", { name: "Failed" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(
			screen.getByRole("button", { name: "Not deployed" }),
		).toHaveAttribute("aria-pressed", "false");
		await user.click(screen.getByRole("button", { name: "Failed" }));
		expect([...last().statuses]).toEqual(["deployed"]);
	});

	test("toggling a chip keeps the other rows and the typed query", async () => {
		const user = userEvent.setup();
		const filter: CanvasFilter = {
			...EMPTY_FILTER,
			kinds: new Set(["app"]),
			query: "web",
		};
		const { last } = setup([web, db, worker], filter);
		await user.click(screen.getByRole("button", { name: "ovh-02" }));
		expect([...last().kinds]).toEqual(["app"]);
		expect([...last().serverIds]).toEqual(["server-b"]);
		expect(last().query).toBe("web");
	});

	test("Clear resets the three chip rows but leaves the search text", async () => {
		const user = userEvent.setup();
		const filter: CanvasFilter = {
			statuses: new Set(["failed"]),
			serverIds: new Set(["server-b"]),
			kinds: new Set(["database"]),
			query: "pg",
		};
		const { last } = setup([web, db, worker], filter);
		await user.click(screen.getByRole("button", { name: "Clear" }));
		expect(last().statuses.size).toBe(0);
		expect(last().serverIds.size).toBe(0);
		expect(last().kinds.size).toBe(0);
		expect(last().query).toBe("pg");
	});

	test("no Clear button until a chip is pressed, and not for a search alone", () => {
		setup([web, db, worker], { ...EMPTY_FILTER, query: "web" });
		expect(
			screen.queryByRole("button", { name: "Clear" }),
		).not.toBeInTheDocument();
	});

	test("with a chip pressed the bar says how many cards match", () => {
		setup([web, db, worker], {
			...EMPTY_FILTER,
			kinds: new Set(["app", "worker"]),
		});
		expect(screen.getByRole("status")).toHaveTextContent("2 of 3");
	});

	test("says 'No matches' when the filter excludes everything", () => {
		setup([web, db, worker], {
			...EMPTY_FILTER,
			statuses: new Set(["rolled_back"]),
		});
		expect(screen.getByRole("status")).toHaveTextContent("No matches");
	});

	test("leaves the count to the search field while a search is typed", () => {
		setup([web, db, worker], {
			...EMPTY_FILTER,
			kinds: new Set(["app"]),
			query: "web",
		});
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
	});

	test("an active filter keeps the bar visible even when only one value remains", () => {
		setup([web, { ...web, id: "web-2" }], {
			...EMPTY_FILTER,
			kinds: new Set(["app"]),
		});
		expect(
			screen.getByRole("group", { name: "Filter services" }),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Clear" })).toBeInTheDocument();
	});

	test("a service without a server gets a 'No server' chip that filters on the sentinel id", async () => {
		const user = userEvent.setup();
		const unplaced = makeService({
			id: "loose",
			serverId: null,
			server: null,
		});
		const { last } = setup([web, unplaced]);
		await user.click(screen.getByRole("button", { name: /No server/ }));
		expect([...last().serverIds]).toEqual([NO_SERVER]);
	});

	test("a server that is not online says so in its chip; an online one stays plain", () => {
		const offlineDb = {
			...db,
			server: { ...SERVER_B, status: "offline" as const },
		};
		setup([web, offlineDb]);
		expect(
			screen.getByRole("button", { name: "ovh-02 Offline" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "hetzner-01" }),
		).toBeInTheDocument();
	});

	test("a server still being set up reads Pending, not Offline", () => {
		const pendingDb = {
			...db,
			server: { ...SERVER_B, status: "pending" as const },
		};
		setup([web, pendingDb]);
		expect(
			screen.getByRole("button", { name: "ovh-02 Pending" }),
		).toBeInTheDocument();
	});

	test("the live server list overrides the status a service was loaded with", () => {
		const live = [{ id: SERVER_B.id, status: "online" }] as Server[];
		const staleDb = {
			...db,
			server: { ...SERVER_B, status: "offline" as const },
		};
		setup([web, staleDb], EMPTY_FILTER, live);
		expect(screen.getByRole("button", { name: "ovh-02" })).toBeInTheDocument();
	});
});
