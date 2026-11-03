import { beforeEach, expect, mock, test } from "bun:test";
import { useSearch } from "@tanstack/react-router";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import {
	type OpenReviewOptions,
	onOpenStagedReview,
} from "#/features/deploy-button/open-review";
import {
	closeMaintenance,
	useMaintenanceTarget,
} from "#/features/maintenance/store";
import {
	closeRunCommand,
	closeRunHistory,
	closeTaskDialog,
	type RunHistoryTarget,
	useRunCommandTarget,
	useRunHistoryTarget,
	useTaskDialogTarget,
} from "#/features/tasks/store";
import {
	closeFiles,
	closeShell,
	useFilesTarget,
	useShellTarget,
} from "#/features/terminal/store";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeDeployment, makeObserved, makeService } from "./lib/fixtures";
import {
	type ServiceAction,
	type ServiceActionId,
	type ServiceActions,
	useServiceActions,
} from "./use-service-actions";

interface Seen {
	actions: ServiceActions;
	shell: string | null;
	files: string | null;
	run: string | null;
	history: RunHistoryTarget | null;
	taskDialog: string | null;
	maintenance: string | null;
	search: Record<string, unknown>;
}

/** Mounts useServiceActions and records it with the store states it drives. */
function Probe({
	service,
	isProduction,
	onDelete,
	seen,
}: {
	service: Service;
	isProduction: boolean;
	onDelete: () => void;
	seen: { current: Seen | undefined };
}) {
	seen.current = {
		actions: useServiceActions(service, { isProduction, onDelete }),
		shell: useShellTarget(),
		files: useFilesTarget(),
		run: useRunCommandTarget(),
		history: useRunHistoryTarget(),
		taskDialog: useTaskDialogTarget(),
		maintenance: useMaintenanceTarget(),
		search: useSearch({ strict: false }) as Record<string, unknown>,
	};
	return null;
}

async function mountActions(
	service: Service,
	{ isProduction = false, onDelete = () => {} } = {},
) {
	const seen: { current: Seen | undefined } = { current: undefined };
	await renderWithProviders(
		<Probe
			service={service}
			isProduction={isProduction}
			onDelete={onDelete}
			seen={seen}
		/>,
		{ path: "/canvas?keep=1" },
	);
	const now = () => {
		if (!seen.current) throw new Error("not mounted");
		return seen.current;
	};
	const action = (id: ServiceActionId, label?: string): ServiceAction => {
		const found = now().actions.menu.find(
			(a) => a.id === id && (label === undefined || a.label === label),
		);
		if (!found) throw new Error(`no ${id} action`);
		return found;
	};
	const ids = () => now().actions.menu.map((a) => a.id);
	const labels = () => now().actions.menu.map((a) => a.label);
	return { now, action, ids, labels };
}

const deployed = makeService({
	id: "web",
	name: "Web",
	currentDeploymentId: "dep-1",
	observed: makeObserved(),
	domains: [],
});

beforeEach(() => {
	closeShell();
	closeFiles();
	closeRunCommand();
	closeRunHistory();
	closeTaskDialog();
	closeMaintenance();
});

test("a deployed image service offers the full menu in order, ending in copy and delete", async () => {
	const { ids } = await mountActions(deployed);
	expect(ids()).toEqual([
		"redeploy",
		"restart",
		"toggle",
		"run-command",
		"schedule-task",
		"shell",
		"files",
		"processes",
		"copy-host",
		"delete",
	]);
});

test("a new commit found by checking adds Deploy latest commit right after Redeploy", async () => {
	const { ids, action } = await mountActions(
		makeService({
			id: "g",
			sourceType: "git",
			currentDeploymentId: "d",
			gitPoll: {
				enabled: true,
				intervalMinutes: 5,
				deploy: false,
				headSha: "abc1234def5678",
				headBranch: "main",
				updateAvailable: true,
			},
		}),
	);
	expect(ids().slice(0, 3)).toEqual(["redeploy", "deploy-latest", "restart"]);
	expect(action("deploy-latest").label).toBe("Deploy latest commit");
	const plain = await mountActions(
		makeService({ id: "p", sourceType: "git", currentDeploymentId: "d" }),
	);
	expect(plain.ids()).not.toContain("deploy-latest");
});

test("actions that cannot apply are left out: never deployed has no stop, shell, files or command", async () => {
	const { ids, action } = await mountActions(
		makeService({ id: "fresh", currentDeploymentId: null }),
	);
	expect(ids()).toEqual([
		"redeploy",
		"restart",
		"processes",
		"copy-host",
		"delete",
	]);
	// Restart has nothing to restart yet.
	expect(action("restart").disabled).toBe(true);
});

