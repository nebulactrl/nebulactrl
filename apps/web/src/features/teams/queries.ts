import {
	type QueryClient,
	queryOptions,
	useQuery,
} from "@tanstack/react-query";
import type { RegisteredRouter } from "@tanstack/react-router";
import {
	getInstanceOptions,
	listTeamsOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import type { InstanceSettings, Team } from "#/api/gen/types.gen";
import { projectsQuery } from "#/api/hooks";
import { isRole, type Role } from "./roles";
import { resetTeamScope } from "./team-scope";

/** The caller's teams. Changes only through the team endpoints and their events. */
export const teamsQuery = () =>
	queryOptions({
		...listTeamsOptions(),
		staleTime: 30_000,
		refetchInterval: false,
	});

/** Instance-wide settings (whether team creation is open, SMTP for admins). */
export const instanceQuery = () =>
	queryOptions({
		...getInstanceOptions(),
		staleTime: 60_000,
		refetchInterval: false,
	});

export interface CurrentTeam {
	teams: readonly Team[];
	/** The team the session acts in, once the list has loaded. */
	team: Team | undefined;
	role: Role | undefined;
	pending: boolean;
}

/** The teams of the signed-in user and the one the console is acting in. */
export function useCurrentTeam(): CurrentTeam {
	const query = useQuery(teamsQuery());
	const teams = query.data?.items ?? [];
	const team = teams.find((t) => t.current);
	return {
		teams,
		team,
		role: team && isRole(team.role) ? team.role : undefined,
		pending: query.isPending,
	};
}

/** Whether the caller may open a new team: allowed for all, or they run the instance. */
export function canCreateTeam(instance: InstanceSettings | undefined): boolean {
	return instance ? instance.allowTeamCreation || instance.canManage : false;
}

/**
 * Leaves the previous team's state behind after switching, leaving or
 * deleting a team: the screen waits on a loader while the cache empties and
 * the new team's teams and projects load (see `resetTeamScope`).
 */
export async function leaveTeamState(
	queryClient: QueryClient,
	router: RegisteredRouter,
) {
	await resetTeamScope(queryClient, router, (client) =>
		Promise.allSettled([
			client.fetchQuery(teamsQuery()),
			client.fetchQuery(projectsQuery()),
		]),
	);
}
