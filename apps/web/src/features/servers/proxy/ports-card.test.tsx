import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ProxyL4Port, ProxyPlan, ProxyPorts } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { PortsCard } from "./ports-card";

const ports = (over: Partial<ProxyPorts> = {}): ProxyPorts => ({
	agentHttp: 80,
	agentHttps: 443,
	appliedHttp: 80,
	appliedHttps: 443,
	busyOther: null,
	http: 80,
	https: 443,
	reported: true,
	setHttp: null,
	setHttps: null,
	source: "flag",
	staged: false,
	supported: true,
	...over,
});

const plan = (over: Partial<ProxyPlan> = {}): ProxyPlan => ({
	diff: [{ field: "ports.http", from: "80", to: "8080" }],
	reasons: ["entrypoint ports changed"],
	restartRequired: true,
	warnings: null,
	...over,
});

const meOf = (instanceAdmin: boolean) => ({
	body: {
		id: "u1",
		name: "Ada",
		email: "ada@example.com",
		instanceAdmin,
		workspaceId: "w1",
		createdAt: "2026-01-01T00:00:00Z",
		principal: { type: "user", label: "Ada", scopes: [] },
	},
});
const ADMIN = meOf(true);
const MEMBER = meOf(false);

const problem = (status: number, detail: string, code = "bad_request") => ({
	status,
	body: { status, title: "Error", detail, code },
});

const PLAN = "POST /servers/:serverId/proxy/plan";
const SETTINGS = "PATCH /servers/:serverId/proxy/settings";

function renderCard(
	value: ProxyPorts,
	{
		agentTooOld = false,
		l4Ports = [],
	}: { agentTooOld?: boolean; l4Ports?: ProxyL4Port[] } = {},
) {
	return renderWithProviders(
		<PortsCard
			serverId="s1"
			serverName="edge-1"
			ports={value}
			l4Ports={l4Ports}
			agentTooOld={agentTooOld}
		/>,
	);
}

const httpField = () => screen.getByLabelText("HTTP port");
const httpsField = () => screen.getByLabelText("HTTPS port");
const review = () => screen.getByRole("button", { name: "Review change" });

/** The form only exists once /me has said the caller is the instance admin. */
async function adminForm() {
	await screen.findByLabelText("HTTP port");
}

