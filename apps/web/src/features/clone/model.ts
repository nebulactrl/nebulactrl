// What a clone or a move plan says, in the shapes the dialogs render. The API
// reports Go slices as nullable arrays; this is the one place they become
// plain ones.

import type {
	CloneDomainOutcome,
	ClonedService,
	CloneResult,
	CloneSkipped,
	MoveIncomingRef,
	MoveResult,
	ReferenceFinding,
} from "#/api/gen/types.gen";

export type DomainMode = "drop" | "suffix";

export interface ClonePlan {
	dryRun: boolean;
	services: ClonedService[];
	skipped: CloneSkipped[];
	domains: CloneDomainOutcome[];
	references: ReferenceFinding[];
	warnings: string[];
	links: number;
	sharedVariables: number;
}

export function toClonePlan(result: CloneResult): ClonePlan {
	return {
		dryRun: result.dryRun,
		services: result.services ?? [],
		skipped: result.skipped ?? [],
		domains: result.domains ?? [],
		references: result.references ?? [],
		warnings: result.warnings ?? [],
		links: result.links,
		sharedVariables: result.sharedVariables,
	};
}

/** Hostnames that are already taken: the clone would fail on them. */
export function conflictHosts(plan: ClonePlan): string[] {
	return plan.domains
		.filter((domain) => domain.action === "conflict")
		.map((domain) => domain.to ?? domain.hostname);
}

/** What happened to one domain, in a few words. */
export function domainOutcomeText(domain: CloneDomainOutcome): string {
	switch (domain.action) {
		case "dropped":
			return "not cloned";
		case "suffixed":
		case "generated":
			return domain.to ?? "";
		case "conflict":
			return `${domain.to ?? domain.hostname} is already in use`;
		default:
			return domain.reason ?? "skipped";
	}
}

/** The suffix the server would use when the field is left empty. */
export function defaultSuffix(
	sameEnvironment: boolean,
	environmentSlug: string,
): string {
	return sameEnvironment ? "-copy" : `-${environmentSlug}`;
}

/** The suffix as the server will read it: lowercase, one leading hyphen. */
export function normalizeSuffix(input: string): string {
	const trimmed = input.trim().toLowerCase();
	if (trimmed === "") return "";
	return trimmed.startsWith("-") ? trimmed : `-${trimmed}`;
}

/** `app.example.com` with `-copy` is `app-copy.example.com`; apex names cannot be suffixed. */
export function previewHostname(
	hostname: string,
	suffix: string,
): string | null {
	const labels = hostname.split(".");
	if (labels.length < 3) return null;
	labels[0] = `${labels[0]}${suffix}`;
	return labels.join(".");
}

/** A broken reference as one line: `api · DATABASE_URL → postgres.DATABASE_URL`. */
export function referenceText(finding: ReferenceFinding): string {
	return `${finding.service} · ${finding.key} → ${finding.ref}`;
}

export interface MovePlan {
	dryRun: boolean;
	applied: boolean;
	blocked: boolean;
	fromName: string;
	toName: string;
	slug: string;
	slugChanged: boolean;
	outgoing: ReferenceFinding[];
	incoming: Array<{ service: string; keys: string[] }>;
	links: string[];
	warnings: string[];
}

export function toMovePlan(result: MoveResult): MovePlan {
	return {
		dryRun: result.dryRun,
		applied: result.applied,
		blocked: result.blocked,
		fromName: result.from.name,
		toName: result.to.name,
		slug: result.slug,
		slugChanged: result.slugChanged,
		outgoing: result.outgoing ?? [],
		incoming: (result.incoming ?? []).map((ref: MoveIncomingRef) => ({
			service: ref.service,
			keys: ref.keys ?? [],
		})),
		links: result.links ?? [],
		warnings: result.warnings ?? [],
	};
}

export type MoveTone = "clean" | "notes" | "blocked";

/** The one line above the plan: can it just go ahead, or does it break something. */
export function moveVerdict(plan: MovePlan): { tone: MoveTone; text: string } {
	if (plan.blocked) {
		const broken = plan.outgoing.length + plan.incoming.length;
		return {
			tone: "blocked",
			text:
				broken === 1
					? "This move breaks one reference."
					: `This move breaks ${broken} references.`,
		};
	}
	if (plan.links.length > 0 || plan.slugChanged || plan.warnings.length > 0) {
		return { tone: "notes", text: "Nothing breaks. Review the notes below." };
	}
	return { tone: "clean", text: "Nothing breaks." };
}
