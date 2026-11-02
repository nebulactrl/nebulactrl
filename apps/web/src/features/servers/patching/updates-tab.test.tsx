import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	PatchPackage,
	PatchPlan,
	PatchRun,
	PatchView,
	PrincipalBody,
	Server as ServerWire,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { type ServerView, toServerView } from "../model";
import { UpdatesTab } from "./updates-tab";

/** Lets a test clear an optional field with `undefined`. */
type Loose<T> = { [K in keyof T]?: T[K] | undefined };

function server(over: Overrides<ServerWire> = {}): ServerView {
	return toServerView(
		make("Server", {
			agentVersion: "1.5.0",
			controlPlane: false,
			serviceCount: 0,
			buildMaxConcurrent: 1,
			canDecommission: true,
			id: "srv-1",
			name: "edge-1",
			status: "online",
			...over,
		}),
	);
}

function pkg(over: Loose<PatchPackage> = {}): PatchPackage {
	return {
		name: "openssl",
		from: "3.0.1",
		to: "3.0.2",
		security: false,
		...over,
	} as PatchPackage;
}

function patchRun(over: Loose<PatchRun> = {}): PatchRun {
	return {
		id: "run-1",
		action: "check",
		trigger: "manual",
		status: "succeeded",
		createdAt: "2026-10-27T10:00:00Z",
		finishedAt: "2026-10-27T10:01:00Z",
		exitCode: 0,
		rebootingSince: null,
		securityOnly: false,
		updatesAfter: 3,
		updatesBefore: null,
		...over,
	} as PatchRun;
}

function view(over: Partial<PatchView> = {}): PatchView {
	return {
		agentSupported: true,
		busy: "",
		checkedAt: "2026-10-27T09:00:00Z",
		controlPlane: false,
		known: true,
		manager: "apt",
		online: true,
		packages: [],
		productionServices: [],
		rebootPackages: [],
		rebootRequired: false,
		runs: [],
		security: 1,
		stale: false,
		supported: true,
		updates: 3,
		...over,
	};
}

function plan(over: Partial<PatchPlan> = {}): PatchPlan {
	return {
		checkedAt: "2026-10-27T09:00:00Z",
		confirm: "edge-1",
		controlPlane: false,
		manager: "apt",
		packages: [
			pkg(),
			pkg({ name: "curl" }),
			pkg({ name: "libc6", security: true }),
		],
		productionServices: [],
		ready: true,
		rebootLikely: false,
		requiresAcknowledgement: false,
		restartsDocker: false,
		risks: [],
		security: 1,
		securityOnly: false,
		server: "edge-1",
		servicesOnServer: 0,
		stale: false,
		updates: 3,
		warnings: [],
		...over,
	} as PatchPlan;
}

function me(role: NonNullable<PrincipalBody["role"]>, instanceAdmin: boolean) {
	return {
		"GET /me": {
			body: make("MeBody", {
				principal: { role, scopes: [], type: "user", label: "x" },
				instanceAdmin,
			}),
		},
		"GET /me/security": {
			body: make("SecurityBody", { isInstanceAdmin: instanceAdmin }),
		},
	};
}
const root = me("owner", true);

const prod = {
	name: "api",
	project: "shop",
	environment: "production",
	environmentId: "env-1",
	projectId: "prj-1",
	serviceId: "svc-1",
};

const GET = "GET /servers/:serverId/patches";
const tab = (srv: ServerView = server()) =>
	renderWithProviders(<UpdatesTab server={srv} />);

/** The page has loaded its view, /me and /me/security: the admin-gated buttons settle. */
async function settled(name: string | RegExp) {
	const button = await screen.findByRole("button", { name });
	await waitFor(() => expect(button).toBeEnabled());
	return button;
}

