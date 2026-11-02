import { describe, expect, it } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { DeployHookView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DeployHookSection } from "./deploy-hook-section";

const GET = "GET /services/:serviceId/deploy-hook";
const PUT = "PUT /services/:serviceId/deploy-hook";
const ROTATE = "POST /services/:serviceId/deploy-hook/rotate";
const PATTERN = "https://nebula.example.com/hooks/deploy/<secret>";
const FULL_URL = "https://nebula.example.com/hooks/deploy/s3cr3t-value";

function hook(over: Partial<DeployHookView> = {}): DeployHookView {
	return {
		enabled: false,
		image: "ghcr.io/acme/api:main",
		isProduction: false,
		lastCalledAt: null,
		lastOutcome: "",
		queued: false,
		rateLimit: "10 calls a minute",
		secretSetAt: null,
		serviceId: "svc-1",
		urlPattern: PATTERN,
		...over,
	};
}

function Page() {
	const [id, setId] = useState("svc-1");
	return (
		<>
			<button type="button" onClick={() => setId("svc-2")}>
				other service
			</button>
			<DeployHookSection serviceId={id} serviceName={id} />
		</>
	);
}

async function show(
	view: DeployHookView,
	extra: Parameters<typeof stubApi>[0] = {},
) {
	const api = stubApi({ [GET]: { body: view }, ...extra });
	await renderWithProviders(
		<DeployHookSection serviceId="svc-1" serviceName="checkout" />,
	);
	await screen.findByRole("switch");
	return api;
}

