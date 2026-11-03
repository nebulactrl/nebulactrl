import {
	keepPreviousData,
	type QueryClient,
	queryOptions,
} from "@tanstack/react-query";
import { listDatabaseTablesOptions } from "#/api/gen/@tanstack/react-query.gen";
import { browseDatabaseTable } from "#/api/gen/sdk.gen";
import { invalidateOperations } from "#/api/invalidate";
import { type TableRef, toQueryResult, toTables } from "./model";

/**
 * The tables and views of a database. Fetched once per visit: a listing is a
 * request to the database through its agent, so it is not refetched on focus.
 */
export const dataTablesQuery = (serviceId: string) => ({
	...listDatabaseTablesOptions({ path: { serviceId } }),
	select: toTables,
	retry: false,
	staleTime: 60_000,
	refetchOnWindowFocus: false,
});

/** The operation id the browse queries carry, so `invalidateOperations` can reach them. */
const BROWSE_OPERATION = "browseDatabaseTable";

/**
 * One page of a table. Every fetch is audited, so a page is not refetched on
 * focus or on remount within a minute; moving to another page keeps the
 * previous one on screen until the next arrives.
 */
export const browseQuery = (
	serviceId: string,
	table: Pick<TableRef, "schema" | "name">,
	offset: number,
) =>
	queryOptions({
		queryKey: [
			{
				_id: BROWSE_OPERATION,
				path: { serviceId },
				body: { schema: table.schema, table: table.name, offset },
			},
		] as const,
		queryFn: async ({ signal }) => {
			const { data } = await browseDatabaseTable({
				path: { serviceId },
				body: { schema: table.schema, table: table.name, offset },
				signal,
				throwOnError: true,
			});
			return toQueryResult(data);
		},
		retry: false,
		staleTime: 60_000,
		refetchOnWindowFocus: false,
		placeholderData: keepPreviousData,
	});

/** After a statement that may have changed data: the table list and every cached page. */
export function invalidateData(queryClient: QueryClient): Promise<void> {
	return invalidateOperations(queryClient, [
		"listDatabaseTables",
		BROWSE_OPERATION,
	]);
}
