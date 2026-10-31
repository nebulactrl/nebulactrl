import type { QueryClient } from "@tanstack/react-query";
import {
	getDatabaseOptions,
	listDatabaseEnginesOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { invalidateOperations, invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";

/** A service with a managed database spec (not a plain custom database image). */
export function isManagedDatabase(
	service: Pick<Service, "kind" | "config">,
): boolean {
	return service.kind === "database" && service.config.database !== undefined;
}

/** The catalog changes only with a release of the control plane. */
const CATALOG_STALE_MS = 10 * 60_000;

export const enginesQuery = () => ({
	...listDatabaseEnginesOptions(),
	staleTime: CATALOG_STALE_MS,
});

/** The managed-database view of a service: spec, exports, info, rotation, upgrade. */
export const databaseQuery = (serviceId: string) =>
	getDatabaseOptions({ path: { serviceId } });

/** How often the view is refetched while a rotation, an upgrade or a refresh is in flight. */
export const DATABASE_POLL_MS = 3_000;

/** Refetches every database view on screen. */
export function invalidateDatabase(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, ["getDatabase"]);
}

/** After a change that stages services: the database view and everything that shows a service. */
export async function invalidateDatabaseAndServices(
	queryClient: QueryClient,
): Promise<void> {
	await Promise.all([
		invalidateDatabase(queryClient),
		invalidateServices(queryClient),
	]);
}
