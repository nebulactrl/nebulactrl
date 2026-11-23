import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PatchAuto, PatchView } from "#/api/gen/types.gen";
import { browserTimezone, isValidTimezone } from "#/features/tasks/model";
import { stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { toServerView } from "../model";
import { AutoUpdatesCard } from "./auto-updates-card";

const PUT = "PUT /servers/:serverId/patches/auto";

const srv = toServerView(
	make("Server", { id: "srv-1", name: "edge-1", status: "online" }),
);

function auto(over: Overrides<PatchAuto> = {}): PatchAuto {
	return make("PatchAuto", {
		builtinHolds: ["docker-ce", "containerd.io"],
		consecutiveFailures: 0,
		disabledReason: null,
		enabled: false,
		holdPackages: [],
		lastScheduledRun: null,
		nativeAutoUpdates: "",
		nextWindowStart: null,
		reboot: "never",
		requiresAcknowledgement: false,
		scope: "security",
		securityOnlySupported: true,
		suggestedStart: null,
		timezone: "Europe/Berlin",
		windowMinutes: 120,
		windowStart: "03:00",
		...over,
	});
}

function view(over: Overrides<PatchView> = {}, a: PatchAuto = auto()) {
	return make("PatchView", {
		agentSupported: true,
		known: true,
		supported: true,
		manager: "apt",
		runs: [],
		productionServices: [],
		controlPlane: false,
		...over,
		auto: a,
	});
}

const prod = {
	name: "api",
	project: "shop",
	environment: "production",
	environmentId: "env-1",
	projectId: "prj-1",
	serviceId: "svc-1",
};

function card(v: PatchView, instanceAdmin = true) {
	return renderWithProviders(
		<AutoUpdatesCard server={srv} view={v} instanceAdmin={instanceAdmin} />,
	);
}

const toggle = () =>
	screen.getByRole("switch", { name: "Update automatically every night" });

describe("AutoUpdatesCard", () => {
	test("renders the off state without settings and with nothing to save", async () => {
		stubApi({});
		await card(view());
		expect(screen.getByText("Automatic updates")).toBeInTheDocument();
		expect(toggle()).not.toBeChecked();
		expect(screen.queryByLabelText("Start time")).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Save automatic updates" }),
		).toBeDisabled();
		expect(screen.queryByText("Next window")).not.toBeInTheDocument();
		expect(
			screen.getByText(/Docker and containerd are never updated/),
		).toBeInTheDocument();
	});

	test("enabling shows the fields, the built-in holds and the reboot guards", async () => {
		stubApi({});
		await card(view());
		const user = userEvent.setup();
		await user.click(toggle());
		expect(toggle()).toBeChecked();
		expect(screen.getByLabelText("Start time")).toHaveValue("03:00");
		expect(
			screen.getByRole("button", { name: "Security only", pressed: true }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "2h", pressed: true }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("combobox", { name: "Time zone" }),
		).toHaveTextContent("Europe/Berlin");
		expect(
			screen.getByRole("button", { name: "Never — notify me", pressed: true }),
		).toBeInTheDocument();
		expect(
			screen.queryByText(/at least 10 minutes of the window are left/),
		).not.toBeInTheDocument();
		await user.click(
			screen.getByRole("button", {
				name: "Reboot inside the window when required",
			}),
		);
		expect(
			screen.getByText(/at least 10 minutes of the window are left/),
		).toBeInTheDocument();
		expect(screen.getByText("docker-ce, containerd.io")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Save automatic updates" }),
		).toBeEnabled();
	});

	test("a server with the default zone gets the browser's zone when first enabled", async () => {
		stubApi({});
		await card(view({}, auto({ timezone: "UTC" })));
		await userEvent.setup().click(toggle());
		const browser = browserTimezone();
		expect(
			screen.getByRole("combobox", { name: "Time zone" }),
		).toHaveTextContent(isValidTimezone(browser) ? browser : "UTC");
	});

	test("security-only is disabled with its reason when the host cannot do it", async () => {
		stubApi({});
		await card(view({}, auto({ securityOnlySupported: false })));
		await userEvent.setup().click(toggle());
		expect(
			screen.getByRole("button", { name: "Security only" }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "All updates", pressed: true }),
		).toBeInTheDocument();
		expect(
			screen.getByText(/cannot limit an upgrade to security updates/),
		).toBeInTheDocument();
	});

	test("the suggestion button fills the start time", async () => {
		stubApi({});
		await card(view({}, auto({ enabled: true, suggestedStart: "04:15" })));
		const user = userEvent.setup();
		const start = screen.getByLabelText("Start time");
		expect(start).toHaveValue("03:00");
		await user.click(
			screen.getByRole("button", {
				name: "Use suggestion 04:15 (quietest in the last 14 days)",
			}),
		);
		expect(start).toHaveValue("04:15");
		expect(screen.getByText("You have unsaved changes.")).toBeInTheDocument();
	});

	test("there is no suggestion button without data", async () => {
		stubApi({});
		await card(view({}, auto({ enabled: true, suggestedStart: null })));
		expect(screen.queryByText(/Use suggestion/)).not.toBeInTheDocument();
	});

	test("Save sends the whole draft and Discard drops it", async () => {
		const api = stubApi({
			[PUT]: { body: auto({ enabled: true }) },
		});
		await card(view());
		const user = userEvent.setup();
		await user.click(toggle());
		fireEvent.change(screen.getByLabelText("Start time"), {
			target: { value: "04:30" },
		});
		await user.click(screen.getByRole("button", { name: "All updates" }));
		await user.click(screen.getByRole("button", { name: "3h" }));
		await user.click(
			screen.getByRole("button", {
				name: "Reboot inside the window when required",
			}),
		);
		await user.click(screen.getByRole("button", { name: "Hold a package" }));
		await user.type(screen.getByLabelText("Package 1"), "openssl");
		await user.click(
			screen.getByRole("button", { name: "Save automatic updates" }),
		);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
				enabled: true,
				scope: "all",
				windowStart: "04:30",
				windowMinutes: 180,
				timezone: "Europe/Berlin",
				reboot: "whenRequired",
				holdPackages: ["openssl"],
			}),
		);
		expect(
			(await screen.findAllByText("Automatic updates saved")).length,
		).toBeGreaterThan(0);
	});

	test("Discard restores the saved settings", async () => {
		const api = stubApi({});
		await card(view());
		const user = userEvent.setup();
		await user.click(toggle());
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(toggle()).not.toBeChecked();
		expect(
			screen.queryByRole("button", { name: "Discard" }),
		).not.toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	test("a bad start time is refused before anything is sent", async () => {
		const api = stubApi({});
		await card(view({}, auto({ enabled: true })));
		const user = userEvent.setup();
		fireEvent.change(screen.getByLabelText("Start time"), {
			target: { value: "" },
		});
		await user.click(
			screen.getByRole("button", { name: "Save automatic updates" }),
		);
		expect(
			await screen.findByText("Enter a start time as HH:MM, 24 hours."),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	test("a production server needs the acknowledgement before the save", async () => {
		const api = stubApi({ [PUT]: { body: auto({ enabled: true }) } });
		await card(
			view(
				{ productionServices: [prod] },
				auto({ requiresAcknowledgement: true }),
			),
		);
		const user = userEvent.setup();
		await user.click(toggle());
		await user.click(
			screen.getByRole("button", { name: "Save automatic updates" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Turn on automatic updates for edge-1?");
		expect(dialog).toHaveTextContent(
			"Production runs on this server: shop/api.",
		);
		// Nothing is sent, and the button waits for the box.
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
		const confirm = within(dialog).getByRole("button", {
			name: "Save automatic updates",
		});
		expect(confirm).toBeDisabled();
		await user.click(within(dialog).getByRole("checkbox"));
		expect(confirm).toBeEnabled();
		await user.click(confirm);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")?.body).toMatchObject({
				enabled: true,
				acknowledgeProduction: true,
			}),
		);
	});

	test("switching off needs no acknowledgement", async () => {
		const api = stubApi({ [PUT]: { body: auto() } });
		await card(
			view(
				{ productionServices: [prod] },
				auto({ enabled: true, requiresAcknowledgement: true }),
			),
		);
		const user = userEvent.setup();
		await user.click(toggle());
		await user.click(
			screen.getByRole("button", { name: "Save automatic updates" }),
		);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")?.body).toMatchObject({
				enabled: false,
			}),
		);
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
	});

	test("a refused save shows its reason in the dialog", async () => {
		stubApi({
			[PUT]: () =>
				Response.json(
					{
						title: "x",
						status: 422,
						code: "production_ack_required",
						detail: "edge-1 runs 1 production service(s).",
					},
					{ status: 422 },
				),
		});
		await card(
			view({ controlPlane: true }, auto({ requiresAcknowledgement: true })),
		);
		const user = userEvent.setup();
		await user.click(toggle());
		await user.click(
			screen.getByRole("button", { name: "Save automatic updates" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("This server runs NebulaCtrl itself.");
		await user.click(within(dialog).getByRole("checkbox"));
		await user.click(
			within(dialog).getByRole("button", { name: "Save automatic updates" }),
		);
		expect(
			await within(dialog).findByText("edge-1 runs 1 production service(s)."),
		).toBeInTheDocument();
	});

	test("a 422 for the time zone lands under the time zone", async () => {
		stubApi({
			[PUT]: () =>
				Response.json(
					{
						title: "x",
						status: 422,
						code: "invalid_timezone",
						detail: "Unknown time zone.",
					},
					{ status: 422 },
				),
		});
		await card(view({}, auto({ enabled: true })));
		const user = userEvent.setup();
		fireEvent.change(screen.getByLabelText("Start time"), {
			target: { value: "04:00" },
		});
		await user.click(
			screen.getByRole("button", { name: "Save automatic updates" }),
		);
		expect(await screen.findByText("Unknown time zone.")).toBeInTheDocument();
		// The footer keeps to its own message.
		expect(screen.getByText("You have unsaved changes.")).toBeInTheDocument();
	});

	test("another 422 is said in the footer", async () => {
		stubApi({
			[PUT]: () =>
				Response.json(
					{
						title: "x",
						status: 422,
						code: "agent_too_old",
						detail: "Update the agent first.",
					},
					{ status: 422 },
				),
		});
		await card(view({}, auto({ enabled: true })));
		const user = userEvent.setup();
		fireEvent.change(screen.getByLabelText("Start time"), {
			target: { value: "04:00" },
		});
		await user.click(
			screen.getByRole("button", { name: "Save automatic updates" }),
		);
		expect(
			await screen.findByText("Update the agent first."),
		).toBeInTheDocument();
	});

	test("a schedule the control plane switched off says why", async () => {
		stubApi({});
		await card(
			view(
				{},
				auto({
					enabled: false,
					consecutiveFailures: 3,
					disabledReason: "Three scheduled upgrades failed in a row.",
				}),
			),
		);
		const alert = screen.getByRole("alert");
		expect(alert).toHaveTextContent("Automatic updates were switched off");
		expect(alert).toHaveTextContent(
			"Three scheduled upgrades failed in a row.",
		);
		expect(screen.getByText(/3 failed runs in a row/)).toBeInTheDocument();
	});

	test("an enabled schedule shows its next window, last run and the host updater note", async () => {
		stubApi({});
		const next = new Date(Date.now() + 3 * 86_400_000).toISOString();
		await card(
			view(
				{
					runs: [
						make("PatchRun", {
							id: "run-9",
							action: "apply",
							trigger: "scheduled",
						}),
					],
				},
				auto({
					enabled: true,
					timezone: "UTC",
					nextWindowStart: next,
					nativeAutoUpdates: "unattended-upgrades",
					lastScheduledRun: {
						id: "run-9",
						status: "succeeded",
						startedAt: "2026-10-27T03:10:00Z",
						finishedAt: "2026-10-27T03:20:00Z",
					},
				}),
			),
		);
		expect(
			screen.getByText("Next window").nextElementSibling,
		).toHaveTextContent(/UTC$/);
		expect(screen.getByText("Succeeded")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Show in history" }),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"NebulaCtrl turns off unattended-upgrades on this server while automatic updates are on.",
			),
		).toBeInTheDocument();
	});

	test("the last run has no link when the history does not list it", async () => {
		stubApi({});
		await card(
			view(
				{},
				auto({
					enabled: true,
					lastScheduledRun: {
						id: "run-old",
						status: "failed",
						startedAt: "2026-10-20T03:10:00Z",
						finishedAt: null,
					},
				}),
			),
		);
		expect(screen.getByText("Failed")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Show in history" }),
		).not.toBeInTheDocument();
	});

	test("only the instance admin can change it", async () => {
		stubApi({});
		await card(view(), false);
		expect(toggle()).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText("Only the instance admin can change automatic updates."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Save automatic updates" }),
		).toBeDisabled();
	});

	test("an agent too old to patch cannot be scheduled", async () => {
		stubApi({});
		await card(view({ agentSupported: false }));
		expect(toggle()).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText(/too old for automatic updates/),
		).toBeInTheDocument();
	});
});