/** A run's row button in the History card (the actions above also say "Check"). */
async function historyRow(name: RegExp) {
	const list = (await screen.findByText("History"))
		.closest("div")
		?.querySelector("ul");
	if (!list) throw new Error("history list missing");
	return within(list as HTMLElement).getByRole("button", { name });
}

describe("UpdatesTab loading and failure", () => {
	test("shows a labelled placeholder while loading", async () => {
		const { promise, resolve } = Promise.withResolvers<Response>();
		stubApi({ ...root, [GET]: () => promise });
		await tab();
		expect(
			screen.getByRole("status", { name: "Loading updates" }),
		).toBeInTheDocument();
		resolve(Response.json(view()));
		expect(
			await screen.findByText("Operating system updates"),
		).toBeInTheDocument();
	});

	test("a failed load says so with the reason and Try again recovers", async () => {
		let calls = 0;
		stubApi({
			...root,
			[GET]: () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{ title: "x", detail: "agent offline" },
							{ status: 500 },
						)
					: Response.json(view());
			},
		});
		await tab();
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Updates did not load");
		expect(alert).toHaveTextContent(
			"The control plane did not answer: agent offline. Nothing was changed.",
		);
		await userEvent
			.setup()
			.click(within(alert).getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByText("Operating system updates"),
		).toBeInTheDocument();
	});
});

describe("UpdatesTab summary", () => {
	test("shows pending updates, the security count, manager and last check", async () => {
		stubApi({ ...root, [GET]: { body: view() } });
		await tab();
		const pending = (await screen.findByText("Pending")).parentElement;
		expect(pending).toHaveTextContent("3");
		expect(pending).toHaveTextContent("updates");
		expect(pending).toHaveTextContent("3 updates (1 security)");
		expect(screen.getByText("apt")).toBeInTheDocument();
		expect(screen.getByText("not required")).toBeInTheDocument();
		expect(screen.queryByText(/out of date/)).toBeNull();
	});

	test("one update reads singular, a stale check is flagged", async () => {
		stubApi({
			...root,
			[GET]: { body: view({ updates: 1, security: 0, stale: true }) },
		});
		await tab();
		const pending = (await screen.findByText("Pending")).parentElement;
		expect(pending).toHaveTextContent("1 update");
		expect(pending).not.toHaveTextContent("1 updates");
		expect(screen.getByText(/out of date/)).toBeInTheDocument();
	});

	test("an unchecked server shows dashes, never, and the host message", async () => {
		stubApi({
			...root,
			[GET]: {
				body: view({
					known: false,
					checkedAt: null,
					manager: "",
					message: "Agent has not reported yet",
				}),
			},
		});
		await tab();
		const pending = (await screen.findByText("Pending")).parentElement;
		expect(pending).toHaveTextContent("–");
		expect(pending).toHaveTextContent("Updates not checked yet");
		expect(screen.getByText("never")).toBeInTheDocument();
		expect(screen.getByText("Agent has not reported yet")).toBeInTheDocument();
	});

	test("a host with no package manager says none was found", async () => {
		stubApi({
			...root,
			[GET]: {
				body: view({ supported: false, manager: "", updates: 0, security: 0 }),
			},
		});
		await tab();
		expect(await screen.findByText("none found")).toBeInTheDocument();
		expect(screen.getByText("Updates not managed here")).toBeInTheDocument();
	});

	test("a required reboot shows the alert with the first four packages and an ellipsis", async () => {
		stubApi({
			...root,
			[GET]: {
				body: view({
					rebootRequired: true,
					rebootPackages: ["linux-image", "libc6", "systemd", "dbus", "extra"],
				}),
			},
		});
		await tab();
		const title = await screen.findByText("edge-1 needs a reboot");
		expect(title.parentElement).toHaveTextContent(
			"(linux-image, libc6, systemd, dbus …)",
		);
		expect(screen.getByText("required")).toBeInTheDocument();
	});

	test("a required reboot with no named packages omits the parentheses", async () => {
		stubApi({ ...root, [GET]: { body: view({ rebootRequired: true }) } });
		await tab();
		const title = await screen.findByText("edge-1 needs a reboot");
		expect(title.parentElement).not.toHaveTextContent("(");
	});

	test("production services are named in a notice", async () => {
		stubApi({
			...root,
			[GET]: {
				body: view({ productionServices: [prod] }),
			},
		});
		await tab();
		const title = await screen.findByText("Production runs here");
		expect(title.parentElement).toHaveTextContent("shop/api");
	});
});

