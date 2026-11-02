import { describe, expect, spyOn, test } from "bun:test";
import { renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PrincipalBody, Server as ServerWire } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { factsWire, serverWire } from "./server.fixture";
import { useOpenDrain, useServer } from "./server-context";
import { ServerLayout } from "./server-layout";

const AGO = new Date(Date.now() - 5 * 60_000).toISOString();

const proxy = (
	over: Partial<ServerWire["proxy"]> = {},
): ServerWire["proxy"] => ({
	...serverWire().proxy,
	...over,
});

const patches = (
	over: Partial<ServerWire["patches"]> = {},
): ServerWire["patches"] => ({ ...serverWire().patches, ...over });

function wire(over: Partial<ServerWire> = {}): ServerWire {
	return serverWire({
		id: "srv_1",
		name: "edge-1",
		publicIp: "203.0.113.7",
		lastSeenAt: AGO,
		facts: factsWire({ os: "linux" }),
		latest: null,
		proxy: proxy({ generation: 1, appliedGeneration: 1 }),
		patches: patches({ known: true }),
		...over,
	});
}

const ME = (
	role: NonNullable<PrincipalBody["role"]>,
	instanceAdmin = false,
) => ({
	"GET /me": { body: make("MeBody", { principal: { role }, instanceAdmin }) },
});

const SERVER = (over: Partial<ServerWire> = {}) => ({
	"GET /servers/:serverId": { body: wire(over) },
});

/** A tab's content: shows the server from context and a key to open the drain sheet. */
function Tab() {
	const server = useServer();
	const openDrain = useOpenDrain();
	return (
		<div>
			<p data-testid="tab-server">{server.name}</p>
			<button type="button" onClick={openDrain}>
				open drain
			</button>
		</div>
	);
}

function renderLayout(
	path = "/servers/srv_1",
	props: {
		drainOpen?: boolean;
		onDrainOpenChange?: (open: boolean) => void;
	} = {},
) {
	return renderWithProviders(
		<ServerLayout serverId="srv_1" {...props}>
			<Tab />
		</ServerLayout>,
		{ path },
	);
}

const nav = () => screen.getByRole("navigation", { name: "Server sections" });
const tabNames = () =>
	Array.from(nav().querySelectorAll("a")).map((a) => a.textContent);

describe("ServerLayout: loading and failing", () => {
	test("shows a placeholder, not the tabs, until the server loads", async () => {
		stubApi({
			"GET /servers/:serverId": () => new Promise<Response>(() => {}),
			...ME("owner"),
		});
		await renderLayout();
		expect(
			screen.getByRole("status", { name: "Loading server" }),
		).toBeInTheDocument();
		expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(
			"Server",
		);
		expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
		expect(screen.queryByTestId("tab-server")).not.toBeInTheDocument();
	});

	test("a missing server says so, with no retry", async () => {
		stubApi({
			"GET /servers/:serverId": problem(404, { title: "Not Found" }),
			...ME("owner"),
		});
		await renderLayout();
		expect(
			await screen.findByText("This server does not exist"),
		).toBeInTheDocument();
		expect(screen.getByText(/It may have been removed/)).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /retry|try again/i }),
		).not.toBeInTheDocument();
		expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
	});

	test("another failure offers a retry that asks again and then shows the server", async () => {
		let calls = 0;
		const api = stubApi({
			"GET /servers/:serverId": () => {
				calls += 1;
				return calls === 1
					? Response.json({ title: "Internal", status: 500 }, { status: 500 })
					: Response.json(wire());
			},
			...ME("owner"),
		});
		const user = userEvent.setup();
		await renderLayout();
		expect(await screen.findByText("Server did not load")).toBeInTheDocument();
		expect(
			screen.getByText(/The control plane did not answer/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /retry|try again/i }));
		expect(await screen.findByTestId("tab-server")).toHaveTextContent("edge-1");
		expect(api.calls.filter((c) => c.path === "/servers/srv_1").length).toBe(2);
	});
});

