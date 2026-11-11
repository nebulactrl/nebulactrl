import { describe, expect, test } from "bun:test";
import {
	actionOf,
	adoptionBlocker,
	applyLabel,
	buildChoices,
	choicesFingerprint,
	leaveAllChoices,
	patchEnvironment,
	patchService,
	projectModeOf,
	resultText,
	resultTitle,
	setHostSkipped,
	setSecret,
	sourceLine,
	totalsText,
} from "./lib";
import {
	adoptedService,
	applied,
	DB,
	ENV,
	environment,
	plan,
	SVC,
	service,
} from "./test-fixtures";

describe("choices", () => {
	test("a patch sets members, and undefined takes one back out", () => {
		let choices = patchService({}, SVC, { name: "api", action: "leave" });
		expect(choices).toEqual({
			services: { [SVC]: { name: "api", action: "leave" } },
		});
		choices = patchService(choices, SVC, { action: undefined });
		expect(choices).toEqual({ services: { [SVC]: { name: "api" } } });
	});

	test("an entry with nothing left in it is dropped, and so is its map", () => {
		const choices = patchService({}, SVC, { name: "api" });
		expect(patchService(choices, SVC, { name: undefined })).toEqual({});
		const env = patchEnvironment({}, ENV, { production: false });
		expect(patchEnvironment(env, ENV, { production: undefined })).toEqual({});
	});

	test("patches keep the other map and the other entries", () => {
		let choices = patchEnvironment({}, ENV, { name: "live" });
		choices = patchService(choices, SVC, { action: "remove" });
		choices = patchService(choices, "other", { action: "leave" });
		expect(choices.environments).toEqual({ [ENV]: { name: "live" } });
		expect(Object.keys(choices.services ?? {})).toEqual([SVC, "other"]);
	});

	test("a secret flag is set per key and keeps the others", () => {
		let choices = setSecret({}, SVC, "A", true);
		choices = setSecret(choices, SVC, "B", false);
		expect(choices.services?.[SVC]?.secret).toEqual({ A: true, B: false });
	});

	test("a host is skipped and brought back", () => {
		let choices = setHostSkipped({}, SVC, "a.example.com", true);
		choices = setHostSkipped(choices, SVC, "b.example.com", true);
		expect(choices.services?.[SVC]?.skipHosts).toEqual([
			"a.example.com",
			"b.example.com",
		]);
		choices = setHostSkipped(choices, SVC, "a.example.com", false);
		expect(choices.services?.[SVC]?.skipHosts).toEqual(["b.example.com"]);
		choices = setHostSkipped(choices, SVC, "b.example.com", false);
		expect(choices).toEqual({});
	});
});

describe("buildChoices", () => {
	test("trims names and drops empty ones, so the server's default applies", () => {
		const built = buildChoices({
			environments: { [ENV]: { name: "  live  ", projectName: "   " } },
			services: { [SVC]: { name: "", action: "adopt" } },
		});
		expect(built).toEqual({
			environments: { [ENV]: { name: "live" } },
			services: { [SVC]: { action: "adopt" } },
		});
	});

	test("an existing project wins over a typed project name", () => {
		const built = buildChoices({
			environments: { [ENV]: { projectId: "p-1", projectName: "Shop" } },
		});
		expect(built.environments?.[ENV]).toEqual({ projectId: "p-1" });
	});

	test("choices that change nothing build to nothing, and fingerprint alike", () => {
		const idle = { services: { [SVC]: { name: " " } } };
		expect(buildChoices(idle)).toEqual({});
		expect(choicesFingerprint(idle)).toBe(choicesFingerprint({}));
		expect(
			choicesFingerprint({ services: { [SVC]: { action: "leave" } } }),
		).not.toBe(choicesFingerprint({}));
	});
});

describe("leaveAllChoices", () => {
	test("leaves every service of the plan, whatever was chosen, and keeps the environment choices", () => {
		const other = service({
			id: "33333333-3333-4333-8333-333333333333",
			slug: "db",
		});
		const result = leaveAllChoices(
			plan({ environments: [environment({}, [service(), other])] }),
			{
				environments: { [ENV]: { name: "live" } },
				services: { [SVC]: { action: "remove", name: "x" } },
			},
		);
		expect(result).toEqual({
			environments: { [ENV]: { name: "live" } },
			services: { [SVC]: { action: "leave" }, [other.id]: { action: "leave" } },
		});
	});

	test("a service this instance already has is not left", () => {
		const here = service({ action: "reattach" });
		const result = leaveAllChoices(
			plan({ environments: [environment({}, [here])] }),
			{},
		);
		expect(result).toEqual({});
	});
});

