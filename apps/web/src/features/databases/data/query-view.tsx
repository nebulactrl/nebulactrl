import { useMutation, useQueryClient } from "@tanstack/react-query";
import { PlayIcon } from "lucide-react";
import { useId, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { queryDatabaseMutation } from "#/api/gen/@tanstack/react-query.gen";
import { ConfirmDialog } from "#/components/common/confirm-dialog";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Kbd } from "#/components/ui/kbd";
import { Label } from "#/components/ui/label";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { BlockedState } from "./blocked-state";
import {
	blockedMessage,
	isConfirmationRequired,
	needsWriteConfirmation,
	type QueryResult,
	type SqlDialectName,
	toQueryResult,
} from "./model";
import { invalidateData } from "./queries";
import { Results } from "./results";
import { SqlEditor } from "./sql-editor";

export interface QueryViewProps {
	serviceId: string;
	/** The slug the reader types to confirm a write in production. */
	serviceSlug: string;
	serviceName: string;
	/** The service is in a production environment: a write is confirmed by typing the slug. */
	production: boolean;
	dialect: SqlDialectName;
}

/**
 * The SQL console: an editor, Run (⌘↵) and an Allow writes switch. Statements
 * run in a read-only session unless the switch is on, and the switch turns
 * itself off after every run so a write is always a deliberate act. In a
 * production environment a run with writes asks for the service slug first.
 * The error of a failed run stands under the editor, where the SQL is.
 */
export function QueryView({
	serviceId,
	serviceSlug,
	serviceName,
	production,
	dialect,
}: QueryViewProps) {
	const queryClient = useQueryClient();
	const writesId = useId();
	const [sql, setSql] = useState("");
	const [allowWrites, setAllowWrites] = useState(false);
	const [result, setResult] = useState<QueryResult | null>(null);
	const [confirming, setConfirming] = useState(false);
	const run = useMutation(queryDatabaseMutation());

	const empty = sql.trim() === "";

	const execute = async (confirm: string | undefined) => {
		const writes = allowWrites;
		const answer = await run.mutateAsync({
			path: { serviceId },
			body: {
				sql,
				...(writes ? { allowWrites: true } : {}),
				...(confirm === undefined ? {} : { confirm }),
			},
		});
		setResult(toQueryResult(answer));
		setAllowWrites(false);
		if (writes) void invalidateData(queryClient);
	};

	const submit = () => {
		if (empty || run.isPending) return;
		if (needsWriteConfirmation(production, allowWrites)) {
			setConfirming(true);
			return;
		}
		// A refusal for a missing confirmation (the page did not know the
		// environment is production) opens the same dialog, then retries.
		execute(undefined).catch((error: unknown) => {
			if (isConfirmationRequired(error)) setConfirming(true);
		});
	};

	const blocked = run.isError ? blockedMessage(run.error) : undefined;
	const failure =
		run.isError && blocked === undefined && !isConfirmationRequired(run.error)
			? errorMessage(run.error)
			: undefined;

	return (
		<section aria-label="SQL console" className="flex min-w-0 flex-col gap-3">
			<SqlEditor
				value={sql}
				onChange={setSql}
				onRun={submit}
				dialect={dialect}
				label="SQL"
			/>
			<div className="flex flex-wrap items-center gap-3">
				<Button size="md" disabled={empty || run.isPending} onClick={submit}>
					{run.isPending ? <Spinner /> : <PlayIcon aria-hidden />}
					Run
					<Kbd variant="key">⌘↵</Kbd>
				</Button>
				<div className="flex items-center gap-2">
					<Switch
						id={writesId}
						size="sm"
						checked={allowWrites}
						disabled={run.isPending}
						onCheckedChange={setAllowWrites}
					/>
					<Label htmlFor={writesId} className="text-ink-2 text-xs">
						Allow writes
					</Label>
				</div>
				<p className="text-ink-3 text-xs">
					{allowWrites
						? "Statements may change data. This resets after the run."
						: "Read-only: statements that change data are refused."}
				</p>
			</div>

			{blocked === undefined ? null : <BlockedState message={blocked} />}
			{failure === undefined ? null : (
				<Alert variant="destructive">
					<AlertTitle>The statement failed</AlertTitle>
					<AlertDescription className="whitespace-pre-wrap font-mono">
						{failure}
					</AlertDescription>
				</Alert>
			)}
			{result === null ? null : <Results result={result} label="Result" />}

			<ConfirmDialog
				open={confirming}
				onOpenChange={setConfirming}
				title={`Run a write on ${serviceName}?`}
				body={
					<>
						This is a production database. The statement runs as written and
						cannot be undone from here. Type the service name to confirm.
					</>
				}
				cta="Run statement"
				danger
				hold={false}
				typeToConfirm={serviceSlug}
				onConfirm={() => execute(serviceSlug)}
			/>
		</section>
	);
}