describe("ServerLayout: the frame", () => {
	test("titles the page with the server's name and links back to the list", async () => {
		stubApi({ ...SERVER(), ...ME("owner") });
		await renderLayout();
		expect(
			await screen.findByRole("heading", { level: 1, name: "edge-1" }),
		).toBeInTheDocument();
		expect(document.title).toBe("edge-1 · NebulaCtrl");
		expect(screen.getByText("All servers").closest("a")).toHaveAttribute(
			"href",
			"/servers",
		);
	});

	test("hands the loaded server to the tab", async () => {
		stubApi({ ...SERVER(), ...ME("owner") });
		await renderLayout();
		expect(await screen.findByTestId("tab-server")).toHaveTextContent("edge-1");
	});

	test("the summary line gives address, last seen and proxy state", async () => {
		stubApi({ ...SERVER(), ...ME("owner") });
		await renderLayout();
		await screen.findByTestId("tab-server");
		expect(screen.getByText("203.0.113.7")).toBeInTheDocument();
		expect(screen.getByText(/^seen/)).toHaveTextContent(/5 minutes ago|5m ago/);
		expect(screen.getByText("Proxy ok")).toBeInTheDocument();
		expect(screen.queryByText("never seen")).not.toBeInTheDocument();
	});

	test("a host that never connected says so in the summary", async () => {
		stubApi({
			...SERVER({
				status: "pending",
				publicIp: null,
				lastSeenAt: null,
				proxy: proxy({ status: "", generation: 0, appliedGeneration: 0 }),
			}),
			...ME("owner"),
		});
		await renderLayout();
		await screen.findByTestId("tab-server");
		expect(screen.getByText("no public IP")).toBeInTheDocument();
		expect(screen.getByText("never seen")).toBeInTheDocument();
		expect(screen.getByText("Proxy not reported")).toBeInTheDocument();
	});

	test("a proxy error is quoted in the summary", async () => {
		stubApi({
			...SERVER({
				proxy: proxy({
					status: "error",
					error: "port 80 in use",
					generation: 1,
					appliedGeneration: 1,
				}),
			}),
			...ME("owner"),
		});
		await renderLayout();
		expect(
			await screen.findByText("Proxy error: port 80 in use"),
		).toBeVisible();
	});

	test("a server being drained shows its drain state beside the status", async () => {
		stubApi({ ...SERVER({ drainState: "draining" }), ...ME("owner") });
		await renderLayout();
		await screen.findByTestId("tab-server");
		expect(screen.getByText("Draining")).toBeInTheDocument();
	});
});