describe("what a row shows", () => {
	test("the reader's action wins over the plan's, but a reattach stays", () => {
		expect(actionOf(service({ action: "adopt" }), { action: "leave" })).toBe(
			"leave",
		);
		expect(actionOf(service({ action: "leave" }), undefined)).toBe("leave");
		expect(actionOf(service({ action: "reattach" }), { action: "leave" })).toBe(
			"reattach",
		);
	});

	test("an environment already here is fixed; a new one follows the project choice", () => {
		expect(projectModeOf(environment({ action: "existing" }), undefined)).toBe(
			"existing",
		);
		expect(projectModeOf(environment(), undefined)).toBe("new");
		expect(projectModeOf(environment(), { projectId: "p-1" })).toBe("existing");
	});

	test("the source line names the image, or the database engine and version", () => {
		expect(sourceLine(service())).toBe("nginx:1.27");
		expect(
			sourceLine(service({ database: { engine: "postgres", version: "17" } })),
		).toBe("postgres 17");
		expect(
			sourceLine(
				service({
					database: { engine: "postgres", version: "17", flavor: "postgis" },
				}),
			),
		).toBe("postgres postgis 17");
		const bare = service();
		delete bare.image;
		expect(sourceLine(bare)).toBe("image");
	});
});

describe("summary texts", () => {
	test("totals name the containers and only the actions in play", () => {
		expect(
			totalsText(
				plan({ totals: { adopt: 2, leave: 1, remove: 0, containers: 5 } }),
			),
		).toBe("5 containers · 2 to adopt · 1 left running");
		expect(
			totalsText(
				plan({ totals: { adopt: 0, leave: 0, remove: 1, containers: 1 } }),
			),
		).toBe("1 container · 1 to remove");
	});

	test("the apply label counts the services adopted", () => {
		expect(applyLabel(plan())).toBe("Adopt 1 service");
		expect(
			applyLabel(
				plan({ totals: { adopt: 3, leave: 0, remove: 0, containers: 3 } }),
			),
		).toBe("Adopt 3 services");
		expect(
			applyLabel(
				plan({ totals: { adopt: 0, leave: 1, remove: 0, containers: 1 } }),
			),
		).toBe("Apply choices");
		expect(applyLabel(undefined)).toBe("Apply choices");
	});

	test("the result says what happened, and only what happened", () => {
		expect(resultText(applied())).toBe("Adopted 1 service.");
		// `services` lists the reattached services too: 1 adopted + 1 reattached.
		const both = applied({
			services: [adoptedService(), adoptedService({ id: DB, slug: "db" })],
			left: 2,
			removed: 1,
			reattached: 1,
		});
		expect(resultText(both)).toBe(
			"Adopted 1 service, reattached 1, left 2 running unmanaged, removed 1.",
		);
		expect(resultTitle(both)).toBe("Workloads adopted");
		const onlyReattached = applied({
			services: [adoptedService()],
			reattached: 1,
		});
		expect(resultText(onlyReattached)).toBe("Reattached 1.");
		expect(resultTitle(onlyReattached)).toBe("Workloads adopted");
		expect(resultText(applied({ services: null, left: 2 }))).toBe(
			"Left 2 workloads running unmanaged.",
		);
		expect(resultText(applied({ services: null, projects: null }))).toBe(
			"Nothing was changed.",
		);
		expect(resultTitle(applied())).toBe("Workloads adopted");
		expect(resultTitle(applied({ services: null, left: 1 }))).toBe(
			"Workloads left unmanaged",
		);
		expect(resultTitle(applied({ services: null, removed: 1 }))).toBe(
			"Choices applied",
		);
	});
});

describe("adoptionBlocker", () => {
	const ready = { plan: plan(), fresh: true, failed: false };

	test("a fresh plan with something to apply and no blockers can go", () => {
		expect(adoptionBlocker(ready)).toBeNull();
	});

	test("waits for a plan, a fresh one, and a working one", () => {
		expect(adoptionBlocker({ ...ready, plan: undefined })).toBe(
			"Reading the host.",
		);
		expect(adoptionBlocker({ ...ready, fresh: false })).toBe(
			"Checking your changes.",
		);
		expect(adoptionBlocker({ ...ready, failed: true })).toBe(
			"The last check failed. Try again.",
		);
	});

	test("blockers count, singular and plural", () => {
		expect(adoptionBlocker({ ...ready, plan: plan({ blockers: ["a"] }) })).toBe(
			"1 problem to fix first.",
		);
		expect(
			adoptionBlocker({ ...ready, plan: plan({ blockers: ["a", "b"] }) }),
		).toBe("2 problems to fix first.");
	});

	test("a plan with nothing to adopt, leave or remove has nothing to apply", () => {
		expect(
			adoptionBlocker({
				...ready,
				plan: plan({
					totals: { adopt: 0, leave: 0, remove: 0, containers: 0 },
				}),
			}),
		).toBe("Nothing to apply.");
	});
});
