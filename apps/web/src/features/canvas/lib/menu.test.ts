import { describe, expect, test } from "bun:test";
import { ROOT_ENTRIES } from "#/features/create/lib";
import { makeDeployment, makeObserved, makeService } from "./fixtures";
import { INTERNET_ID } from "./internet";
import {
	canTakeDomain,
	deployableOf,
	type EdgeMenuInput,
	edgeMenu,
	groupMenu,
	internetMenu,
	MENU_LIST_LIMIT,
	type MenuAction,
	type MenuModel,
	paneMenu,
	resolveMenuTarget,
	restartableOf,
	selectionMenu,
	serviceMenu,
	type TargetKnown,
	type TargetProbe,
} from "./menu";

function items(model: MenuModel) {
	return model.sections.flatMap((section) => section.items);
}

function labels(model: MenuModel): string[] {
	return items(model).map((item) => item.label);
}

function find(model: MenuModel, key: string) {
	const found = items(model).find((item) => item.key === key);
	if (!found) throw new Error(`no row ${key}`);
	return found;
}

function notes(model: MenuModel): string[] {
	return model.sections.flatMap((section) =>
		section.note === undefined ? [] : [section.note],
	);
}

describe("serviceMenu", () => {
	const service = makeService({ id: "svc", name: "api" });
	const actions = [
		{ id: "redeploy", label: "Redeploy", disabled: false, destructive: false },
		{ id: "restart", label: "Restart", disabled: false, destructive: false },
		{ id: "toggle", label: "Stop", disabled: false, destructive: false },
		{
			id: "copy-host",
			label: "Copy private host",
			disabled: false,
			destructive: false,
		},
		{ id: "delete", label: "Delete…", disabled: false, destructive: true },
	] as const;

	test("leads with Open, Deploy and Scale; the Deploy row wears the redeploy action's label", () => {
		const model = serviceMenu({ service, actions, busy: false });
		expect(model.title).toBe("api");
		expect(model.ariaLabel).toBe("Actions for api");
		expect(labels(model)).toEqual([
			"Open",
			"Redeploy",
			"Scale…",
			"Restart",
			"Stop",
			"Copy private host",
			"Delete…",
		]);
		const fresh = actions.map((action) =>
			action.id === "redeploy" ? { ...action, label: "Deploy" } : action,
		);
		expect(
			labels(serviceMenu({ service, actions: fresh, busy: false }))[1],
		).toBe("Deploy");
	});

	test("lists the cables the card ends, named by the card at the other end", () => {
		const model = serviceMenu({
			service,
			actions,
			busy: false,
			links: [
				{ edgeId: "e1", otherName: "postgres" },
				{ edgeId: "e2", otherName: "web" },
			],
		});
		const section = model.sections.find((s) => s.label === "Links");
		expect(section?.items.map((i) => i.label)).toEqual([
			"Review link to postgres",
			"Review link to web",
		]);
		expect(section?.items[0]?.action).toEqual({
			type: "review-references",
			edgeId: "e1",
		});
		// Before the destructive rows.
		expect(model.sections.at(-1)?.items[0]?.label).toBe("Delete…");
	});

	test("a card without cables has no Links section; a long list folds", () => {
		expect(
			serviceMenu({ service, actions, busy: false }).sections.some(
				(s) => s.label === "Links",
			),
		).toBe(false);
		const many = Array.from({ length: MENU_LIST_LIMIT + 2 }, (_, i) => ({
			edgeId: `e${i}`,
			otherName: `svc-${i}`,
		}));
		const section = serviceMenu({
			service,
			actions,
			busy: false,
			links: many,
		}).sections.find((s) => s.label === "Links");
		expect(section?.items).toHaveLength(MENU_LIST_LIMIT);
		expect(section?.note).toBe("2 more in Variables.");
	});

	test("puts destructive rows in their own last section", () => {
		const model = serviceMenu({ service, actions, busy: false });
		const last = model.sections.at(-1);
		expect(last?.items.map((item) => item.label)).toEqual(["Delete…"]);
		expect(last?.items[0]?.tone).toBe("destructive");
		expect(
			model.sections
				.slice(0, -1)
				.flatMap((s) => s.items)
				.some((i) => i.tone),
		).toBe(false);
	});

	test("a request in flight disables Deploy only", () => {
		const model = serviceMenu({ service, actions, busy: true });
		expect(find(model, "deploy").disabled).toBe(true);
		expect(find(model, "open").disabled).toBeUndefined();
	});

	test("an ineligible service explains why Scale is off", () => {
		const fixed = makeService({
			scaling: {
				...service.scaling,
				eligible: false,
				ineligibleReason: "A service with a volume runs one replica.",
			},
		});
		const model = serviceMenu({ service: fixed, actions, busy: false });
		expect(find(model, "scale").disabled).toBe(true);
		expect(notes(model)).toEqual(["A service with a volume runs one replica."]);
	});

	test("every row carries its service id", () => {
		const model = serviceMenu({ service, actions, busy: false });
		for (const item of items(model)) {
			expect("serviceId" in item.action ? item.action.serviceId : null).toBe(
				"svc",
			);
		}
	});
});

