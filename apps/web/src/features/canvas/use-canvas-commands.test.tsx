import { afterEach, beforeEach, expect, mock, spyOn, test } from "bun:test";
import { act, screen } from "@testing-library/react";
import type { Service } from "#/api/models";
import {
	type PaletteCommand,
	useCommandRegistry,
} from "#/components/shell/palette-registry";
import {
	composeImportActions,
	useComposeImportState,
} from "#/features/compose-import/store";
import {
	coolifyImportActions,
	useCoolifyImportState,
} from "#/features/coolify-import/store";
import {
	type OpenReviewOptions,
	onOpenStagedReview,
} from "#/features/deploy-button/open-review";
import {
	createStackActions,
	useCreateStackState,
} from "#/features/stacks/store";
import {
	closeRunCommand,
	closeTaskDialog,
	useRunCommandTarget,
	useTaskDialogTarget,
} from "#/features/tasks/store";
import {
	templatesActions,
	useTemplatesState,
} from "#/features/templates/store";
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
import { useCanvasCommands } from "./use-canvas-commands";

interface Mounted {
	commands(): readonly PaletteCommand[];
	find(id: string): PaletteCommand;
	ids(): string[];
	stores(): {
		templates: boolean;
		compose: boolean;
		coolify: boolean;
		stack: boolean;
		run: string | null;
		taskDialog: string | null;
		shell: string | null;
		files: string | null;
	};
}

interface Seen {
	commands: readonly PaletteCommand[];
	stores: ReturnType<Mounted["stores"]> | undefined;
}

/** Mounts useCanvasCommands and records the palette registry and store states it leaves behind. */
function Probe({
	services,
	isProduction,
	onInspect,
	onCreate,
	seen,
}: {
	services: readonly Service[];
	isProduction: boolean;
	onInspect: (id: string, tab?: never, section?: string) => void;
	onCreate: () => void;
	seen: Seen;
}) {
	useCanvasCommands({
		services,
		isProduction,
		onInspect: onInspect as never,
		onCreate,
	});
	seen.commands = useCommandRegistry();
	seen.stores = {
		templates: useTemplatesState().open,
		compose: useComposeImportState().open,
		coolify: useCoolifyImportState().open,
		stack: useCreateStackState().open,
		run: useRunCommandTarget(),
		taskDialog: useTaskDialogTarget(),
		shell: useShellTarget(),
		files: useFilesTarget(),
	};
	return null;
}

async function mountCommands(
	services: readonly Service[],
	{
		isProduction = false,
		onInspect = () => {},
		onCreate = () => {},
	}: {
		isProduction?: boolean;
		onInspect?: (id: string, tab?: never, section?: string) => void;
		onCreate?: () => void;
	} = {},
): Promise<Mounted> {
	const seen: Seen = { commands: [], stores: undefined };
	await renderWithProviders(
		<Probe
			services={services}
			isProduction={isProduction}
			onInspect={onInspect}
			onCreate={onCreate}
			seen={seen}
		/>,
	);
	const find = (id: string) => {
		const found = seen.commands.find((c) => c.id === id);
		if (!found) throw new Error(`no command ${id}`);
		return found;
	};
	return {
		commands: () => seen.commands,
		find,
		ids: () => seen.commands.map((c) => c.id),
		stores: () => {
			if (!seen.stores) throw new Error("not mounted");
			return seen.stores;
		},
	};
}

const deployed = (overrides: Partial<Service> = {}) =>
	makeService({
		id: "web",
		name: "Web",
		slug: "web",
		currentDeploymentId: "dep-1",
		observed: makeObserved(),
		...overrides,
	});

beforeEach(() => {
	templatesActions.close();
	composeImportActions.close();
	coolifyImportActions.close();
	createStackActions.close();
	closeRunCommand();
	closeTaskDialog();
	closeShell();
	closeFiles();
});
afterEach(() => {
	mock.restore();
});

test("with no services only the five create entries are offered", async () => {
	const m = await mountCommands([]);
	expect(m.ids()).toEqual([
		"canvas:create-service",
		"canvas:new-from-template",
		"canvas:import-docker-compose",
		"canvas:import-coolify",
		"canvas:new-compose-stack",
	]);
});

test("Create service calls the canvas's create flow", async () => {
	const onCreate = mock(() => {});
	const m = await mountCommands([], { onCreate });
	act(() => m.find("canvas:create-service").run());
	expect(onCreate).toHaveBeenCalledTimes(1);
});