describe("UpdatesTab busy and hints", () => {
	test.each([
		[
			"check",
			"Checking for updates",
			"This page updates by itself when the host answers.",
		],
		[
			"apply",
			"Upgrading packages",
			"This page updates by itself when the host answers.",
		],
		[
			"reboot",
			"Rebooting",
			"NebulaCtrl is waiting for the agent to reconnect on a new boot.",
		],
	] as const)(
		"a %s in flight is announced and disables the actions",
		async (busy, title, body) => {
			stubApi({ ...root, [GET]: { body: view({ busy }) } });
			await tab();
			const alertTitle = (await screen.findAllByText(title))[0] as HTMLElement;
			expect(
				alertTitle.closest("[role=alert]") ?? alertTitle.parentElement,
			).toHaveTextContent(body);
			expect(
				screen.getByRole("button", { name: /Check for updates/ }),
			).toBeDisabled();
			expect(
				screen.getByRole("button", { name: /Apply 3 updates/ }),
			).toBeDisabled();
			expect(
				screen.getByRole("button", { name: /Reboot server/ }),
			).toBeDisabled();
			expect(
				screen.getAllByText(`${title} is in progress.`).length,
			).toBeGreaterThan(0);
		},
	);

	test("a developer can read but not check, apply or reboot", async () => {
		stubApi({ ...me("developer", false), [GET]: { body: view() } });
		await tab();
		expect(
			await screen.findByText("Only Owners and Admins can check a host."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Check for updates" }),
		).toBeDisabled();
		expect(
			screen.getAllByText(
				"Only the instance admin can upgrade packages or reboot a server.",
			),
		).toHaveLength(2);
		expect(
			screen.getByRole("button", { name: /Apply 3 updates/ }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: /Reboot server/ }),
		).toBeDisabled();
	});

	test("an admin who is not the instance admin may check but not upgrade", async () => {
		stubApi({ ...me("admin", false), [GET]: { body: view() } });
		await tab();
		await settled("Check for updates");
		expect(
			screen.getByRole("button", { name: /Apply 3 updates/ }),
		).toBeDisabled();
	});

	test("an old agent explains why nothing can run", async () => {
		stubApi({ ...root, [GET]: { body: view({ agentSupported: false }) } });
		await tab();
		expect(
			(
				await screen.findAllByText(
					"This agent is too old to manage updates. Update it first.",
				)
			).length,
		).toBe(3);
	});

	test("an offline server says actions start when it is back", async () => {
		stubApi({ ...root, [GET]: { body: view() } });
		await tab(server({ status: "offline" }));
		expect(
			(
				await screen.findAllByText(
					"The server is offline. Actions start when it is back online.",
				)
			).length,
		).toBe(3);
	});

	test("applying needs a first check and a supported package manager", async () => {
		stubApi({
			...root,
			[GET]: { body: view({ known: false, updates: 0, security: 0 }) },
		});
		const first = await tab();
		expect(
			await screen.findByText("Check for updates first."),
		).toBeInTheDocument();
		first.unmount();
		stubApi({
			...root,
			[GET]: { body: view({ supported: false, updates: 0, security: 0 }) },
		});
		await tab();
		expect(
			await screen.findByText(
				"This host has no supported package manager (apt or dnf).",
			),
		).toBeInTheDocument();
	});

	test("with nothing pending the Apply button is disabled and unnumbered", async () => {
		stubApi({ ...root, [GET]: { body: view({ updates: 0, security: 0 }) } });
		await tab();
		const apply = await screen.findByRole("button", { name: "Apply updates…" });
		expect(apply).toBeDisabled();
	});
});

