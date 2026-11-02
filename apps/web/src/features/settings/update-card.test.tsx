import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UpdateView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { UpdateCard } from "./update-card";

const base: UpdateView = {
	available: false,
	canUpdate: false,
	checkEnabled: true,
	checkedAt: "2026-01-01T00:00:00Z",
	current: "0.1.0",
	isAdmin: true,
	latest: "0.1.0",
	manualCommand: "curl -fsSL https://get.example.com | sh",
};

const available: UpdateView = {
	...base,
	available: true,
	canUpdate: true,
	latest: "0.2.0",
	publishedAt: "2026-02-03T00:00:00Z",
};

const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

const running = (startedMsAgo: number): UpdateView => ({
	...available,
	canUpdate: false,
	updating: {
		fromVersion: "0.1.0",
		targetVersion: "0.2.0",
		startedAt: ago(startedMsAgo),
		state: "running",
	},
});

const problem = (status: number, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code: "bad" },
});

describe("UpdateCard states", () => {
	test("a failed load shows an error with a retry that recovers", async () => {
		let attempts = 0;
		stubApi({
			"GET /system/update": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json(base);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<UpdateCard />);
		expect(
			await screen.findByText("The version did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByText("Installed")).toBeInTheDocument();
	});

	test("up to date: shows both versions and that nothing newer exists", async () => {
		stubApi({ "GET /system/update": { body: base } });
		await renderWithProviders(<UpdateCard />);
		await screen.findByText("Installed");
		const versions = screen.getAllByText("v0.1.0");
		expect(versions).toHaveLength(2);
		expect(
			screen.getByText("You are on the latest version."),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Update now" }),
		).not.toBeInTheDocument();
		// The manual command is only for states without a button.
		expect(screen.queryByText(base.manualCommand)).not.toBeInTheDocument();
	});

	test("a development build explains it is not compared with releases", async () => {
		stubApi({
			"GET /system/update": {
				body: { ...base, current: "dev", latest: undefined },
			},
		});
		await renderWithProviders(<UpdateCard />);
		expect(
			await screen.findByText(/development build, so it is not compared/),
		).toBeInTheDocument();
		expect(screen.getByText("Not checked yet")).toBeInTheDocument();
	});

	test("before the first check completes it says so", async () => {
		stubApi({
			"GET /system/update": { body: { ...base, latest: undefined } },
		});
		await renderWithProviders(<UpdateCard />);
		expect(
			await screen.findByText(/No check for new releases has completed yet/),
		).toBeInTheDocument();
	});

	test("with checks off, says so and shows the manual command", async () => {
		stubApi({
			"GET /system/update": {
				body: { ...base, checkEnabled: false, latest: undefined },
			},
		});
		await renderWithProviders(<UpdateCard />);
		expect(await screen.findByText("Checks are off")).toBeInTheDocument();
		expect(
			screen.getByText(/Update checks are off for this control plane/),
		).toBeInTheDocument();
		expect(screen.getByText(base.manualCommand)).toBeInTheDocument();
		expect(screen.getByText(/To update by hand/)).toBeInTheDocument();
		expect(screen.queryByText("Last checked")).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Check now" }),
		).not.toBeInTheDocument();
	});

	test("a non-admin sees the last check time but no Check now button", async () => {
		stubApi({
			"GET /system/update": { body: { ...base, isAdmin: false } },
		});
		await renderWithProviders(<UpdateCard />);
		expect(await screen.findByText("Last checked")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Check now" }),
		).not.toBeInTheDocument();
	});

	test("shows why the last release check failed", async () => {
		stubApi({
			"GET /system/update": {
				body: { ...base, checkError: "feed returned 503" },
			},
		});
		await renderWithProviders(<UpdateCard />);
		expect(
			await screen.findByText(
				/The last check for new releases did not work: feed returned 503/,
			),
		).toBeInTheDocument();
	});

	test("an available release this caller cannot install shows why and the manual command", async () => {
		stubApi({
			"GET /system/update": {
				body: {
					...available,
					canUpdate: false,
					cannotUpdateReason:
						"No agent is online on the control plane's server.",
				},
			},
		});
		await renderWithProviders(<UpdateCard />);
		expect(await screen.findByText("v0.2.0 is available")).toBeInTheDocument();
		expect(
			screen.getByText("No agent is online on the control plane's server."),
		).toBeInTheDocument();
		expect(screen.getByText(base.manualCommand)).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Update now" }),
		).not.toBeInTheDocument();
	});
});

describe("UpdateCard check now", () => {
	test("announces a newer release and refreshes the card", async () => {
		const api = stubApi({
			"GET /system/update": { body: base },
			"POST /system/update/check": { body: available },
		});
		const user = userEvent.setup();
		await renderWithProviders(<UpdateCard />);
		await user.click(await screen.findByRole("button", { name: "Check now" }));

		const alert = await screen.findByRole("note");
		expect(within(alert).getByText("v0.2.0 is available")).toBeInTheDocument();
		expect(
			screen.getAllByText("v0.2.0 is available").length,
		).toBeGreaterThanOrEqual(2); // toast and alert
		expect(
			api.calls.filter((c) => c.path === "/system/update/check"),
		).toHaveLength(1);
	});

	test("says you are on the latest version when nothing is newer", async () => {
		stubApi({
			"GET /system/update": { body: base },
			"POST /system/update/check": { body: base },
		});
		const user = userEvent.setup();
		await renderWithProviders(<UpdateCard />);
		await user.click(await screen.findByRole("button", { name: "Check now" }));
		// The toast and the idle summary use the same sentence.
		await waitFor(() =>
			expect(
				screen.getAllByText("You are on the latest version."),
			).toHaveLength(2),
		);
	});

	test("a failed check toasts the reason and re-reads the recorded check error", async () => {
		let checkError: string | undefined;
		stubApi({
			"GET /system/update": () => Response.json({ ...base, checkError }),
			"POST /system/update/check": () => {
				checkError = "feed unreachable";
				return Response.json(
					{ status: 502, title: "x", detail: "feed unreachable", code: "bad" },
					{ status: 502 },
				);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<UpdateCard />);
		await user.click(await screen.findByRole("button", { name: "Check now" }));
		expect(
			await screen.findByText("Could not check for updates"),
		).toBeInTheDocument();
		expect(
			await screen.findByText(
				/The last check for new releases did not work: feed unreachable/,
			),
		).toBeInTheDocument();
	});
});

describe("UpdateCard installing", () => {
	test("confirming sends the announced version with an idempotency key and shows the run", async () => {
		const keys: (string | null)[] = [];
		const api = stubApi({
			"GET /system/update": { body: available },
			"POST /system/update": (request) => {
				keys.push(request.headers.get("Idempotency-Key"));
				return Response.json(running(1_000), { status: 202 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<UpdateCard />);

		expect(await screen.findByText(/Published .*2026/)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Update now" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Update to v0.2.0?")).toBeInTheDocument();
		expect(
			within(dialog).getByText(/rolls back to v0\.1\.0 by itself/),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
		await user.click(
			within(dialog).getByRole("button", { name: "Update now" }),
		);

		expect(
			await screen.findByText(/Updating to v0\.2\.0\. The dashboard is/),
		).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/system/update");
		expect(post?.body).toEqual({ version: "0.2.0" });
		expect(keys).toHaveLength(1);
		expect(keys[0]).toMatch(/^[0-9a-f-]{36}$/);
		expect(
			screen.getByText(/Running for \d+s, from v0\.1\.0/),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Update now" }),
		).not.toBeInTheDocument();
	});

	test("cancelling the confirmation starts nothing", async () => {
		const api = stubApi({ "GET /system/update": { body: available } });
		const user = userEvent.setup();
		await renderWithProviders(<UpdateCard />);
		await user.click(await screen.findByRole("button", { name: "Update now" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
	});

	test("a refused start keeps the dialog open with the reason", async () => {
		stubApi({
			"GET /system/update": { body: available },
			"POST /system/update": problem(409, "an update is already running"),
		});
		const user = userEvent.setup();
		await renderWithProviders(<UpdateCard />);
		await user.click(await screen.findByRole("button", { name: "Update now" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Update now" }),
		);
		expect(
			await within(dialog).findByText("an update is already running"),
		).toBeInTheDocument();
		expect(screen.queryByText(/Updating to v0\.2\.0/)).not.toBeInTheDocument();
	});
});

describe("UpdateCard running", () => {
	test("shows the target, the source version and elapsed time", async () => {
		stubApi({ "GET /system/update": { body: running(95_000) } });
		await renderWithProviders(<UpdateCard />);
		expect(
			await screen.findByText(/Updating to v0\.2\.0\. The dashboard is/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Running for 1m \d\ds, from v0\.1\.0/),
		).toBeInTheDocument();
		expect(screen.getByRole("status")).toBeInTheDocument();
		expect(
			screen.queryByText(/The control plane is restarting/),
		).not.toBeInTheDocument();
		expect(
			screen.queryByText(/taking longer than expected/),
		).not.toBeInTheDocument();
	});

	test("a failed read mid-update reads as a restart", async () => {
		let calls = 0;
		stubApi({
			"GET /system/update": () => {
				calls += 1;
				return calls === 1
					? Response.json(running(5_000))
					: Response.json(
							{ status: 502, title: "x", detail: "bad gateway", code: "bad" },
							{ status: 502 },
						);
			},
			"POST /system/update/check": problem(502, "bad gateway"),
		});
		const user = userEvent.setup();
		await renderWithProviders(<UpdateCard />);
		await user.click(await screen.findByRole("button", { name: "Check now" }));
		expect(
			await screen.findByText(
				"The control plane is restarting. This page reconnects by itself.",
			),
		).toBeInTheDocument();
		// The last answer stays on screen.
		expect(screen.getByText(/Updating to v0\.2\.0/)).toBeInTheDocument();
	});

	test("an update past the give-up window warns, naming the restart state", async () => {
		stubApi({ "GET /system/update": { body: running(40 * 60_000) } });
		await renderWithProviders(<UpdateCard />);
		expect(
			await screen.findByText("The update is taking longer than expected"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/It has been running for 40m 0\ds/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/past the 30m 00s an update should take/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/answers, but it has not restarted on the new version/),
		).toBeInTheDocument();
		expect(screen.getByText(/journalctl -u/)).toBeInTheDocument();
		expect(screen.queryByText(/Updating to v0\.2\.0/)).not.toBeInTheDocument();
	});

	test("a stalled update with an unreachable control plane says it has not come back", async () => {
		let calls = 0;
		stubApi({
			"GET /system/update": () => {
				calls += 1;
				return calls === 1
					? Response.json(running(40 * 60_000))
					: Response.json(
							{ status: 503, title: "x", detail: "down", code: "bad" },
							{ status: 503 },
						);
			},
			"POST /system/update/check": problem(503, "down"),
		});
		const user = userEvent.setup();
		await renderWithProviders(<UpdateCard />);
		await user.click(await screen.findByRole("button", { name: "Check now" }));
		expect(
			await screen.findByText(/The control plane has not come back\./),
		).toBeInTheDocument();
	});
});

describe("UpdateCard failed update", () => {
	const failed = (over: Partial<UpdateView> = {}): UpdateView => ({
		...available,
		updating: {
			fromVersion: "0.1.0",
			targetVersion: "0.2.0",
			startedAt: ago(600_000),
			finishedAt: ago(300_000),
			state: "failed",
			error: "health check failed; rolled back to v0.1.0",
		},
		...over,
	});

	test("shows the error, the versions and a Try again that re-confirms", async () => {
		const api = stubApi({
			"GET /system/update": { body: failed() },
			"POST /system/update": { status: 202, body: running(1_000) },
		});
		const user = userEvent.setup();
		await renderWithProviders(<UpdateCard />);
		expect(
			await screen.findByText("The update did not complete."),
		).toBeInTheDocument();
		expect(
			screen.getByText("health check failed; rolled back to v0.1.0"),
		).toBeInTheDocument();
		expect(screen.getByText("From v0.1.0 to v0.2.0.")).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Try again" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Update now" }),
		);
		expect(await screen.findByText(/Updating to v0\.2\.0/)).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			version: "0.2.0",
		});
	});

	test("without a reported error it points at the installer log", async () => {
		const view = failed();
		stubApi({
			"GET /system/update": {
				body: {
					...view,
					updating: view.updating && { ...view.updating, error: undefined },
				},
			},
		});
		await renderWithProviders(<UpdateCard />);
		expect(
			await screen.findByText(
				/No details were reported\. Check the installer log/,
			),
		).toBeInTheDocument();
	});

	test("when it cannot be retried from here there is no button but the reason and manual command", async () => {
		stubApi({
			"GET /system/update": {
				body: failed({
					canUpdate: false,
					cannotUpdateReason: "The server's agent is offline.",
				}),
			},
		});
		await renderWithProviders(<UpdateCard />);
		await screen.findByText("The update did not complete.");
		expect(
			screen.queryByRole("button", { name: "Try again" }),
		).not.toBeInTheDocument();
		expect(
			screen.getByText("The server's agent is offline."),
		).toBeInTheDocument();
		expect(screen.getByText(base.manualCommand)).toBeInTheDocument();
	});
});

describe("UpdateCard release notes", () => {
	test("collapsed until opened, then rendered as markdown", async () => {
		stubApi({
			"GET /system/update": {
				body: {
					...available,
					notes: "## Highlights\n\n- faster **deploys**",
					notesUrl: "https://example.com/releases/0.2.0",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<UpdateCard />);
		const toggle = await screen.findByRole("button", {
			name: "Release notes for v0.2.0",
		});
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "true");
		const notes = await screen.findByRole("region", {
			name: "Release notes for v0.2.0",
		});
		expect(within(notes).getByText("faster")).toBeInTheDocument();
		expect(within(notes).getByText("deploys").tagName).toBe("STRONG");
		expect(
			screen.getByRole("link", { name: /Full release notes/ }),
		).toHaveAttribute("href", "https://example.com/releases/0.2.0");
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "false");
	});

	test("only a notes URL shows the link without a toggle", async () => {
		stubApi({
			"GET /system/update": {
				body: { ...available, notesUrl: "https://example.com/r" },
			},
		});
		await renderWithProviders(<UpdateCard />);
		expect(
			await screen.findByRole("link", { name: /Full release notes/ }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Release notes for/ }),
		).not.toBeInTheDocument();
	});

	test("an unsafe notes URL is not offered as a link", async () => {
		stubApi({
			"GET /system/update": {
				body: {
					...available,
					notes: "Some notes",
					notesUrl: "javascript:alert(1)",
				},
			},
		});
		await renderWithProviders(<UpdateCard />);
		await screen.findByRole("button", { name: "Release notes for v0.2.0" });
		expect(
			screen.queryByRole("link", { name: /Full release notes/ }),
		).not.toBeInTheDocument();
	});

	test("no notes for a version that is not announced", async () => {
		stubApi({
			"GET /system/update": {
				body: { ...base, latest: undefined, notes: "orphan notes" },
			},
		});
		await renderWithProviders(<UpdateCard />);
		await screen.findByText("Installed");
		expect(screen.queryByText(/Release notes for/)).not.toBeInTheDocument();
	});
});
