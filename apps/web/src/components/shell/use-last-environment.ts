import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { projectsQuery } from "#/api/hooks";
import { useCurrentTeam } from "#/features/teams/queries";
import {
	type EnvironmentRef,
	resolveSidebarEnvironment,
} from "./sidebar-environment";
import type { ProjectContext } from "./use-project-context";

/** The one global key older builds wrote; it belonged to no team. */
const LEGACY_KEY = "nebulactrl:last-environment";
const STORAGE_PREFIX = "nebulactrl:last-environment:";

export type { EnvironmentRef };

function storageKey(teamId: string): string {
	return `${STORAGE_PREFIX}${teamId}`;
}

function parse(raw: string | null): EnvironmentRef | undefined {
	if (!raw) return undefined;
	try {
		const value: unknown = JSON.parse(raw);
		if (
			typeof value === "object" &&
			value !== null &&
			"projectId" in value &&
			"envSlug" in value &&
			typeof value.projectId === "string" &&
			typeof value.envSlug === "string"
		) {
			return { projectId: value.projectId, envSlug: value.envSlug };
		}
	} catch {
		// An unreadable value is the same as none.
	}
	return undefined;
}

function read(teamId: string | undefined): EnvironmentRef | undefined {
	if (teamId === undefined) return undefined;
	try {
		window.localStorage.removeItem(LEGACY_KEY);
		return parse(window.localStorage.getItem(storageKey(teamId)));
	} catch {
		return undefined;
	}
}

/**
 * The environment the sidebar's project items point at: the one on screen,
 * or, on pages outside a project (home, servers, settings), the last one the
 * reader had open in this team. Undefined until they have opened one, and
 * when that project is gone or belongs to another team.
 */
export function useSidebarEnvironment(
	context: ProjectContext | undefined,
): EnvironmentRef | undefined {
	const { team } = useCurrentTeam();
	const teamId = team?.id;
	const projects = useQuery(projectsQuery());
	const [stored, setStored] = useState(() => ({
		teamId,
		ref: read(teamId),
	}));
	// A switch of team reads that team's own memory.
	const remembered = stored.teamId === teamId ? stored.ref : read(teamId);
	const projectId = context?.projectId;
	const envSlug = context?.envSlug;
	useEffect(() => {
		if (!projectId || !envSlug || teamId === undefined) return;
		setStored((previous) =>
			previous.teamId === teamId &&
			previous.ref?.projectId === projectId &&
			previous.ref.envSlug === envSlug
				? previous
				: { teamId, ref: { projectId, envSlug } },
		);
		try {
			window.localStorage.setItem(
				storageKey(teamId),
				JSON.stringify({ projectId, envSlug }),
			);
		} catch {
			// Private mode or a full quota: the choice lasts for this visit.
		}
	}, [teamId, projectId, envSlug]);
	const projectIds = projects.data
		? new Set(projects.data.map((project) => project.id))
		: undefined;
	return resolveSidebarEnvironment({ context, remembered, projectIds });
}
