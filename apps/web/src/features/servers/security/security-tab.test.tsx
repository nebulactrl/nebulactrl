import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	HardeningFix,
	HardeningRemaining,
	HardeningRun,
	HardeningView,
	Server as ServerWire,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { type ServerView, toServerView } from "../model";
import { SecurityTab } from "./security-tab";

/** Lets a test clear an optional field with `undefined`. */
type Loose<T> = { [K in keyof T]?: T[K] | undefined };

function server(over: Overrides<ServerWire> = {}): ServerView {
	return toServerView(
		make("Server", {
			agentVersion: "1.5.0",
			buildMaxConcurrent: 1,
			canDecommission: true,
			controlPlane: false,
			id: "srv-1",
			name: "edge-1",
			serviceCount: 0,
			status: "online",
			...over,
		}),
	);
}

function fix(over: Partial<HardeningFix> = {}): HardeningFix {
	return {
		id: "sysctl-hardening",
		title: "Harden kernel parameters",
		default: true,
		state: "pending",
		why: "Turns off source routing.",
		risk: "Rarely breaks asymmetric routing.",
		lynisTests: ["KRNL-6000"],
		manual: ["sysctl -w net.ipv4.conf.all.accept_source_route=0"],
		rebootRequired: false,
		...over,
	};
}

function finding(over: Partial<HardeningRemaining> = {}): HardeningRemaining {
	return {
		testId: "SSH-7440",
		severity: "suggestion",
		title: "Consider hardening SSH configuration",
		link: "https://cisofy.com/lynis/controls/SSH-7440/",
		manual: [],
		...over,
	};
}

function run(over: Partial<HardeningRun> = {}): HardeningRun {
	return {
		id: "run-1",
		action: "audit",
		trigger: "manual",
		status: "succeeded",
		createdAt: "2026-10-27T10:00:00Z",
		finishedAt: "2026-10-27T10:01:00Z",
		fixIds: [],
		hardeningIndex: 70,
		indexBefore: null,
		outcomes: [],
		...over,
	};
}

function view(over: Loose<HardeningView> = {}): HardeningView {
	return {
		agentSupported: true,
		auditedAt: "2026-10-27T10:00:00Z",
		firstIndex: 60,
		fixes: [],
		hardeningIndex: 72,
		lynisInstalled: true,
		lynisVersion: "3.1.0",
		mode: "helper",
		nextAuditAt: null,
		pendingSafe: 0,
		rebootRequired: false,
		remaining: [],
		running: false,
		runs: [],
		suggestions: 5,
		testsDone: 240,
		warnings: 3,
		weekly: false,
		...over,
	} as HardeningView;
}

const admin = {
	"GET /me": {
		body: make("MeBody", {
			principal: { role: "admin", scopes: ["admin"], type: "user", label: "r" },
			instanceAdmin: true,
		}),
	},
};
const viewer = {
	"GET /me": {
		body: make("MeBody", {
			principal: {
				role: "viewer",
				scopes: ["servers"],
				type: "user",
				label: "v",
			},
			instanceAdmin: false,
		}),
	},
};

/** The switch is a span: enabled means it is not marked aria-disabled. */
const switchEnabled = (el: HTMLElement) =>
	waitFor(() => expect(el).not.toHaveAttribute("aria-disabled", "true"));

const GET = "GET /servers/:serverId/hardening";

/** A page whose first load shows `v` and whose next loads show whatever `latest` holds. */
function tab(srv: ServerView = server()) {
	return renderWithProviders(<SecurityTab server={srv} />);
}