test("a git service adds the clean rebuild; a database gets neither it nor processes", async () => {
	const git = await mountActions(
		makeService({ id: "g", sourceType: "git", currentDeploymentId: "d" }),
	);
	expect(git.ids()).toContain("redeploy-no-cache");
	const db = await mountActions(
		makeService({
			id: "db",
			kind: "database",
			sourceType: "image",
			currentDeploymentId: "d",
		}),
	);
	expect(db.ids()).not.toContain("redeploy-no-cache");
	expect(db.ids()).not.toContain("processes");
});

test("a route has no shell, files, command or processes", async () => {
	const { ids } = await mountActions(
		makeService({ id: "r", kind: "route", currentDeploymentId: "d" }),
	);
	for (const id of ["run-command", "shell", "files", "processes"] as const) {
		expect(ids()).not.toContain(id);
	}
});

test("a worker offers processes; a stack does not", async () => {
	const worker = await mountActions(
		makeService({ id: "w", kind: "worker", currentDeploymentId: "d" }),
	);
	expect(worker.ids()).toContain("processes");
	const stack = await mountActions(
		makeService({
			id: "s",
			kind: "stack",
			sourceType: "compose",
			currentDeploymentId: "d",
		} as Partial<Service>),
	);
	expect(stack.ids()).not.toContain("processes");
});

test("Schedule task… follows Run command… and opens the task dialog on the service", async () => {
	const { ids, action, now } = await mountActions(deployed);
	const at = ids().indexOf("run-command");
	expect(ids()[at + 1]).toBe("schedule-task");
	expect(action("schedule-task").label).toBe("Schedule task…");
	expect(now().taskDialog).toBeNull();
	act(() => action("schedule-task").run());
	expect(now().taskDialog).toBe("web");
});

test("a one-shot service and a stack offer Run command… but no Schedule task…", async () => {
	const base = makeService({ id: "o", currentDeploymentId: "d" });
	const oneShot = await mountActions({
		...base,
		config: { ...base.config, oneshot: true },
	});
	expect(oneShot.ids()).toContain("run-command");
	expect(oneShot.ids()).not.toContain("schedule-task");
	const stack = await mountActions(
		makeService({
			id: "s",
			kind: "stack",
			sourceType: "compose",
			currentDeploymentId: "d",
		} as Partial<Service>),
	);
	expect(stack.ids()).not.toContain("schedule-task");
	const route = await mountActions(
		makeService({ id: "r", kind: "route", currentDeploymentId: "d" }),
	);
	expect(route.ids()).not.toContain("schedule-task");
});

test("the deploy row says Redeploy for a released service, Deploy for one that never ran or a route", async () => {
	const released = await mountActions(
		makeService({ id: "rl", currentDeploymentId: "d" }),
	);
	expect(released.action("redeploy").label).toBe("Redeploy");
	const fresh = await mountActions(
		makeService({ id: "fr", currentDeploymentId: null }),
	);
	expect(fresh.action("redeploy").label).toBe("Deploy");
	const route = await mountActions(
		makeService({ id: "ro", kind: "route", currentDeploymentId: "d" }),
	);
	expect(route.action("redeploy").label).toBe("Deploy");
});

test("deploying outside production posts a deployment and says so", async () => {
	const api = stubApi({
		"POST /services/:id/deployments": {
			status: 202,
			body: make("DeploymentView", { id: "dep-2", status: "queued" }),
		},
	});
	const { now } = await mountActions(deployed);
	act(() => now().actions.deploy());
	expect(await screen.findByText("Deploying Web")).toBeInTheDocument();
	expect(
		api.calls.filter((c) => c.method === "POST").map((c) => c.path),
	).toEqual(["/services/web/deployments"]);
});

test("deploying in production opens the scoped review sheet and posts nothing", async () => {
	const api = stubApi({});
	const opened: OpenReviewOptions[] = [];
	const off = onOpenStagedReview((o) => opened.push(o));
	const { now, action } = await mountActions(deployed, { isProduction: true });
	act(() => now().actions.deploy());
	act(() => action("redeploy").run());
	off();
	expect(opened).toEqual([{ serviceId: "web" }, { serviceId: "web" }]);
	expect(api.calls).toEqual([]);
});

test("a failed deploy shows the server's reason", async () => {
	stubApi({
		"POST /services/:id/deployments": problem(409, {
			title: "Conflict",
			detail: "a deployment is already running",
		}),
	});
	const { now } = await mountActions(deployed);
	act(() => now().actions.deploy());
	expect(await screen.findByText("Could not deploy Web")).toBeInTheDocument();
	expect(
		screen.getByText("a deployment is already running"),
	).toBeInTheDocument();
});