describe("PortsCard display", () => {
	test("lists the applied ports and the HTTP/3 note", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(ports({ appliedHttp: 8080, http: 8080 }));
		const list = screen.getByRole("list", { name: "Proxy ports" });
		const [http, https] = within(list).getAllByRole("listitem");
		expect(http).toHaveTextContent("HTTP8080");
		expect(https).toHaveTextContent("HTTPS443also UDP (HTTP/3)");
		expect(within(list).queryByText("Staged")).not.toBeInTheDocument();
	});

	test("a staged move shows applied → next with a Staged badge, only for the port that moves", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(
			ports({ staged: true, http: 8080, setHttp: 8080, source: "server" }),
		);
		const [http, https] = within(
			screen.getByRole("list", { name: "Proxy ports" }),
		).getAllByRole("listitem");
		expect(http).toHaveTextContent("HTTP80→8080Staged");
		expect(https).not.toHaveTextContent("Staged");
		expect(https).not.toHaveTextContent("→");
	});

	test("staged equal to applied shows no move", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(ports({ staged: true, setHttp: 80, http: 80 }));
		expect(screen.queryByText("Staged")).not.toBeInTheDocument();
	});

	test("names the source: the agent's flags with their values, the server setting, or not reported", async () => {
		stubApi({ "GET /me": MEMBER });
		const flag = await renderCard(
			ports({ source: "flag", agentHttp: 8081, agentHttps: 8444 }),
		);
		expect(screen.getByTestId("ports-source")).toHaveTextContent(
			"Set in the agent's service flags (--proxy-http-port 8081, --proxy-https-port 8444).",
		);
		flag.unmount();

		stubApi({ "GET /me": MEMBER });
		const server = await renderCard(ports({ source: "server" }));
		expect(screen.getByTestId("ports-source")).toHaveTextContent(
			/^Server setting\.$/,
		);
		server.unmount();

		stubApi({ "GET /me": MEMBER });
		const flagUnreported = await renderCard(
			ports({ source: "flag", reported: false }),
		);
		expect(screen.getByTestId("ports-source")).toHaveTextContent(
			/^Set in the agent's service flags\.$/,
		);
		flagUnreported.unmount();

		stubApi({ "GET /me": MEMBER });
		await renderCard(ports({ source: "", reported: false }));
		expect(screen.getByTestId("ports-source")).toHaveTextContent(
			/^Not reported yet\.$/,
		);
	});

	test("says which of the standard ports another process holds", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(ports({ busyOther: [80, 443] }));
		expect(
			screen.getByText(
				"Another process on this server holds port 80 and 443 right now.",
			),
		).toBeInTheDocument();
	});

	test("says nothing about busy ports when none are held", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(ports({ busyOther: [] }));
		expect(screen.queryByText(/Another process/)).not.toBeInTheDocument();
	});

	test("an agent that cannot change ports says so", async () => {
		stubApi({ "GET /me": ADMIN });
		await renderCard(ports({ supported: false }));
		expect(
			screen.getByText(/cannot change its ports from the server/),
		).toBeInTheDocument();
		await adminForm();
		expect(httpField()).toBeDisabled();
		expect(httpsField()).toBeDisabled();
		expect(review()).toBeDisabled();
	});

	test("a too-old agent gets no 'cannot change ports' note, but locked controls", async () => {
		stubApi({ "GET /me": ADMIN });
		await renderCard(ports({ supported: false }), { agentTooOld: true });
		await adminForm();
		expect(
			screen.queryByText(/cannot change its ports/),
		).not.toBeInTheDocument();
		expect(httpField()).toBeDisabled();
		expect(httpsField()).toBeDisabled();
		expect(review()).toBeDisabled();
	});

	test("someone who is not the instance administrator sees a lock note and no form", async () => {
		stubApi({ "GET /me": MEMBER });
		await renderCard(ports());
		expect(
			await screen.findByText(
				"Only the instance administrator can change the proxy ports.",
			),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("HTTP port")).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Review change" }),
		).not.toBeInTheDocument();
	});
});

