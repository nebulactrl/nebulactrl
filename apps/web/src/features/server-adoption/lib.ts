import type {
	AdoptionChoices,
	AdoptionEnvironment,
	AdoptionEnvironmentChoice,
	AdoptionPlan,
	AdoptionResult,
	AdoptionService,
	AdoptionServiceChoice,
} from "#/api/gen/types.gen";

/** What the reader can choose for a service. */
export type ServiceAction = "adopt" | "leave" | "remove";

export const EMPTY_CHOICES: AdoptionChoices = {};

function plural(count: number, one: string, many = `${one}s`): string {
	return `${count} ${count === 1 ? one : many}`;
}

// ---- choices ----
//
// The reader's decisions are an `AdoptionChoices`, keyed by the ids the plan
// carries (the ids found on the containers). A member set to `undefined` goes
// back to the plan's default, and an entry with nothing left in it is dropped,
// so a request that changes nothing has the same fingerprint as no request.

type EnvironmentChange = {
	[K in keyof AdoptionEnvironmentChoice]:
		| AdoptionEnvironmentChoice[K]
		| undefined;
};
type ServiceChange = {
	[K in keyof AdoptionServiceChoice]: AdoptionServiceChoice[K] | undefined;
};

function merge<T extends object>(
	current: T | undefined,
	change: { [K in keyof T]?: T[K] | undefined },
): T | undefined {
	const next: Record<string, unknown> = { ...current };
	for (const [key, value] of Object.entries(change)) {
		if (value === undefined) delete next[key];
		else next[key] = value;
	}
	return Object.keys(next).length === 0 ? undefined : (next as T);
}

function withEntry<T extends object>(
	map: { [key: string]: T } | undefined,
	id: string,
	entry: T | undefined,
): { [key: string]: T } | undefined {
	const next = { ...map };
	if (entry === undefined) delete next[id];
	else next[id] = entry;
	return Object.keys(next).length === 0 ? undefined : next;
}

function withMaps(
	environments: AdoptionChoices["environments"],
	services: AdoptionChoices["services"],
): AdoptionChoices {
	return {
		...(environments ? { environments } : {}),
		...(services ? { services } : {}),
	};
}

export function patchEnvironment(
	choices: AdoptionChoices,
	environmentId: string,
	change: EnvironmentChange,
): AdoptionChoices {
	const entry = merge(choices.environments?.[environmentId], change);
	return withMaps(
		withEntry(choices.environments, environmentId, entry),
		choices.services,
	);
}

export function patchService(
	choices: AdoptionChoices,
	serviceId: string,
	change: ServiceChange,
): AdoptionChoices {
	const entry = merge(choices.services?.[serviceId], change);
	return withMaps(
		choices.environments,
		withEntry(choices.services, serviceId, entry),
	);
}

/** Forces the secret flag of one variable on or off. */
export function setSecret(
	choices: AdoptionChoices,
	serviceId: string,
	key: string,
	secret: boolean,
): AdoptionChoices {
	const secrets = { ...choices.services?.[serviceId]?.secret, [key]: secret };
	return patchService(choices, serviceId, { secret: secrets });
}

/** Leaves a domain out of the adoption, or brings it back. */
export function setHostSkipped(
	choices: AdoptionChoices,
	serviceId: string,
	host: string,
	skipped: boolean,
): AdoptionChoices {
	const current = choices.services?.[serviceId]?.skipHosts ?? [];
	const rest = current.filter((h) => h !== host);
	const next = skipped ? [...rest, host] : rest;
	return patchService(choices, serviceId, {
		skipHosts: next.length === 0 ? undefined : next,
	});
}

function trimmed<T extends { name?: string | undefined }>(entry: T): T {
	const name = entry.name?.trim();
	const { name: _name, ...rest } = entry;
	return (name ? { ...rest, name } : rest) as T;
}

/**
 * The choices as they go on the wire: names trimmed, empty names and project
 * names dropped (the server's default applies), and entries with nothing left
 * removed.
 */
export function buildChoices(choices: AdoptionChoices): AdoptionChoices {
	const environments: { [key: string]: AdoptionEnvironmentChoice } = {};
	for (const [id, entry] of Object.entries(choices.environments ?? {})) {
		const cleaned = trimmed(entry);
		const projectName = cleaned.projectName?.trim();
		const { projectName: _drop, ...rest } = cleaned;
		const next: AdoptionEnvironmentChoice = {
			...rest,
			...(projectName && cleaned.projectId === undefined
				? { projectName }
				: {}),
		};
		if (Object.keys(next).length > 0) environments[id] = next;
	}
	const services: { [key: string]: AdoptionServiceChoice } = {};
	for (const [id, entry] of Object.entries(choices.services ?? {})) {
		const next = trimmed(entry);
		if (Object.keys(next).length > 0) services[id] = next;
	}
	return withMaps(
		Object.keys(environments).length > 0 ? environments : undefined,
		Object.keys(services).length > 0 ? services : undefined,
	);
}

