import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { deploymentView, eventView, logStreamResponse } from "../test-fixtures";
import { DeploymentDebugPanel } from "./debug-panel";

const serviceWire = make("ServiceView", { id: "svc-1", name: "billing-api" });

const releaseWire = make("ReleaseDetailView", {
	id: "rel-5",
	deploymentId: "dep-1",
	serviceId: "svc-1",
	generation: 2,
	createdAt: "2026-10-31T10:00:00Z",
	imageRef: "registry.local/api:abc",
	config: { port: 8080 },
	source: { type: "image", image: "api:abc" },
	build: null,
	variables: null,
	variablesPruned: false,
	placement: {
		strategy: "single",
		targetServerId: "srv-1",
		targetServerName: "edge-1",
		buildServerId: "",
		buildServerName: "",
		registryId: "",
		registryRef: "",
		platform: "linux/amd64",
	},
});

const commandWire = make("CommandView", {
	id: "cmd-1",
	kind: "deploy",
	status: "succeeded",
	outcome: "",
	error: "",
	serverId: "srv-1",
	serverName: "edge-1",
	createdAt: "2026-10-31T10:00:00.000Z",
	sentAt: null,
	ackedAt: null,
	finishedAt: null,
	expiresAt: "2026-10-31T10:15:00Z",
	payload: {},
	result: null,
});

function routes(overrides: Record<string, unknown> = {}) {
	return {
		"GET /deployments/dep-1": {
			body: deploymentView({ id: "dep-1", buildServerId: "srv-b" }),
		},
		"GET /services/svc-1": { body: serviceWire },
		"GET /servers": {
			body: make("PageServer", {
				items: [
					make("Server", { id: "srv-1", name: "edge-1" }),
					make("Server", { id: "srv-b", name: "build-1" }),
				],
			}),
		},
		"GET /deployments/dep-1/events": {
			body: {
				items: [
					eventView({
						id: 1,
						step: "pull",
						status: "started",
						createdAt: "2026-10-31T10:00:01Z",
					}),
					eventView({
						id: 2,
						step: "pull",
						status: "succeeded",
						createdAt: "2026-10-31T10:00:03Z",
					}),
				],
			},
		},
		"GET /deployments/dep-1/logs": () =>
			logStreamResponse([{ line: "pulling image", stream: "build" }]),
		"GET /deployments/dep-1/release": { body: releaseWire },
		"GET /deployments/dep-1/commands": { body: { items: [commandWire] } },
		...overrides,
	} as Parameters<typeof stubApi>[0];
}

function renderPanel(props: { open?: boolean; onClose?: () => void } = {}) {
	return renderWithProviders(
		<DeploymentDebugPanel
			deploymentId="dep-1"
			open={props.open ?? true}
			onClose={props.onClose ?? (() => {})}
		/>,
	);
}