describe("SecurityTab loading and failure", () => {
	test("shows a labelled loading placeholder until the view arrives", async () => {
		const { promise: gate, resolve: release } =
			Promise.withResolvers<Response>();
		stubApi({ ...admin, [GET]: () => gate });
		await tab();
		expect(
			screen.getByRole("status", { name: "Loading security" }),
		).toBeInTheDocument();
		release(Response.json(view()));
		expect(await screen.findByText("Lynis score")).toBeInTheDocument();
		expect(
			screen.queryByRole("status", { name: "Loading security" }),
		).toBeNull();
	});

	test("an API failure says so with its reason, and Try again loads the view", async () => {
		let calls = 0;
		const api = stubApi({
			...admin,
			[GET]: () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{ title: "Boom", detail: "agent offline" },
							{ status: 500 },
						)
					: Response.json(view());
			},
		});
		await tab();
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Security did not load");
		expect(alert).toHaveTextContent(
			"The control plane did not answer: agent offline. Nothing was changed.",
		);
		await userEvent
			.setup()
			.click(within(alert).getByRole("button", { name: "Try again" }));
		expect(await screen.findByText("Lynis score")).toBeInTheDocument();
		expect(screen.queryByRole("alert")).toBeNull();
		expect(
			api.calls.filter((c) => c.path === "/servers/srv-1/hardening"),
		).toHaveLength(2);
	});
});

describe("SecurityTab index card", () => {
	test("shows the score, movement since the first audit and the audit facts", async () => {
		stubApi({ ...admin, [GET]: { body: view() } });
		await tab();
		const score = (await screen.findByText("Lynis score")).parentElement;
		expect(score).toHaveTextContent("72");
		expect(score).toHaveTextContent("of 100");
		expect(score).toHaveTextContent("+12 since the first audit (60)");
		expect(screen.getByText("version 3.1.0")).toBeInTheDocument();
		expect(screen.getByText(/3 warnings, 5 suggestions/)).toHaveTextContent(
			"from 240 tests",
		);
		expect(screen.getByText("through a helper container")).toBeInTheDocument();
	});

	test("a score that fell reads as a drop, a host-mode agent as direct", async () => {
		stubApi({
			...admin,
			[GET]: {
				body: view({ hardeningIndex: 50, firstIndex: 60, mode: "host" }),
			},
		});
		await tab();
		expect(
			await screen.findByText("−10 since the first audit (60)"),
		).toBeInTheDocument();
		expect(
			screen.getByText("directly (agent on the host)"),
		).toBeInTheDocument();
	});

	test("before the first audit there is no score, no findings and no What is left card", async () => {
		stubApi({
			...admin,
			[GET]: {
				body: view({
					hardeningIndex: null,
					firstIndex: null,
					auditedAt: null,
					lynisInstalled: false,
					lynisVersion: undefined,
					testsDone: 0,
					mode: "",
				}),
			},
		});
		await tab();
		const score = (await screen.findByText("Lynis score")).parentElement;
		expect(score).toHaveTextContent("–");
		expect(score).not.toHaveTextContent("of 100");
		expect(screen.getByText("never")).toBeInTheDocument();
		expect(screen.getByText("not installed")).toBeInTheDocument();
		expect(
			screen.getByText("Lynis is not installed on this server"),
		).toBeInTheDocument();
		expect(screen.queryByText("What is left")).toBeNull();
		expect(screen.queryByText(/from \d+ tests/)).toBeNull();
	});

	test("an installed Lynis without a reported version reads unknown", async () => {
		stubApi({ ...admin, [GET]: { body: view({ lynisVersion: "" }) } });
		await tab();
		expect(await screen.findByText("version unknown")).toBeInTheDocument();
	});

	test("no install notice when the agent is too old to audit at all", async () => {
		stubApi({
			...admin,
			[GET]: { body: view({ lynisInstalled: false, agentSupported: false }) },
		});
		await tab();
		await screen.findByText("Lynis score");
		expect(
			screen.queryByText("Lynis is not installed on this server"),
		).toBeNull();
	});

	test("a reboot-required view shows the reboot alert naming the server", async () => {
		stubApi({ ...admin, [GET]: { body: view({ rebootRequired: true }) } });
		await tab();
		const title = await screen.findByText("A reboot is needed");
		expect(title.parentElement).toHaveTextContent(
			"An applied fix takes effect after the next reboot of edge-1.",
		);
	});

	test("no reboot alert otherwise", async () => {
		stubApi({ ...admin, [GET]: { body: view() } });
		await tab();
		await screen.findByText("Lynis score");
		expect(screen.queryByText("A reboot is needed")).toBeNull();
	});
});