describe("paneMenu", () => {
	test("offers every way to add a service the Create button offers, at the pointer", () => {
		const at = { x: 40, y: 80 };
		const model = paneMenu({ at, empty: false });
		const create = model.sections[0]?.items.map((item) => item.action);
		expect(create).toEqual([
			{ type: "create", source: "git", at },
			{ type: "create", source: "image", at },
			{ type: "create", source: "database", at },
			{ type: "create", source: "templates", at },
			{ type: "create", source: "compose", at },
			{ type: "create", source: "coolify", at },
			{ type: "create", source: "stack", at },
			{ type: "create", source: "external", at },
			{ type: "create", source: "port", at },
		] satisfies MenuAction[]);
		expect(model.sections[0]?.items.map((item) => item.label)).toEqual(
			ROOT_ENTRIES.map((entry) => entry.label),
		);
	});

	test("an empty canvas has nothing to arrange, fit or search", () => {
		const model = paneMenu({ at: null, empty: true });
		for (const key of ["arrange", "fit", "search"]) {
			expect(find(model, key).disabled).toBe(true);
		}
		expect(find(model, "shortcuts").disabled).toBeUndefined();
		expect(find(model, "create-git").disabled).toBeUndefined();
	});
});

describe("internetMenu", () => {
	const domains = [
		{ hostname: "api.example.com", serviceId: "a", serviceName: "api" },
		{ hostname: "web.example.com", serviceId: "b", serviceName: "web" },
	];

	test("lists every public domain to open", () => {
		const model = internetMenu({ domains, candidates: [] });
		expect(model.sections[0]?.label).toBe("Open");
		expect(model.sections[0]?.items.map((item) => item.action)).toEqual([
			{ type: "open-domain", hostname: "api.example.com" },
			{ type: "open-domain", hostname: "web.example.com" },
		]);
	});

	test("offers each candidate service to add a domain to", () => {
		const model = internetMenu({
			domains,
			candidates: [
				{ id: "a", name: "api" },
				{ id: "c", name: "worker" },
			],
		});
		const add = model.sections.find((s) => s.label === "Add domain to");
		expect(add?.items.map((item) => item.action)).toEqual([
			{ type: "add-domain", serviceId: "a" },
			{ type: "add-domain", serviceId: "c" },
		]);
		expect(add?.note).toBeUndefined();
	});

	test("says so when no service can take a domain", () => {
		const model = internetMenu({ domains, candidates: [] });
		const add = model.sections.find((s) => s.label === "Add domain to");
		expect(add?.items).toEqual([]);
		expect(add?.note).toContain("No app or route");
	});

	test("folds long lists and counts what is left out", () => {
		const many = Array.from({ length: MENU_LIST_LIMIT + 3 }, (_, i) => ({
			hostname: `h${i}.example.com`,
			serviceId: "a",
			serviceName: "api",
		}));
		const candidates = Array.from({ length: MENU_LIST_LIMIT + 1 }, (_, i) => ({
			id: `s${i}`,
			name: `svc ${i}`,
		}));
		const model = internetMenu({ domains: many, candidates });
		expect(model.sections[0]?.items).toHaveLength(MENU_LIST_LIMIT);
		expect(model.sections[0]?.note).toContain("3 more domains");
		const add = model.sections[1];
		expect(add?.items).toHaveLength(MENU_LIST_LIMIT);
		expect(add?.note).toContain("1 more service");
	});

	test("only apps and routes take a domain from the canvas", () => {
		expect(canTakeDomain({ kind: "app" })).toBe(true);
		expect(canTakeDomain({ kind: "route" })).toBe(true);
		expect(canTakeDomain({ kind: "database" })).toBe(false);
		expect(canTakeDomain({ kind: "worker" })).toBe(false);
	});
});

