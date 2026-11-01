import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { ErrorState } from "#/components/common/error-state";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import {
	saveTeamBuildSettings,
	TEAM_BUILD_SETTINGS_KEY,
	type TeamBuildSettings,
	teamBuildSettingsQuery,
} from "./api";
import { BuildSettingsCard } from "./build-settings-card";
import { LocationControl, type LocationValue } from "./location-control";
import { resolveBuild, type TeamBuildLocation } from "./model";

const toValue = (settings: TeamBuildSettings): LocationValue => ({
	location: settings.buildLocation,
	buildServerId: "",
	registryId: settings.registryId ?? "",
});

/** A team always has a concrete location; the control never offers Inherit here. */
const concrete = (value: LocationValue): TeamBuildLocation =>
	value.location === "build_server" ? "build_server" : "target";

/**
 * Team settings → Build location: the team's default for where builds run and which
 * registry they use. A project or a service can override it. Saving changes
 * nothing already deployed; the next deployment reads the new default.
 */
export function TeamBuildsSection() {
	const queryClient = useQueryClient();
	const settings = useQuery(teamBuildSettingsQuery());
	const [draft, setDraft] = useState<LocationValue | null>(null);

	// The form is seeded once from the load; a save writes it back itself.
	useEffect(() => {
		if (settings.data) setDraft((current) => current ?? toValue(settings.data));
	}, [settings.data]);

	const save = useMutation({
		mutationFn: (next: LocationValue) =>
			saveTeamBuildSettings({
				buildLocation: concrete(next),
				registryId: next.registryId === "" ? null : next.registryId,
			}),
		onSuccess: (saved) => {
			queryClient.setQueryData(TEAM_BUILD_SETTINGS_KEY, saved);
			setDraft(toValue(saved));
			toast.success(
				"Build settings saved",
				"Projects and services that inherit them use the new default from their next deployment.",
			);
		},
	});

	if (settings.isError && !settings.data) {
		return (
			<ErrorState
				frame="raised"
				title="Build settings did not load"
				body="Build settings are for team admins. Check your role and session and try again; nothing was changed."
				retry={() => void settings.refetch()}
			/>
		);
	}
	if (!settings.data || draft === null) {
		return <Skeleton className="h-48 rounded-card" />;
	}

	const baseline = toValue(settings.data);
	const dirty =
		draft.location !== baseline.location ||
		draft.registryId !== baseline.registryId;

	return (
		<BuildSettingsCard
			dirty={dirty}
			saving={save.isPending}
			error={save.isError ? errorMessage(save.error) : null}
			onSave={() => save.mutate(draft)}
			onDiscard={() => setDraft(baseline)}
		>
			<LocationControl
				scope="team"
				idPrefix="team-build"
				value={draft}
				onChange={setDraft}
				effective={resolveBuild({
					team: {
						buildLocation: concrete(draft),
						registryId: draft.registryId === "" ? null : draft.registryId,
					},
				})}
				registryParentLabel={null}
			/>
		</BuildSettingsCard>
	);
}
