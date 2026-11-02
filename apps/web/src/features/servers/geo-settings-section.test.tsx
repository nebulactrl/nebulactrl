import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { getMaxMindCredentialsOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { GeoCredentialsStatus, Server } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import {
	type GeoDraft,
	GeoFields,
	geoDraftBody,
	geoDraftOf,
	useGeoProblem,
} from "./geo-settings-section";
import { type ServerView, toServerView } from "./model";

function serverView(over: Partial<ServerView> = {}): ServerView {
	const wire = {
		id: "srv-1",
		name: "edge-1",
		status: "online",
		publicIp: null,
		baseDomain: null,
		acmeEmail: null,
		agentVersion: null,
		protocolVersion: null,
		facts: {
			arch: "amd64",
			cgroupVersion: "2",
			cpus: 1,
			diskTotal: 1,
			dockerVersion: "27",
			kernel: "6",
			memTotal: 1,
			os: "Ubuntu",
			port443Free: true,
			port80Free: true,
		},
		lastSeenAt: null,
		createdAt: "2026-10-01T00:00:00Z",
		composeVersion: "",
		proxy: {
			status: "ok",
			v2: true,
			generation: 1,
			appliedGeneration: 1,
			tailnet: {
				appliedMode: "off",
				hostname: "",
				ips: null,
				mode: "off",
				state: "",
			},
		},
		latest: {
			cpuPercent: 0,
			diskTotal: 0,
			diskUsed: 0,
			memTotal: 0,
			memUsed: 0,
			sampledAt: "",
		},
		serviceCount: 0,
		drainState: "none",
		drainMessage: "",
		drainStartedAt: null,
		acceptWorkloads: true,
		buildEnabled: false,
		buildMaxConcurrent: 2,
		buildAllowEmulation: false,
		buildAllowPreviews: false,
		controlPlane: false,
		unrestricted: false,
		canDecommission: true,
		diskAlert: false,
		diskAlertThresholdPercent: 85,
		metricsRetentionDays: 30,
		trafficAnalyticsEnabled: true,
		trafficHourlyRetentionDays: 14,
		trafficDailyRetentionDays: 90,
		geoEnabled: true,
		geoSource: "dbip",
		geoLevel: "country",
		geoRefreshDays: 7,
		patches: {
			busy: "",
			checkedAt: null,
			known: false,
			rebootRequired: false,
			security: 0,
			supported: true,
			updates: 0,
		},
		tunnel: {
			active: false,
			connections: 0,
			enabled: false,
			observedAt: null,
			state: "off",
		},
	} satisfies Server;
	return { ...toServerView(wire), ...over };
}

const DRAFT: GeoDraft = {
	enabled: true,
	source: "dbip",
	level: "country",
	refreshDays: "7",
};
const MAXMIND: GeoDraft = { ...DRAFT, source: "maxmind" };

/** The server the page refetches after a credentials change. */
const EDGE_1 = make("Server", { id: "srv-1", name: "edge-1" });

/** GeoFields with the draft state its parent form would own; the draft is mirrored into the page for assertions. */
function Harness({
	server = serverView(),
	initial = DRAFT,
	trafficOff = false,
}: {
	server?: ServerView;
	initial?: GeoDraft;
	trafficOff?: boolean;
}) {
	const [draft, setDraft] = useState(initial);
	return (
		<>
			<GeoFields
				server={server}
				draft={draft}
				onChange={setDraft}
				trafficOff={trafficOff}
			/>
			<output aria-label="draft">{JSON.stringify(draft)}</output>
		</>
	);
}

function currentDraft(): GeoDraft {
	return JSON.parse(screen.getByLabelText("draft").textContent ?? "");
}

function credentials(over: Partial<GeoCredentialsStatus> = {}) {
	return {
		accountId: "",
		configured: false,
		serversUsing: 0,
		updatedAt: null,
		...over,
	};
}

/**
 * A query client that already knows the team's MaxMind account, as it does
 * once the monitoring page has loaded: the account block reads it when it
 * first appears.
 */
function knownAccount(status: GeoCredentialsStatus) {
	const client = testQueryClient();
	client.setQueryData(getMaxMindCredentialsOptions().queryKey, status);
	return client;
}

/** Renders useGeoProblem's answer as text. */
function Probe({
	id,
	active,
	draft,
}: {
	id: string;
	active: boolean;
	draft: GeoDraft;
}) {
	const problem = useGeoProblem(draft, active);
	return <output aria-label={`problem ${id}`}>{problem ?? "none"}</output>;
}

describe("geoDraftOf / geoDraftBody", () => {
	test("the draft mirrors the saved settings, with the interval as text", () => {
		const base = serverView();
		const draft = geoDraftOf({
			...base,
			settings: {
				...base.settings,
				geoEnabled: false,
				geoSource: "maxmind",
				geoLevel: "city",
				geoRefreshDays: 30,
			},
		});
		expect(draft).toEqual({
			enabled: false,
			source: "maxmind",
			level: "city",
			refreshDays: "30",
		});
	});

	test("the PATCH fields carry the choices and a parsed interval", () => {
		expect(
			geoDraftBody({ ...DRAFT, level: "city", refreshDays: "30" }),
		).toEqual({
			geoEnabled: true,
			geoSource: "dbip",
			geoLevel: "city",
			geoRefreshDays: 30,
		});
	});

	test("an invalid interval falls back to the minimum instead of NaN", () => {
		expect(geoDraftBody({ ...DRAFT, refreshDays: "abc" }).geoRefreshDays).toBe(
			1,
		);
		expect(geoDraftBody({ ...DRAFT, refreshDays: "" }).geoRefreshDays).toBe(1);
	});
});

describe("GeoFields: the fields", () => {
	test("show the draft's switch, database, detail and interval", async () => {
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(<Harness />);
		expect(
			screen.getByRole("switch", { name: /Geolocate requests/ }),
		).toBeChecked();
		expect(
			screen.getByRole("combobox", { name: "Geolocation database" }),
		).toHaveTextContent("DB-IP Lite (no key)");
		expect(
			screen.getByRole("combobox", { name: "Geolocation detail" }),
		).toHaveTextContent("Country (~8 MB)");
		expect(
			screen.getByLabelText("Refresh the database every (days)"),
		).toHaveValue(7);
		expect(screen.queryByText("MaxMind account")).toBeNull();
	});

	test("the switch and the interval write to the draft", async () => {
		const user = userEvent.setup();
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(<Harness />);
		await user.click(
			screen.getByRole("switch", { name: /Geolocate requests/ }),
		);
		expect(currentDraft().enabled).toBe(false);
		const days = screen.getByLabelText("Refresh the database every (days)");
		await user.clear(days);
		await user.type(days, "14");
		expect(currentDraft().refreshDays).toBe("14");
	});

	test("choosing City and MaxMind writes the level and the source", async () => {
		const user = userEvent.setup();
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(<Harness />);
		await user.click(
			screen.getByRole("combobox", { name: "Geolocation detail" }),
		);
		await user.click(
			await screen.findByRole("option", { name: "City (~130 MB)" }),
		);
		expect(currentDraft().level).toBe("city");
		await user.click(
			screen.getByRole("combobox", { name: "Geolocation database" }),
		);
		await user.click(
			await screen.findByRole("option", { name: "MaxMind GeoLite2 (key)" }),
		);
		expect(currentDraft().source).toBe("maxmind");
		expect(await screen.findByText("MaxMind account")).toBeInTheDocument();
	});

	test("an interval outside 1–90 is flagged on the field", async () => {
		const user = userEvent.setup();
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(<Harness />);
		const days = screen.getByLabelText("Refresh the database every (days)");
		expect(days).toBeValid();
		await user.clear(days);
		await user.type(days, "100");
		expect(await screen.findByText("Use 1–90 days.")).toBeVisible();
		expect(days).toBeInvalid();
	});

	test("with analytics off the switch is disabled and says why", async () => {
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(<Harness trafficOff />);
		expect(
			screen.getByRole("switch", { name: /Geolocate requests/ }),
		).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText(
				"Needs traffic analytics, which is off for this server.",
			),
		).toBeVisible();
	});

	test("reads the team's MaxMind account from GET /geo/maxmind", async () => {
		const api = stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(<Harness />);
		await waitFor(() =>
			expect(api.calls.map((c) => c.path)).toContain("/geo/maxmind"),
		);
		expect(api.unmatched).toEqual([]);
	});
});

