import type { QueryClient } from "@tanstack/react-query";
import type { RegisteredRouter } from "@tanstack/react-router";
import { Fragment, type ReactNode, useSyncExternalStore } from "react";
import { Loading } from "#/components/common/loading";

/**
 * Which team the console's pages belong to. A switch, a leave or a delete
 * changes the team every query was asked in, so the pages built on the old
 * one must go before the new one's data arrives: the screen is replaced by a
 * loader (`switching`) and, once the new team's lists are in, mounted afresh
 * (`epoch`), so no observer still holds the previous team's rows.
 */
interface TeamScope {
	epoch: number;
	switching: boolean;
}

let scope: TeamScope = { epoch: 0, switching: false };
const listeners = new Set<() => void>();

function publish(next: TeamScope) {
	scope = next;
	for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
	listeners.add(listener);
	return () => listeners.delete(listener);
}

export function getTeamScope(): TeamScope {
	return scope;
}

/** Whether a team change is under way, for chrome that shows team data. */
export function useTeamScope(): TeamScope {
	return useSyncExternalStore(subscribe, getTeamScope, getTeamScope);
}

/**
 * Leaves the previous team's state behind: suspends the screen, empties the
 * cache, reloads the routes, returns to the projects home and waits for the
 * new team's lists (`warm`) before the screen mounts again.
 */
export async function resetTeamScope(
	queryClient: QueryClient,
	router: RegisteredRouter,
	warm: (client: QueryClient) => Promise<unknown>,
): Promise<void> {
	publish({ epoch: scope.epoch + 1, switching: true });
	try {
		await queryClient.cancelQueries();
		queryClient.clear();
		await router.invalidate();
		await router.navigate({ to: "/" });
		await warm(queryClient);
	} finally {
		publish({ ...scope, switching: false });
	}
}

/**
 * Wraps the routed screen. While a team change is under way it shows a
 * loader, never the old team's page; afterwards the page mounts fresh.
 */
export function TeamScopeBoundary({ children }: { children: ReactNode }) {
	const current = useTeamScope();
	if (current.switching) {
		return <Loading label="Switching team" delayMs={0} />;
	}
	return <Fragment key={current.epoch}>{children}</Fragment>;
}
