import { describe, expect, test } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { EnvironmentService } from "#/api/gen/types.gen";
import { renderWithProviders } from "#/test/render";
import {
	EnvironmentUptimeError,
	EnvironmentUptimeList,
	EnvironmentUptimeSkeleton,
} from "./environment-uptime";

const service = (
	name: string,
	patch: Partial<EnvironmentService> = {},
): EnvironmentService => ({
	serviceId: `id-${name}`,
	slug: name,
	name,
	kind: "app",
	state: "up",
	monitored: true,
	availability30d: 100,
	days: [],
	lastCheckAt: "2026-10-29T10:00:00Z",
	lastError: "",
	latencyMs: 12,
	since: null,
	...patch,
});

const render = (items: EnvironmentService[]) =>
	renderWithProviders(
		<EnvironmentUptimeList items={items} projectId="proj1" envSlug="prod" />,
	);

function Where() {
	return (
		<output aria-label="where">
			{useRouterState({ select: (s) => s.location.pathname })}
		</output>
	);
}

describe("EnvironmentUptimeList", () => {
	test("an environment without services says no uptime yet", async () => {
		await render([]);
		expect(
			screen.getByText(
				"No uptime yet. A service is checked once it is running.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});

	test("lists down services first, then degraded, then healthy by name", async () => {
		await render([
			service("web"),
			service("api", { state: "down", lastError: "HTTP 500" }),
			service("cache", { state: "degraded", lastError: "slow" }),
			service("auth"),
		]);
		const names = screen
			.getAllByRole("link", { name: /^(api|cache|auth|web)$/ })
			.map((a) => a.textContent);
		expect(names).toEqual(["api", "cache", "auth", "web"]);
	});

	test("a row links to the service's Overview in the environment", async () => {
		await render([service("api")]);
		const link = screen.getByRole("link", { name: "api" });
		expect(link.getAttribute("href")).toBe(
			"/p/proj1/prod?service=id-api&tab=overview",
		);
	});

	test("shows the 30-day availability and the state of each service", async () => {
		await render([
			service("api", { availability30d: 99.987 }),
			service("blank", { availability30d: null, state: "up" }),
		]);
		expect(screen.getByText("99.98%")).toBeInTheDocument();
		// null availability reads "No data" for screen readers, not 0%.
		expect(screen.getByText("No data")).toBeInTheDocument();
		expect(screen.queryByText("0%")).not.toBeInTheDocument();
		expect(screen.getAllByText("Up")).toHaveLength(2);
	});

	test("a failing service shows its last error under the name", async () => {
		await render([
			service("api", { state: "down", lastError: "connection refused" }),
		]);
		expect(screen.getByText("connection refused")).toBeInTheDocument();
		expect(screen.getByText("Down")).toBeInTheDocument();
	});

	test("an up service whose last check failed gets the Failing hint", async () => {
		await render([service("api", { state: "up", lastError: "timeout" })]);
		expect(screen.getByText("Failing")).toBeInTheDocument();
		expect(screen.queryByText("timeout")).not.toBeInTheDocument();
	});

	test("a service with monitoring off reads 'Monitoring off' whatever its state", async () => {
		await render([
			service("api", { state: "down", monitored: false, lastError: "x" }),
		]);
		expect(screen.getByText("Monitoring off")).toBeInTheDocument();
		expect(screen.queryByText("Down")).not.toBeInTheDocument();
		expect(screen.queryByText("x")).not.toBeInTheDocument();
	});

	test("a monitored service that stopped reporting says since when, and links to monitor settings", async () => {
		await render([
			service("api", {
				state: "unknown",
				lastCheckAt: "2020-03-04T09:05:00Z",
			}),
		]);
		expect(
			screen.getByText("No checks since 4 Mar 09:05 UTC"),
		).toBeInTheDocument();
		const settings = screen.getByRole("link", { name: "Monitor settings" });
		expect(settings.getAttribute("href")).toBe(
			"/p/proj1/prod?service=id-api&tab=settings&section=operations",
		);
	});

	test("an unknown service that never checked shows the plain state", async () => {
		await render([service("api", { state: "unknown", lastCheckAt: null })]);
		expect(screen.getByText("No data")).toBeInTheDocument();
		expect(screen.queryByText(/No checks since/)).not.toBeInTheDocument();
		expect(
			screen.queryByRole("link", { name: "Monitor settings" }),
		).not.toBeInTheDocument();
	});

	test("shows when the state began for a monitored service", async () => {
		await render([
			service("api", {
				since: new Date(Date.now() - 3 * 3600_000).toISOString(),
			}),
		]);
		expect(screen.getByText(/^since/)).toBeInTheDocument();
	});

	describe("folding a long list", () => {
		const long = () => [
			service("a1"),
			service("a2"),
			service("a3"),
			service("a4"),
			service("a5"),
			service("a6"),
			service("a7"),
			service("a8", { state: "paused" }),
		];
		const rowLinks = () =>
			screen.getAllByRole("link").map((a) => a.textContent);

		test("shows the first five rows and a button saying what is hidden", async () => {
			await render(long());
			expect(rowLinks()).toEqual(["a1", "a2", "a3", "a4", "a5"]);
			const more = screen.getByRole("button", {
				name: "Show 3 more: 2 up, 1 paused",
			});
			expect(more).toHaveAttribute("aria-expanded", "false");
		});

		test("Show more reveals every row and Show fewer folds again", async () => {
			const user = userEvent.setup();
			await render(long());
			await user.click(screen.getByRole("button", { name: /Show 3 more/ }));
			expect(rowLinks()).toHaveLength(8);
			const fewer = screen.getByRole("button", { name: "Show fewer" });
			expect(fewer).toHaveAttribute("aria-expanded", "true");
			await user.click(fewer);
			expect(rowLinks()).toHaveLength(5);
		});

		test("a failing row stays visible below the fold", async () => {
			const items = long();
			items[6] = service("a7", { state: "up", lastError: "timeout" });
			await render(items);
			// a6 and a8 hide; a7 is past the fifth row but is failing, so it stays.
			expect(rowLinks()).toEqual(["a1", "a2", "a3", "a4", "a5", "a7"]);
			expect(
				screen.getByRole("button", { name: "Show 2 more: 1 up, 1 paused" }),
			).toBeInTheDocument();
		});

		test("a single hidden row is not worth a button", async () => {
			await render(long().slice(0, 6));
			expect(rowLinks()).toHaveLength(6);
			expect(screen.queryByRole("button")).not.toBeInTheDocument();
		});
	});

	test("the wide layout has column headings", async () => {
		await render([service("api")]);
		expect(screen.getByText("Service")).toBeInTheDocument();
		expect(screen.getByText("Last 30 days, UTC")).toBeInTheDocument();
		expect(screen.getByText("Last check")).toBeInTheDocument();
		// "30 days" heads the column and labels the figure in the narrow layout.
		expect(screen.getAllByText("30 days")).toHaveLength(2);
	});

	test("the wide header and the narrow range row name the first and last day from the data", async () => {
		const days = (from: string, to: string): EnvironmentService["days"] =>
			[from, to].map((at) => ({
				at,
				avgLatencyMs: 0,
				checks: 1,
				degraded: 0,
				down: 0,
				maintenance: 0,
				maxLatencyMs: 0,
				up: 1,
			}));
		await render([
			// Sorts first but has no days, so the range comes from the next one.
			service("api", { state: "down", lastError: "HTTP 500", days: [] }),
			service("web", { days: days("2026-03-02T00:00:00Z", "2026-03-31") }),
		]);
		// The days also render in the bars; the header row is the one with "Service".
		const header = screen.getByText("Service").parentElement as HTMLElement;
		expect(within(header).getByText("Mar 2, 2026")).toBeInTheDocument();
		expect(within(header).getByText("Mar 31, 2026")).toBeInTheDocument();
		expect(within(header).getByTitle("Last 30 days, UTC")).toBeInTheDocument();
		expect(
			within(header).queryByText("Last 30 days, UTC"),
		).not.toBeInTheDocument();
		// The narrow layout has no header, so it names the same range once, not per row.
		const narrow = screen
			.getAllByTitle("Last 30 days, UTC")
			.find((el) => !header.contains(el)) as HTMLElement;
		expect(within(narrow).getByText("Mar 2, 2026")).toBeInTheDocument();
		expect(within(narrow).getByText("Mar 31, 2026")).toBeInTheDocument();
	});

	test("a link keeps the project and environment it was given", async () => {
		await renderWithProviders(
			<>
				<EnvironmentUptimeList
					items={[service("api")]}
					projectId="p9"
					envSlug="staging"
				/>
				<Where />
			</>,
		);
		const user = userEvent.setup();
		await user.click(screen.getByRole("link", { name: "api" }));
		expect(screen.getByLabelText("where")).toHaveTextContent("/p/p9/staging");
	});
});

describe("EnvironmentUptimeSkeleton", () => {
	test("announces that uptime is loading", async () => {
		await renderWithProviders(<EnvironmentUptimeSkeleton />);
		expect(
			screen.getByRole("status", { name: "Loading uptime" }),
		).toBeInTheDocument();
	});
});

describe("EnvironmentUptimeError", () => {
	test("says uptime did not load and retries on demand", async () => {
		let retries = 0;
		const user = userEvent.setup();
		await renderWithProviders(
			<EnvironmentUptimeError
				retry={() => {
					retries += 1;
				}}
			/>,
		);
		expect(
			screen.getByText(
				"Uptime did not load. The rest of the page is unaffected.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(retries).toBe(1);
	});
});