describe("UpdatesTab check", () => {
	test("Check for updates posts, toasts and reloads the view", async () => {
		let loads = 0;
		const api = stubApi({
			...root,
			[GET]: () => {
				loads += 1;
				return Response.json(view());
			},
			"POST /servers/:serverId/patches/check": {
				status: 202,
				body: patchRun({ action: "check", status: "pending" }),
			},
		});
		await tab();
		await userEvent.setup().click(await settled("Check for updates"));
		expect(
			(await screen.findAllByText("Checking for updates")).length,
		).toBeGreaterThan(0);
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/servers/srv-1/patches/check",
		);
		await waitFor(() => expect(loads).toBe(2));
		expect(api.unmatched).toEqual([]);
	});

	test("a failed check shows the error", async () => {
		stubApi({
			...root,
			[GET]: { body: view() },
			"POST /servers/:serverId/patches/check": () =>
				Response.json(
					{ title: "x", detail: "A check is already queued" },
					{ status: 409 },
				),
		});
		await tab();
		await userEvent.setup().click(await settled("Check for updates"));
		expect(
			await screen.findByText("A check is already queued"),
		).toBeInTheDocument();
	});
});

describe("UpdatesTab apply", () => {
	test("opens the plan, asks for the server name, then upgrades", async () => {
		const api = stubApi({
			...root,
			[GET]: { body: view() },
			"GET /servers/:serverId/patches/plan": { body: plan() },
			"POST /servers/:serverId/patches/apply": {
				status: 202,
				body: patchRun({ action: "apply", status: "pending" }),
			},
		});
		await tab();
		const user = userEvent.setup();
		await user.click(await settled(/Apply 3 updates/));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			api.calls
				.find((c) => c.path.endsWith("/plan"))
				?.query.get("securityOnly"),
		).toBe("false");
		expect(dialog).toHaveTextContent("Upgrade 3 packages on edge-1?");
		expect(dialog).toHaveTextContent(
			"Upgrades 3 packages (1 security) with apt, as root, on edge-1.",
		);
		expect(dialog).toHaveTextContent("Packages: openssl, curl, libc6");
		expect(dialog).not.toHaveTextContent("Production runs on this server");

		const confirm = within(dialog).getByRole("button", {
			name: "Upgrade packages",
		});
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByRole("textbox"), "edge-1");
		expect(confirm).toBeEnabled();
		await user.click(confirm);
		await waitFor(() =>
			expect(api.calls.find((c) => c.path.endsWith("/apply"))).toMatchObject({
				method: "POST",
				path: "/servers/srv-1/patches/apply",
				body: {
					confirm: "edge-1",
					securityOnly: false,
					acknowledgeProduction: false,
				},
			}),
		);
		expect(
			(await screen.findAllByText("Upgrading packages")).length,
		).toBeGreaterThan(0);
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});

	test("a wrong name keeps the upgrade locked and sends nothing", async () => {
		const api = stubApi({
			...root,
			[GET]: { body: view() },
			"GET /servers/:serverId/patches/plan": { body: plan() },
		});
		await tab();
		const user = userEvent.setup();
		await user.click(await settled(/Apply 3 updates/));
		const dialog = await screen.findByRole("alertdialog");
		await user.type(within(dialog).getByRole("textbox"), "edge-2");
		expect(
			within(dialog).getByRole("button", { name: "Upgrade packages" }),
		).toBeDisabled();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});

	test("Security only asks for the security plan and sends securityOnly true", async () => {
		const api = stubApi({
			...root,
			[GET]: { body: view() },
			"GET /servers/:serverId/patches/plan": {
				body: plan({
					securityOnly: true,
					updates: 1,
					packages: [pkg({ name: "libc6", security: true })],
				}),
			},
			"POST /servers/:serverId/patches/apply": {
				status: 202,
				body: patchRun({
					action: "apply",
					status: "pending",
					securityOnly: true,
				}),
			},
		});
		await tab();
		const user = userEvent.setup();
		await user.click(await settled(/Security only \(1\)/));
		const dialog = await screen.findByRole("alertdialog");
		expect(
			api.calls
				.find((c) => c.path.endsWith("/plan"))
				?.query.get("securityOnly"),
		).toBe("true");
		expect(dialog).toHaveTextContent(
			"Upgrades the 1 package with a security update",
		);
		await user.type(within(dialog).getByRole("textbox"), "edge-1");
		await user.click(
			within(dialog).getByRole("button", { name: "Upgrade packages" }),
		);
		await waitFor(() =>
			expect(api.calls.find((c) => c.path.endsWith("/apply"))?.body).toEqual({
				confirm: "edge-1",
				securityOnly: true,
				acknowledgeProduction: false,
			}),
		);
	});

	test.each([
		["all updates are security ones", { security: 3 }],
		["nothing is security", { security: 0 }],
		["the host was never checked", { known: false }],
	])("no Security only button when %s", async (_name, over) => {
		stubApi({ ...root, [GET]: { body: view(over) } });
		await tab();
		await screen.findByText("Operating system updates");
		expect(screen.queryByRole("button", { name: /Security only/ })).toBeNull();
	});

	test("an unready plan toasts its reason and opens no dialog", async () => {
		stubApi({
			...root,
			[GET]: { body: view() },
			"GET /servers/:serverId/patches/plan": {
				body: plan({ ready: false, notReadyReason: "The last check is stale" }),
			},
		});
		await tab();
		await userEvent.setup().click(await settled(/Apply 3 updates/));
		expect(await screen.findByText("Nothing to upgrade")).toBeInTheDocument();
		expect(screen.getByText("The last check is stale")).toBeInTheDocument();
		expect(screen.queryByRole("alertdialog")).toBeNull();
	});

	test("a plan that fails to load toasts the error", async () => {
		stubApi({
			...root,
			[GET]: { body: view() },
			"GET /servers/:serverId/patches/plan": () =>
				Response.json(
					{ title: "x", detail: "agent timed out" },
					{ status: 504 },
				),
		});
		await tab();
		await userEvent.setup().click(await settled(/Apply 3 updates/));
		expect(
			await screen.findByText("The plan did not load"),
		).toBeInTheDocument();
		expect(screen.getByText("agent timed out")).toBeInTheDocument();
		expect(screen.queryByRole("alertdialog")).toBeNull();
	});

	test("the plan lists cautions, production and control-plane warnings, and truncates long package lists", async () => {
		const many = Array.from({ length: 14 }, (_, i) => pkg({ name: `pkg${i}` }));
		stubApi({
			...root,
			[GET]: { body: view() },
			"GET /servers/:serverId/patches/plan": {
				body: plan({
					packages: many,
					updates: 14,
					productionServices: [prod],
					controlPlane: true,
					requiresAcknowledgement: true,
					risks: [
						{
							package: "docker-ce",
							reason: "restarts containers",
							reboot: false,
						},
					],
					warnings: ["Low disk space"],
				}),
			},
		});
		await tab();
		await userEvent.setup().click(await settled(/Apply 3 updates/));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent(
			"Production runs on this server: shop/api.",
		);
		expect(dialog).toHaveTextContent("This server runs NebulaCtrl itself.");
		expect(dialog).toHaveTextContent("docker-ce: restarts containers");
		expect(dialog).toHaveTextContent("Low disk space");
		expect(dialog).toHaveTextContent("pkg11, and 2 more");
		expect(dialog).not.toHaveTextContent("pkg12");
		// Production means a hold, not a click.
		expect(
			within(dialog).getByRole("button", { name: /Hold to upgrade packages/ }),
		).toBeInTheDocument();
	});

	test("a plan that needs acknowledgement is held to confirm and sends acknowledgeProduction", async () => {
		const api = stubApi({
			...root,
			[GET]: { body: view() },
			"GET /servers/:serverId/patches/plan": {
				body: plan({
					requiresAcknowledgement: true,
					productionServices: [prod],
				}),
			},
			"POST /servers/:serverId/patches/apply": {
				status: 202,
				body: patchRun({ action: "apply", status: "pending" }),
			},
		});
		await tab();
		const user = userEvent.setup();
		await user.click(await settled(/Apply 3 updates/));
		const dialog = await screen.findByRole("alertdialog");
		await user.type(within(dialog).getByRole("textbox"), "edge-1");
		const button = within(dialog).getByRole("button", {
			name: /Hold to upgrade packages/,
		});
		// A tap is not enough.
		fireEvent.keyDown(button, { key: " " });
		fireEvent.keyUp(button, { key: " " });
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		fireEvent.keyDown(button, { key: " " });
		await waitFor(
			() =>
				expect(api.calls.find((c) => c.path.endsWith("/apply"))?.body).toEqual({
					confirm: "edge-1",
					securityOnly: false,
					acknowledgeProduction: true,
				}),
			{ timeout: 4000 },
		);
	});
});