describe("SecurityTab audit", () => {
	test("Run audit posts installLynis false, toasts and reloads the view", async () => {
		let loads = 0;
		const api = stubApi({
			...admin,
			[GET]: () => {
				loads += 1;
				return Response.json(view());
			},
			"POST /servers/:serverId/hardening/audit": {
				status: 202,
				body: run({ action: "audit", status: "pending" }),
			},
		});
		await tab();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Run audit" }));
		expect(await screen.findAllByText("Audit started")).not.toHaveLength(0);
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/servers/srv-1/hardening/audit");
		expect(post?.body).toEqual({ installLynis: false });
		await waitFor(() => expect(loads).toBe(2));
		expect(api.unmatched).toEqual([]);
	});

	test("without Lynis the button installs it first: installLynis true", async () => {
		const api = stubApi({
			...admin,
			[GET]: { body: view({ lynisInstalled: false, hardeningIndex: null }) },
			"POST /servers/:serverId/hardening/audit": {
				status: 202,
				body: run({ action: "audit", status: "pending" }),
			},
		});
		await tab();
		await userEvent.setup().click(
			await screen.findByRole("button", {
				name: "Install Lynis and run audit",
			}),
		);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
				installLynis: true,
			}),
		);
	});

	test("a failed audit start shows the error under the cards", async () => {
		stubApi({
			...admin,
			[GET]: { body: view() },
			"POST /servers/:serverId/hardening/audit": () =>
				Response.json(
					{ title: "Conflict", detail: "A run is already queued" },
					{ status: 409 },
				),
		});
		await tab();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Run audit" }));
		expect(
			await screen.findByText("A run is already queued"),
		).toBeInTheDocument();
	});

	test("a run in flight shows Working and disables the button with the reason", async () => {
		stubApi({ ...admin, [GET]: { body: view({ running: true }) } });
		await tab();
		const button = await screen.findByRole("button", { name: /Working/ });
		expect(button).toBeDisabled();
		expect(screen.getByText("A run is in progress.")).toBeInTheDocument();
	});

	test("a viewer cannot audit or switch the weekly audit, and is told why", async () => {
		stubApi({
			...viewer,
			[GET]: {
				body: view({ weekly: true, nextAuditAt: "2026-11-03T10:00:00Z" }),
			},
		});
		await tab();
		await waitFor(() =>
			expect(
				screen.getByText("Only Owners and Admins can audit or change a host."),
			).toBeInTheDocument(),
		);
		expect(screen.getByRole("button", { name: "Run audit" })).toBeDisabled();
		expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
	});

	test("an old agent explains why nothing can run", async () => {
		stubApi({ ...admin, [GET]: { body: view({ agentSupported: false }) } });
		await tab();
		expect(
			await screen.findByText(
				"This agent is too old to audit the host. Update it first.",
			),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Run audit" })).toBeDisabled();
	});

	test("an offline server says runs start when it is back", async () => {
		stubApi({ ...admin, [GET]: { body: view() } });
		await tab(server({ status: "offline" }));
		expect(
			await screen.findByText(
				"The server is offline. Runs start when it is back online.",
			),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Run audit" })).toBeDisabled();
	});

	test("a revoked server says so", async () => {
		stubApi({ ...admin, [GET]: { body: view() } });
		await tab(server({ status: "revoked" }));
		expect(
			await screen.findByText("This server was revoked."),
		).toBeInTheDocument();
	});
});

