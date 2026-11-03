// The Data tab's models: what the control plane answers for the table list, a
// page of a table and a SQL run, with the wire's nullable arrays made plain.

import { errorCode } from "#/api/client-config";
import type { DataResult, DataTables } from "#/api/gen/types.gen";

/** Rows per page of the table browser; the control plane serves exactly this many. */
export const PAGE_SIZE = 100;

interface DataColumn {
	name: string;
	type: string;
}

/** One statement's result. A `null` cell is SQL NULL; clipped text ends with an ellipsis. */
export interface ResultSet {
	columns: DataColumn[];
	rows: (string | null)[][];
	rowCount: number;
	/** More rows existed than came back. */
	truncated: boolean;
	/** The server's completion tag of a statement without rows, e.g. "INSERT 0 1". */
	commandTag: string;
}

export interface QueryResult {
	sets: ResultSet[];
	durationMs: number;
	/** A cell was longer than the per-cell limit and is clipped. */
	truncatedCells: boolean;
}

type TableKind = "table" | "view" | "other";

export interface TableRef {
	schema: string;
	name: string;
	kind: TableKind;
	/** The engine's own estimate; absent when it has none. */
	rowEstimate: number | undefined;
}

export function toQueryResult(wire: DataResult): QueryResult {
	return {
		durationMs: wire.durationMs,
		truncatedCells: wire.truncatedCells ?? false,
		sets: (wire.results ?? []).map((set) => ({
			columns: set.columns ?? [],
			rows: (set.rows ?? []).map((row) => row ?? []),
			rowCount: set.rowCount,
			truncated: set.truncated ?? false,
			commandTag: set.commandTag ?? "",
		})),
	};
}

export function toTables(wire: DataTables): TableRef[] {
	return (wire.tables ?? []).map((table) => ({
		schema: table.schema,
		name: table.name,
		kind: table.kind,
		rowEstimate: table.rowEstimate,
	}));
}

/** A table's identity in a list and in a query key: "schema.name", or "name" without a schema. */
export function tableKey(table: Pick<TableRef, "schema" | "name">): string {
	return table.schema === "" ? table.name : `${table.schema}.${table.name}`;
}

/** Tables whose name contains `filter`, ignoring case; the whole list for a blank filter. */
export function filterTables(
	tables: readonly TableRef[],
	filter: string,
): TableRef[] {
	const wanted = filter.trim().toLowerCase();
	if (wanted === "") return [...tables];
	return tables.filter((table) =>
		tableKey(table).toLowerCase().includes(wanted),
	);
}

/** "1 row", "2 rows". */
export function rowsLabel(count: number): string {
	return count === 1 ? "1 row" : `${count} rows`;
}

/** The estimate as a short label: "~1,200 rows", or "" when the engine has none. */
export function estimateLabel(rowEstimate: number | undefined): string {
	if (rowEstimate === undefined) return "";
	return `~${rowsLabel(rowEstimate)}`;
}

/** The rows across every set. */
export function totalRows(result: QueryResult): number {
	return result.sets.reduce((sum, set) => sum + set.rowCount, 0);
}

/** The footer under results: "3 rows · 12 ms". */
export function resultFooter(result: QueryResult): string {
	return `${rowsLabel(totalRows(result))} · ${result.durationMs} ms`;
}

/** A page of the table browser has a next page when the control plane cut it. */
export function hasNextPage(set: ResultSet | undefined): boolean {
	return set?.truncated === true;
}

/** A write in a production environment is confirmed by typing the service slug. */
export function needsWriteConfirmation(
	production: boolean,
	allowWrites: boolean,
): boolean {
	return production && allowWrites;
}

/** The SQL dialect of the editor: the engine's own, ClickHouse falling back to standard SQL. */
export type SqlDialectName = "postgres" | "mysql" | "mariadb" | "standard";

export function dialectFor(engine: string | undefined): SqlDialectName {
	switch (engine) {
		case "postgres":
			return "postgres";
		case "mysql":
			return "mysql";
		case "mariadb":
			return "mariadb";
		default:
			return "standard";
	}
}

/** The API codes the Data tab answers with a whole-pane message instead of a failed request. */
const BLOCKED_MESSAGE: Record<string, string> = {
	service_stopped: "Start the database to browse its data.",
	not_deployed: "Deploy the database first.",
	database_fenced: "This database is fenced.",
	agent_too_old: "Update the agent on this server.",
	data_not_supported: "This database has no data browser.",
	server_offline:
		"The server is offline. Its data is unavailable until it is back.",
	server_timeout: "The server did not answer in time. Try again.",
};

/**
 * The message for an error that means "the data cannot be read right now",
 * with the reason; undefined for any other failure (a query that failed, a
 * network error), which the panels show where it happened.
 */
export function blockedMessage(error: unknown): string | undefined {
	const code = errorCode(error);
	return code === undefined ? undefined : BLOCKED_MESSAGE[code];
}

/** `confirmation_required`: the server wants the service slug typed for a write. */
export function isConfirmationRequired(error: unknown): boolean {
	return errorCode(error) === "confirmation_required";
}