test("Restart posts a restart; a stopped service shows Start in its place", async () => {
	const api = stubApi({
		"POST /services/:id/restart": { status: 202, body: make("ActionResult") },
	});
	const running = await mountActions(deployed);
	expect(running.action("restart").label).toBe("Restart");
	act(() => running.action("restart").run());
	expect(await screen.findByText("Restarting Web")).toBeInTheDocument();
	expect(api.calls.at(-1)).toMatchObject({
		method: "POST",
		path: "/services/web/restart",
	});

	const stopped = await mountActions(
		makeService({
			id: "idle",
			name: "Idle",
			desiredState: "stopped",
			currentDeploymentId: "d",
		}),
	);
	expect(stopped.labels()).toContain("Start");
	expect(stopped.labels()).not.toContain("Stop");
	expect(stopped.labels()).not.toContain("Restart");
	expect(stopped.action("toggle").label).toBe("Start");
});

test("Start on a service that was never deployed is disabled", async () => {
	const { action } = await mountActions(
		makeService({
			id: "n",
			desiredState: "stopped",
			currentDeploymentId: null,
		}),
	);
	expect(action("toggle").label).toBe("Start");
	expect(action("toggle").disabled).toBe(true);
});

test("Stop posts a stop request", async () => {
	const api = stubApi({
		"POST /services/:id/stop": { status: 202, body: make("ActionResult") },
	});
	const { action } = await mountActions(deployed);
	act(() => action("toggle", "Stop").run());
	expect(await screen.findByText("Stopping Web")).toBeInTheDocument();
	expect(api.calls.at(-1)).toMatchObject({
		method: "POST",
		path: "/services/web/stop",
	});
});

test("a failed restart reports it", async () => {
	stubApi({
		"POST /services/:id/restart": problem(500, {
			detail: "server unreachable",
		}),
	});
	const { action } = await mountActions(deployed);
	act(() => action("restart").run());
	expect(await screen.findByText("Could not restart Web")).toBeInTheDocument();
	expect(screen.getByText("server unreachable")).toBeInTheDocument();
});

test("Redeploy without cache asks first, then posts noCache", async () => {
	const api = stubApi({
		"POST /services/:id/deployments": {
			status: 202,
			body: make("DeploymentView", { id: "dep-3", status: "queued" }),
		},
	});
	const user = userEvent.setup();
	const { action } = await mountActions(
		makeService({
			id: "g",
			name: "Git app",
			sourceType: "git",
			currentDeploymentId: "d",
		}),
	);
	act(() => action("redeploy-no-cache").run());
	const dialog = await screen.findByRole("alertdialog");
	expect(
		within(dialog).getByText("Redeploy Git app without cache?"),
	).toBeInTheDocument();
	expect(api.calls).toEqual([]);
	await user.click(
		within(dialog).getByRole("button", { name: "Redeploy without cache" }),
	);
	expect(
		await screen.findByText("Deploying Git app without cache"),
	).toBeInTheDocument();
	expect(api.calls.at(-1)).toMatchObject({
		method: "POST",
		path: "/services/g/deployments",
		body: { noCache: true },
	});
});

test("declining the clean rebuild sends nothing", async () => {
	const api = stubApi({});
	const user = userEvent.setup();
	const { action } = await mountActions(
		makeService({ id: "g", sourceType: "git", currentDeploymentId: "d" }),
	);
	act(() => action("redeploy-no-cache").run());
	const dialog = await screen.findByRole("alertdialog");
	await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
	await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	expect(api.calls).toEqual([]);
});

test("Cancel deployment is offered only while a deployment is in flight", async () => {
	const idle = await mountActions(
		makeService({
			id: "a",
			currentDeploymentId: "d",
			latestDeployment: makeDeployment("succeeded"),
		}),
	);
	expect(idle.ids()).not.toContain("cancel");
	const building = await mountActions(
		makeService({
			id: "b",
			currentDeploymentId: "d",
			latestDeployment: makeDeployment("building"),
		}),
	);
	expect(building.ids()).toContain("cancel");
});