describe("SecurityTab weekly audit", () => {
	test("turning it on PUTs weekly true and the view reloads", async () => {
		const api = stubApi({
			...admin,
			[GET]: { body: view({ weekly: false }) },
			"PUT /servers/:serverId/hardening": { body: view({ weekly: true }) },
		});
		await tab();
		const toggle = await screen.findByRole("switch");
		await switchEnabled(toggle);
		expect(
			screen.getByText("Off. Audits run only when you start one."),
		).toBeInTheDocument();
		await userEvent.setup().click(toggle);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
				weekly: true,
			}),
		);
		await waitFor(() =>
			expect(
				api.calls.filter(
					(c) => c.method === "GET" && c.path === "/servers/srv-1/hardening",
				),
			).toHaveLength(2),
		);
	});

	test("without Lynis it is locked with the install hint", async () => {
		stubApi({
			...admin,
			[GET]: { body: view({ lynisInstalled: false, weekly: false }) },
		});
		await tab();
		const toggle = await screen.findByRole("switch");
		expect(toggle).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText(/Needs Lynis\. Install it with/),
		).toBeInTheDocument();
	});

	test("one that is on without Lynis stays reachable so it can be turned off", async () => {
		const api = stubApi({
			...admin,
			[GET]: { body: view({ lynisInstalled: false, weekly: true }) },
			"PUT /servers/:serverId/hardening": { body: view() },
		});
		await tab();
		const toggle = await screen.findByRole("switch");
		await switchEnabled(toggle);
		expect(screen.getByText(/every weekly audit fails/)).toBeInTheDocument();
		await userEvent.setup().click(toggle);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
				weekly: false,
			}),
		);
	});

	test("on with a schedule shows when the next audit is; on without one says none is scheduled", async () => {
		stubApi({
			...admin,
			[GET]: {
				body: view({ weekly: true, nextAuditAt: "2026-11-03T10:00:00Z" }),
			},
		});
		const first = await tab();
		expect(await screen.findByText(/^Next audit/)).toBeInTheDocument();
		first.unmount();

		stubApi({
			...admin,
			[GET]: { body: view({ weekly: true, nextAuditAt: null }) },
		});
		await tab();
		expect(
			await screen.findByText("On. No audit is scheduled yet."),
		).toBeInTheDocument();
	});

	test("a failed switch shows the error", async () => {
		stubApi({
			...admin,
			[GET]: { body: view() },
			"PUT /servers/:serverId/hardening": () =>
				Response.json(
					{ title: "Nope", detail: "weekly audit needs Lynis" },
					{ status: 422 },
				),
		});
		await tab();
		const toggle = await screen.findByRole("switch");
		await switchEnabled(toggle);
		await userEvent.setup().click(toggle);
		expect(
			await screen.findByText("weekly audit needs Lynis"),
		).toBeInTheDocument();
	});
});