describe("edgeMenu", () => {
	const base: EdgeMenuInput = {
		edgeId: "l1",
		kind: "manual",
		from: { nodeId: "a", name: "api", isService: true },
		to: { nodeId: "c", name: "db", isService: true },
		link: { id: "l1", source: "manual" },
		references: [],
		hostnames: [],
	};

	test("a manual link can be reviewed and removed", () => {
		const model = edgeMenu(base);
		expect(model.title).toBe("api → db");
		expect(find(model, "references").disabled).toBe(false);
		const remove = find(model, "remove");
		expect(remove.disabled).toBe(false);
		expect(remove.tone).toBe("destructive");
		expect(remove.action).toEqual({ type: "remove-link", edgeId: "l1" });
		expect(notes(model)).toEqual([]);
	});

	test("a link made of references cannot be removed, and says why", () => {
		const model = edgeMenu({
			...base,
			kind: "reference",
			link: { id: "l1", source: "reference" },
			references: ["DATABASE_URL"],
		});
		expect(find(model, "remove").disabled).toBe(true);
		expect(find(model, "references").label).toBe("View references (1)");
		expect(notes(model)[0]).toContain("variable references");
	});

	test("a manual link that variables also reference warns that the cable stays", () => {
		const model = edgeMenu({
			...base,
			kind: "reference",
			references: ["DATABASE_URL", "DATABASE_HOST"],
		});
		expect(find(model, "remove").disabled).toBe(false);
		expect(notes(model)[0]).toContain("cable stays drawn");
	});

	test("jumps to the consumer's variables and to either end", () => {
		const model = edgeMenu(base);
		expect(labels(model)).toEqual([
			"View references",
			"Variables of api",
			"Open api",
			"Open db",
			"Remove link",
		]);
		expect(find(model, "open:a:variables").action).toEqual({
			type: "open-service",
			serviceId: "a",
			section: "variables",
		});
	});

	test("a collapsed group end has no inspector to open", () => {
		const model = edgeMenu({
			...base,
			to: { nodeId: "group:g", name: "Stores", isService: false },
		});
		expect(labels(model)).toEqual([
			"View references",
			"Variables of api",
			"Open api",
			"Remove link",
		]);
	});

	test("the Internet cable opens the target's domains and its settings", () => {
		const model = edgeMenu({
			edgeId: "internet:a",
			kind: "internet",
			from: { nodeId: INTERNET_ID, name: "Internet", isService: false },
			to: { nodeId: "a", name: "api", isService: true },
			link: null,
			references: [],
			hostnames: ["api.example.com", "www.example.com"],
		});
		expect(model.title).toBe("Internet → api");
		expect(labels(model)).toEqual([
			"api.example.com",
			"www.example.com",
			"Open api",
		]);
		expect(find(model, "open:a:settings").action).toEqual({
			type: "open-service",
			serviceId: "a",
			section: "settings",
		});
		expect(items(model).some((item) => item.key === "remove")).toBe(false);
	});
});

describe("groupMenu", () => {
	const base = {
		groupId: "g1",
		name: "Backend",
		collapsed: false,
		memberCount: 3,
		isStack: false,
	};

	test("renames, collapses, zooms and ungroups", () => {
		const model = groupMenu(base);
		expect(model.title).toBe("Backend");
		expect(labels(model)).toEqual([
			"Rename",
			"Collapse",
			"Zoom to group",
			"Ungroup",
		]);
		expect(find(model, "rename").action).toEqual({
			type: "rename-group",
			groupId: "g1",
		});
	});

	test("a collapsed group offers Expand", () => {
		const model = groupMenu({ ...base, collapsed: true });
		expect(find(model, "toggle").label).toBe("Expand");
		expect(find(model, "toggle").icon).toBe("expand");
	});

	test("ungrouping says the services stay", () => {
		expect(notes(groupMenu(base))).toEqual([
			"The 3 services stay; only the frame goes.",
		]);
		expect(notes(groupMenu({ ...base, memberCount: 1 }))).toEqual([
			"The 1 service stays; only the frame goes.",
		]);
	});

	test("only an installed stack can be deleted as a whole", () => {
		expect(labels(groupMenu(base))).not.toContain("Delete stack…");
		const stack = groupMenu({ ...base, isStack: true });
		const row = find(stack, "delete-stack");
		expect(row.tone).toBe("destructive");
		expect(stack.sections.at(-1)?.items).toEqual([row]);
	});

	test("an empty group has nothing to zoom to", () => {
		expect(find(groupMenu({ ...base, memberCount: 0 }), "zoom").disabled).toBe(
			true,
		);
	});
});

