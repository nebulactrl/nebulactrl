import { useMemo, useState } from "react";
import type { Project } from "#/api/gen/types.gen";
import { CheckLabel } from "#/components/ui/check-label";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import { parseQuery } from "../logs/search/query";
import type { SetDraft } from "./kind-fields";
import type { Draft } from "./model";
import type { TeamService } from "./use-team-services";

type Scope = "all" | "projects";

/** Services this many or more get a filter box above the list. */
const FILTER_FROM = 8;

/**
 * Which logs the drain takes: every project or chosen ones, optionally only
 * some services, optionally only lines that match a search query (the same
 * language as the log views).
 */
export function ScopeFields({
	draft,
	set,
	projects,
	projectsPending,
	services,
	servicesPending,
}: {
	draft: Draft;
	set: SetDraft;
	projects: readonly Pick<Project, "id" | "name">[];
	projectsPending: boolean;
	services: readonly TeamService[];
	servicesPending: boolean;
}) {
	const [filter, setFilter] = useState("");
	const scope: Scope = draft.allProjects ? "all" : "projects";
	const inScope = useMemo(
		() =>
			services.filter(
				(s) => draft.allProjects || draft.projectIds.includes(s.projectId),
			),
		[services, draft.allProjects, draft.projectIds],
	);
	const shown = useMemo(() => {
		const needle = filter.trim().toLowerCase();
		return needle === ""
			? inScope
			: inScope.filter((s) =>
					`${s.name} ${s.projectName} ${s.environmentName}`
						.toLowerCase()
						.includes(needle),
				);
	}, [inScope, filter]);
	const queryError =
		draft.query.trim() === "" ? undefined : parseQuery(draft.query).errors[0];

	const toggle = (list: readonly string[], id: string, on: boolean) =>
		on ? [...list, id] : list.filter((x) => x !== id);

	return (
		<div className="flex flex-col gap-4">
			<Field>
				<FieldLabel id="drain-scope-label">Projects</FieldLabel>
				<div>
					<Segmented<Scope>
						aria-label="Which projects"
						font="sans"
						density="regular"
						value={scope}
						onValueChange={(next) => set("allProjects", next === "all")}
						options={[
							{ value: "all", label: "All projects" },
							{ value: "projects", label: "Selected projects" },
						]}
					/>
				</div>
				{draft.allProjects ? (
					<FieldDescription>
						Every project of the team, including ones created later.
					</FieldDescription>
				) : (
					<fieldset
						aria-labelledby="drain-scope-label"
						className="surface-well m-0 flex max-h-36 min-w-0 flex-col gap-2 overflow-y-auto overscroll-contain rounded-card p-3"
					>
						{projects.length > 0 ? (
							projects.map((p) => (
								<CheckLabel
									key={p.id}
									className="gap-2.5 text-sm"
									checked={draft.projectIds.includes(p.id)}
									onCheckedChange={(on) =>
										set("projectIds", toggle(draft.projectIds, p.id, on))
									}
								>
									{p.name}
								</CheckLabel>
							))
						) : (
							<span className="text-ink-3 text-xs">
								{projectsPending ? "Loading projects…" : "No projects yet."}
							</span>
						)}
					</fieldset>
				)}
			</Field>

			<Field>
				<FieldLabel id="drain-services-label">
					Only these services (optional)
				</FieldLabel>
				{inScope.length >= FILTER_FROM ? (
					<Input
						aria-label="Filter services"
						type="search"
						autoComplete="off"
						placeholder="Filter services"
						value={filter}
						onChange={(e) => setFilter(e.target.value)}
					/>
				) : null}
				<fieldset
					aria-labelledby="drain-services-label"
					className="surface-well m-0 flex max-h-44 min-w-0 flex-col gap-2 overflow-y-auto overscroll-contain rounded-card p-3"
				>
					{shown.length > 0 ? (
						shown.map((s) => (
							<CheckLabel
								key={s.id}
								className="gap-2.5 text-sm"
								checked={draft.serviceIds.includes(s.id)}
								onCheckedChange={(on) =>
									set("serviceIds", toggle(draft.serviceIds, s.id, on))
								}
							>
								{s.name}
								<span className="ml-2 text-ink-4 text-xs">
									{s.projectName} · {s.environmentName}
								</span>
							</CheckLabel>
						))
					) : (
						<span className="text-ink-3 text-xs">
							{servicesPending
								? "Loading services…"
								: filter.trim() !== ""
									? "No service matches the filter."
									: "No services in this scope yet."}
						</span>
					)}
				</fieldset>
				<FieldDescription>
					Leave every box unchecked to ship all services in the scope.
				</FieldDescription>
			</Field>

			<Field>
				<FieldLabel htmlFor="drain-query">Query (optional)</FieldLabel>
				<Input
					id="drain-query"
					autoComplete="off"
					spellCheck={false}
					className="font-mono"
					placeholder='level:error  OR  "payment failed"  -service:web'
					aria-invalid={queryError !== undefined}
					value={draft.query}
					onChange={(e) => set("query", e.target.value)}
				/>
				<FieldDescription>
					Only lines matching this query are shipped. It is the search of the
					log views: words, "phrases", level:error, -service:web, @status:[400
					TO 499].
				</FieldDescription>
				{queryError ? <FieldError>{queryError.message}</FieldError> : null}
			</Field>
		</div>
	);
}