describe("SecurityTab safe fixes", () => {
	const fixes = [
		fix({ id: "a", title: "Fix A", risk: "Risk A", rebootRequired: true }),
		fix({ id: "b", title: "Fix B", risk: "Risk B" }),
		fix({ id: "c", title: "Fix C", state: "applied", message: "applied ok" }),
		fix({ id: "d", title: "Fix D", state: "compliant" }),
		fix({
			id: "e",
			title: "Fix E",
			state: "unavailable",
			message: "no sshd here",
		}),
		fix({ id: "f", title: "Fix F", default: false, state: "pending" }),
	];

	test("each fix shows its status words, badges and offered actions", async () => {
		stubApi({ ...admin, [GET]: { body: view({ fixes }) } });
		await tab();
		const rows = within(
			(await screen.findByText("Fix A")).closest("ul") as HTMLElement,
		).getAllByRole("listitem");
		expect(rows).toHaveLength(6);
		const [a, b, c, d, e, f] = rows as [
			HTMLElement,
			HTMLElement,
			HTMLElement,
			HTMLElement,
			HTMLElement,
			HTMLElement,
		];
		expect(a).toHaveTextContent("Not applied");
		expect(a).toHaveTextContent("Needs a reboot");
		expect(
			within(a).getByRole("button", { name: "Apply" }),
		).toBeInTheDocument();
		expect(
			within(b).getByRole("button", { name: "Apply" }),
		).toBeInTheDocument();
		expect(c).toHaveTextContent("Applied");
		expect(c).not.toHaveTextContent("applied ok");
		expect(within(c).getByRole("button", { name: "Undo" })).toBeInTheDocument();
		expect(within(c).queryByRole("button", { name: "Apply" })).toBeNull();
		expect(d).toHaveTextContent("Already in place");
		expect(within(d).queryByRole("button", { name: /Apply|Undo/ })).toBeNull();
		expect(e).toHaveTextContent("Not available here");
		expect(e).toHaveTextContent("· no sshd here");
		expect(within(e).queryByRole("button", { name: /Apply|Undo/ })).toBeNull();
		expect(f).toHaveTextContent("On request");
		expect(
			within(f).getByRole("button", { name: "Apply" }),
		).toBeInTheDocument();
	});

	test("the note and the button count only recommended pending fixes and explain On request ones", async () => {
		stubApi({ ...admin, [GET]: { body: view({ fixes }) } });
		await tab();
		expect(
			await screen.findByRole("button", { name: "Apply safe fixes (2)" }),
		).toBeInTheDocument();
		expect(
			screen.getByText(/One fix is marked On request: the batch leaves it out/),
		).toBeInTheDocument();
	});

	test("with nothing left to apply the batch button is disabled and says so", async () => {
		stubApi({
			...admin,
			[GET]: { body: view({ fixes: [fix({ state: "applied" })] }) },
		});
		await tab();
		const button = await screen.findByRole("button", {
			name: "Safe fixes applied",
		});
		expect(button).toBeDisabled();
	});

	test("Apply safe fixes lists what will change, then posts the ids", async () => {
		const api = stubApi({
			...admin,
			[GET]: { body: view({ fixes }) },
			"POST /servers/:serverId/hardening/apply": {
				status: 202,
				body: run({ action: "apply", status: "pending" }),
			},
		});
		await tab();
		const user = userEvent.setup();
		const button = await screen.findByRole("button", {
			name: "Apply safe fixes (2)",
		});
		await waitFor(() => expect(button).toBeEnabled());
		await user.click(button);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Apply 2 safe fixes on edge-1?");
		expect(dialog).toHaveTextContent("Fix A");
		expect(dialog).toHaveTextContent("Risk A");
		expect(dialog).toHaveTextContent("Takes effect after the next reboot.");
		expect(dialog).toHaveTextContent("Fix B");
		expect(dialog).not.toHaveTextContent("Fix F");
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);

		await user.click(
			within(dialog).getByRole("button", { name: "Apply 2 fixes" }),
		);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")).toMatchObject({
				path: "/servers/srv-1/hardening/apply",
				body: { fixIds: ["a", "b"] },
			}),
		);
		expect(await screen.findAllByText("Applying fixes")).not.toHaveLength(0);
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});

	test("cancelling the confirmation sends nothing", async () => {
		const api = stubApi({ ...admin, [GET]: { body: view({ fixes }) } });
		await tab();
		const user = userEvent.setup();
		const button = await screen.findByRole("button", {
			name: "Apply safe fixes (2)",
		});
		await waitFor(() => expect(button).toBeEnabled());
		await user.click(button);
		await user.click(
			within(await screen.findByRole("alertdialog")).getByRole("button", {
				name: "Cancel",
			}),
		);
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls.filter((c) => c.method !== "GET")).toEqual([]);
	});

	test("a row's Apply asks about that fix alone and sends only its id", async () => {
		const api = stubApi({
			...admin,
			[GET]: { body: view({ fixes }) },
			"POST /servers/:serverId/hardening/apply": {
				status: 202,
				body: run({ action: "apply", status: "pending" }),
			},
		});
		await tab();
		const user = userEvent.setup();
		const row = (await screen.findByText("Fix F")).closest("li") as HTMLElement;
		const apply = within(row).getByRole("button", { name: "Apply" });
		await waitFor(() => expect(apply).toBeEnabled());
		await user.click(apply);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Apply “Fix F”?");
		await user.click(within(dialog).getByRole("button", { name: "Apply fix" }));
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
				fixIds: ["f"],
			}),
		);
	});

	test("a failed apply keeps the dialog open with the reason", async () => {
		stubApi({
			...admin,
			[GET]: { body: view({ fixes }) },
			"POST /servers/:serverId/hardening/apply": () =>
				Response.json({ title: "Bad", detail: "host busy" }, { status: 409 }),
		});
		await tab();
		const user = userEvent.setup();
		const button = await screen.findByRole("button", {
			name: "Apply safe fixes (2)",
		});
		await waitFor(() => expect(button).toBeEnabled());
		await user.click(button);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Apply 2 fixes" }),
		);
		expect(await within(dialog).findByText("host busy")).toBeInTheDocument();
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
	});

	test("Undo asks first, then posts a rollback for that fix", async () => {
		const api = stubApi({
			...admin,
			[GET]: { body: view({ fixes }) },
			"POST /servers/:serverId/hardening/rollback": {
				status: 202,
				body: run({ action: "rollback", status: "pending" }),
			},
		});
		await tab();
		const user = userEvent.setup();
		const row = (await screen.findByText("Fix C")).closest("li") as HTMLElement;
		const undo = within(row).getByRole("button", { name: "Undo" });
		await waitFor(() => expect(undo).toBeEnabled());
		await user.click(undo);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Undo “Fix C”?");
		expect(dialog).toHaveTextContent(
			"Anything you changed afterwards is kept.",
		);
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		await user.click(within(dialog).getByRole("button", { name: "Undo fix" }));
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")).toMatchObject({
				path: "/servers/srv-1/hardening/rollback",
				body: { fixIds: ["c"] },
			}),
		);
		expect(await screen.findAllByText("Undoing the fix")).not.toHaveLength(0);
	});

	test("row buttons are disabled while a run is in progress", async () => {
		stubApi({ ...admin, [GET]: { body: view({ fixes, running: true }) } });
		await tab();
		await screen.findByText("Fix A");
		for (const b of screen.getAllByRole("button", { name: /^(Apply|Undo)$/ })) {
			expect(b).toBeDisabled();
		}
		expect(
			screen.getByRole("button", { name: "Apply safe fixes (2)" }),
		).toBeDisabled();
	});

	test("expanding a fix shows what it does, the downside, Lynis links and the manual commands", async () => {
		stubApi({
			...admin,
			[GET]: {
				body: view({
					fixes: [
						fix({
							title: "Harden kernel",
							lynisTests: ["KRNL-6000", "KRNL-6001"],
							manual: ["line one", "line two"],
						}),
					],
				}),
			},
		});
		await tab();
		const user = userEvent.setup();
		const row = (await screen.findByText("Harden kernel")).closest(
			"li",
		) as HTMLElement;
		expect(within(row).queryByText("What it does")).toBeNull();
		await user.click(within(row).getByRole("button", { name: "Show details" }));
		expect(
			await within(row).findByText("Turns off source routing."),
		).toBeInTheDocument();
		expect(
			within(row).getByText("Rarely breaks asymmetric routing."),
		).toBeInTheDocument();
		expect(
			within(row).getByRole("link", { name: "KRNL-6000" }),
		).toHaveAttribute("href", "https://cisofy.com/lynis/controls/KRNL-6000/");
		expect(
			within(row).getByRole("link", { name: "KRNL-6001" }),
		).toHaveAttribute("href", "https://cisofy.com/lynis/controls/KRNL-6001/");
		expect(within(row).getByText(/line one/)).toHaveTextContent("line two");
		expect(
			within(row).getByRole("button", { name: "Hide details" }),
		).toHaveAttribute("aria-expanded", "true");
		await user.click(within(row).getByRole("button", { name: "Hide details" }));
		expect(
			within(row).getByRole("button", { name: "Show details" }),
		).toHaveAttribute("aria-expanded", "false");
		// Closed, the body is hidden at once and unmounted once the collapse ends.
		const body = within(row).queryByText("What it does");
		if (body) expect(body.closest("[aria-hidden=true]")).not.toBeNull();
	});

	test("clicking the fix title expands it too", async () => {
		stubApi({
			...admin,
			[GET]: { body: view({ fixes: [fix({ title: "T" })] }) },
		});
		await tab();
		const title = await screen.findByRole("button", { name: /^T/ });
		expect(title).toHaveAttribute("aria-expanded", "false");
		await userEvent.setup().click(title);
		expect(await screen.findByText("The downside")).toBeInTheDocument();
	});
});