describe("GeoFields: database status", () => {
	test("off: says the server keeps no database", async () => {
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		const server = serverView();
		await renderWithProviders(
			<Harness
				server={{
					...server,
					settings: { ...server.settings, geoEnabled: false },
				}}
			/>,
		);
		expect(
			screen.getByText(
				"No database. Geolocation is off, so the server keeps none.",
			),
		).toBeVisible();
	});

	test("an offline agent leaves the status unknown", async () => {
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(
			<Harness server={serverView({ status: "offline" })} />,
		);
		expect(
			screen.getByText("Unknown while the agent is offline."),
		).toBeVisible();
	});

	test("before the agent reports, the status says so", async () => {
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(<Harness />);
		expect(screen.getByText("Not reported yet.")).toBeVisible();
	});

	test("a downloaded database lists its source, release and size", async () => {
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(
			<Harness
				server={serverView({
					geoStatus: {
						downloaded: true,
						source: "dbip",
						level: "country",
						label: "2026-10",
						sizeBytes: 8 * 1024 * 1024,
					},
				})}
			/>,
		);
		const status = document.getElementById("geo-status") as HTMLElement;
		expect(within(status).getByText("Downloaded.")).toBeVisible();
		expect(within(status).getByText("DB-IP Lite, country level")).toBeVisible();
		expect(within(status).getByText("2026-10 release")).toBeVisible();
		expect(within(status).getByText(/^8(\.0)? ?MiB$/)).toBeVisible();
	});

	test("a failed download shows the agent's error", async () => {
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(
			<Harness
				server={serverView({
					geoStatus: { downloaded: false, error: "licence key rejected" },
				})}
			/>,
		);
		expect(screen.getByText("Not downloaded.")).toBeVisible();
		expect(
			screen.getByText(
				"licence key rejected. The agent tries again by itself.",
			),
		).toBeVisible();
	});
});

