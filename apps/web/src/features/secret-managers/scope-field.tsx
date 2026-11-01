import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { Project } from "#/api/gen/types.gen";
import { environmentsQuery, projectsQuery } from "#/api/hooks";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Segmented } from "#/components/ui/segmented";
import {
	type Grant,
	type IntegrationScope,
	isEnvironmentGranted,
	isProjectGranted,
	toggleEnvironment,
	toggleProject,
} from "./model";

/**
 * Who may reference the integration: the whole team, or only the selected
 * projects (every environment, also later ones) and single environments. The
 * server refuses an out-of-scope reference at plan and deploy time, naming it.
 */
export function ScopeField({
	scope,
	grants,
	error,
	onChange,
}: {
	scope: IntegrationScope;
	grants: Grant[];
	error?: string | undefined;
	onChange: (next: { scope: IntegrationScope; grants: Grant[] }) => void;
}) {
	const projects = useQuery({
		...projectsQuery(),
		enabled: scope === "selected",
	});
	const visible = (projects.data ?? []).filter((p) => !p.system);
	return (
		<Field>
			<FieldLabel id="sm-scope-label">Available to</FieldLabel>
			<div>
				<Segmented<IntegrationScope>
					aria-label="Which projects may use this integration"
					font="sans"
					density="regular"
					value={scope}
					onValueChange={(next) => onChange({ scope: next, grants })}
					options={[
						{ value: "team", label: "Whole team" },
						{ value: "selected", label: "Selected projects" },
					]}
				/>
			</div>
			{scope === "team" ? (
				<FieldDescription>
					Any variable of any project may reference it. Anyone who can write
					variables can read what its credential can read.
				</FieldDescription>
			) : (
				<>
					<fieldset
						aria-labelledby="sm-scope-label"
						className="surface-well m-0 flex max-h-56 min-w-0 flex-col gap-2 overflow-y-auto overscroll-contain rounded-card p-3"
					>
						{visible.length > 0 ? (
							visible.map((p) => (
								<ProjectRow
									key={p.id}
									project={p}
									grants={grants}
									onGrants={(next) => onChange({ scope, grants: next })}
								/>
							))
						) : (
							<span className="text-ink-3 text-xs">
								{projects.isPending ? "Loading projects…" : "No projects yet."}
							</span>
						)}
					</fieldset>
					{error ? (
						<FieldError>{error}</FieldError>
					) : (
						<FieldDescription>
							A whole project also covers environments created later, such as
							previews. A reference from anywhere else is refused with an error
							that names it.
						</FieldDescription>
					)}
				</>
			)}
		</Field>
	);
}

function ProjectRow({
	project,
	grants,
	onGrants,
}: {
	project: Project;
	grants: Grant[];
	onGrants: (next: Grant[]) => void;
}) {
	const whole = isProjectGranted(grants, project.id);
	const some = grants.some((g) => g.projectId === project.id);
	const [open, setOpen] = useState(some && !whole);
	const environments = useQuery({
		...environmentsQuery(project.id),
		enabled: open,
	});
	const standard = (environments.data ?? []).filter(
		(e) => e.kind === "standard",
	);
	const ids = standard.map((e) => e.id);
	return (
		<div className="flex flex-col gap-1.5">
			<div className="flex items-center gap-2">
				<CheckLabel
					className="flex-1 gap-2.5 text-sm"
					checked={whole}
					onCheckedChange={(on) =>
						onGrants(toggleProject(grants, project.id, on))
					}
				>
					{project.name}
					{some && !whole ? (
						<span className="ml-2 text-ink-3 text-xs">some environments</span>
					) : null}
				</CheckLabel>
				<Button
					type="button"
					variant="ghost"
					size="sm"
					aria-expanded={open}
					onClick={() => setOpen((v) => !v)}
				>
					{open ? "Hide environments" : "Environments"}
				</Button>
			</div>
			{open ? (
				<div className="ml-7 flex flex-col gap-1.5">
					{standard.length > 0 ? (
						standard.map((e) => (
							<CheckLabel
								key={e.id}
								className="gap-2.5 text-sm"
								checked={isEnvironmentGranted(grants, project.id, e.id)}
								onCheckedChange={(on) =>
									onGrants(toggleEnvironment(grants, project.id, e.id, on, ids))
								}
							>
								{e.name}
							</CheckLabel>
						))
					) : (
						<span className="text-ink-3 text-xs">
							{environments.isPending
								? "Loading environments…"
								: "No environments."}
						</span>
					)}
				</div>
			) : null}
		</div>
	);
}