describe("SecurityTab what is left", () => {
	test("an audit with nothing remaining says so", async () => {
		stubApi({ ...admin, [GET]: { body: view({ remaining: [] }) } });
		await tab();
		expect(
			await screen.findByText("The latest audit reports nothing more."),
		).toBeInTheDocument();
	});

	test("findings are grouped by who acts, warnings first, with fix states", async () => {
		stubApi({
			...admin,
			[GET]: {
				body: view({
					fixes: [fix({ id: "sysctl" })],
					remaining: [
						finding({
							testId: "A-1",
							title: "Suggest fixable",
							fixId: "sysctl",
							fixState: "pending",
						}),
						finding({
							testId: "A-2",
							title: "Warn fixable",
							severity: "warning",
							fixId: "sysctl",
							fixState: "applied",
						}),
						finding({
							testId: "B-1",
							title: "Manual one",
							fixId: "x",
							fixState: "unavailable",
						}),
						finding({
							testId: "C-1",
							title: "Never one",
							never: "Changing SSH could lock you out.",
						}),
					],
				}),
			},
		});
		await tab();
		const fixable = (
			await screen.findByText(/A safe fix answers these/)
		).closest("section") as HTMLElement;
		expect(fixable).toHaveTextContent("(2)");
		const rows = within(fixable).getAllByRole("listitem");
		expect(rows[0]).toHaveTextContent("A-2");
		expect(rows[0]).toHaveTextContent("Warn fixable");
		expect(rows[0]).toHaveTextContent("Fix applied");
		expect(rows[1]).toHaveTextContent("A-1");
		expect(rows[1]).toHaveTextContent("Fix available");

		const manual = screen
			.getByText(/For you to decide/)
			.closest("section") as HTMLElement;
		expect(manual).toHaveTextContent("B-1");
		expect(manual).toHaveTextContent("Fix not available here");

		const never = screen
			.getByText(/NebulaCtrl never changes these/)
			.closest("section") as HTMLElement;
		expect(never).toHaveTextContent("C-1");
		expect(never).not.toHaveTextContent("Fix");
	});

	test("empty groups render no heading", async () => {
		stubApi({
			...admin,
			[GET]: { body: view({ remaining: [finding()] }) },
		});
		await tab();
		await screen.findByText(/For you to decide/);
		expect(screen.queryByText(/A safe fix answers these/)).toBeNull();
		expect(screen.queryByText(/NebulaCtrl never changes these/)).toBeNull();
	});

	test("opening a finding shows detail, explanation, unavailable reason, the never note, commands and the CISofy link", async () => {
		stubApi({
			...admin,
			[GET]: {
				body: view({
					remaining: [
						finding({
							testId: "SSH-7440",
							detail: "PermitRootLogin is yes",
							explanation: "Root login over SSH is risky.",
							manual: ["nano /etc/ssh/sshd_config", "systemctl reload sshd"],
							never: "SSH is yours to change.",
							fixId: "ssh",
							fixState: "unavailable",
							fixMessage: "no sshd_config found",
						}),
					],
				}),
			},
		});
		await tab();
		const user = userEvent.setup();
		const row = (
			await screen.findByText("Consider hardening SSH configuration")
		).closest("li") as HTMLElement;
		await user.click(within(row).getByRole("button", { name: /SSH-7440/ }));
		expect(
			await within(row).findByText("PermitRootLogin is yes"),
		).toBeInTheDocument();
		expect(
			within(row).getByText("Root login over SSH is risky."),
		).toBeInTheDocument();
		expect(within(row).getByText("no sshd_config found")).toBeInTheDocument();
		expect(
			within(row).getByText("NebulaCtrl leaves this to you"),
		).toBeInTheDocument();
		expect(
			within(row).getByText("SSH is yours to change."),
		).toBeInTheDocument();
		expect(
			within(row).getByText(/nano \/etc\/ssh\/sshd_config/),
		).toHaveTextContent("systemctl reload sshd");
		expect(
			within(row).getByRole("link", { name: /About SSH-7440 at CISOfy/ }),
		).toHaveAttribute("href", "https://cisofy.com/lynis/controls/SSH-7440/");
		expect(within(row).queryByText("Lynis gave no further detail.")).toBeNull();
	});

	test("a finding with nothing more says Lynis gave no further detail", async () => {
		stubApi({ ...admin, [GET]: { body: view({ remaining: [finding()] }) } });
		await tab();
		const user = userEvent.setup();
		const row = (
			await screen.findByText("Consider hardening SSH configuration")
		).closest("li") as HTMLElement;
		await user.click(within(row).getByRole("button", { name: /SSH-7440/ }));
		expect(
			await within(row).findByText("Lynis gave no further detail."),
		).toBeInTheDocument();
	});
});

