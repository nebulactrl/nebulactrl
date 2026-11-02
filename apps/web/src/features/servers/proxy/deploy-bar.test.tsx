import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	ProxyDeployInfo,
	ProxyPlan,
	ServerProxy,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { DeployBar, PlanBody } from "./deploy-bar";

const PLAN = "POST /servers/:serverId/proxy/plan";
const DEPLOY = "POST /servers/:serverId/proxy/deploy";

const offPolicy = {
	allowHostGateway: false,
	allowLinkLocal: false,
	allowLoopback: false,
};

const proxy = (over: Partial<ServerProxy> = {}): ServerProxy => ({
	agentTooOld: false,
	appliedGeneration: 3,
	generation: 4,
	appliedUpstreamPolicy: offPolicy,
	upstreamPolicy: offPolicy,
	capabilities: null,
	certificates: null,
	error: "",
	l4Ports: null,
	ports: {
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
	},
	staged: true,
	status: "ok",
	tailnet: {
		appliedMode: "off",
		hostTailscale: { detected: false, ips: null },
		hostname: "",
		integrationConfigured: false,
		magicDns: false,
		mode: "off",
	},
	...over,
});

const deploy = (
	status: ProxyDeployInfo["status"],
	message = "",
): ProxyDeployInfo => ({
	commandId: "cmd-1",
	startedAt: "2026-01-01T00:00:00Z",
	endedAt: null,
	status,
	message,
});

const plan = (over: Partial<ProxyPlan> = {}): ProxyPlan => ({
	diff: [{ field: "upstreamPolicy.allowLoopback", from: "false", to: "true" }],
	reasons: null,
	restartRequired: false,
	warnings: null,
	...over,
});

const problem = (status: number, code: string, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code },
});

const bar = (p: ServerProxy) => (
	<DeployBar serverId="srv-1" serverName="edge-1" proxy={p} />
);

// While loading the button also carries the spinner's "Loading" label.
const deployButton = () =>
	screen.getByRole("button", {
		name: /^(Loading )?Deploy proxy$/,
		// Skips the slow computed-style accessibility check; the bar is not hidden.
		hidden: true,
	});