describe("PortsCard form", () => {
	test("starts empty with the agent's ports as placeholders and nothing to review", async () => {
		stubApi({ "GET /me": ADMIN });
		await renderCard(ports({ agentHttp: 8081, agentHttps: 8444 }));
		await adminForm();
		expect(httpField()).toHaveValue("");
		expect(httpField()).toHaveAttribute("placeholder", "8081");
		expect(httpsField()).toHaveAttribute("placeholder", "8444");
		expect(
			screen.getByText(/Leave a field empty to use the agent's own port/),
		).toHaveTextContent("(8081 and 8444)");
		expect(review()).toBeDisabled();
		expect(
			screen.queryByRole("button", { name: "Reset to agent defaults" }),
		).not.toBeInTheDocument();
	});

	test("starts from the staged setting", async () => {
		stubApi({ "GET /me": ADMIN });
		await renderCard(
			ports({ setHttp: 8080, setHttps: 8443, http: 8080, https: 8443 }),
		);
		await adminForm();
		expect(httpField()).toHaveValue("8080");
		expect(httpsField()).toHaveValue("8443");
		// Unchanged from what is staged: nothing to review yet.
		expect(review()).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Reset to agent defaults" }),
		).toBeEnabled();
	});

	test("rejects a non-numeric port inline and keeps Review disabled", async () => {
		stubApi({ "GET /me": ADMIN });
		const user = userEvent.setup();
		await renderCard(ports());
		await adminForm();
		await user.type(httpField(), "80a");
		expect(screen.getByText("HTTP port must be a number.")).toBeInTheDocument();
		expect(httpField()).toHaveAttribute("aria-invalid", "true");
		expect(httpsField()).not.toHaveAttribute("aria-invalid");
		expect(review()).toBeDisabled();
	});

	test("rejects an out-of-range HTTPS port", async () => {
		stubApi({ "GET /me": ADMIN });
		const user = userEvent.setup();
		await renderCard(ports());
		await adminForm();
		await user.type(httpsField(), "70000");
		expect(
			screen.getByText("HTTPS port must be between 1 and 65535."),
		).toBeInTheDocument();
		expect(httpsField()).toHaveAttribute("aria-invalid", "true");
		expect(review()).toBeDisabled();
	});

	test("rejects equal ports, the admin port and a port a TCP route holds", async () => {
		stubApi({ "GET /me": ADMIN });
		const user = userEvent.setup();
		await renderCard(ports(), {
			l4Ports: [
				{
					deployed: true,
					port: 5432,
					protocol: "tcp",
					serviceId: "svc1",
					serviceName: "pg-proxy",
				},
			],
		});
		await adminForm();
		await user.type(httpField(), "443");
		expect(
			screen.getByText(
				"HTTP and HTTPS must use different ports (both are 443).",
			),
		).toBeInTheDocument();
		expect(review()).toBeDisabled();

		await user.clear(httpField());
		await user.type(httpField(), "9000");
		expect(
			screen.getByText("Port 9000 is the proxy's admin port."),
		).toBeInTheDocument();

		await user.clear(httpField());
		await user.type(httpField(), "5432");
		expect(
			screen.getByText(
				"TCP port 5432 is reserved by the route pg-proxy; move that route or choose other ports.",
			),
		).toBeInTheDocument();
		expect(review()).toBeDisabled();

		await user.clear(httpField());
		await user.type(httpField(), "8080");
		expect(screen.queryByText(/reserved by the route/)).not.toBeInTheDocument();
		expect(review()).toBeEnabled();
	});
});