test("cancelling a deployment confirms, then posts the cancel for the latest deployment", async () => {
	const api = stubApi({
		"POST /deployments/:id/cancel": {
			status: 200,
			body: make("DeploymentView", { status: "canceled" }),
		},
	});
	const user = userEvent.setup();
	const { action } = await mountActions(
		makeService({
			id: "b",
			name: "Builder",
			currentDeploymentId: "d",
			latestDeployment: makeDeployment("building", { id: "dep-live" }),
		}),
	);
	act(() => action("cancel").run());
	const dialog = await screen.findByRole("alertdialog");
	expect(
		within(dialog).getByText("Cancel this deployment of Builder?"),
	).toBeInTheDocument();
	await user.click(
		within(dialog).getByRole("button", { name: "Keep running" }),
	);
	await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	expect(api.calls).toEqual([]);

	act(() => action("cancel").run());
	const again = await screen.findByRole("alertdialog");
	await user.click(
		within(again).getByRole("button", { name: "Cancel deployment" }),
	);
	expect(
		await screen.findByText("Canceling the deployment of Builder"),
	).toBeInTheDocument();
	expect(api.calls.at(-1)).toMatchObject({
		method: "POST",
		path: "/deployments/dep-live/cancel",
	});
});

const withDomain = {
	hostname: "app.example.com",
} as Service["domains"][number];

test("Maintenance mode… opens the maintenance dialog of this service", async () => {
	const { action, now } = await mountActions(
		makeService({
			id: "m",
			name: "Shop",
			currentDeploymentId: "d",
			domains: [withDomain],
		}),
	);
	expect(action("maintenance").label).toBe("Maintenance mode…");
	act(() => action("maintenance").run());
	expect(now().maintenance).toBe("m");
});

test("a service in maintenance offers End maintenance, which turns it off at once", async () => {
	const api = stubApi({
		"PUT /services/:id/maintenance": {
			status: 200,
			body: make("ServiceView", {
				maintenance: {
					enabled: false,
					title: "",
					message: "",
					eta: null,
					since: null,
				},
			}),
		},
	});
	const { action } = await mountActions(
		makeService({
			id: "m2",
			name: "Shop",
			currentDeploymentId: "d",
			domains: [withDomain],
			maintenance: {
				enabled: true,
				title: "t",
				message: "m",
				eta: null,
				since: null,
			},
		}),
	);
	expect(action("maintenance").label).toBe("End maintenance");
	act(() => action("maintenance").run());
	expect(await screen.findByText("Shop is back")).toBeInTheDocument();
	expect(api.calls.at(-1)).toMatchObject({
		method: "PUT",
		path: "/services/m2/maintenance",
		body: { enabled: false },
	});
});

test("a service without a domain has no maintenance action", async () => {
	const { ids } = await mountActions(deployed);
	expect(ids()).not.toContain("maintenance");
});

test("run command, shell and files open their panels on this service", async () => {
	const { action, now } = await mountActions(deployed);
	act(() => action("run-command").run());
	expect(now().run).toBe("web");
	act(() => action("shell").run());
	expect(now().shell).toBe("web");
	act(() => action("files").run());
	expect(now().files).toBe("web");
});

test("Open processes moves the address to this service's process settings, keeping the rest of it", async () => {
	const { action, now } = await mountActions(deployed);
	act(() => action("processes").run());
	await waitFor(() => expect(now().search.service).toBe("web"));
	expect(now().search).toMatchObject({
		keep: 1,
		service: "web",
		tab: "settings",
		section: "processes",
	});
});

test("Copy private host puts the host on the clipboard and says so", async () => {
	const writeText = mock(() => Promise.resolve());
	Object.defineProperty(navigator, "clipboard", {
		value: { writeText },
		configurable: true,
	});
	const { action } = await mountActions(
		makeService({ id: "c", privateHost: "web.internal" }),
	);
	act(() => action("copy-host").run());
	expect(writeText).toHaveBeenCalledWith("web.internal");
	expect(await screen.findByText("Copied")).toBeInTheDocument();
	expect(screen.getByText("web.internal")).toBeInTheDocument();
});

test("Delete is the last, destructive row and hands over to the caller", async () => {
	const onDelete = mock(() => {});
	const { action, ids, now } = await mountActions(deployed, { onDelete });
	expect(ids().at(-1)).toBe("delete");
	expect(action("delete").destructive).toBe(true);
	expect(now().actions.menu.filter((a) => a.destructive)).toHaveLength(1);
	act(() => action("delete").run());
	expect(onDelete).toHaveBeenCalledTimes(1);
});

test("busy follows the requests: true while a deploy is on its way", async () => {
	let release: (() => void) | undefined;
	stubApi({
		"POST /services/:id/deployments": () =>
			new Promise<Response>((resolve) => {
				release = () => resolve(Response.json({ id: "d" }, { status: 202 }));
			}),
	});
	const { now, action } = await mountActions(deployed);
	expect(now().actions.busy).toBe(false);
	act(() => now().actions.deploy());
	await waitFor(() => expect(now().actions.busy).toBe(true));
	expect(action("redeploy").disabled).toBe(true);
	await act(async () => release?.());
	await waitFor(() => expect(now().actions.busy).toBe(false));
});

