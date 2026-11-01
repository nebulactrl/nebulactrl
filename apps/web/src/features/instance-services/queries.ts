import { type QueryClient, queryOptions } from "@tanstack/react-query";
import {
	getInstanceServicesOptions,
	getTeamSliceOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { invalidateOperations } from "#/api/invalidate";

/** A service that is starting is re-read quickly; the event feed does not carry it. */
export const SERVICES_REFRESH_MS = 15_000;

/** Admin → Built-in services: the services, the default quota and every team's slices. */
export const instanceServicesQuery = () =>
	queryOptions({
		...getInstanceServicesOptions(),
		staleTime: 10_000,
		retry: false,
		refetchInterval: SERVICES_REFRESH_MS,
	});

export const INSTANCE_SERVICES_OPERATIONS = [
	"getInstanceServices",
	"getTeamSlice",
] as const;

export function invalidateInstanceServices(
	queryClient: QueryClient,
): Promise<void> {
	return invalidateOperations(queryClient, INSTANCE_SERVICES_OPERATIONS);
}

/** This team's share of the built-in services: used space and quota, never a key. */
export const teamSliceQuery = () =>
	queryOptions({
		...getTeamSliceOptions(),
		staleTime: 30_000,
		retry: false,
		refetchInterval: SERVICES_REFRESH_MS,
	});
