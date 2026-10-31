import type { ReactNode } from "react";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import {
	type ClonePlan,
	conflictHosts,
	domainOutcomeText,
	type MovePlan,
	moveVerdict,
	referenceText,
} from "./model";

function Section({ title, children }: { title: string; children: ReactNode }) {
	return (
		<section className="flex flex-col gap-1.5">
			<h3 className="font-medium text-ink-2 text-xs">{title}</h3>
			{children}
		</section>
	);
}

function Lines({ lines }: { lines: readonly ReactNode[] }) {
	return (
		<ul className="flex flex-col gap-1 font-mono text-[11.5px] text-ink-2 leading-snug">
			{lines.map((line, index) => (
				// Plan lines are static text in a fixed order for one render.
				// biome-ignore lint/suspicious/noArrayIndexKey: see above.
				<li key={index} className="break-words">
					{line}
				</li>
			))}
		</ul>
	);
}

/**
 * The result of a clone or its dry run: what would be (or was) created, what
 * happens to the domains, the references that do not resolve and the notes.
 * Every state is words first; nothing relies on colour.
 */
export function ClonePlanView({
	plan,
	showServices,
}: {
	plan: ClonePlan;
	/** An environment clone lists every service; a service clone has just one. */
	showServices: boolean;
}) {
	const conflicts = conflictHosts(plan);
	return (
		<div className="flex flex-col gap-3" aria-live="polite">
			{conflicts.length > 0 ? (
				<Alert variant="destructive">
					<AlertTitle>
						{conflicts.length === 1
							? "A hostname is already in use"
							: "Some hostnames are already in use"}
					</AlertTitle>
					<AlertDescription>
						{conflicts.join(", ")}. Choose another suffix, or clone without
						domains.
					</AlertDescription>
				</Alert>
			) : null}
			{showServices && plan.services.length > 0 ? (
				<Section title={`Services (${plan.services.length})`}>
					<Lines
						lines={plan.services.map(
							(service) =>
								`${service.slug} · ${service.kind}${
									service.fresh ? " · new, empty database" : ""
								}`,
						)}
					/>
				</Section>
			) : null}
			{plan.domains.length > 0 ? (
				<Section title="Domains">
					<Lines
						lines={plan.domains.map(
							(domain) => `${domain.hostname} → ${domainOutcomeText(domain)}`,
						)}
					/>
				</Section>
			) : null}
			{plan.references.length > 0 ? (
				<Alert variant="warning">
					<AlertTitle>References that do not resolve there</AlertTitle>
					<AlertDescription>
						<Lines
							lines={plan.references.map(
								(finding) => `${referenceText(finding)}: ${finding.message}`,
							)}
						/>
					</AlertDescription>
				</Alert>
			) : null}
			{plan.skipped.length > 0 ? (
				<Section title="Not cloned">
					<Lines
						lines={plan.skipped.map(
							(entry) => `${entry.slug}: ${entry.reason}`,
						)}
					/>
				</Section>
			) : null}
			{plan.links > 0 || plan.sharedVariables > 0 ? (
				<p className="type-secondary text-ink-3">
					{[
						plan.links > 0 ? `${plan.links} link(s)` : null,
						plan.sharedVariables > 0
							? `${plan.sharedVariables} shared variable(s)`
							: null,
					]
						.filter(Boolean)
						.join(" and ")}{" "}
					copied.
				</p>
			) : null}
			{plan.warnings.map((warning) => (
				<p key={warning} className="type-secondary text-ink-3">
					{warning}
				</p>
			))}
		</div>
	);
}

/** The plan of a move: broken references, removed links, a new slug. */
export function MovePlanView({ plan }: { plan: MovePlan }) {
	const verdict = moveVerdict(plan);
	return (
		<div className="flex flex-col gap-3" aria-live="polite">
			<Alert
				variant={
					verdict.tone === "blocked"
						? "warning"
						: verdict.tone === "notes"
							? "info"
							: "success"
				}
			>
				<AlertTitle>{verdict.text}</AlertTitle>
				{plan.blocked ? (
					<AlertDescription>
						Move it anyway and fix them afterwards, or cancel and change the
						variables first. The services that hold a broken reference fail
						their next deploy until it is fixed.
					</AlertDescription>
				) : null}
			</Alert>
			{plan.outgoing.length > 0 ? (
				<Section title="Variables of this service">
					<Lines
						lines={plan.outgoing.map(
							(finding) => `${referenceText(finding)}: ${finding.message}`,
						)}
					/>
				</Section>
			) : null}
			{plan.incoming.length > 0 ? (
				<Section title="Other services that reference it">
					<Lines
						lines={plan.incoming.map(
							(ref) => `${ref.service} · ${ref.keys.join(", ")}`,
						)}
					/>
				</Section>
			) : null}
			{plan.links.length > 0 ? (
				<Section title="Links removed">
					<Lines lines={plan.links} />
				</Section>
			) : null}
			{plan.slugChanged ? (
				<p className="type-secondary text-ink-3">
					{plan.toName} already has a service with this name: it becomes{" "}
					<span className="font-mono">{plan.slug}</span>.
				</p>
			) : null}
			{plan.warnings.map((warning) => (
				<p key={warning} className="type-secondary text-ink-3">
					{warning}
				</p>
			))}
		</div>
	);
}