describe("UpdatesTab reboot", () => {
	test("the confirmation lists what goes down, and a held press reboots", async () => {
		const api = stubApi({
			...root,
			[GET]: {
				body: view({
					productionServices: [prod],
					controlPlane: true,
				}),
			},
			"POST /servers/:serverId/patches/reboot": {
				status: 202,
				body: patchRun({ action: "reboot", status: "rebooting" }),
			},
		});
		await tab(server({ serviceCount: 4 }));
		const user = userEvent.setup();
		await user.click(await settled(/Reboot server/));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Reboot edge-1?");
		expect(dialog).toHaveTextContent(
			"4 services on this server go down while it reboots and start again when Docker is back.",
		);
		expect(dialog).toHaveTextContent("Production is affected: shop/api.");
		expect(dialog).toHaveTextContent("This server runs NebulaCtrl itself.");
		expect(dialog).toHaveTextContent("more than 20 minutes");

		await user.type(within(dialog).getByRole("textbox"), "edge-1");
		const button = within(dialog).getByRole("button", {
			name: /Hold to reboot server/,
		});
		fireEvent.keyDown(button, { key: " " });
		fireEvent.keyUp(button, { key: " " });
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		fireEvent.keyDown(button, { key: " " });
		await waitFor(
			() =>
				expect(api.calls.find((c) => c.path.endsWith("/reboot"))).toMatchObject(
					{
						method: "POST",
						body: { confirm: "edge-1", acknowledgeProduction: true },
					},
				),
			{ timeout: 4000 },
		);
		expect(
			(await screen.findAllByText("Reboot scheduled")).length,
		).toBeGreaterThan(0);
	});

	test("a quiet server reboots without the production acknowledgement", async () => {
		const api = stubApi({
			...root,
			[GET]: { body: view() },
			"POST /servers/:serverId/patches/reboot": {
				status: 202,
				body: patchRun({ action: "reboot", status: "rebooting" }),
			},
		});
		await tab();
		const user = userEvent.setup();
		await user.click(await settled(/Reboot server/));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent(
			"Anything that runs on this server goes down while it reboots.",
		);
		expect(dialog).not.toHaveTextContent("Production is affected");
		await user.type(within(dialog).getByRole("textbox"), "edge-1");
		fireEvent.keyDown(
			within(dialog).getByRole("button", { name: /Hold to reboot server/ }),
			{
				key: " ",
			},
		);
		await waitFor(
			() =>
				expect(api.calls.find((c) => c.path.endsWith("/reboot"))?.body).toEqual(
					{
						confirm: "edge-1",
						acknowledgeProduction: false,
					},
				),
			{ timeout: 4000 },
		);
	});

	test("an unconfirmed name refuses the hold", async () => {
		const api = stubApi({ ...root, [GET]: { body: view() } });
		await tab();
		const user = userEvent.setup();
		await user.click(await settled(/Reboot server/));
		const dialog = await screen.findByRole("alertdialog");
		fireEvent.keyDown(
			within(dialog).getByRole("button", { name: /Hold to reboot server/ }),
			{
				key: " ",
			},
		);
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});
});

