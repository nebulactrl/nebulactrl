import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import type { AdoptionResult } from "#/api/gen/types.gen";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { resultText } from "./lib";

function Section({
	title,
	aside,
	children,
}: {
	title: string;
	aside?: string;
	children: ReactNode;
}) {
	return (
		<section aria-label={title} className="flex flex-col gap-2.5">
			<div className="flex items-baseline gap-2">
				<h3 className="type-section-title m-0">{title}</h3>
				{aside ? (
					<span className="font-mono text-2xs text-ink-3 tabular-nums">
						{aside}
					</span>
				) : null}
			</div>
			{children}
		</section>
	);
}

/**
 * Step two: what the adoption did. The counts, the projects it made and a link
 * to each adopted service, and the server's warnings.
 */
export function ResultStep({
	result,
	onNavigate,
}: {
	result: AdoptionResult;
	/** Called when a link is followed, so the dialog can close. */
	onNavigate?: () => void;
}) {
	const projects = result.projects ?? [];
	const services = result.services ?? [];
	const warnings = result.warnings ?? [];

	return (
		<div className="flex flex-col gap-6">
			<div className="flex flex-col gap-1">
				<p
					className="m-0 font-medium text-[13px] text-ink-1"
					role="status"
					aria-live="polite"
				>
					{resultText(result)}
				</p>
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					{services.length > 0
						? "The adopted services were not restarted. The next deploy replaces their containers as usual."
						: "Nothing was adopted."}
					{result.removed > 0
						? " The containers of the services you removed are removed shortly; their volumes stay on the host."
						: ""}
					{result.left > 0
						? " The services you left keep running, unmanaged."
						: ""}
				</p>
			</div>

			{warnings.length > 0 ? (
				<Alert variant="warning" role="note">
					<AlertTitle>
						{warnings.length === 1 ? "A note" : `${warnings.length} notes`}{" "}
						about this adoption
					</AlertTitle>
					<AlertDescription>
						<ul className="m-0 flex list-disc flex-col gap-1 pl-4">
							{warnings.map((warning) => (
								<li key={warning}>{warning}</li>
							))}
						</ul>
					</AlertDescription>
				</Alert>
			) : null}

			{projects.length > 0 ? (
				<Section title="New projects" aside={`${projects.length}`}>
					<ul className="m-0 flex list-none flex-col gap-1 p-0">
						{projects.map((project) => (
							<li
								key={project.id}
								className="surface-well flex flex-wrap items-center gap-x-2 gap-y-1 rounded-ctl border border-line px-3 py-1.5"
							>
								<span className="font-medium text-[13px] text-ink-1">
									{project.name}
								</span>
								<span className="font-mono text-2xs text-ink-3">
									{project.slug}
								</span>
							</li>
						))}
					</ul>
				</Section>
			) : null}

			<Section title="Services" aside={`${services.length}`}>
				{services.length === 0 ? (
					<p className="m-0 text-[12px] text-ink-3">No service was adopted.</p>
				) : (
					<ul className="m-0 flex list-none flex-col gap-1 p-0">
						{services.map((service) => (
							<li
								key={service.id}
								className="surface-well flex flex-wrap items-center gap-x-2 gap-y-1 rounded-ctl border border-line px-3 py-1.5"
							>
								<Link
									to="/p/$projectId/$envSlug"
									params={{
										projectId: service.projectId,
										envSlug: service.envSlug,
									}}
									search={{ service: service.id }}
									onClick={onNavigate}
									className="rounded-sm font-medium text-[13px] text-ink-1 underline underline-offset-2 outline-none focus-visible:focus-ring"
								>
									{service.name}
								</Link>
								<span className="font-mono text-2xs text-ink-3">
									{service.projectSlug}/{service.envSlug}/{service.slug}
								</span>
								<Badge variant="success">deployed</Badge>
							</li>
						))}
					</ul>
				)}
			</Section>
		</div>
	);
}
