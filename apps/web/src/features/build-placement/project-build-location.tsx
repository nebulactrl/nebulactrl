import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { updateProject } from "#/api/gen/sdk.gen";
import { projectsQuery } from "#/api/hooks";
import type { Project } from "#/api/models";
import { ErrorState } from "#/components/common/error-state";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { useRegistries } from "#/features/registries/queries";
import { teamBuildSettingsQuery } from "./api";
import { BuildSettingsCard } from "./build-settings-card";
import { LocationControl, type LocationValue } from "./location-control";
import { type BuildLocation, resolveBuild } from "./model";

const toValue = (project: Project): LocationValue => ({
	location: (project.buildLocation ?? "inherit") as BuildLocation,
	buildServerId: project.buildServerId ?? "",
	registryId: project.registryId ?? "",
});

const same = (a: LocationValue, b: LocationValue) =>
	a.location === b.location &&
	a.buildServerId === b.buildServerId &&
	a.registryId === b.registryId;

function BuildsCard({ project }: { project: Project }) {
	const queryClient = useQueryClient();
	const team = useQuery(teamBuildSettingsQuery());
	const registries = useRegistries();
	const baseline = toValue(project);
	const [draft, setDraft] = useState<LocationValue>(baseline);

	// Someone else's save (or ours, once refetched) replaces the form only while it is clean.
	const key = JSON.stringify(baseline);
	const [seen, setSeen] = useState(key);
	useEffect(() => {
		if (seen === key) return;
		setDraft((current) =>
			JSON.stringify(current) === seen ? JSON.parse(key) : current,
		);
		setSeen(key);
	}, [key, seen]);

	const save = useMutation({
		mutationFn: (next: LocationValue) =>
			updateProject({
				path: { projectId: project.id },
				body: {
					buildLocation: next.location,
					// The placement is replaced as a unit: an omitted id is cleared.
					...(next.buildServerId === ""
						? {}
						: { buildServerId: next.buildServerId }),
					...(next.registryId === "" ? {} : { registryId: next.registryId }),
				},
				throwOnError: true,
			}),
		onSuccess: async () => {
			toast.success(
				"Project build settings saved",
				"Services that inherit them use the new settings from their next deployment.",
			);
			await queryClient.invalidateQueries({
				queryKey: projectsQuery().queryKey,
			});
		},
	});

	const teamSettings = team.data ?? {
		buildLocation: "target" as const,
		registryId: null,
	};
	const teamRegistry = registries.data?.find(
		(registry) => registry.id === teamSettings.registryId,
	);
	const registryParentLabel = teamRegistry
		? `team default: ${teamRegistry.name}`
		: "team default: none";

	return (
		<BuildSettingsCard
			dirty={!same(draft, baseline)}
			saving={save.isPending}
			error={save.isError ? errorMessage(save.error) : null}
			onSave={() => save.mutate(draft)}
			onDiscard={() => setDraft(baseline)}
		>
			<LocationControl
				scope="project"
				idPrefix="project-build"
				value={draft}
				onChange={setDraft}
				effective={resolveBuild({
					project: {
						buildLocation: draft.location,
						buildServerId: draft.buildServerId,
						registryId: draft.registryId,
					},
					team: teamSettings,
				})}
				registryParentLabel={registryParentLabel}
			/>
		</BuildSettingsCard>
	);
}

/**
 * Where this project's git services are built, and which registry holds the
 * images. The project settings page gives it its heading; this is the control
 * and its own save bar.
 */
export function ProjectBuildLocation({ projectId }: { projectId: string }) {
	const projects = useQuery(projectsQuery());
	return (
		<SkeletonReveal
			loading={projects.data === undefined && !projects.isError}
			skeleton={<Skeleton className="h-64 rounded-card" />}
		>
			{() => {
				if (projects.data === undefined) {
					return (
						<ErrorState
							frame="raised"
							title="Couldn't load the project"
							body={errorMessage(projects.error)}
							retry={() => void projects.refetch()}
						/>
					);
				}
				const project = projects.data.find(
					(candidate) => candidate.id === projectId,
				);
				if (project === undefined) {
					return (
						<ErrorState
							frame="raised"
							title="Project not found"
							body="This project does not exist, or you cannot see it."
						/>
					);
				}
				return <BuildsCard project={project} />;
			}}
		</SkeletonReveal>
	);
}
