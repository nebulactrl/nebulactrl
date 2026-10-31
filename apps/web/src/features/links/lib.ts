import type { ProposedVariable } from "#/api/gen/types.gen";
import type { Service, ServiceLink } from "#/api/models";

/** A variable name the environment accepts. */
export const KEY_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** One proposed variable in the review: a checkbox, a name that can be edited, a value that cannot. */
export interface ProposalRow {
	/** The name the control plane proposed; identifies the row. */
	proposed: string;
	/** The name the reader wants. */
	key: string;
	value: string;
	checked: boolean;
	/** Part of the recommended set the control plane preselects. */
	recommended?: boolean;
	/** What the reference is, in the control plane's words. */
	description?: string;
	/** The reference resolves to a secret (a connection URL carries the password). */
	secret?: boolean;
	/** On the consumer already: this exact reference, another value, or nothing. */
	status?: ProposedVariable["status"];
}

/**
 * The rows of a link preview. The recommended set is checked, except what the
 * consumer already holds as this very reference: ticking it would change
 * nothing.
 */
export function rowsFromProposals(
	proposals: readonly ProposedVariable[],
): ProposalRow[] {
	return proposals.map((proposal) => ({
		proposed: proposal.key,
		key: proposal.key,
		value: proposal.value,
		checked: proposal.recommended && proposal.status !== "unchanged",
		recommended: proposal.recommended,
		secret: proposal.secret,
		...(proposal.description ? { description: proposal.description } : {}),
		...(proposal.status ? { status: proposal.status } : {}),
	}));
}

/**
 * Which of two services holds the variables when the reader draws a cable
 * from `dragged` to `target`. A database never consumes: drawing from a
 * database to an app means the app reaches the database, so the direction
 * follows what the services are, not which jack the cable left. Between two
 * databases, or two services that are not databases, the drawn direction
 * stands.
 */
export function orientLink(
	dragged: Pick<Service, "kind">,
	target: Pick<Service, "kind">,
): "forward" | "reversed" {
	return dragged.kind === "database" && target.kind !== "database"
		? "reversed"
		: "forward";
}

/** Services that read a database: apps and workers with no cable to any database yet. */
export function wireTargets(
	services: readonly Pick<Service, "id" | "kind">[],
	links: readonly Pick<ServiceLink, "fromServiceId" | "toServiceId">[],
	database: Pick<Service, "id">,
): string[] {
	const databases = new Set(
		services.filter((service) => service.kind === "database").map((s) => s.id),
	);
	const wired = new Set(
		links
			.filter((link) => databases.has(link.toServiceId))
			.map((link) => link.fromServiceId),
	);
	return services
		.filter(
			(service) =>
				(service.kind === "app" || service.kind === "worker") &&
				service.id !== database.id &&
				!wired.has(service.id),
		)
		.map((service) => service.id);
}

/**
 * The databases that appeared between two reads of an environment's
 * services: present now, absent before. The first read (`previous` null)
 * proposes nothing, so opening an environment never nags about databases
 * that were always there.
 */
export function addedDatabases<T extends Pick<Service, "id" | "kind">>(
	previous: ReadonlySet<string> | null,
	services: readonly T[],
): T[] {
	if (previous === null) return [];
	return services.filter(
		(service) => service.kind === "database" && !previous.has(service.id),
	);
}

export interface RowState {
	/** Why a checked row cannot be added; unset when it can. */
	error?: string;
	/** The source service already has this variable, so adding replaces it. */
	replaces: boolean;
}

/**
 * Checks each row against the rules the control plane would apply: a valid
 * name, and no name used twice among the rows that will be added. Unchecked
 * rows are never in error: they are not going anywhere.
 */
export function rowStates(
	rows: readonly ProposalRow[],
	existing: ReadonlySet<string>,
): RowState[] {
	const uses = new Map<string, number>();
	for (const row of rows) {
		if (row.checked) uses.set(row.key, (uses.get(row.key) ?? 0) + 1);
	}
	return rows.map((row): RowState => {
		// A row that is this very reference, under the name it came with, has
		// nothing to replace; one renamed onto a taken name replaces that variable.
		const same = row.status === "unchanged" && row.key === row.proposed;
		const replaces = existing.has(row.key) && !same;
		if (!row.checked) return { replaces };
		if (row.key === "") return { error: "Give it a name.", replaces };
		if (!KEY_PATTERN.test(row.key)) {
			return {
				error:
					"Use letters, digits and underscores; do not start with a digit.",
				replaces,
			};
		}
		if ((uses.get(row.key) ?? 0) > 1) {
			return { error: "Another row uses this name.", replaces };
		}
		return { replaces };
	});
}

/** The rows that will be written, with their chosen names. */
export function chosenVariables(
	rows: readonly ProposalRow[],
): Array<{ key: string; value: string }> {
	return rows
		.filter((row) => row.checked)
		.map((row) => ({ key: row.key, value: row.value }));
}

/**
 * The link and, when the other way round is drawn too, its reverse: the two
 * directions of one pair of services, the way the references sheet lists them.
 */
export function directionsOf(
	link: ServiceLink,
	links: readonly ServiceLink[],
): ServiceLink[] {
	const reverse = links.find(
		(candidate) =>
			candidate.fromServiceId === link.toServiceId &&
			candidate.toServiceId === link.fromServiceId,
	);
	return reverse ? [link, reverse] : [link];
}