test("the template, compose, coolify and stack entries each open their own dialog", async () => {
	const m = await mountCommands([]);
	act(() => m.find("canvas:new-from-template").run());
	expect(m.stores()).toMatchObject({
		templates: true,
		compose: false,
		coolify: false,
		stack: false,
	});
	act(() => m.find("canvas:import-docker-compose").run());
	expect(m.stores().compose).toBe(true);
	act(() => m.find("canvas:import-coolify").run());
	expect(m.stores().coolify).toBe(true);
	act(() => m.find("canvas:new-compose-stack").run());
	expect(m.stores().stack).toBe(true);
});

test("a service gets open, deploy and restart entries named after it", async () => {
	const m = await mountCommands([deployed()]);
	const open = m.find("service:open:web");
	expect(open).toMatchObject({
		label: "Web",
		group: "Service",
		hint: "hetzner-01",
	});
	expect(m.find("service:deploy:web").label).toBe("Redeploy Web");
	expect(m.find("service:restart:web").label).toBe("Restart Web");
});

test("a service without a server says so in its hint", async () => {
	const m = await mountCommands([deployed({ server: null, serverId: null })]);
	expect(m.find("service:open:web").hint).toBe("no server");
});

test("a service that never ran is offered Deploy, and no shell, files or command", async () => {
	const m = await mountCommands([deployed({ currentDeploymentId: null })]);
	expect(m.find("service:deploy:web").label).toBe("Deploy Web");
	for (const kind of ["run", "shell", "files"]) {
		expect(m.ids()).not.toContain(`service:${kind}:web`);
	}
});

test("a cron service has no container to restart or open a shell or files in, but keeps Run command", async () => {
	const base = deployed();
	const m = await mountCommands([
		deployed({
			kind: "worker",
			config: {
				...base.config,
				oneshot: true,
				cron: { schedule: "* * * * *", timezone: "UTC" },
			},
		}),
	]);
	expect(m.find("service:deploy:web").label).toBe("Redeploy Web");
	for (const kind of ["restart", "shell", "files", "schedule-task"]) {
		expect(m.ids()).not.toContain(`service:${kind}:web`);
	}
	expect(m.ids()).toContain("service:run:web");
});

test("a route gets no shell, files or run-command entries", async () => {
	const m = await mountCommands([deployed({ kind: "route" })]);
	for (const kind of ["run", "shell", "files"]) {
		expect(m.ids()).not.toContain(`service:${kind}:web`);
	}
});

test("Open inspects the service; settings entries open its settings page and only show once typed", async () => {
	const onInspect = mock((_id: string, _tab?: never, _section?: string) => {});
	const m = await mountCommands([deployed()], { onInspect });
	act(() => m.find("service:open:web").run());
	expect(onInspect).toHaveBeenLastCalledWith("web");
	const settings = m
		.commands()
		.filter((c) => c.id.startsWith("service:settings:web:"));
	expect(settings.length).toBeGreaterThan(3);
	for (const entry of settings) {
		expect(entry.searchOnly).toBe(true);
		expect(entry.label).toMatch(/^Open Web settings: /);
		expect(entry.keywords).toContain("web");
	}
	const first = settings[0];
	if (!first) throw new Error("no settings entries");
	const category = first.id.split(":").at(-1);
	act(() => first.run());
	expect(onInspect).toHaveBeenLastCalledWith("web", "settings", category);
});

test("a oneshot service has different settings pages than a plain app", async () => {
	const plain = await mountCommands([deployed()]);
	const plainIds = plain
		.ids()
		.filter((id) => id.startsWith("service:settings:"));
	const task = deployed();
	task.config = { ...task.config, oneshot: true };
	const m = await mountCommands([task]);
	const oneshotIds = m.ids().filter((id) => id.startsWith("service:settings:"));
	expect(oneshotIds).not.toEqual(plainIds);
});

test("each public address is an Open entry: the first at once, the rest only when typed", async () => {
	const open = spyOn(window, "open").mockImplementation(() => null);
	const m = await mountCommands([
		deployed({
			domains: [
				{ hostname: "a.example.com" },
				{ hostname: "b.example.com", pathPrefix: "/docs", tlsMode: "none" },
			] as Service["domains"],
		}),
	]);
	const first = m.find("service:open-site:web:https://a.example.com/");
	const second = m.find("service:open-site:web:http://b.example.com/docs");
	expect(first).toMatchObject({
		label: "Open a.example.com",
		hint: "Web",
		searchOnly: false,
	});
	expect(second.searchOnly).toBe(true);
	act(() => second.run());
	expect(open).toHaveBeenCalledWith(
		"http://b.example.com/docs",
		"_blank",
		"noopener,noreferrer",
	);
});