describe("PortsCard review and stage", () => {
	test("Review change asks the plan, shows the diff and restart reasons, and stages on confirm", async () => {
		const api = stubApi({
			"GET /me": ADMIN,
			[PLAN]: { body: plan() },
			[SETTINGS]: { body: make("ServerProxy") },
		});
		const user = userEvent.setup();
		await renderCard(ports());
		await adminForm();
		await user.type(httpField(), "8080");
		await user.click(review());

		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Move the proxy on edge-1 to another port?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("ports.http: 80 → 8080"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("entrypoint ports changed"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/Confirming stages the change/),
		).toBeInTheDocument();
		const planCall = api.calls.find((c) => c.path.endsWith("/plan"));
		expect(planCall?.method).toBe("POST");
		expect(planCall?.path).toBe("/servers/s1/proxy/plan");
		expect(planCall?.body).toEqual({ ports: { http: 8080, https: 0 } });
		// Planning does not stage anything.
		expect(api.calls.filter((c) => c.method === "PATCH")).toEqual([]);

		await user.click(
			within(dialog).getByRole("button", { name: "Stage ports" }),
		);
		expect(await screen.findByText("Ports staged")).toBeInTheDocument();
		const patch = api.calls.find((c) => c.method === "PATCH");
		expect(patch?.path).toBe("/servers/s1/proxy/settings");
		expect(patch?.body).toEqual({ ports: { http: 8080, https: 0 } });
		expect(api.unmatched).toEqual([]);
	});

	test("submitting with Enter reviews too, and sends both typed ports", async () => {
		const api = stubApi({
			"GET /me": ADMIN,
			[PLAN]: { body: plan() },
		});
		const user = userEvent.setup();
		await renderCard(ports());
		await adminForm();
		await user.type(httpField(), "8080");
		await user.type(httpsField(), "8443{Enter}");
		await screen.findByRole("alertdialog");
		expect(api.calls.find((c) => c.path.endsWith("/plan"))?.body).toEqual({
			ports: { http: 8080, https: 8443 },
		});
	});

	test("Enter on an invalid form sends nothing", async () => {
		const api = stubApi({ "GET /me": ADMIN });
		const user = userEvent.setup();
		await renderCard(ports());
		await adminForm();
		await user.type(httpField(), "abc{Enter}");
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
	});

	test("a plan that changes nothing says the proxy already listens there", async () => {
		stubApi({
			"GET /me": ADMIN,
			[PLAN]: {
				body: plan({ diff: [], reasons: [], restartRequired: false }),
			},
		});
		const user = userEvent.setup();
		await renderCard(ports());
		await adminForm();
		await user.type(httpField(), "8080");
		await user.click(review());
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText(/The proxy already listens on these ports/),
		).toBeInTheDocument();
		expect(
			within(dialog).queryByText(/The proxy restarts/),
		).not.toBeInTheDocument();
	});

	test("cancelling the dialog stages nothing", async () => {
		const api = stubApi({ "GET /me": ADMIN, [PLAN]: { body: plan() } });
		const user = userEvent.setup();
		await renderCard(ports());
		await adminForm();
		await user.type(httpField(), "8080");
		await user.click(review());
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.filter((c) => c.method === "PATCH")).toEqual([]);
		expect(screen.queryByText("Ports staged")).not.toBeInTheDocument();
	});

	test("a failing stage keeps the dialog open with the server's message", async () => {
		stubApi({
			"GET /me": ADMIN,
			[PLAN]: { body: plan() },
			[SETTINGS]: problem(
				409,
				"port 8080 is taken by route api",
				"port_in_use",
			),
		});
		const user = userEvent.setup();
		await renderCard(ports());
		await adminForm();
		await user.type(httpField(), "8080");
		await user.click(review());
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Stage ports" }),
		);
		expect(
			(await screen.findAllByText("port 8080 is taken by route api")).length,
		).toBeGreaterThan(0);
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
		expect(screen.queryByText("Ports staged")).not.toBeInTheDocument();
	});

	test("a failing plan shows the error under the form and opens no dialog", async () => {
		stubApi({
			"GET /me": ADMIN,
			[PLAN]: problem(500, "planner exploded", "internal"),
		});
		const user = userEvent.setup();
		await renderCard(ports());
		await adminForm();
		await user.type(httpField(), "8080");
		await user.click(review());
		expect(await screen.findByText("planner exploded")).toBeInTheDocument();
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
	});

	test("an agent_too_old answer shows the upgrade banner instead of an inline error", async () => {
		stubApi({
			"GET /me": ADMIN,
			[PLAN]: problem(422, "agent predates proxy v2", "agent_too_old"),
		});
		const user = userEvent.setup();
		await renderCard(ports());
		await adminForm();
		await user.type(httpField(), "8080");
		await user.click(review());
		expect(await screen.findByTestId("agent-too-old")).toHaveTextContent(
			"The agent on edge-1 is too old for this",
		);
		expect(
			screen.queryByText("agent predates proxy v2"),
		).not.toBeInTheDocument();
	});
});

describe("PortsCard reset", () => {
	test("Reset to agent defaults plans {0,0} and says it returns to the agent's ports", async () => {
		const api = stubApi({
			"GET /me": ADMIN,
			[PLAN]: { body: plan() },
			[SETTINGS]: { body: make("ServerProxy") },
		});
		const user = userEvent.setup();
		await renderCard(
			ports({ setHttp: 8080, http: 8080, source: "server", staged: true }),
		);
		await adminForm();
		await user.click(
			screen.getByRole("button", { name: "Reset to agent defaults" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Return edge-1 to the agent's own ports?"),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.path.endsWith("/plan"))?.body).toEqual({
			ports: { http: 0, https: 0 },
		});
		await user.click(
			within(dialog).getByRole("button", { name: "Stage ports" }),
		);
		await screen.findByText("Ports staged");
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			ports: { http: 0, https: 0 },
		});
	});

	test("a too-old agent cannot reset", async () => {
		stubApi({ "GET /me": ADMIN });
		await renderCard(ports({ setHttp: 8080, http: 8080 }), {
			agentTooOld: true,
		});
		await adminForm();
		expect(
			screen.getByRole("button", { name: "Reset to agent defaults" }),
		).toBeDisabled();
	});
});