/** A deployed one-shot worker on a schedule, as the dock and the card menu see it. */
function cronService(over: Partial<Service> = {}): Service {
	const base = makeService();
	return makeService({
		id: "nightly",
		name: "Nightly",
		kind: "worker",
		config: {
			...base.config,
			oneshot: true,
			cron: { schedule: "0 3 * * *", timezone: "UTC" },
		},
		currentDeploymentId: "dep-1",
		cron: { taskId: "task-7", schedule: "0 3 * * *", timezone: "UTC" },
		...over,
	});
}

test("a cron service swaps Restart and Stop for Pause schedule and keeps no container-only rows", async () => {
	const { ids, labels, action } = await mountActions(cronService());
	expect(ids()).toEqual([
		"redeploy",
		"toggle",
		"run-command",
		"run-now",
		"run-history",
		"copy-host",
		"delete",
	]);
	expect(labels()).toContain("Run now");
	expect(labels()).toContain("Run history");
	expect(labels()).not.toContain("Restart");
	expect(labels()).not.toContain("Stop");
	expect(action("toggle").label).toBe("Pause schedule");
	expect(action("toggle").disabled).toBe(false);
});

test("Pause schedule stops the service and says the schedule is paused", async () => {
	const api = stubApi({
		"POST /services/:id/stop": { status: 202, body: make("ActionResult") },
	});
	const { action } = await mountActions(cronService());
	act(() => action("toggle").run());
	expect(
		await screen.findByText("Paused the schedule of Nightly"),
	).toBeInTheDocument();
	expect(api.calls.at(-1)).toMatchObject({
		method: "POST",
		path: "/services/nightly/stop",
	});
});

test("a paused cron service offers Resume schedule, which restarts it", async () => {
	const api = stubApi({
		"POST /services/:id/restart": { status: 202, body: make("ActionResult") },
	});
	const { action, labels } = await mountActions(
		cronService({ desiredState: "stopped" }),
	);
	expect(labels()).not.toContain("Pause schedule");
	expect(action("toggle").label).toBe("Resume schedule");
	act(() => action("toggle").run());
	expect(
		await screen.findByText("Resumed the schedule of Nightly"),
	).toBeInTheDocument();
	expect(api.calls.at(-1)).toMatchObject({
		method: "POST",
		path: "/services/nightly/restart",
	});
});

test("Resume schedule before the first deploy is disabled", async () => {
	const { action } = await mountActions(
		cronService({ desiredState: "stopped", currentDeploymentId: null }),
	);
	expect(action("toggle").disabled).toBe(true);
});

test("Run now starts the cron task and opens its run history", async () => {
	const api = stubApi({
		"POST /tasks/:taskId/run": { status: 202, body: make("RunView") },
		"GET /services/:serviceId/tasks": { body: { items: [] } },
	});
	const { action, now } = await mountActions(cronService());
	act(() => action("run-now").run());
	expect(await screen.findByText("Started Nightly")).toBeInTheDocument();
	expect(
		api.calls.filter((c) => c.method === "POST").map((c) => c.path),
	).toEqual(["/tasks/task-7/run"]);
	expect(now().history).toEqual({ serviceId: "nightly", taskId: "task-7" });
});

test("a failed Run now shows why the run could not start", async () => {
	stubApi({
		"POST /tasks/:taskId/run": problem(409, {
			title: "Conflict",
			detail: "not deployed",
			code: "not_deployed",
		}),
	});
	const { action, now } = await mountActions(cronService());
	act(() => action("run-now").run());
	expect(await screen.findByText("Could not run Nightly")).toBeInTheDocument();
	expect(now().history).toBeNull();
});

test("Run history opens the drawer on the cron task, and Run command… still opens the dialog", async () => {
	const { action, now } = await mountActions(cronService());
	act(() => action("run-history").run());
	expect(now().history).toEqual({ serviceId: "nightly", taskId: "task-7" });
	act(() => action("run-command").run());
	expect(now().run).toBe("nightly");
});

test("a cron service whose schedule row is not synced yet has no Run now or Run history", async () => {
	const { cron: _cron, ...unsynced } = cronService();
	const { ids } = await mountActions(unsynced);
	expect(ids()).not.toContain("run-now");
	expect(ids()).not.toContain("run-history");
	expect(ids()).toContain("toggle");
});