describe("UpdatesTab packages", () => {
	test("lists pending packages with versions, a Security badge and the risk note", async () => {
		stubApi({
			...root,
			[GET]: {
				body: view({
					packages: [
						pkg({ name: "libc6", from: "2.35", to: "2.36", security: true }),
						pkg({
							name: "docker-ce",
							from: undefined,
							to: "27.0",
							risk: "Restarts containers",
						}),
					],
				}),
			},
		});
		await tab();
		const list = (await screen.findByText("Pending packages"))
			.closest("div")
			?.querySelector("ul") as HTMLElement;
		const [first, second] = within(list).getAllByRole("listitem");
		expect(first).toHaveTextContent("libc6");
		expect(first).toHaveTextContent("2.35 → 2.36");
		expect(first).toHaveTextContent("Security");
		expect(second).toHaveTextContent("docker-ce");
		expect(second).toHaveTextContent("27.0");
		expect(second).not.toHaveTextContent("→");
		expect(second).not.toHaveTextContent("Security");
		expect(second).toHaveTextContent("Restarts containers");
	});

	test("a truncated list says how many of the total are shown", async () => {
		stubApi({
			...root,
			[GET]: {
				body: view({ updates: 40, packages: [pkg(), pkg({ name: "b" })] }),
			},
		});
		await tab();
		expect(
			await screen.findByText(
				"The first 2 of 40 are listed, security updates first.",
			),
		).toBeInTheDocument();
	});

	test("no package card when nothing is pending", async () => {
		stubApi({ ...root, [GET]: { body: view({ packages: [] }) } });
		await tab();
		await screen.findByText("Operating system updates");
		expect(screen.queryByText("Pending packages")).toBeNull();
	});
});