describe("DeployHookSection", () => {
	it("starts off: a switch, an Off state, and none of the URL, last-call or setup parts", async () => {
		await show(hook());
		expect(screen.getByRole("switch")).not.toBeChecked();
		expect(screen.getByText("Off")).toBeInTheDocument();
		expect(screen.queryByText("Deploy hook URL")).toBeNull();
		expect(screen.queryByText("Last call")).toBeNull();
		expect(screen.queryByText("Set it up")).toBeNull();
	});

	it("says so when the hook cannot be read, and offers no switch", async () => {
		stubApi({ [GET]: problem(500, { detail: "boom" }) });
		await renderWithProviders(
			<DeployHookSection serviceId="svc-1" serviceName="checkout" />,
		);
		expect(
			await screen.findByText(/Could not load the deploy hook/),
		).toBeInTheDocument();
		expect(screen.queryByRole("switch")).toBeNull();
	});

	it("turns the hook on, shows the URL once and copies nothing until asked", async () => {
		const user = userEvent.setup();
		let current = hook();
		const api = await show(current, {
			[PUT]: () => {
				current = hook({ enabled: true, secretSetAt: "2026-01-01T00:00:00Z" });
				return Response.json({ ...current, url: FULL_URL });
			},
			[GET]: () => Response.json(current),
		});
		await user.click(screen.getByRole("switch"));
		expect(await screen.findByText("Deploy hook is on")).toBeInTheDocument();
		expect(
			screen.getByText("Copy the URL now: it is shown once."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")).toMatchObject({
			path: "/services/svc-1/deploy-hook",
			body: { enabled: true },
		});
		expect(await screen.findByText("Shown once")).toBeInTheDocument();
		// The URL shows in the copy line and in the set-up snippet.
		expect(
			screen.getAllByText(FULL_URL, { exact: false }).length,
		).toBeGreaterThan(0);
		expect(screen.getByText("On")).toBeInTheDocument();
		expect(screen.queryByText(/Lost it\? Rotate/)).toBeNull();
	});

	it("I have saved it hides the URL for good and shows the masked pattern", async () => {
		const user = userEvent.setup();
		let current = hook();
		await show(current, {
			[PUT]: () => {
				current = hook({ enabled: true });
				return Response.json({ ...current, url: FULL_URL });
			},
			[GET]: () => Response.json(current),
		});
		await user.click(screen.getByRole("switch"));
		await user.click(
			await screen.findByRole("button", { name: "I have saved it" }),
		);
		expect(screen.queryByText("Shown once")).toBeNull();
		expect(screen.queryByText(FULL_URL, { exact: false })).toBeNull();
		expect(
			screen.getByText(
				"https://nebula.example.com/hooks/deploy/••••••••••••••••",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Lost it\? Rotate to get a new URL/),
		).toBeInTheDocument();
	});

	it("an enabled hook shows the masked URL, last call with its outcome, and snippets with the pattern", async () => {
		await show(
			hook({
				enabled: true,
				lastCalledAt: new Date(Date.now() - 3 * 60_000).toISOString(),
				lastOutcome: "queued",
				queued: true,
			}),
		);
		expect(screen.getByText("On")).toBeInTheDocument();
		expect(
			screen.getByText(/https:\/\/nebula\.example\.com\/hooks\/deploy\/•+/),
		).toBeInTheDocument();
		const row = screen.getByText(/queued behind a running deployment/);
		expect(row).toHaveTextContent("3m ago");
		expect(row).toHaveTextContent(
			"a call is waiting for the running deployment",
		);
		expect(
			screen.getByText(`curl -fsS -X POST "${PATTERN}"`, { exact: false }),
		).toBeInTheDocument();
	});

	it("reports an unknown outcome as received and a hook never called as such", async () => {
		await show(
			hook({
				enabled: true,
				lastCalledAt: "2026-01-01T00:00:00Z",
				lastOutcome: "",
			}),
		);
		expect(screen.getByText(/· received/)).toBeInTheDocument();
		expect(screen.queryByText("Never called")).toBeNull();
	});

	it("says Never called for a hook nobody has called", async () => {
		await show(hook({ enabled: true }));
		expect(screen.getByText("Never called")).toBeInTheDocument();
	});

	it("warns on a production service with the rate limit", async () => {
		await show(hook({ isProduction: true, rateLimit: "6 calls a minute" }));
		const alert = screen
			.getByText("Production environment")
			.closest("[role=alert]");
		expect(alert).toHaveTextContent("deploy to production without signing in");
		expect(alert).toHaveTextContent("6 calls a minute");
	});

	it("does not show the production warning for other environments", async () => {
		await show(hook({ isProduction: false }));
		expect(screen.queryByText("Production environment")).toBeNull();
	});

	it("shows git services' registry snippets with a caution", async () => {
		const user = userEvent.setup();
		await show(hook({ enabled: true, image: "" }));
		await user.click(screen.getByRole("tab", { name: "Docker Hub" }));
		expect(
			await screen.findByText(
				/builds from git, so a registry push never matches it/,
			),
		).toBeInTheDocument();
	});

	it("turning it off asks first and sends enabled=false only after confirming", async () => {
		const user = userEvent.setup();
		let current = hook({ enabled: true });
		const api = await show(current, {
			[PUT]: () => {
				current = hook({ enabled: false });
				return Response.json(current);
			},
			[GET]: () => Response.json(current),
		});
		await user.click(screen.getByRole("switch"));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Turn off the deploy hook of checkout?");
		expect(dialog).toHaveTextContent("get 404 from now on");
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
		await user.click(within(dialog).getByRole("button", { name: "Turn off" }));
		expect(await screen.findByText("Deploy hook is off")).toBeInTheDocument();
		expect(
			screen.getByText("The old URL no longer works."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			enabled: false,
		});
		expect(await screen.findByText("Off")).toBeInTheDocument();
	});

	it("cancelling the turn-off leaves the hook on and sends nothing", async () => {
		const user = userEvent.setup();
		const api = await show(hook({ enabled: true }));
		await user.click(screen.getByRole("switch"));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
		expect(screen.getByRole("switch")).toBeChecked();
	});

	it("toasts the server's reason when the switch fails", async () => {
		const user = userEvent.setup();
		await show(hook(), {
			[PUT]: problem(403, { detail: "Needs deployments:write." }),
		});
		await user.click(screen.getByRole("switch"));
		expect(
			await screen.findByText("Could not change the deploy hook"),
		).toBeInTheDocument();
		expect(screen.getByText("Needs deployments:write.")).toBeInTheDocument();
		expect(screen.getByRole("switch")).not.toBeChecked();
	});

	it("rotating asks first, then reveals the new URL once", async () => {
		const user = userEvent.setup();
		const current = hook({ enabled: true });
		const api = await show(current, {
			[ROTATE]: () => Response.json({ ...current, url: FULL_URL }),
			[GET]: () => Response.json(current),
		});
		await user.click(screen.getByRole("button", { name: "Rotate URL…" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Rotate the deploy hook URL?");
		expect(api.calls.filter((c) => c.path.endsWith("/rotate"))).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Rotate URL" }),
		);
		expect(await screen.findByText("Deploy hook rotated")).toBeInTheDocument();
		expect(await screen.findByText("Shown once")).toBeInTheDocument();
		expect(api.calls.find((c) => c.path.endsWith("/rotate"))?.method).toBe(
			"POST",
		);
		expect(
			screen.getAllByText(FULL_URL, { exact: false }).length,
		).toBeGreaterThan(0);
		expect(screen.queryByRole("button", { name: "Rotate URL…" })).toBeNull();
	});

	it("toasts the server's reason when rotating fails and keeps the masked URL", async () => {
		const user = userEvent.setup();
		await show(hook({ enabled: true }), {
			[ROTATE]: problem(500, { detail: "Could not write the secret." }),
		});
		await user.click(screen.getByRole("button", { name: "Rotate URL…" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Rotate URL" }),
		);
		expect(
			await screen.findByText("Could not rotate the deploy hook"),
		).toBeInTheDocument();
		expect(screen.getByText("Could not write the secret.")).toBeInTheDocument();
		expect(screen.queryByText("Shown once")).toBeNull();
	});

	it("forgets a revealed URL when the page moves to another service", async () => {
		const user = userEvent.setup();
		const views: Record<string, DeployHookView> = {
			"svc-1": hook({ serviceId: "svc-1" }),
			"svc-2": hook({ serviceId: "svc-2", enabled: true }),
		};
		stubApi({
			[GET]: (req) =>
				Response.json(views[new URL(req.url).pathname.split("/").at(-2) ?? ""]),
			[PUT]: () => {
				views["svc-1"] = hook({ serviceId: "svc-1", enabled: true });
				return Response.json({ ...views["svc-1"], url: FULL_URL });
			},
		});
		await renderWithProviders(<Page />);
		await user.click(await screen.findByRole("switch"));
		expect(await screen.findByText("Shown once")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "other service" }));
		expect(await screen.findByText(/Lost it\? Rotate/)).toBeInTheDocument();
		expect(screen.queryByText("Shown once")).toBeNull();
	});
});