describe("GeoFields: MaxMind account", () => {
	test("with no stored key the form is open, Save key waits for valid input and there is no Cancel", async () => {
		const user = userEvent.setup();
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(<Harness initial={MAXMIND} />);
		const save = await screen.findByRole("button", { name: "Save key" });
		expect(save).toBeDisabled();
		expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
		await user.type(screen.getByLabelText("MaxMind account ID"), "12ab");
		await user.type(screen.getByLabelText("MaxMind licence key"), "secret_key");
		expect(save).toBeDisabled();
		await user.clear(screen.getByLabelText("MaxMind account ID"));
		await user.type(screen.getByLabelText("MaxMind account ID"), "123456");
		expect(save).toBeEnabled();
		await user.clear(screen.getByLabelText("MaxMind licence key"));
		await user.type(screen.getByLabelText("MaxMind licence key"), "abc");
		expect(save).toBeDisabled();
	});

	test("Save key PUTs the trimmed account and key, toasts, and shows the stored account", async () => {
		const user = userEvent.setup();
		let stored = credentials();
		const api = stubApi({
			"GET /geo/maxmind": () => Response.json(stored),
			"PUT /geo/maxmind": () => {
				stored = credentials({ accountId: "123456", configured: true });
				return Response.json(stored);
			},
			"GET /servers/:serverId": { body: EDGE_1 },
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(<Harness initial={MAXMIND} />);
		await user.type(
			await screen.findByLabelText("MaxMind account ID"),
			" 123456 ",
		);
		await user.type(
			screen.getByLabelText("MaxMind licence key"),
			" abc123_-XYZ ",
		);
		await user.click(screen.getByRole("button", { name: "Save key" }));

		expect(
			await screen.findByText("MaxMind account saved"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"The key is stored encrypted and sent to agents that use it.",
			),
		).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/geo/maxmind");
		expect(put?.body).toEqual({
			accountId: "123456",
			licenseKey: "abc123_-XYZ",
		});
		expect(
			await screen.findByText("Account 123456 · licence key stored"),
		).toBeVisible();
		expect(screen.queryByLabelText("MaxMind licence key")).toBeNull();
	});

	test("Enter in a field saves the key without submitting the surrounding form", async () => {
		const user = userEvent.setup();
		let stored = credentials();
		let submitted = false;
		const api = stubApi({
			"GET /geo/maxmind": () => Response.json(stored),
			"PUT /geo/maxmind": () => {
				stored = credentials({ accountId: "42", configured: true });
				return Response.json(stored);
			},
			"GET /servers/:serverId": { body: EDGE_1 },
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(
			<form
				onSubmit={(e) => {
					e.preventDefault();
					submitted = true;
				}}
			>
				<Harness initial={MAXMIND} />
			</form>,
		);
		await user.type(await screen.findByLabelText("MaxMind account ID"), "42");
		await user.type(
			screen.getByLabelText("MaxMind licence key"),
			"abcdef{Enter}",
		);
		expect(
			await screen.findByText("MaxMind account saved"),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "PUT")).toHaveLength(1);
		expect(submitted).toBe(false);
	});

	test("Enter with an invalid key sends nothing", async () => {
		const user = userEvent.setup();
		const api = stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(<Harness initial={MAXMIND} />);
		await user.type(
			await screen.findByLabelText("MaxMind account ID"),
			"42{Enter}",
		);
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	test("a rejected key shows the API's reason and keeps the form open", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /geo/maxmind": { body: credentials() },
			"PUT /geo/maxmind": problem(422, {
				title: "Unprocessable",
				detail: "MaxMind refused this key",
			}),
		});
		await renderWithProviders(<Harness initial={MAXMIND} />);
		await user.type(await screen.findByLabelText("MaxMind account ID"), "42");
		await user.type(screen.getByLabelText("MaxMind licence key"), "abcdef");
		await user.click(screen.getByRole("button", { name: "Save key" }));
		expect(await screen.findByText(/MaxMind refused this key/)).toBeVisible();
		expect(screen.queryByText("MaxMind account saved")).toBeNull();
		expect(screen.getByLabelText("MaxMind licence key")).toHaveValue("abcdef");
	});

	test("a stored key shows its account; Replace key opens the form with a Cancel that closes it again", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /geo/maxmind": {
				body: credentials({ accountId: "777", configured: true }),
			},
		});
		await renderWithProviders(<Harness initial={MAXMIND} />, {
			queryClient: knownAccount(
				credentials({ accountId: "777", configured: true }),
			),
		});
		expect(
			await screen.findByText("Account 777 · licence key stored"),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Replace key" }));
		expect(screen.getByLabelText("MaxMind account ID")).toHaveValue("777");
		expect(screen.getByLabelText("MaxMind licence key")).toHaveValue("");
		await user.type(screen.getByLabelText("MaxMind licence key"), "typed-key");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(screen.getByText("Account 777 · licence key stored")).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Replace key" }));
		expect(screen.getByLabelText("MaxMind licence key")).toHaveValue("");
	});

	test("Remove DELETEs the account, toasts and reopens the empty form", async () => {
		const user = userEvent.setup();
		let stored = credentials({ accountId: "777", configured: true });
		const api = stubApi({
			"GET /geo/maxmind": () => Response.json(stored),
			"DELETE /geo/maxmind": () => {
				stored = credentials();
				return new Response(null, { status: 204 });
			},
			"GET /servers/:serverId": { body: EDGE_1 },
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(<Harness initial={MAXMIND} />, {
			queryClient: knownAccount(stored),
		});
		await user.click(await screen.findByRole("button", { name: "Remove" }));
		expect(
			await screen.findByText("MaxMind account removed"),
		).toBeInTheDocument();
		expect(
			api.calls.some((c) => c.method === "DELETE" && c.path === "/geo/maxmind"),
		).toBe(true);
		expect(await screen.findByLabelText("MaxMind account ID")).toHaveValue("");
	});

	test("Remove is disabled while servers use MaxMind and names how many", async () => {
		stubApi({
			"GET /geo/maxmind": {
				body: credentials({
					accountId: "777",
					configured: true,
					serversUsing: 2,
				}),
			},
		});
		await renderWithProviders(<Harness initial={MAXMIND} />, {
			queryClient: knownAccount(
				credentials({ accountId: "777", configured: true, serversUsing: 2 }),
			),
		});
		const remove = await screen.findByRole("button", { name: "Remove" });
		expect(remove).toBeDisabled();
		expect(remove).toHaveAttribute(
			"title",
			"2 server(s) use MaxMind; switch them to DB-IP first",
		);
	});

	test("a failed Remove shows the error and keeps the stored account", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /geo/maxmind": {
				body: credentials({ accountId: "777", configured: true }),
			},
			"DELETE /geo/maxmind": problem(409, {
				title: "Conflict",
				detail: "still in use",
			}),
		});
		await renderWithProviders(<Harness initial={MAXMIND} />, {
			queryClient: knownAccount(
				credentials({ accountId: "777", configured: true }),
			),
		});
		await user.click(await screen.findByRole("button", { name: "Remove" }));
		expect(await screen.findByText(/still in use/)).toBeVisible();
		expect(screen.getByText("Account 777 · licence key stored")).toBeVisible();
	});
});