describe("SecurityTab recent runs", () => {
	test("with no runs it says none has run", async () => {
		stubApi({ ...admin, [GET]: { body: view({ runs: [] }) } });
		await tab();
		expect(
			await screen.findByText(/No audit has run on this server yet/),
		).toBeInTheDocument();
	});

	test("each run shows its action, trigger and one-line summary", async () => {
		stubApi({
			...admin,
			[GET]: {
				body: view({
					runs: [
						run({
							id: "r1",
							action: "audit",
							trigger: "scheduled",
							hardeningIndex: 72,
							indexBefore: 60,
						}),
						run({
							id: "r2",
							action: "apply",
							hardeningIndex: 75,
							indexBefore: 72,
							outcomes: [{ id: "a", outcome: "applied" }],
						}),
						run({
							id: "r3",
							action: "rollback",
							hardeningIndex: null,
							outcomes: [{ id: "a", outcome: "rolled_back" }],
						}),
						run({ id: "r4", status: "failed", hardeningIndex: null }),
						run({ id: "r5", status: "skipped", hardeningIndex: null }),
						run({ id: "r6", status: "pending", hardeningIndex: null }),
					],
				}),
			},
		});
		await tab();
		const heading = await screen.findByText("Recent runs");
		const list = heading.closest("div")?.querySelector("ul") as HTMLElement;
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(6);
		expect(rows[0]).toHaveTextContent("Audit");
		expect(rows[0]).toHaveTextContent("Weekly");
		expect(rows[0]).toHaveTextContent("index 60 → 72");
		expect(rows[1]).toHaveTextContent("Apply fixes");
		expect(rows[1]).toHaveTextContent("By hand");
		expect(rows[1]).toHaveTextContent("1 fix applied · index 72 → 75");
		expect(rows[2]).toHaveTextContent("Undo fixes");
		expect(rows[2]).toHaveTextContent("1 fix undone");
		expect(rows[3]).toHaveTextContent("Failed");
		expect(rows[4]).toHaveTextContent("Lynis is not installed");
		expect(rows[5]).toHaveTextContent("Running");
	});

	test("a failed run opens to its error, message and per-fix outcomes", async () => {
		stubApi({
			...admin,
			// A function reply: the spec's outcome enum has no "mystery", which is
			// the unknown value from a newer control plane the row must still show.
			[GET]: () =>
				Response.json(
					view({
						runs: [
							run({
								action: "apply",
								status: "failed",
								hardeningIndex: null,
								error: "agent disconnected",
								message: "Stopped after the first fix",
								outcomes: [
									{ id: "sysctl", outcome: "applied" },
									{
										id: "ssh",
										outcome: "failed",
										message: "did not take effect",
									},
									{ id: "net", outcome: "unchanged" },
									{ id: "x", outcome: "skipped" },
									{ id: "y", outcome: "rolled_back" },
									{ id: "z", outcome: "mystery" as never },
								],
							}),
						],
					}),
				),
		});
		await tab();
		const user = userEvent.setup();
		const summary = await screen.findByRole("button", { name: /Apply fixes/ });
		expect(summary).toHaveAttribute("aria-expanded", "false");
		await user.click(summary);
		expect(await screen.findByText("agent disconnected")).toBeInTheDocument();
		expect(screen.getByText("Stopped after the first fix")).toBeInTheDocument();
		const text = (id: string) =>
			screen.getByText(id).parentElement as HTMLElement;
		expect(text("sysctl")).toHaveTextContent("Applied");
		expect(text("ssh")).toHaveTextContent("Failed");
		expect(screen.getByText("did not take effect")).toBeInTheDocument();
		expect(text("net")).toHaveTextContent("Already in place");
		expect(text("x")).toHaveTextContent("Skipped");
		expect(text("y")).toHaveTextContent("Undone");
		expect(text("z")).toHaveTextContent("mystery");
	});

	test("a run with nothing to show is not expandable", async () => {
		stubApi({ ...admin, [GET]: { body: view({ runs: [run()] }) } });
		await tab();
		const row = await screen.findByRole("button", { name: /Audit/ });
		expect(row).toBeDisabled();
		expect(row).not.toHaveAttribute("aria-expanded");
	});
});