/** One string per distinct request; the plan follows it, and the idempotency key too. */
export function choicesFingerprint(choices: AdoptionChoices): string {
	return JSON.stringify(buildChoices(choices));
}

/**
 * "Leave all running unmanaged": every service the plan found is left, whatever
 * else was chosen for it. A service this instance already has (reattach) is not
 * a workload to leave: it stays as it is.
 */
export function leaveAllChoices(
	plan: AdoptionPlan,
	choices: AdoptionChoices,
): AdoptionChoices {
	const services: { [key: string]: AdoptionServiceChoice } = {};
	for (const environment of plan.environments ?? []) {
		for (const service of environment.services ?? []) {
			if (service.action === "reattach") continue;
			services[service.id] = { action: "leave" };
		}
	}
	return buildChoices({
		...(choices.environments ? { environments: choices.environments } : {}),
		services,
	});
}

// ---- what a row shows ----

/** The action on screen: the reader's choice, else what the plan decided. */
export function actionOf(
	service: AdoptionService,
	choice: AdoptionServiceChoice | undefined,
): AdoptionService["action"] {
	if (service.action === "reattach") return "reattach";
	return choice?.action ?? service.action;
}

export const ACTION_LABEL: Record<ServiceAction, string> = {
	adopt: "Adopt",
	leave: "Leave running",
	remove: "Remove",
};

export const KIND_LABEL: Record<AdoptionService["kind"], string> = {
	app: "App",
	worker: "Worker",
	database: "Database",
	stack: "Stack",
	route: "Route",
};

/** The project an environment goes into, as the reader's choice and the plan say. */
export function projectModeOf(
	environment: AdoptionEnvironment,
	choice: AdoptionEnvironmentChoice | undefined,
): "existing" | "new" {
	if (environment.action === "existing") return "existing";
	return choice?.projectId !== undefined ? "existing" : "new";
}

/** The service's image or database, for the line under its name. */
export function sourceLine(service: AdoptionService): string {
	const db = service.database;
	if (db) {
		const flavor = db.flavor ? ` ${db.flavor}` : "";
		return `${db.engine}${flavor} ${db.version}`;
	}
	return service.image ?? service.sourceType;
}

/** "1 service · 3 containers": the services in the plan that will run as chosen. */
export function totalsText(plan: AdoptionPlan): string {
	const { adopt, leave, remove, containers } = plan.totals;
	const parts = [plural(containers, "container")];
	if (adopt > 0) parts.push(`${adopt} to adopt`);
	if (leave > 0) parts.push(`${leave} left running`);
	if (remove > 0) parts.push(`${remove} to remove`);
	return parts.join(" · ");
}

/** The label of the apply button. */
export function applyLabel(plan: AdoptionPlan | undefined): string {
	const adopt = plan?.totals.adopt ?? 0;
	return adopt > 0 ? `Adopt ${plural(adopt, "service")}` : "Apply choices";
}

/**
 * Why Apply cannot be pressed, or null. The plan on screen must answer the
 * choices on screen: a stale plan could list blockers the choices already
 * fixed, or hide ones they just caused.
 */
export function adoptionBlocker({
	plan,
	fresh,
	failed,
}: {
	plan: AdoptionPlan | undefined;
	fresh: boolean;
	failed: boolean;
}): string | null {
	if (!plan) return "Reading the host.";
	if (failed) return "The last check failed. Try again.";
	if (!fresh) return "Checking your changes.";
	const blockers = plan.blockers ?? [];
	if (blockers.length > 0) {
		return `${plural(blockers.length, "problem")} to fix first.`;
	}
	const { adopt, leave, remove } = plan.totals;
	if (adopt + leave + remove === 0) return "Nothing to apply.";
	return null;
}

/**
 * What was done, in the action's own words: "Adopted 3 services, reattached 1,
 * left 2 running unmanaged, removed 1." Only what happened is named, so a
 * review that only left workloads says "Left 2 workloads running unmanaged."
 */
export function resultText(result: AdoptionResult): string {
	// `services` lists the adopted and the reattached services alike.
	const adopted = Math.max(
		0,
		(result.services ?? []).length - result.reattached,
	);
	const parts: string[] = [];
	if (adopted > 0) parts.push(`adopted ${plural(adopted, "service")}`);
	if (result.reattached > 0) parts.push(`reattached ${result.reattached}`);
	if (result.left > 0) {
		parts.push(
			adopted > 0 || result.reattached > 0
				? `left ${result.left} running unmanaged`
				: `left ${plural(result.left, "workload")} running unmanaged`,
		);
	}
	if (result.removed > 0) parts.push(`removed ${result.removed}`);
	if (parts.length === 0) return "Nothing was changed.";
	const text = parts.join(", ");
	return `${text.charAt(0).toUpperCase()}${text.slice(1)}.`;
}

/** The result step's title: the action that was taken. */
export function resultTitle(result: AdoptionResult): string {
	if ((result.services ?? []).length > 0 || result.reattached > 0) {
		return "Workloads adopted";
	}
	return result.left > 0 ? "Workloads left unmanaged" : "Choices applied";
}