describe("useGeoProblem", () => {
	test("is inert off the monitoring page: no problem, no request", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<Probe id="x" active={false} draft={{ ...MAXMIND, refreshDays: "0" }} />,
		);
		expect(screen.getByLabelText("problem x")).toHaveTextContent("none");
		expect(api.calls).toEqual([]);
	});

	test("names a bad refresh interval", async () => {
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(
			<Probe id="x" active draft={{ ...DRAFT, refreshDays: "0" }} />,
		);
		expect(screen.getByLabelText("problem x")).toHaveTextContent(
			"Use 1–90 days.",
		);
	});

	test("MaxMind without a stored account is a problem; DB-IP or a disabled switch is not", async () => {
		stubApi({ "GET /geo/maxmind": { body: credentials() } });
		await renderWithProviders(
			<>
				<Probe id="maxmind" active draft={MAXMIND} />
				<Probe id="dbip" active draft={DRAFT} />
				<Probe id="off" active draft={{ ...MAXMIND, enabled: false }} />
			</>,
		);
		expect(screen.getByLabelText("problem maxmind")).toHaveTextContent(
			"Add your MaxMind account ID and licence key first.",
		);
		expect(screen.getByLabelText("problem dbip")).toHaveTextContent("none");
		expect(screen.getByLabelText("problem off")).toHaveTextContent("none");
	});

	test("a stored MaxMind account clears the problem", async () => {
		stubApi({
			"GET /geo/maxmind": {
				body: credentials({ accountId: "1", configured: true }),
			},
		});
		await renderWithProviders(<Probe id="x" active draft={MAXMIND} />);
		await waitFor(() =>
			expect(screen.getByLabelText("problem x")).toHaveTextContent("none"),
		);
	});
});