test("a tailnet-only address says so", async () => {
	const m = await mountCommands([
		deployed({
			domains: [],
			tailnet: {
				state: "online",
				url: "https://web.tail1234.ts.net",
				hidePublic: true,
			} as Service["tailnet"],
		}),
	]);
	const entry = m
		.commands()
		.find((c) => c.id.startsWith("service:open-site:web:"));
	expect(entry).toMatchObject({
		label: "Open web.tail1234.ts.net",
		hint: "Web, tailnet only",
	});
});

test("Run command, shell and files open their panels on the service", async () => {
	const m = await mountCommands([deployed()]);
	act(() => m.find("service:run:web").run());
	expect(m.stores().run).toBe("web");
	act(() => m.find("service:shell:web").run());
	expect(m.stores().shell).toBe("web");
	act(() => m.find("service:files:web").run());
	expect(m.stores().files).toBe("web");
	expect(m.find("service:run:web").label).toBe("Run command in Web…");
	expect(m.find("service:shell:web").label).toBe("Open shell in Web…");
	expect(m.find("service:files:web").label).toBe("Browse files of Web…");
});

test("Schedule task for X… opens the task dialog; a one-shot, stack or route has none", async () => {
	const m = await mountCommands([deployed()]);
	expect(m.find("service:schedule-task:web").label).toBe(
		"Schedule task for Web…",
	);
	expect(m.stores().taskDialog).toBeNull();
	act(() => m.find("service:schedule-task:web").run());
	expect(m.stores().taskDialog).toBe("web");
	act(() => closeTaskDialog());

	const base = deployed();
	const oneShot = await mountCommands([
		{ ...base, config: { ...base.config, oneshot: true } },
	]);
	expect(oneShot.ids()).not.toContain("service:schedule-task:web");
	const route = await mountCommands([deployed({ kind: "route" })]);
	expect(route.ids()).not.toContain("service:schedule-task:web");
});

test("Deploy outside production deploys at once and says so", async () => {
	const api = stubApi({
		"POST /services/:id/deployments": {
			status: 202,
			body: make("DeploymentView", { id: "d2", status: "queued" }),
		},
	});
	const m = await mountCommands([deployed()]);
	expect(m.find("service:deploy:web").hint).toBe(
		"from the current configuration",
	);
	act(() => m.find("service:deploy:web").run());
	expect(await screen.findByText("Deploying Web")).toBeInTheDocument();
	expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
		"POST /services/web/deployments",
	]);
});

test("a service with staged changes deploys 'with its staged changes'", async () => {
	const m = await mountCommands([
		deployed({
			hasStagedChanges: true,
			latestDeployment: makeDeployment("succeeded"),
		}),
	]);
	expect(m.find("service:deploy:web").hint).toBe("with its staged changes");
});

test("Deploy in production opens the review sheet for that service and sends nothing", async () => {
	const api = stubApi({});
	const opened: OpenReviewOptions[] = [];
	const off = onOpenStagedReview((o) => opened.push(o));
	const m = await mountCommands([deployed()], { isProduction: true });
	expect(m.find("service:deploy:web").hint).toBe("review, then confirm");
	act(() => m.find("service:deploy:web").run());
	off();
	expect(opened).toEqual([{ serviceId: "web" }]);
	expect(api.calls).toEqual([]);
});

test("a failed deploy reports the reason", async () => {
	stubApi({
		"POST /services/:id/deployments": problem(500, { detail: "no capacity" }),
	});
	const m = await mountCommands([deployed()]);
	act(() => m.find("service:deploy:web").run());
	expect(await screen.findByText("Could not deploy Web")).toBeInTheDocument();
	expect(screen.getByText("no capacity")).toBeInTheDocument();
});

test("Restart posts the restart and confirms it", async () => {
	const api = stubApi({
		"POST /services/:id/restart": { status: 202, body: make("ActionResult") },
	});
	const m = await mountCommands([deployed()]);
	act(() => m.find("service:restart:web").run());
	expect(await screen.findByText("Restarting Web")).toBeInTheDocument();
	expect(api.calls.at(-1)).toMatchObject({
		method: "POST",
		path: "/services/web/restart",
	});
});

test("a failed restart reports the reason", async () => {
	stubApi({
		"POST /services/:id/restart": problem(500, { detail: "agent offline" }),
	});
	const m = await mountCommands([deployed()]);
	act(() => m.find("service:restart:web").run());
	expect(await screen.findByText("Could not restart Web")).toBeInTheDocument();
	expect(screen.getByText("agent offline")).toBeInTheDocument();
});

test("the entries follow the service list", async () => {
	const m = await mountCommands([
		deployed({ id: "a", name: "A", slug: "a" }),
		deployed({ id: "b", name: "B", slug: "b" }),
	]);
	expect(m.ids()).toEqual(
		expect.arrayContaining(["service:open:a", "service:open:b"]),
	);
});