describe("selectionMenu", () => {
	const idle = makeService({ id: "a", name: "api" });
	const live = makeService({
		id: "b",
		name: "web",
		currentDeploymentId: "d1",
		observed: makeObserved(),
		latestDeployment: makeDeployment("succeeded"),
	});
	const building = makeService({
		id: "c",
		name: "db",
		currentDeploymentId: "d2",
		latestDeployment: makeDeployment("building"),
	});

	test("deploys what is not already deploying", () => {
		const model = selectionMenu({
			services: [idle, live, building],
			isProduction: false,
		});
		expect(model.title).toBe("3 services selected");
		const deploy = find(model, "deploy");
		expect(deploy.label).toBe("Deploy 2 services");
		expect(deploy.action).toEqual({
			type: "deploy-selected",
			serviceIds: ["a", "b"],
		});
		expect(notes(model)[0]).toContain("1 service is already deploying");
	});

	test("restarts only what is deployed and idle", () => {
		const model = selectionMenu({
			services: [idle, live, building],
			isProduction: false,
		});
		const restart = find(model, "restart");
		expect(restart.label).toBe("Restart 1 service");
		expect(restart.action).toEqual({
			type: "restart-selected",
			serviceIds: ["b"],
		});
	});

	test("nothing to restart disables the row", () => {
		const model = selectionMenu({
			services: [idle, idle],
			isProduction: false,
		});
		expect(find(model, "restart").disabled).toBe(true);
	});

	test("production deploys through the review sheet", () => {
		const model = selectionMenu({ services: [idle, live], isProduction: true });
		expect(find(model, "deploy").label).toBe("Review and deploy…");
		expect(notes(model)[0]).toContain("review sheet");
	});

	test("groups and arranges the selected ids", () => {
		const model = selectionMenu({
			services: [idle, live],
			isProduction: false,
		});
		expect(find(model, "group").action).toEqual({
			type: "group-selected",
			serviceIds: ["a", "b"],
		});
		expect(find(model, "arrange").action).toEqual({
			type: "arrange-selected",
			serviceIds: ["a", "b"],
		});
		expect(find(model, "clear").action).toEqual({ type: "clear-selection" });
	});

	test("the selectors agree with the card's own in-flight rule", () => {
		expect(deployableOf([idle, building]).map((s) => s.id)).toEqual(["a"]);
		expect(restartableOf([idle, live, building]).map((s) => s.id)).toEqual([
			"b",
		]);
	});
});

describe("selectionMenu combine row", () => {
	const git = (id: string, over: Parameters<typeof makeService>[0] = {}) =>
		makeService({
			id,
			name: id,
			slug: id,
			sourceType: "git",
			config: {
				...makeService().config,
				gitUrl: "https://github.com/acme/shop.git",
				gitBranch: "main",
			},
			...over,
		});

	test("offers 'Combine into one service…' for services of one repository", () => {
		const model = selectionMenu({
			services: [git("web"), git("worker")],
			isProduction: false,
		});
		const row = find(model, "combine");
		expect(row.label).toBe("Combine into one service…");
		expect(row.icon).toBe("combine");
		expect(row.disabled).toBeFalsy();
		expect(row.action).toEqual({
			type: "combine-selected",
			serviceIds: ["web", "worker"],
		});
	});

	test("is absent unless at least two services are selected", () => {
		const one = selectionMenu({ services: [git("web")], isProduction: false });
		expect(items(one).some((item) => item.key === "combine")).toBe(false);
		const none = selectionMenu({ services: [], isProduction: false });
		expect(items(none).some((item) => item.key === "combine")).toBe(false);
	});

	test("is off, with the first reason under it, when the pre-check fails", () => {
		const model = selectionMenu({
			services: [git("web"), git("worker", { sourceType: "image" })],
			isProduction: false,
		});
		expect(find(model, "combine").disabled).toBe(true);
		expect(notes(model).some((note) => note.includes("deploys an image"))).toBe(
			true,
		);
	});

	test("a different branch is named in the reason", () => {
		const other = git("worker", {
			config: {
				...makeService().config,
				gitUrl: "https://github.com/acme/shop.git",
				gitBranch: "next",
			},
		});
		const model = selectionMenu({
			services: [git("web"), other],
			isProduction: false,
		});
		expect(find(model, "combine").disabled).toBe(true);
		expect(notes(model).join(" ")).toContain("different branch");
	});

	test("a service that is deploying blocks it", () => {
		const model = selectionMenu({
			services: [
				git("web"),
				git("worker", { latestDeployment: makeDeployment("building") }),
			],
			isProduction: false,
		});
		expect(find(model, "combine").disabled).toBe(true);
		expect(notes(model).join(" ")).toContain("worker is deploying");
	});

	test("a service that already has processes blocks it", () => {
		const model = selectionMenu({
			services: [
				git("web"),
				git("worker", {
					processes: [
						{
							name: "web",
							default: true,
							port: 0,
							command: [],
							singleton: false,
							stopGraceSeconds: 30,
							scaling: makeService().scaling,
							observed: null,
							pending: false,
							removed: false,
						},
					],
				}),
			],
			isProduction: false,
		});
		expect(find(model, "combine").disabled).toBe(true);
		expect(notes(model).join(" ")).toContain("already has several processes");
	});
});

