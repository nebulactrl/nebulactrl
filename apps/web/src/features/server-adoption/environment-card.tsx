import { useId } from "react";
import type {
	AdoptionChoices,
	AdoptionEnvironment,
	Project,
} from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Switch } from "#/components/ui/switch";
import { patchEnvironment, projectModeOf } from "./lib";
import { ServiceRow } from "./service-row";

const NEW_PROJECT = "new";

/**
 * One environment found on the host: the project it goes into (an existing
 * one, or a new one that others can share by its name), its name, whether it
 * is production, and the services in it. An environment this instance already
 * has keeps its project and name.
 */
export function EnvironmentCard({
	environment,
	choices,
	existingProjects,
	onChoices,
	locked,
}: {
	environment: AdoptionEnvironment;
	choices: AdoptionChoices;
	existingProjects: readonly Project[];
	onChoices(update: (current: AdoptionChoices) => AdoptionChoices): void;
	/** An apply is in flight: every control is off. */
	locked: boolean;
}) {
	const id = useId();
	const choice = choices.environments?.[environment.id];
	const fixed = environment.action === "existing";
	const mode = projectModeOf(environment, choice);
	const projectId = choice?.projectId ?? "";
	const patch = (change: Parameters<typeof patchEnvironment>[2]) =>
		onChoices((current) => patchEnvironment(current, environment.id, change));
	const services = environment.services ?? [];
	const name = choice?.name ?? environment.name;
	const production = choice?.production ?? environment.production;

	return (
		<li
			data-slot="adoption-environment-card"
			data-action={environment.action}
			className="surface-raised flex flex-col gap-3 rounded-card border border-line px-4 py-3.5"
		>
			<section
				aria-label={`Environment ${environment.name}`}
				className="flex flex-col gap-3"
			>
				{fixed ? (
					<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
						<h3 className="type-label m-0">Environment</h3>
						<span className="font-medium text-[14px] text-ink-1">
							{environment.name}
						</span>
						<span className="font-mono text-2xs text-ink-3">
							{environment.projectName}/{environment.slug}
						</span>
						<Badge>already here</Badge>
						{environment.production ? (
							<Badge variant="accent">production</Badge>
						) : null}
					</div>
				) : (
					<div className="flex flex-wrap items-end gap-3">
						<div className="flex w-56 max-w-full flex-col gap-1">
							<span id={`${id}-into`} className="type-label">
								Adopt into
							</span>
							<Select
								disabled={locked}
								value={mode === "new" ? NEW_PROJECT : projectId}
								items={[
									{ value: NEW_PROJECT, label: "A new project" },
									...existingProjects.map((p) => ({
										value: p.id,
										label: p.name,
									})),
								]}
								onValueChange={(next) => {
									const value = String(next ?? NEW_PROJECT);
									patch(
										value === NEW_PROJECT
											? { projectId: undefined }
											: { projectId: value, projectName: undefined },
									);
								}}
							>
								<SelectTrigger
									aria-labelledby={`${id}-into`}
									size="sm"
									className="w-full"
								>
									<SelectValue />
								</SelectTrigger>
								<SelectContent alignItemWithTrigger={false}>
									<SelectItem value={NEW_PROJECT}>A new project</SelectItem>
									{existingProjects.map((p) => (
										<SelectItem key={p.id} value={p.id}>
											{p.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
						{mode === "new" ? (
							<div className="flex min-w-48 flex-1 flex-col gap-1">
								<label htmlFor={`${id}-project`} className="type-label">
									New project name
								</label>
								<Input
									id={`${id}-project`}
									autoComplete="off"
									spellCheck={false}
									value={choice?.projectName ?? environment.projectName}
									disabled={locked}
									onChange={(event) =>
										patch({ projectName: event.target.value })
									}
									className="h-7 max-w-sm"
								/>
							</div>
						) : null}
						<div className="flex min-w-40 flex-1 flex-col gap-1">
							<label htmlFor={`${id}-name`} className="type-label">
								Environment name
							</label>
							<Input
								id={`${id}-name`}
								autoComplete="off"
								spellCheck={false}
								value={name}
								disabled={locked}
								onChange={(event) => patch({ name: event.target.value })}
								className="h-7 max-w-64"
							/>
						</div>
						<label
							htmlFor={`${id}-production`}
							className="flex items-center gap-2 pb-1 text-[12px] text-ink-2"
						>
							<Switch
								id={`${id}-production`}
								size="sm"
								checked={production}
								disabled={locked}
								onCheckedChange={(next) => patch({ production: next })}
							/>
							Production
						</label>
					</div>
				)}

				{services.length === 0 ? (
					<p className="m-0 text-[12px] text-ink-3">
						No service of this environment runs on this host.
					</p>
				) : (
					<ul className="m-0 flex list-none flex-col gap-2 p-0">
						{services.map((service) => (
							<ServiceRow
								key={service.id}
								service={service}
								choices={choices}
								onChoices={onChoices}
								locked={locked}
							/>
						))}
					</ul>
				)}
			</section>
		</li>
	);
}
