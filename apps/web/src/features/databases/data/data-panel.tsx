import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { Service } from "#/api/models";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { BlockedState } from "./blocked-state";
import { blockedMessage, dialectFor } from "./model";
import { dataTablesQuery } from "./queries";
import { QueryView } from "./query-view";
import { type DataSelection, TableList } from "./table-list";
import { TableView } from "./table-view";

export interface DataPanelProps {
	service: Pick<Service, "id" | "name" | "slug" | "config">;
	/** The service is in a production environment. */
	production: boolean;
}

/**
 * The Data tab of a managed SQL database: its tables on the left, and on the
 * right either one table's rows (100 a page) or the SQL console. The console
 * stays mounted while a table is shown, so a half-written query survives a
 * look at a table.
 */
export function DataPanel({ service, production }: DataPanelProps) {
	const tables = useQuery(dataTablesQuery(service.id));
	const [selection, setSelection] = useState<DataSelection | undefined>();

	// Until the reader picks something, the first table (or the console when there is none).
	const first = tables.data?.[0];
	useEffect(() => {
		if (selection === undefined && tables.isSuccess) {
			setSelection(first ? { kind: "table", table: first } : { kind: "query" });
		}
	}, [selection, tables.isSuccess, first]);

	const blocked = tables.isError ? blockedMessage(tables.error) : undefined;
	if (blocked !== undefined) return <BlockedState message={blocked} />;
	if (tables.isError) {
		return (
			<Alert variant="destructive">
				<AlertTitle>The tables could not be listed</AlertTitle>
				<AlertDescription className="flex flex-col items-start gap-2">
					<span>{errorMessage(tables.error)}</span>
					<Button
						variant="secondary"
						size="sm"
						onClick={() => void tables.refetch()}
					>
						Try again
					</Button>
				</AlertDescription>
			</Alert>
		);
	}
	if (!tables.isSuccess || selection === undefined) {
		return (
			<div aria-hidden className="flex gap-4">
				<Skeleton className="h-64 w-48 rounded-card" />
				<Skeleton className="h-64 flex-1 rounded-card" />
			</div>
		);
	}

	const showingTable = selection.kind === "table" ? selection.table : undefined;
	return (
		<div className="grid min-w-0 gap-4 md:grid-cols-[14rem_minmax(0,1fr)]">
			<TableList
				tables={tables.data}
				selection={selection}
				onSelect={setSelection}
			/>
			<div className="flex min-w-0 flex-col">
				{showingTable ? (
					<TableView
						// A new table starts on its first page.
						key={`${showingTable.schema}.${showingTable.name}`}
						serviceId={service.id}
						table={showingTable}
					/>
				) : null}
				<div hidden={selection.kind !== "query"}>
					<QueryView
						serviceId={service.id}
						serviceSlug={service.slug}
						serviceName={service.name}
						production={production}
						dialect={dialectFor(service.config.database?.engine)}
					/>
				</div>
			</div>
		</div>
	);
}
