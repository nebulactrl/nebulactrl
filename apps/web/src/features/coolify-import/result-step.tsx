import type { ReactNode } from "react";
import type { CoolifyApplyResult } from "#/api/gen/types.gen";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { FollowupList } from "./followups";
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
 * Step three: what the import made. The projects and services it created, the
 * server's warnings, and the manual steps that remain, with their commands
 * ready to copy.
 */
export function ResultStep({
	result,
	deployed,
}: {
	result: CoolifyApplyResult;
	deployed: boolean;
}) {
	const projects = result.projects ?? [];
	const services = result.services ?? [];
	const warnings = result.warnings ?? [];
	const followups = result.followups ?? [];

	return (
		<div className="flex flex-col gap-6">
			<div className="flex flex-col gap-1">
				<p
					className="m-0 font-medium text-[13px] text-ink-1"
					role="status"
					aria-live="polite"
				>
					Imported: {resultText(result)}.
				</p>
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					{deployed
						? "The services are deploying. Follow them on each project's canvas."
						: "Everything is staged and nothing runs yet. Review the services, then deploy from the staged bar."}
				</p>
			</div>

			{warnings.length > 0 ? (
				<Alert variant="warning" role="note">
					<AlertTitle>
						{warnings.length === 1 ? "A note" : `${warnings.length} notes`}{" "}
						about this import
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

			<Section title="Projects" aside={`${projects.length}`}>
				<ul className="m-0 flex list-none flex-col gap-2 p-0">
					{projects.map((project) => (
						<li
							key={project.id}
							className="surface-well flex flex-col gap-1.5 rounded-ctl border border-line px-3 py-2"
						>
							<span className="flex flex-wrap items-center gap-x-2 gap-y-1">
								<span className="font-medium text-[13px] text-ink-1">
									{project.name}
								</span>
								<span className="font-mono text-2xs text-ink-3">
									{project.slug}
								</span>
								<Badge variant={project.created ? "accent" : "default"}>
									{project.created ? "created" : "merged into"}
								</Badge>
							</span>
							<span className="flex flex-wrap items-center gap-1.5">
								{(project.environments ?? []).map((environment) => (
									<Badge key={environment.id} variant="outline">
										{environment.name}
										{environment.created ? " · new" : " · reused"}
									</Badge>
								))}
							</span>
						</li>
					))}
				</ul>
			</Section>

			<Section title="Services" aside={`${services.length}`}>
				{services.length === 0 ? (
					<p className="m-0 text-[12px] text-ink-3">No service was created.</p>
				) : (
					<ul className="m-0 flex list-none flex-col gap-1 p-0">
						{services.map((service) => (
							<li
								key={service.id}
								className="surface-well flex flex-wrap items-center gap-x-2 gap-y-1 rounded-ctl border border-line px-3 py-1.5"
							>
								<span className="font-medium text-[13px] text-ink-1">
									{service.name}
								</span>
								<span className="font-mono text-2xs text-ink-3">
									{service.projectSlug}/{service.envSlug}/{service.slug}
								</span>
								<Badge>{service.kind}</Badge>
							</li>
						))}
					</ul>
				)}
			</Section>

			{followups.length > 0 ? (
				<Section title="Still to do" aside={`${followups.length}`}>
					<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
						Data is never copied. Run these when you are ready.
					</p>
					<FollowupList followups={followups} />
				</Section>
			) : null}
		</div>
	);
}