describe("DeploymentDebugPanel header", () => {
	test("names the service and the deployment and summarises its status, trigger, actor and servers", async () => {
		stubApi(routes());
		await renderPanel();

		expect(
			await screen.findByRole("button", { name: "Back to billing-api" }),
		).toBeInTheDocument();
		expect(screen.getByTitle("dep-1")).toHaveTextContent("dep-1");
		expect(
			screen.getByRole("heading", { name: /Debug deployment/ }),
		).toHaveTextContent("Succeeded");
		const meta = (label: string) =>
			screen.getByText(label, { selector: "dt" }).nextElementSibling;
		expect(meta("Trigger")).toHaveTextContent("Deploy");
		expect(meta("Actor")).toHaveTextContent("Ada");
		expect(meta("Duration")).toHaveTextContent("1m 00s");
		expect(meta("Server · built on")).toHaveTextContent("edge-1 · build-1");
	});

	test("a deployment built on its own server shows just the server", async () => {
		stubApi(
			routes({
				"GET /deployments/dep-1": {
					body: deploymentView({ id: "dep-1", buildServerId: "" }),
				},
			}),
		);
		await renderPanel();

		await screen.findByRole("button", { name: "Back to billing-api" });
		const meta = screen.getByText("Server", {
			selector: "dt",
		}).nextElementSibling;
		expect(meta).toHaveTextContent(/^edge-1$/);
	});

	test("a build server that is gone is called 'build server'", async () => {
		stubApi(
			routes({
				"GET /servers": {
					body: make("PageServer", {
						items: [make("Server", { id: "srv-1", name: "edge-1" })],
					}),
				},
			}),
		);
		await renderPanel();

		await screen.findByRole("button", { name: "Back to billing-api" });
		expect(
			screen.getByText("Server · built on", { selector: "dt" })
				.nextElementSibling,
		).toHaveTextContent("edge-1 · build server");
	});

	test("a running deployment says 'Running for'", async () => {
		stubApi(
			routes({
				"GET /deployments/dep-1": {
					body: deploymentView({
						id: "dep-1",
						status: "building",
						finishedAt: null,
					}),
				},
			}),
		);
		await renderPanel();

		expect(
			await screen.findByText("Running for", { selector: "dt" }),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Duration", { selector: "dt" }),
		).not.toBeInTheDocument();
	});

	test("a deployment that did not load shows an error with a retry", async () => {
		let attempts = 0;
		stubApi(
			routes({
				"GET /deployments/dep-1": () => {
					attempts += 1;
					return attempts === 1
						? Response.json(
								{ code: "not_found", detail: "gone" },
								{ status: 404 },
							)
						: Response.json(deploymentView({ id: "dep-1" }));
				},
			}),
		);
		await renderPanel();

		expect(
			await screen.findByText("The deployment did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Copy debug report" }),
		).toBeDisabled();

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Try again" }));

		expect(
			await screen.findByText("Duration", { selector: "dt" }),
		).toBeInTheDocument();
		expect(
			screen.queryByText("The deployment did not load"),
		).not.toBeInTheDocument();
	});

	test("while closed it loads nothing", async () => {
		const api = stubApi(routes());
		await renderPanel({ open: false });
		expect(api.calls).toEqual([]);
		expect(screen.queryByTitle("dep-1")).not.toBeInTheDocument();
	});
});

describe("DeploymentDebugPanel tabs", () => {
	test("opens on the raw timeline", async () => {
		stubApi(routes());
		await renderPanel();

		expect(
			await screen.findByRole("list", { name: "Raw deployment events" }),
		).toBeInTheDocument();
		expect(screen.getByRole("tab", { name: "Timeline" })).toHaveAttribute(
			"aria-selected",
			"true",
		);
	});

	test("the release and commands are only fetched when their tab opens", async () => {
		const api = stubApi(routes());
		await renderPanel();
		const user = userEvent.setup();
		await screen.findByRole("list", { name: "Raw deployment events" });
		expect(api.calls.some((c) => c.path.endsWith("/release"))).toBe(false);
		expect(api.calls.some((c) => c.path.endsWith("/commands"))).toBe(false);

		await user.click(screen.getByRole("tab", { name: "Release" }));
		expect(await screen.findByText("rel-5")).toBeInTheDocument();
		expect(api.calls.some((c) => c.path.endsWith("/release"))).toBe(true);
		expect(api.calls.some((c) => c.path.endsWith("/commands"))).toBe(false);

		await user.click(screen.getByRole("tab", { name: "Agent commands" }));
		expect(
			await screen.findByRole("list", { name: "Agent commands" }),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.path.endsWith("/commands"))).toBe(true);
	});

	test("a deployment without a release gets the empty release state", async () => {
		stubApi(
			routes({
				"GET /deployments/dep-1/release": problem(404, {
					code: "not_found",
					detail: "no release",
				}),
			}),
		);
		await renderPanel();

		await userEvent
			.setup()
			.click(await screen.findByRole("tab", { name: "Release" }));

		expect(await screen.findByText("No release yet")).toBeInTheDocument();
	});

	test("the debug log tab follows every stream, including debug", async () => {
		const api = stubApi(routes());
		await renderPanel();

		await userEvent
			.setup()
			.click(await screen.findByRole("tab", { name: "Debug log" }));

		expect(
			await screen.findByRole("region", { name: "Debug log" }),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(
				api.calls
					.find((c) => c.path === "/deployments/dep-1/logs")
					?.query.get("streams"),
			).toBe("all"),
		);
	});
});

