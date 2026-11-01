import { describe, expect, test } from "bun:test";
import type {
	ComposePlan,
	ComposePlanEnv,
	ComposeStackComponent,
	ComposeStackPlan,
} from "#/api/gen/types.gen";
import {
	buildImportRequest,
	buildPlanRequest,
	componentDomain,
	emptySource,
	NO_CHOICES,
	setDomain,
	setVariableChoice,
	stackPlanOf,
	stackSummaryText,
	stackVariableState,
	stageBlocker,
} from "./lib";

const web: ComposeStackComponent = {
	name: "web",
	index: 1,
	image: "nginx:1.27",
	ports: [80],
	profiles: [],
	action: "run",
	port: 80,
	notes: [],
	domains: [
		{
			enabled: true,
			generate: false,
			host: "shop.example.com",
			source: "traefik",
		},
	],
};
const db: ComposeStackComponent = {
	...web,
	name: "db",
	index: 0,
	image: "postgres:16",
	ports: [5432],
	port: 0,
	domains: [],
};

function variable(over: Partial<ComposePlanEnv>): ComposePlanEnv {
	return {
		key: "TAG",
		origin: "dotenv",
		rewritten: false,
		secret: false,
		secretGuess: false,
		skipped: false,
		value: "1.4",
		...over,
	};
}

function stack(over: Partial<ComposeStackPlan> = {}): ComposeStackPlan {
	return {
		name: "shop",
		slug: "shop",
		components: [db, web],
		composeAvailable: true,
		notes: [],
		unrestricted: false,
		variables: [variable({}), variable({ key: "COMPOSE_FILE", skipped: true })],
		violations: [],
		...over,
	};
}

function plan(over: Partial<ComposePlan> = {}): ComposePlan {
	return {
		blockers: [],
		checks: [],
		installable: true,
		missing: [],
		mode: "stack",
		name: "shop",
		neededFiles: [],
		networks: [],
		notes: [],
		rewrites: [],
		services: [],
		sourceKind: "paste",
		stack: stack(),
		warnings: [],
		...over,
	};
}

describe("requests", () => {
	const draft = { ...emptySource(), compose: "services: {}" };

	test("split is the default and sends no mode", () => {
		expect(buildPlanRequest(draft, NO_CHOICES, "", []).mode).toBeUndefined();
		expect(
			buildImportRequest(draft, NO_CHOICES, "srv", [], false).mode,
		).toBeUndefined();
	});

	test("stack mode is sent on the plan and the import", () => {
		const stackDraft = { ...draft, importMode: "stack" as const };
		expect(buildPlanRequest(stackDraft, NO_CHOICES, "srv", [])).toMatchObject({
			mode: "stack",
			serverId: "srv",
		});
		expect(
			buildImportRequest(stackDraft, NO_CHOICES, "srv", [], true),
		).toMatchObject({ mode: "stack", deploy: true, serverId: "srv" });
	});
});

describe("stack choices", () => {
	test("a variable's secret and skip flags live under choices.variables", () => {
		let choices = setVariableChoice(NO_CHOICES, "TAG", { secret: true });
		expect(choices.variables).toEqual({ TAG: { secret: true } });
		choices = setVariableChoice(choices, "TAG", { skip: true });
		expect(choices.variables?.TAG).toEqual({ secret: true, skip: true });
		choices = setVariableChoice(choices, "TAG", { skip: false });
		expect(choices.variables?.TAG).toEqual({ secret: true });
		choices = setVariableChoice(choices, "TAG", { secret: null });
		expect(choices).toEqual({});
	});

	test("the plan's guess applies until the reader chooses", () => {
		const guessed = variable({
			key: "DB_PASSWORD",
			secret: true,
			secretGuess: true,
		});
		expect(stackVariableState(guessed, undefined).secret).toBe(true);
		expect(stackVariableState(guessed, { secret: false }).secret).toBe(false);
	});

	test("COMPOSE_ and DOCKER_ keys are skipped for good", () => {
		const locked = variable({ key: "DOCKER_HOST", skipped: true });
		expect(stackVariableState(locked, undefined)).toEqual({
			secret: false,
			skipped: true,
			lockedByPlan: true,
		});
	});

	test("a component's domain follows the suggestion until it is edited", () => {
		expect(componentDomain(web, NO_CHOICES)).toEqual({
			enabled: true,
			host: "shop.example.com",
		});
		expect(componentDomain(db, NO_CHOICES)).toEqual({
			enabled: false,
			host: "",
		});
		const off = setDomain(NO_CHOICES, "web", { enabled: false });
		expect(componentDomain(web, off).enabled).toBe(false);
		const renamed = setDomain(NO_CHOICES, "web", {
			enabled: true,
			host: "www.example.com",
		});
		expect(componentDomain(web, renamed).host).toBe("www.example.com");
	});
});

describe("reading a stack plan", () => {
	test("only a stack plan has a stack", () => {
		expect(stackPlanOf(plan())?.name).toBe("shop");
		expect(stackPlanOf(plan({ mode: "split" }))).toBeUndefined();
		expect(stackPlanOf(undefined)).toBeUndefined();
	});

	test("the summary counts components, open problems and variables that are used", () => {
		expect(stackSummaryText(stack())).toBe(
			"2 components, 0 problems, 1 variable",
		);
		const violations = [
			{
				key: "privileged",
				kind: "restricted" as const,
				message: "no",
				service: "web",
			},
			{ key: "build", kind: "unsupported" as const, message: "no" },
		];
		expect(stackSummaryText(stack({ violations }))).toBe(
			"2 components, 2 problems, 1 variable",
		);
		// An unrestricted server lifts the restricted ones.
		expect(stackSummaryText(stack({ violations, unrestricted: true }))).toBe(
			"2 components, 1 problem, 1 variable",
		);
	});

	test("Stage waits for the plan, a server and the components", () => {
		const input = { plan: plan(), included: 2, serverId: "srv", fresh: true };
		expect(stageBlocker(input)).toBeNull();
		expect(stageBlocker({ ...input, serverId: "" })).toBe("Choose a server.");
		expect(stageBlocker({ ...input, included: 0 })).toBe(
			"Include at least one service.",
		);
		expect(
			stageBlocker({
				...input,
				plan: plan({
					installable: false,
					blockers: ["web: privileged is restricted on this server."],
				}),
			}),
		).toBe("web: privileged is restricted on this server.");
	});
});