describe("UpdatesTab history", () => {
	test("an empty history says nothing has run", async () => {
		stubApi({ ...root, [GET]: { body: view({ runs: [] }) } });
		await tab();
		expect(
			await screen.findByText(/Nothing has run on this server yet/),
		).toBeInTheDocument();
	});

	test("each run shows its action, trigger and summary", async () => {
		stubApi({
			...root,
			[GET]: {
				body: view({
					runs: [
						patchRun({
							id: "r1",
							action: "check",
							trigger: "scheduled",
							updatesAfter: 0,
						}),
						patchRun({ id: "r2", action: "check", updatesAfter: 5 }),
						patchRun({ id: "r3", action: "check", updatesAfter: null }),
						patchRun({
							id: "r4",
							action: "apply",
							updatesBefore: 5,
							updatesAfter: 1,
						}),
						patchRun({
							id: "r5",
							action: "apply",
							updatesBefore: null,
							updatesAfter: null,
						}),
						patchRun({
							id: "r6",
							action: "reboot",
							message: "Back after 3 min",
						}),
						patchRun({ id: "r7", action: "reboot", message: undefined }),
						patchRun({ id: "r8", status: "failed", error: "dpkg locked" }),
						patchRun({ id: "r9", status: "pending" }),
						patchRun({
							id: "r10",
							action: "reboot",
							status: "rebooting",
							message: undefined,
						}),
					],
				}),
			},
		});
		await tab();
		const list = (await screen.findByText("History"))
			.closest("div")
			?.querySelector("ul") as HTMLElement;
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(10);
		expect(rows[0]).toHaveTextContent("Check");
		expect(rows[0]).toHaveTextContent("Automatic");
		expect(rows[0]).toHaveTextContent("Up to date");
		expect(rows[1]).toHaveTextContent("By hand");
		expect(rows[1]).toHaveTextContent("5 updates pending");
		expect(rows[2]).toHaveTextContent("Checked");
		expect(rows[3]).toHaveTextContent("Upgrade");
		expect(rows[3]).toHaveTextContent("5 updates before, 1 left");
		expect(rows[4]).toHaveTextContent("Packages upgraded");
		expect(rows[5]).toHaveTextContent("Reboot");
		expect(rows[5]).toHaveTextContent("Back after 3 min");
		expect(rows[6]).toHaveTextContent("Rebooted");
		expect(rows[7]).toHaveTextContent("dpkg locked");
		expect(rows[8]).toHaveTextContent("Running");
		expect(rows[9]).toHaveTextContent("Rebooting");
	});

	test("a check that said nothing is not expandable", async () => {
		stubApi({ ...root, [GET]: { body: view({ runs: [patchRun()] }) } });
		await tab();
		const row = await historyRow(/By hand/);
		expect(row).toBeDisabled();
		expect(row).not.toHaveAttribute("aria-expanded");
	});

	test("a failed check opens to its error and message without asking for a log", async () => {
		const api = stubApi({
			...root,
			[GET]: {
				body: view({
					runs: [
						patchRun({
							status: "failed",
							error: "apt update failed",
							message: "exit 100",
						}),
					],
				}),
			},
		});
		await tab();
		const row = await historyRow(/By hand/);
		await userEvent.setup().click(row);
		const detail = (await screen.findByText("exit 100"))
			.parentElement as HTMLElement;
		expect(detail).toHaveTextContent("apt update failed");
		expect(api.calls.filter((c) => c.path.includes("/runs/"))).toEqual([]);
	});

	test("an upgrade opens to the log the package manager printed", async () => {
		const api = stubApi({
			...root,
			[GET]: {
				body: view({
					runs: [
						patchRun({
							id: "run-9",
							action: "apply",
							updatesBefore: 3,
							updatesAfter: 0,
						}),
					],
				}),
			},
			"GET /servers/:serverId/patches/runs/:runId": {
				body: patchRun({
					id: "run-9",
					action: "apply",
					log: "Unpacking openssl...\nDone.",
				}),
			},
		});
		await tab();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: /Upgrade/ }));
		expect(await screen.findByText(/Unpacking openssl/)).toHaveTextContent(
			"Done.",
		);
		expect(api.calls.find((c) => c.path.includes("/runs/"))?.path).toBe(
			"/servers/srv-1/patches/runs/run-9",
		);
	});

	test("an upgrade with an empty log says the package manager printed nothing", async () => {
		stubApi({
			...root,
			[GET]: {
				body: view({ runs: [patchRun({ id: "run-9", action: "apply" })] }),
			},
			"GET /servers/:serverId/patches/runs/:runId": {
				body: patchRun({ id: "run-9", action: "apply", log: "" }),
			},
		});
		await tab();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: /Upgrade/ }));
		expect(
			await screen.findByText("The package manager printed nothing."),
		).toBeInTheDocument();
	});

	test("a running upgrade says the log appears when it ends", async () => {
		stubApi({
			...root,
			[GET]: {
				body: view({
					runs: [patchRun({ id: "run-9", action: "apply", status: "pending" })],
				}),
			},
			"GET /servers/:serverId/patches/runs/:runId": {
				body: patchRun({ id: "run-9", action: "apply", status: "pending" }),
			},
		});
		await tab();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: /Upgrade/ }));
		expect(
			await screen.findByText(
				"The upgrade is running; its log appears when it ends.",
			),
		).toBeInTheDocument();
	});
});
