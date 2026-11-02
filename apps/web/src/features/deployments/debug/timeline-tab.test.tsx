import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { EventView } from "#/api/gen/types.gen";
import { TimelineTab } from "./timeline-tab";

const ORIGIN = "2026-10-31T10:00:00.000Z";

function event(
	id: number,
	step: string,
	status: string,
	at: string,
	message = "",
): EventView {
	return { id, step, status, createdAt: `2026-10-31T${at}Z`, message };
}

function renderTab(props: Partial<Parameters<typeof TimelineTab>[0]> = {}) {
	return render(
		<TimelineTab
			events={[]}
			pending={false}
			error={false}
			onRetry={() => {}}
			originIso={ORIGIN}
			live={false}
			{...props}
		/>,
	);
}

const failedBuild = [
	event(1, "status", "queued", "10:00:00.100"),
	event(2, "pull", "started", "10:00:00.200"),
	event(3, "pull", "succeeded", "10:00:02.200"),
	event(4, "predeploy", "started", "10:00:02.300"),
	event(5, "predeploy", "failed", "10:00:12.300", "migration exited 2"),
];

describe("TimelineTab states", () => {
	test("pending shows a busy placeholder", () => {
		const { container } = renderTab({ events: undefined, pending: true });
		expect(container.querySelector("[aria-busy]")).not.toBeNull();
		expect(screen.queryByText("No events yet")).not.toBeInTheDocument();
	});

	test("an error offers a retry", async () => {
		let retries = 0;
		renderTab({
			events: undefined,
			error: true,
			onRetry: () => {
				retries += 1;
			},
		});
		expect(screen.getByText("The timeline did not load")).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Try again" }));
		expect(retries).toBe(1);
	});

	test("no events says the control plane has recorded none", () => {
		renderTab({ events: [] });
		expect(screen.getByText("No events yet")).toBeInTheDocument();
	});

	test("no data yet (neither pending nor failed) is the empty state", () => {
		renderTab({ events: undefined });
		expect(screen.getByText("No events yet")).toBeInTheDocument();
	});
});

describe("TimelineTab rows", () => {
	test("lists every raw event in order, including started rows and status transitions", () => {
		renderTab({ events: failedBuild });
		const rows = within(
			screen.getByRole("list", { name: "Raw deployment events" }),
		).getAllByRole("listitem");

		expect(rows).toHaveLength(5);
		expect(rows[0]).toHaveTextContent("status → queued");
		expect(rows[1]).toHaveTextContent("pull · started");
		expect(rows[2]).toHaveTextContent("pull · succeeded");
		expect(rows[3]).toHaveTextContent("Pre-deploy · started");
		expect(rows[4]).toHaveTextContent("Pre-deploy · failed");
		expect(rows[4]).toHaveTextContent("migration exited 2");
	});

	test("orders by event id whatever order they arrive in", () => {
		renderTab({ events: [...failedBuild].reverse() });
		const rows = within(
			screen.getByRole("list", { name: "Raw deployment events" }),
		).getAllByRole("listitem");
		expect(rows[0]).toHaveTextContent("status → queued");
		expect(rows[4]).toHaveTextContent("Pre-deploy · failed");
	});

	test("shows the offset from creation and from the previous event on every row", () => {
		renderTab({ events: failedBuild });
		const rows = within(
			screen.getByRole("list", { name: "Raw deployment events" }),
		).getAllByRole("listitem");

		// since start / since last
		expect(rows[1]).toHaveTextContent("+200 ms");
		expect(rows[1]).toHaveTextContent("+100 ms");
		expect(rows[2]).toHaveTextContent("+2.200 s");
		expect(rows[2]).toHaveTextContent("+2.000 s");
		expect(rows[4]).toHaveTextContent("+12.300 s");
		expect(rows[4]).toHaveTextContent("+10.000 s");
	});

	test("the summary counts events, the total span and the longest wait", () => {
		renderTab({ events: failedBuild });
		expect(
			screen.getByText(
				"5 events · 12.300 s from creation to the last event · longest wait 10.000 s before Pre-deploy failed",
			),
		).toBeInTheDocument();
	});

	test("marks only the longest wait", () => {
		renderTab({ events: failedBuild });
		const rows = within(
			screen.getByRole("list", { name: "Raw deployment events" }),
		).getAllByRole("listitem");
		const slowest = rows.filter((row) => row.hasAttribute("data-slowest"));
		expect(slowest).toHaveLength(1);
		expect(slowest[0]).toBe(rows[4]);
		expect(
			within(rows[4] as HTMLElement).getByText("longest wait"),
		).toBeInTheDocument();
		expect(
			within(rows[4] as HTMLElement).getByTitle(
				"The longest wait of this deployment",
			),
		).toBeInTheDocument();
	});

	test("a status as the longest wait reads 'before status → …' with nothing trailing", () => {
		renderTab({
			events: [
				event(1, "pull", "succeeded", "10:00:00.100"),
				event(2, "status", "rolled_back", "10:00:05.100"),
			],
		});
		expect(
			screen.getByText(/longest wait 5\.000 s before status → rolled back$/),
		).toBeInTheDocument();
	});

	test("a single event has a total but no longest wait", () => {
		renderTab({ events: [event(1, "pull", "started", "10:00:00.500")] });
		expect(
			screen.getByText("1 events · 500 ms from creation to the last event"),
		).toBeInTheDocument();
		expect(screen.queryByText(/longest wait/)).not.toBeInTheDocument();
	});

	test("events with unreadable clocks list without offsets or a total", () => {
		renderTab({
			events: [
				{
					id: 1,
					step: "pull",
					status: "started",
					createdAt: "not a date",
					message: "",
				},
			],
		});
		const row = within(
			screen.getByRole("list", { name: "Raw deployment events" }),
		).getByRole("listitem");
		expect(row).toHaveTextContent("pull · started");
		expect(screen.getByText("1 events")).toBeInTheDocument();
	});

	test("a newest started row spins only while the deployment is live", () => {
		const events = [
			event(1, "pull", "succeeded", "10:00:00.100"),
			event(2, "build", "started", "10:00:00.200"),
		];
		const { unmount } = renderTab({ events, live: true });
		expect(
			screen.getByRole("presentation", { hidden: true }),
		).toBeInTheDocument();
		unmount();

		renderTab({ events, live: false });
		expect(
			screen.queryByRole("presentation", { hidden: true }),
		).not.toBeInTheDocument();
	});

	test("an earlier started row does not spin even while live", () => {
		renderTab({
			events: [
				event(1, "pull", "started", "10:00:00.100"),
				event(2, "pull", "succeeded", "10:00:00.200"),
			],
			live: true,
		});
		expect(
			screen.queryByRole("presentation", { hidden: true }),
		).not.toBeInTheDocument();
	});
});
