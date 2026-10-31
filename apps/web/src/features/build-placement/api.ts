// The team's build settings over the generated SDK, mapped to the model the
// forms use (an empty registry id is "no registry").

import { queryOptions } from "@tanstack/react-query";
import { getTeamBuildSettings, putTeamBuildSettings } from "#/api/gen/sdk.gen";
import type { TeamSettings } from "#/api/gen/types.gen";
import type { TeamBuildLocation } from "./model";

/** The team's default: where builds run, and the registry they push to. */
export interface TeamBuildSettings {
	buildLocation: TeamBuildLocation;
	registryId: string | null;
}

const toSettings = (wire: TeamSettings): TeamBuildSettings => ({
	buildLocation: wire.buildLocation,
	registryId: wire.registryId === "" ? null : wire.registryId,
});

export const TEAM_BUILD_SETTINGS_KEY = ["team", "build-settings"] as const;

export const teamBuildSettingsQuery = () =>
	queryOptions({
		queryKey: TEAM_BUILD_SETTINGS_KEY,
		queryFn: async ({ signal }) => {
			const { data } = await getTeamBuildSettings({
				signal,
				throwOnError: true,
			});
			return toSettings(data);
		},
	});

export async function saveTeamBuildSettings(
	settings: TeamBuildSettings,
): Promise<TeamBuildSettings> {
	const { data } = await putTeamBuildSettings({
		body: {
			buildLocation: settings.buildLocation,
			...(settings.registryId ? { registryId: settings.registryId } : {}),
		},
		throwOnError: true,
	});
	return toSettings(data);
}