describe("resolveMenuTarget", () => {
	const known: TargetKnown = {
		serviceIds: new Set(["a", "b", "c"]),
		groupIds: new Set(["g1"]),
		edgeIds: new Set(["l1", "internet:a"]),
		selectedServiceIds: [],
	};
	const probe = (over: Partial<TargetProbe> = {}): TargetProbe => ({
		nodeId: null,
		edgeId: null,
		selectionRect: false,
		dockServiceId: null,
		...over,
	});

	test("a card is a service", () => {
		expect(resolveMenuTarget(probe({ nodeId: "a" }), known)).toEqual({
			kind: "service",
			serviceId: "a",
		});
	});

	test("the Internet node and group nodes are their own targets", () => {
		expect(resolveMenuTarget(probe({ nodeId: INTERNET_ID }), known)).toEqual({
			kind: "internet",
		});
		expect(resolveMenuTarget(probe({ nodeId: "group:g1" }), known)).toEqual({
			kind: "group",
			groupId: "g1",
		});
	});

	test("a group that no longer exists falls back to the pane", () => {
		expect(resolveMenuTarget(probe({ nodeId: "group:gone" }), known)).toEqual({
			kind: "pane",
		});
		expect(resolveMenuTarget(probe({ nodeId: "gone" }), known)).toEqual({
			kind: "pane",
		});
	});

	test("a cable is an edge, a known one only", () => {
		expect(resolveMenuTarget(probe({ edgeId: "l1" }), known)).toEqual({
			kind: "edge",
			edgeId: "l1",
		});
		expect(resolveMenuTarget(probe({ edgeId: "nope" }), known)).toEqual({
			kind: "pane",
		});
	});

	test("the card's dock is the card", () => {
		expect(resolveMenuTarget(probe({ dockServiceId: "b" }), known)).toEqual({
			kind: "service",
			serviceId: "b",
		});
		expect(resolveMenuTarget(probe({ dockServiceId: "zzz" }), known)).toEqual({
			kind: "pane",
		});
	});

	test("nothing under the pointer is the pane", () => {
		expect(resolveMenuTarget(probe(), known)).toEqual({ kind: "pane" });
	});

	describe("with several cards selected", () => {
		const several: TargetKnown = { ...known, selectedServiceIds: ["a", "b"] };

		test("the selection box is the selection", () => {
			expect(
				resolveMenuTarget(probe({ selectionRect: true }), several),
			).toEqual({ kind: "selection", serviceIds: ["a", "b"] });
		});

		test("a selected card is the selection", () => {
			expect(resolveMenuTarget(probe({ nodeId: "b" }), several)).toEqual({
				kind: "selection",
				serviceIds: ["a", "b"],
			});
		});

		test("an unselected card is only itself", () => {
			expect(resolveMenuTarget(probe({ nodeId: "c" }), several)).toEqual({
				kind: "service",
				serviceId: "c",
			});
		});

		test("the selection box with one card selected is not a selection", () => {
			const one: TargetKnown = { ...known, selectedServiceIds: ["a"] };
			expect(resolveMenuTarget(probe({ selectionRect: true }), one)).toEqual({
				kind: "pane",
			});
		});

		test("groups and the Internet node keep their own menus", () => {
			expect(resolveMenuTarget(probe({ nodeId: "group:g1" }), several)).toEqual(
				{
					kind: "group",
					groupId: "g1",
				},
			);
			expect(
				resolveMenuTarget(probe({ nodeId: INTERNET_ID }), several),
			).toEqual({
				kind: "internet",
			});
		});
	});
});