describe("DeploymentDebugPanel closing", () => {
	test("the close button calls onClose", async () => {
		let closed = 0;
		stubApi(routes());
		await renderPanel({ onClose: () => (closed += 1) });

		await userEvent
			.setup()
			.click(
				await screen.findByRole("button", { name: "Close the debug panel" }),
			);

		expect(closed).toBeGreaterThanOrEqual(1);
	});

	test("Escape closes it", async () => {
		let closed = 0;
		stubApi(routes());
		await renderPanel({ onClose: () => (closed += 1) });
		await screen.findByRole("button", { name: "Back to billing-api" });

		fireEvent.keyDown(document.body, { key: "Escape" });

		expect(closed).toBeGreaterThanOrEqual(1);
	});

	test("Escape belongs to a menu or dialog opened above the panel", async () => {
		let closed = 0;
		stubApi(routes());
		await renderPanel({ onClose: () => (closed += 1) });
		await screen.findByRole("button", { name: "Back to billing-api" });
		const menu = document.createElement("div");
		menu.setAttribute("role", "menu");
		menu.setAttribute("data-open", "");
		document.body.append(menu);

		fireEvent.keyDown(document.body, { key: "Escape" });

		expect(closed).toBe(0);
	});
});

describe("DeploymentDebugPanel copy report", () => {
	test("copies a report built from fresh reads and confirms it", async () => {
		const api = stubApi(routes());
		await renderPanel();
		const user = userEvent.setup();
		await screen.findByRole("button", { name: "Back to billing-api" });

		await user.click(screen.getByRole("button", { name: "Copy debug report" }));

		expect(await screen.findByText("Debug report copied")).toBeInTheDocument();
		expect(
			screen.getByText("Secrets are masked. Paste it into a bug report."),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("button", { name: "Copied" }),
		).toBeInTheDocument();
		const report = await navigator.clipboard.readText();
		expect(report).toContain("NebulaCtrl deployment debug report");
		expect(report).toContain("billing-api (svc-1)");
		expect(report).toMatch(/Server\s+edge-1/);
		expect(report).toMatch(/Build server\s+build-1/);
		// The release and commands were fetched for the report even though their tabs never opened.
		expect(api.calls.some((c) => c.path === "/deployments/dep-1/release")).toBe(
			true,
		);
		expect(
			api.calls.some((c) => c.path === "/deployments/dep-1/commands"),
		).toBe(true);
		expect(report).toContain("cmd-1");
	});

	test("a release that cannot be read is left out of the report, not fatal", async () => {
		stubApi(
			routes({
				"GET /deployments/dep-1/release": problem(404, {
					code: "not_found",
					detail: "no release",
				}),
			}),
		);
		await renderPanel();
		await screen.findByRole("button", { name: "Back to billing-api" });

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Copy debug report" }));

		expect(await screen.findByText("Debug report copied")).toBeInTheDocument();
		expect(await navigator.clipboard.readText()).toContain(
			"NebulaCtrl deployment debug report",
		);
	});

	test("a blocked clipboard says so", async () => {
		stubApi(routes());
		await renderPanel();
		await screen.findByRole("button", { name: "Back to billing-api" });
		const user = userEvent.setup();
		// user-event installs its own clipboard on setup: refuse after that.
		navigator.clipboard.writeText = async () => {
			throw new Error("denied");
		};

		await user.click(screen.getByRole("button", { name: "Copy debug report" }));

		expect(
			await screen.findByText("Could not copy the report"),
		).toBeInTheDocument();
		expect(
			screen.getByText("The browser blocked clipboard access."),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Copied" }),
		).not.toBeInTheDocument();
	});

	test("when the events cannot be read the report fails with the reason", async () => {
		let calls = 0;
		stubApi(
			routes({
				"GET /deployments/dep-1/events": () => {
					calls += 1;
					// The panel's own read works; the fresh read for the report does not.
					return calls === 1
						? Response.json({ items: [] })
						: Response.json(
								{ code: "internal", detail: "events store down" },
								{ status: 500 },
							);
				},
			}),
		);
		await renderPanel();
		await screen.findByRole("button", { name: "Back to billing-api" });

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Copy debug report" }));

		expect(
			await screen.findByText("Could not build the report"),
		).toBeInTheDocument();
		expect(screen.getByText("events store down")).toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Copy debug report" }),
			).toBeEnabled(),
		);
	});
});

describe("DeploymentDebugPanel within", () => {
	test("lives in a labelled dialog the page can find", async () => {
		stubApi(routes());
		await renderPanel();
		expect(
			await screen.findByRole("dialog", { name: /Debug deployment/ }),
		).toBeInTheDocument();
	});
});