describe("DeployBar", () => {
	test("renders nothing when nothing is staged, in flight or failed", async () => {
		const api = stubApi({});
		await renderWithProviders(
			bar(proxy({ generation: 3, appliedGeneration: 3, staged: false })),
		);
		expect(
			screen.queryByRole("region", { name: "Deploy proxy" }),
		).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("a finished deploy with nothing new staged stays hidden", async () => {
		stubApi({});
		await renderWithProviders(
			bar(
				proxy({
					generation: 3,
					lastDeploy: deploy("succeeded", "ok"),
				}),
			),
		);
		expect(
			screen.queryByRole("region", { name: "Deploy proxy" }),
		).not.toBeInTheDocument();
	});

	test("asks for the plan of the staged generation and counts its changes", async () => {
		const api = stubApi({
			[PLAN]: {
				body: plan({
					diff: [
						{ field: "a", from: "1", to: "2" },
						{ field: "b", from: "x", to: "y" },
					],
				}),
			},
		});
		await renderWithProviders(bar(proxy()));
		expect(
			await screen.findByText("2 staged proxy changes"),
		).toBeInTheDocument();
		expect(api.calls).toEqual([
			{
				method: "POST",
				path: "/servers/srv-1/proxy/plan",
				query: expect.anything(),
				body: {},
			},
		]);
	});

	test("a single change reads in the singular", async () => {
		stubApi({ [PLAN]: { body: plan() } });
		await renderWithProviders(bar(proxy()));
		expect(
			await screen.findByText("1 staged proxy change"),
		).toBeInTheDocument();
	});

	test("a plan without changes falls back to 'Staged proxy changes' and offers no plan toggle", async () => {
		stubApi({ [PLAN]: { body: plan({ diff: null }) } });
		await renderWithProviders(bar(proxy()));
		expect(await screen.findByText("Staged proxy changes")).toBeInTheDocument();
		await waitFor(() => expect(deployButton()).toBeEnabled());
		expect(
			screen.queryByRole("button", { name: "Show plan" }),
		).not.toBeInTheDocument();
	});

	test("Deploy proxy stays disabled until the plan has loaded", async () => {
		const gate = Promise.withResolvers<Response>();
		stubApi({ [PLAN]: () => gate.promise });
		await renderWithProviders(bar(proxy()));
		expect(deployButton()).toBeDisabled();
		gate.resolve(Response.json(plan()));
		expect(
			await screen.findByText("1 staged proxy change"),
		).toBeInTheDocument();
		expect(deployButton()).toBeEnabled();
	});

	test("notes that the deploy restarts the proxy", async () => {
		stubApi({
			[PLAN]: {
				body: plan({ restartRequired: true, reasons: ["ports changed"] }),
			},
		});
		await renderWithProviders(bar(proxy()));
		expect(await screen.findByText("restarts the proxy")).toBeInTheDocument();
	});

	test("does not claim a restart when the plan needs none", async () => {
		stubApi({ [PLAN]: { body: plan() } });
		await renderWithProviders(bar(proxy()));
		await screen.findByText("1 staged proxy change");
		expect(screen.queryByText("restarts the proxy")).not.toBeInTheDocument();
	});

	test("Show plan expands the changes and Hide plan collapses them", async () => {
		stubApi({
			[PLAN]: {
				body: plan({
					diff: [
						{ field: "tailnet.hostname", from: "", to: "edge" },
						{ field: "ports.http", from: "80", to: "" },
					],
				}),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(bar(proxy()));
		const show = await screen.findByRole("button", { name: "Show plan" });
		expect(show).toHaveAttribute("aria-expanded", "false");
		expect(
			screen.queryByText("tailnet.hostname: - → edge"),
		).not.toBeInTheDocument();
		await user.click(show);
		expect(screen.getByText("tailnet.hostname: - → edge")).toBeInTheDocument();
		expect(screen.getByText("ports.http: 80 → -")).toBeInTheDocument();
		const hide = screen.getByRole("button", { name: "Hide plan" });
		expect(hide).toHaveAttribute("aria-expanded", "true");
		await user.click(hide);
		expect(screen.queryByText("ports.http: 80 → -")).not.toBeInTheDocument();
	});

	test("confirming sends the deploy with an idempotency key and announces it", async () => {
		let key: string | null = null;
		const api = stubApi({
			[PLAN]: { body: plan() },
			[DEPLOY]: (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json({ commandId: "c", generation: 4, plan: plan() });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(bar(proxy()));
		await waitFor(() => expect(deployButton()).toBeEnabled());
		await user.click(deployButton());
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Deploy the proxy on edge-1?"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("upstreamPolicy.allowLoopback: false → true"),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.path.endsWith("/deploy"))).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Deploy proxy" }),
		);
		expect(
			await screen.findByText("Deploying the proxy on edge-1"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Status updates on this page."),
		).toBeInTheDocument();
		const sent = api.calls.filter((c) => c.path.endsWith("/deploy"));
		expect(sent).toHaveLength(1);
		expect(sent[0]?.path).toBe("/servers/srv-1/proxy/deploy");
		expect(sent[0]?.body).toEqual({});
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("cancelling the confirmation deploys nothing", async () => {
		const api = stubApi({ [PLAN]: { body: plan() } });
		const user = userEvent.setup();
		await renderWithProviders(bar(proxy()));
		await waitFor(() => expect(deployButton()).toBeEnabled());
		await user.click(deployButton());
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.map((c) => c.path)).toEqual(["/servers/srv-1/proxy/plan"]);
	});

	test("the confirmation spells out the restart and the warnings", async () => {
		stubApi({
			[PLAN]: {
				body: plan({
					restartRequired: true,
					reasons: ["the HTTP port changed"],
					warnings: ["Loopback upstreams are allowed"],
				}),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(bar(proxy()));
		await waitFor(() => expect(deployButton()).toBeEnabled());
		await user.click(deployButton());
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText(
				"The proxy restarts (about a second of dropped connections):",
			),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("the HTTP port changed"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("Loopback upstreams are allowed"),
		).toBeInTheDocument();
	});

	test("a failing deploy keeps the confirmation open with the error and the bar shows it too", async () => {
		stubApi({
			[PLAN]: { body: plan() },
			[DEPLOY]: problem(
				409,
				"deploy_in_progress",
				"A deploy is already running",
			),
		});
		const user = userEvent.setup();
		await renderWithProviders(bar(proxy()));
		await waitFor(() => expect(deployButton()).toBeEnabled());
		await user.click(deployButton());
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Deploy proxy" }),
		);
		expect(
			await within(dialog).findByText("A deploy is already running"),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Deploying the proxy on edge-1"),
		).not.toBeInTheDocument();
	});

	test.each(["pending", "sent", "acked"] as const)(
		"while a deploy is %s it shows progress, blocks Deploy proxy and asks for no plan",
		async (status) => {
			const api = stubApi({});
			await renderWithProviders(
				bar(proxy({ lastDeploy: deploy(status, "pulling image") })),
			);
			expect(
				screen.getByText("Deploying the proxy… pulling image"),
			).toBeInTheDocument();
			expect(deployButton()).toBeDisabled();
			expect(api.calls).toEqual([]);
			expect(api.unmatched).toEqual([]);
		},
	);

	test("a failed deploy with nothing staged says so and shows the agent's message", async () => {
		const api = stubApi({});
		await renderWithProviders(
			bar(
				proxy({
					generation: 3,
					lastDeploy: deploy("failed", "port 443 is busy"),
				}),
			),
		);
		expect(
			screen.getByText("The last proxy deploy failed"),
		).toBeInTheDocument();
		expect(screen.getByText("port 443 is busy")).toBeInTheDocument();
		expect(deployButton()).toBeDisabled();
		expect(api.calls).toEqual([]);
	});

	test("a failed deploy without a message falls back to a generic line", async () => {
		stubApi({});
		await renderWithProviders(
			bar(proxy({ generation: 3, lastDeploy: deploy("failed", "") })),
		);
		expect(
			screen.getByText("The agent reported a failure."),
		).toBeInTheDocument();
	});

	test("a failed deploy with new changes staged offers the retry and shows both", async () => {
		stubApi({ [PLAN]: { body: plan() } });
		await renderWithProviders(
			bar(proxy({ lastDeploy: deploy("failed", "port 443 is busy") })),
		);
		expect(
			await screen.findByText("1 staged proxy change"),
		).toBeInTheDocument();
		expect(screen.getByText("port 443 is busy")).toBeInTheDocument();
		await waitFor(() => expect(deployButton()).toBeEnabled());
	});

	test("a plan failure shows the control plane's message and keeps Deploy proxy disabled", async () => {
		stubApi({ [PLAN]: problem(500, "internal", "plan exploded") });
		await renderWithProviders(bar(proxy()));
		expect(
			await screen.findByText("The plan did not load: plan exploded"),
		).toBeInTheDocument();
		expect(deployButton()).toBeDisabled();
	});

	test("an agent_too_old plan failure tells the operator to upgrade the agent", async () => {
		stubApi({ [PLAN]: problem(422, "agent_too_old", "agent too old") });
		await renderWithProviders(bar(proxy()));
		expect(
			await screen.findByText(
				"The agent is too old for these changes. Upgrade it first.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText(/The plan did not load/)).not.toBeInTheDocument();
	});
});

describe("PlanBody", () => {
	test("shows the changes, the restart reasons and each warning", async () => {
		await renderWithProviders(
			<PlanBody
				highlights={{
					changes: ["a: 1 → 2"],
					restart: ["reason one", "reason two"],
					warnings: ["careful", "very careful"],
					deletesTailnetDevice: false,
				}}
			/>,
		);
		expect(screen.getByText("a: 1 → 2")).toBeInTheDocument();
		expect(screen.getByText("reason one")).toBeInTheDocument();
		expect(screen.getByText("reason two")).toBeInTheDocument();
		expect(screen.getByText("careful")).toBeInTheDocument();
		expect(screen.getByText("very careful")).toBeInTheDocument();
	});

	test("omits the sections that are empty", async () => {
		await renderWithProviders(
			<PlanBody
				highlights={{
					changes: [],
					restart: [],
					warnings: [],
					deletesTailnetDevice: false,
				}}
			/>,
		);
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
		expect(screen.queryByText(/restarts/)).not.toBeInTheDocument();
	});
});
