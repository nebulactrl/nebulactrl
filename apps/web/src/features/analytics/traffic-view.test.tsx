import { describe, expect, mock, setDefaultTimeout, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { renderWithProviders } from "#/test/render";
import { stubResizeObserver } from "./resize-stub";
import { makeGeo, makeReport } from "./test-fixtures";
import { TrafficView, type TrafficViewProps } from "./traffic-view";

// A whole dashboard renders per test; a loaded CI box needs more than 5 s.
setDefaultTimeout(20_000);

const UPDATED_AT = Date.UTC(2026, 0, 1, 12);

async function view(props: Partial<TrafficViewProps> = {}) {
	return renderWithProviders(
		<TrafficView
			report={makeReport()}
			range="1h"
			updatedAt={UPDATED_AT}
			pagesOnly
			onPagesOnlyChange={() => {}}
			{...props}
		/>,
	);
}

/** The value next to a label in the secondary stats strip. */
function stat(label: string) {
	const dt = screen.getByText(label, { selector: "dt" });
	return dt.nextElementSibling as HTMLElement;
}

/** Holds `pagesOnly` in state and reports every change to `onChange`. */
function Harness({ onChange }: { onChange: (next: boolean) => void }) {
	const [pagesOnly, setPagesOnly] = useState(true);
	return (
		<TrafficView
			report={makeReport({ topPaths: [] })}
			range="1h"
			updatedAt={UPDATED_AT}
			pagesOnly={pagesOnly}
			onPagesOnlyChange={(next) => {
				onChange(next);
				setPagesOnly(next);
			}}
		/>
	);
}

describe("TrafficView headline numbers", () => {
	test("KPI tiles and the secondary strip format the summary", async () => {
		await view();
		expect(
			screen.getByText("Requests", { selector: "span" }),
		).toBeInTheDocument();
		expect(screen.getByText("12.4k")).toBeInTheDocument();
		expect(screen.getByText("900")).toBeInTheDocument();
		expect(screen.getByText("5 MiB")).toBeInTheDocument();
		expect(screen.getByText("180 ms")).toBeInTheDocument();
		expect(stat("Error rate")).toHaveTextContent("0.42%");
		expect(stat("Client errors")).toHaveTextContent("2");
		expect(stat("p50")).toHaveTextContent("21 ms");
		expect(stat("p99")).toHaveTextContent("950 ms");
		expect(stat("Peak rps")).toHaveTextContent("4.3");
		// 1240 bots of 12400 requests.
		expect(stat("Bot share")).toHaveTextContent("10");
	});

	test("peak rps drops its decimal from 10 up", async () => {
		const base = makeReport();
		await view({
			report: { ...base, summary: { ...base.summary, peakRps: 42.6 } },
		});
		expect(stat("Peak rps")).toHaveTextContent(/^43$/);
	});

	test("no comparison means no delta chips", async () => {
		await view();
		expect(screen.queryByText(/vs previous/)).not.toBeInTheDocument();
	});

	test("a comparison adds a delta chip to every tile, titled with the range", async () => {
		const base = makeReport();
		await view({
			report: {
				...base,
				comparison: {
					...base.summary,
					requests: 10_000,
					visitors: 1_000,
					bytesOut: 5 * 1024 * 1024,
					p95Ms: 200,
				},
			},
		});
		const chips = screen
			.getAllByTitle("vs previous 1h")
			.filter((el) => el.getAttribute("data-slot") === "badge");
		expect(chips.map((c) => c.textContent)).toEqual([
			"24%",
			"10%",
			"0%",
			"10%",
		]);
		// More requests is good; fewer visitors is bad; faster p95 is good.
		expect(chips.map((c) => c.getAttribute("data-tone"))).toEqual([
			"ok",
			"bad",
			"off",
			"ok",
		]);
	});

	test("an idle window against an idle previous window shows no delta at all", async () => {
		const base = makeReport();
		const zero = { ...base.summary, requests: 0, visitors: 0, bytesOut: 0 };
		await view({ report: { ...base, summary: zero, comparison: zero } });
		expect(screen.queryByTitle("vs previous 1h")).not.toBeInTheDocument();
		// And latency is an empty mark, not "<1 ms".
		expect(stat("p50")).toHaveTextContent("–");
		expect(stat("p99")).toHaveTextContent("–");
	});

	test("the footnote states the retention", async () => {
		await view();
		expect(
			screen.getByText(
				/History kept: 48 h per minute, 30 d per hour, 365 d per day/,
			),
		).toBeInTheDocument();
		expect(screen.queryByText(/reaches further back/)).not.toBeInTheDocument();
	});

	test("the footnote flags a range that reaches past the kept history", async () => {
		const base = makeReport();
		await view({
			report: { ...base, range: { ...base.range, truncated: true } },
		});
		expect(screen.getByText(/reaches further back/)).toBeInTheDocument();
	});
});

describe("TrafficView full dashboard", () => {
	test("shows the status chart, latency chart and ranked lists", async () => {
		await view();
		expect(
			screen.getByRole("heading", { name: "Requests by status class" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "Latency" }),
		).toBeInTheDocument();
		// 3 of 303 series requests are... server errors: 6 5xx of 303.
		expect(screen.getByText(/12\.4k requests · /)).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "Top pages" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "Status codes" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "Top not found" }),
		).toBeInTheDocument();
		expect(screen.getByText("/wp-login.php")).toBeInTheDocument();
	});

	test("top server errors rank paths by 5xx count, not volume", async () => {
		await view();
		const section = screen
			.getByRole("heading", { name: "Top server errors" })
			.closest("section") as HTMLElement;
		const rows = within(section).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("/api/orders");
		expect(rows[0]).toHaveTextContent("50");
		expect(rows[1]).toHaveTextContent("/api/users");
		expect(rows[1]).toHaveTextContent("8");
		expect(within(section).queryByText("/")).not.toBeInTheDocument();
	});

	test("with no 5xx or 404 paths, both lists say so", async () => {
		await view({
			report: makeReport({
				topPaths: [{ k: "/", n: 10, s5xx: 0 }],
				notFound: [],
			}),
		});
		expect(screen.getByText("No 404s in this range.")).toBeInTheDocument();
		expect(
			screen.getByText("No 5xx responses in this range."),
		).toBeInTheDocument();
	});

	test("the Pages/All toggle reports the choice and the title follows pagesOnly", async () => {
		const seen: boolean[] = [];
		const user = userEvent.setup();
		await renderWithProviders(<Harness onChange={(next) => seen.push(next)} />);
		expect(
			screen.getByRole("heading", { name: "Top pages" }),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"No page views in this range. Try All requests, or a longer range.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "All requests" }));
		expect(seen).toEqual([false]);
		expect(
			screen.getByRole("heading", { name: "Top paths" }),
		).toBeInTheDocument();
		expect(
			screen.getByText("No requests in this range. Try a longer range."),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Pages" }));
		expect(seen).toEqual([false, true]);
		expect(
			screen.getByRole("heading", { name: "Top pages" }),
		).toBeInTheDocument();
	});

	test("the Sources tabs switch between referrers, browsers, OS, devices and hosts", async () => {
		const user = userEvent.setup();
		await view();
		// Referrers first; the blank key reads Direct.
		expect(screen.getByText("Direct / none")).toBeInTheDocument();
		expect(screen.getByText("news.example.org")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Browsers" }));
		expect(screen.getByText("Firefox")).toBeInTheDocument();
		expect(screen.queryByText("news.example.org")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "OS" }));
		expect(screen.getByText("Linux")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Devices" }));
		expect(screen.getByText("desktop")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Hosts" }));
		expect(screen.getByText("app.example.com")).toBeInTheDocument();
		expect(screen.queryByText("desktop")).not.toBeInTheDocument();
	});

	test("an empty Sources tab says why", async () => {
		const user = userEvent.setup();
		await view({
			report: makeReport({
				topReferrers: [],
				topBrowsers: [],
				topOS: [],
				topDevices: [],
				topHosts: [],
			}),
		});
		expect(
			screen.getByText(/^No referrers in this range\./),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Browsers" }));
		expect(
			screen.getByText(/^No browsers in this range\./),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "OS" }));
		expect(
			screen.getByText(/^No operating systems in this range\./),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Devices" }));
		expect(screen.getByText(/^No devices in this range\./)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Hosts" }));
		expect(
			screen.getByText(/^No hostnames in this range\./),
		).toBeInTheDocument();
	});

	test("hosts are clickable only when onHostSelect is given", async () => {
		const user = userEvent.setup();
		const onHostSelect = mock((_h: string) => {});
		await view({ onHostSelect });
		await user.click(screen.getByRole("button", { name: "Hosts" }));
		await user.click(screen.getByRole("button", { name: /www\.example\.com/ }));
		expect(onHostSelect).toHaveBeenCalledWith("www.example.com");
	});

	test("without onHostSelect hosts are plain rows", async () => {
		const user = userEvent.setup();
		await view();
		await user.click(screen.getByRole("button", { name: "Hosts" }));
		expect(screen.getByText("www.example.com")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /www\.example\.com/ }),
		).not.toBeInTheDocument();
	});

	test("active hours appear only when the report has the grid", async () => {
		const { unmount } = await view();
		expect(
			screen.queryByRole("heading", { name: "Active hours" }),
		).not.toBeInTheDocument();
		unmount();
		const grid = Array.from({ length: 7 }, () => Array(24).fill(1));
		await view({ report: makeReport({ activeHours: grid }) });
		expect(
			screen.getByRole("heading", { name: "Active hours" }),
		).toBeInTheDocument();
		expect(screen.getByText("busiest hour 1 requests")).toBeInTheDocument();
	});

	test("unrouted hosts are listed when requests reached the server for unknown hostnames", async () => {
		const { unmount } = await view();
		expect(
			screen.queryByRole("heading", { name: "Unrouted hosts" }),
		).not.toBeInTheDocument();
		unmount();
		await view({
			report: makeReport({
				unrouted: {
					requests: 1_500,
					hosts: [{ k: "scanner.example.net", n: 1_500 }],
				},
			}),
		});
		expect(
			screen.getByRole("heading", { name: "Unrouted hosts" }),
		).toBeInTheDocument();
		expect(
			screen.getByText(/1\.5k requests reached this server for a hostname/),
		).toBeInTheDocument();
		expect(screen.getByText("scanner.example.net")).toBeInTheDocument();
	});

	test("an unrouted block with zero requests is left out", async () => {
		await view({
			report: makeReport({ unrouted: { requests: 0, hosts: [] } }),
		});
		expect(
			screen.queryByRole("heading", { name: "Unrouted hosts" }),
		).not.toBeInTheDocument();
	});

	test("deployments become markers on the charts", async () => {
		const restore = stubResizeObserver({ width: 600, height: 200 });
		const at = new Date(UPDATED_AT - 30 * 60_000).toISOString();
		const { container } = await view({
			report: makeReport({
				deployments: [{ id: "deploy-long-id", at, sha7: "abc1234" } as never],
			}),
		});
		try {
			await screen.findAllByText("abc1234", { selector: "text, tspan" });
			expect(
				container.querySelectorAll(".recharts-reference-line").length,
			).toBeGreaterThan(0);
		} finally {
			restore();
		}
	});
});

describe("TrafficView summary variant", () => {
	test("stops after the status chart and shows the origins card instead", async () => {
		await view({ variant: "summary" });
		expect(
			screen.getByRole("heading", { name: "Requests by status class" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "Where requests come from" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("heading", { name: "Latency" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("heading", { name: "Top pages" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("heading", { name: "Sources" }),
		).not.toBeInTheDocument();
	});

	test("origins in the full view show the server's settings link when geolocation is off", async () => {
		await view({ geoServerId: "srv-1" });
		const link = screen.getByRole("button", { name: "Open traffic settings" });
		expect(link).toHaveAttribute("href", "/servers/srv-1/settings/monitoring");
	});

	test("origins in the full view list countries when geo data is present", async () => {
		await view({ report: makeReport({ geo: makeGeo() }) });
		expect(screen.getByText("Germany")).toBeInTheDocument();
		expect(screen.getByText("United States")).toBeInTheDocument();
	});
});