describe("ServerLayout: tabs", () => {
	test("owners get all six tabs, linking to their own addresses", async () => {
		stubApi({ ...SERVER(), ...ME("owner") });
		await renderLayout();
		await screen.findByTestId("tab-server");
		await waitFor(() =>
			expect(tabNames()).toEqual([
				"Overview",
				"Traffic",
				"Proxy",
				"Security",
				"Updates",
				"Settings",
			]),
		);
		const hrefs = Array.from(nav().querySelectorAll("a")).map((a) =>
			a.getAttribute("href"),
		);
		expect(hrefs).toEqual([
			"/servers/srv_1",
			"/servers/srv_1/traffic",
			"/servers/srv_1/proxy",
			"/servers/srv_1/security",
			"/servers/srv_1/updates",
			"/servers/srv_1/settings",
		]);
	});

	test("an admin gets Settings too", async () => {
		stubApi({ ...SERVER(), ...ME("admin") });
		await renderLayout();
		await screen.findByTestId("tab-server");
		await waitFor(() => expect(tabNames()).toContain("Settings"));
	});

	test("a developer has no settings to open, so no Settings tab", async () => {
		const api = stubApi({ ...SERVER(), ...ME("developer") });
		await renderLayout();
		await screen.findByTestId("tab-server");
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/me")).toBe(true),
		);
		expect(tabNames()).toEqual([
			"Overview",
			"Traffic",
			"Proxy",
			"Security",
			"Updates",
		]);
	});

	test("the tab for the current address is the current page", async () => {
		stubApi({ ...SERVER(), ...ME("owner") });
		await renderLayout("/servers/srv_1/proxy");
		await screen.findByTestId("tab-server");
		const current = Array.from(nav().querySelectorAll("a")).filter(
			(a) => a.getAttribute("aria-current") === "page",
		);
		expect(current.map((a) => a.textContent)).toEqual(["Proxy"]);
	});

	test("the bare server address is Overview", async () => {
		stubApi({ ...SERVER(), ...ME("owner") });
		await renderLayout("/servers/srv_1");
		await screen.findByTestId("tab-server");
		const current = Array.from(nav().querySelectorAll("a")).filter(
			(a) => a.getAttribute("aria-current") === "page",
		);
		expect(current.map((a) => a.textContent)).toEqual(["Overview"]);
	});

	test("Proxy is marked Staged while changes wait for a deploy", async () => {
		stubApi({
			...SERVER({
				proxy: proxy({ status: "ok", generation: 3, appliedGeneration: 2 }),
			}),
			...ME("owner"),
		});
		await renderLayout();
		await screen.findByTestId("tab-server");
		expect(screen.getByRole("link", { name: /Proxy/ })).toHaveTextContent(
			"ProxyStaged",
		);
	});

	test("Proxy carries no badge once everything is applied", async () => {
		stubApi({ ...SERVER(), ...ME("owner") });
		await renderLayout();
		await screen.findByTestId("tab-server");
		expect(screen.getByRole("link", { name: "Proxy" })).toBeInTheDocument();
		expect(screen.queryByText("Staged")).not.toBeInTheDocument();
	});

	test("Updates says Reboot when the host asks for one", async () => {
		stubApi({
			...SERVER({
				patches: patches({
					known: true,
					supported: true,
					security: 4,
					rebootRequired: true,
				}),
			}),
			...ME("owner"),
		});
		await renderLayout();
		await screen.findByTestId("tab-server");
		expect(screen.getByRole("link", { name: /Updates/ })).toHaveTextContent(
			"UpdatesReboot",
		);
	});

	test("Updates counts security updates otherwise", async () => {
		stubApi({
			...SERVER({
				patches: patches({
					known: true,
					supported: true,
					security: 2,
					rebootRequired: false,
				}),
			}),
			...ME("owner"),
		});
		await renderLayout();
		await screen.findByTestId("tab-server");
		expect(screen.getByRole("link", { name: /Updates/ })).toHaveTextContent(
			"Updates2 security",
		);
	});
});

describe("ServerLayout: drain sheet", () => {
	test("a tab asks the route to open the drain sheet", async () => {
		stubApi({ ...SERVER(), ...ME("owner") });
		const changes: boolean[] = [];
		const user = userEvent.setup();
		await renderLayout("/servers/srv_1", {
			onDrainOpenChange: (open) => changes.push(open),
		});
		await user.click(await screen.findByRole("button", { name: "open drain" }));
		expect(changes).toEqual([true]);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	test("the route's drain flag opens the sheet for this server", async () => {
		stubApi({
			...SERVER(),
			...ME("owner"),
			"GET /servers/:serverId/drain": { body: { drain: null } },
			"POST /servers/:serverId/drain/plan": {
				body: { moves: [], capacity: { targetFits: true, reasons: null } },
			},
		});
		await renderLayout("/servers/srv_1", { drainOpen: true });
		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveTextContent("edge-1");
	});
});

describe("server context", () => {
	test("a tab outside the server page's frame fails loudly", () => {
		const quiet = spyOn(console, "error").mockImplementation(() => {});
		try {
			expect(() => renderHook(() => useServer())).toThrow(
				"This needs the server page's frame",
			);
			expect(() => renderHook(() => useOpenDrain())).toThrow(
				"This needs the server page's frame",
			);
		} finally {
			quiet.mockRestore();
		}
	});
});
