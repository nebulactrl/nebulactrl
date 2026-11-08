import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { serviceWire } from "#/features/canvas/lib/wire-fixtures";
import { stubLayout } from "#/features/logs/test-layout";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { LogsPage } from "./logs-page";

let restoreLayout: () => void;
const openStreams: Array<() => void> = [];
beforeEach(() => {
	restoreLayout = stubLayout();
});
afterEach(() => {
	restoreLayout();
	for (const close of openStreams.splice(0)) {
		try {
			close();
		} catch {
			// already closed
		}
	}
});

/** A log stream that stays open and silent. */
function silentStream(): Response {
	let controller!: ReadableStreamDefaultController<Uint8Array>;
	const body = new ReadableStream<Uint8Array>({
		start: (c) => {
			controller = c;
		},
	});
	openStreams.push(() => controller.close());
	return new Response(body, {
		headers: { "Content-Type": "text/event-stream" },
	});
}

const env = {
	id: "env1",
	projectId: "p1",
	slug: "prod",
	name: "Production",
	kind: "standard",
	isProduction: true,
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
	createdAt: "2026-01-01T00:00:00Z",
};

const serviceView = (id: string, name: string) =>
	serviceWire({
		id,
		environmentId: "env1",
		projectId: "p1",
		name,
		slug: name,
		config: {
			healthcheck: {
				type: "http",
				intervalSeconds: 0,
				timeoutSeconds: 0,
				startPeriodSeconds: 0,
				deadlineSeconds: 0,
				successThreshold: 1,
			},
			imageRetention: 5,
			stopGraceSeconds: 30,
			processes: [],
		},
	});

function routes(overrides: Record<string, unknown> = {}) {
	return {
		"GET /projects/:id/environments": { body: { items: [env] } },
		"GET /environments/:id/services": {
			body: { items: [serviceView("s1", "api"), serviceView("s2", "worker")] },
		},
		"GET /environments/:id/logs": silentStream,
		"GET /environments/:id/logs/history": {
			body: { capturing: true, items: [] },
		},
		"GET /environments/:id/logs/facets": {
			body: {
				matched: 0,
				scanned: 0,
				partial: false,
				capturing: true,
				requestsCapturing: true,
				facets: [],
				attrs: [],
				histogram: [],
			},
		},
		...overrides,
	} as Parameters<typeof stubApi>[0];
}

const render = (path = "/") =>
	renderWithProviders(<LogsPage projectId="p1" envSlug="prod" />, { path });

describe("LogsPage", () => {
	test("titles the page and describes what it searches", async () => {
		stubApi(routes());
		await render();
		expect(
			screen.getByRole("heading", { level: 1, name: "Logs" }),
		).toBeInTheDocument();
		expect(
			await screen.findByText(
				"Search runtime output and requests across this environment.",
			),
		).toBeInTheDocument();
	});

	test("shows the live log of the environment", async () => {
		const api = stubApi(routes());
		await render();
		expect(
			await screen.findByRole("log", { name: "Environment logs" }),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(api.calls.map((c) => c.path)).toContain("/environments/env1/logs"),
		);
		expect(api.calls.map((c) => c.path)).toContain(
			"/environments/env1/services",
		);
	});

	test("shows the live log and facets of the environment, then switches to the stored history", async () => {
		const api = stubApi(routes());
		const user = userEvent.setup();
		await render();
		await screen.findByRole("log", { name: "Environment logs" });
		await waitFor(() =>
			expect(api.calls.map((c) => c.path)).toContain(
				"/environments/env1/logs/facets",
			),
		);
		// Live asks for no range: the server's rolling last 15 minutes.
		expect(
			api.calls.find((c) => c.path.endsWith("/logs/facets"))?.query.has("from"),
		).toBe(false);
		await user.click(screen.getByRole("button", { name: "History" }));
		expect(screen.getByRole("button", { name: "History" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.queryByRole("button", { name: "Follow" })).toBeNull();
		await waitFor(() =>
			expect(
				api.calls.some(
					(c) => c.path.endsWith("/logs/facets") && c.query.has("from"),
				),
			).toBe(true),
		);
		expect(
			api.calls.some(
				(c) =>
					c.path === "/environments/env1/logs/history" &&
					c.query.get("sources") === "runtime,requests",
			),
		).toBe(true);
	});

	test("services that did not load say so", async () => {
		stubApi(
			routes({
				"GET /environments/:id/services": () =>
					Response.json(
						{ status: 500, title: "x", detail: "db down", code: "internal" },
						{ status: 500 },
					),
			}),
		);
		await render();
		expect(
			await screen.findByText("Services did not load"),
		).toBeInTheDocument();
		expect(screen.queryByRole("log")).toBeNull();
	});

	test("an unknown environment says so", async () => {
		stubApi(routes());
		await renderWithProviders(<LogsPage projectId="p1" envSlug="nope" />);
		expect(
			await screen.findByText("Environment not found"),
		).toBeInTheDocument();
	});
});
