import { useQuery } from "@tanstack/react-query";
import { RotateCcwIcon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import type { VariableHistoryEntry } from "#/api/gen/types.gen";
import { variableHistoryQuery } from "#/api/hooks";
import { Alert } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { useVariableMutations } from "./use-variable-mutations";

const SECRET_MASK = "••••••";

/** The rows of a history that a person reads: what happened, to which value. */
function describeEntry(entry: VariableHistoryEntry): string {
	const generated = (entry.generated ?? [])
		.map((g) => `${g.fn}(${(g.args ?? []).join(", ")})`)
		.join(" + ");
	switch (entry.op) {
		case "delete":
			return "Deleted";
		case "rotate":
			return generated ? `Rotated · generated ${generated}` : "Rotated";
		case "restore":
			return "Restored";
		default:
			return generated ? `Set · generated ${generated}` : "Set";
	}
}

/**
 * The versions of one variable, newest first. Secrets keep no value, only a
 * fingerprint (equal fingerprints mean equal values); a non-secret version can
 * be restored as a new version.
 */
export function HistoryDialog({
	serviceId,
	variableKey,
	onClose,
}: {
	serviceId: string;
	/** The variable to show; null when closed. */
	variableKey: string | null;
	onClose(): void;
}) {
	return (
		<Dialog open={variableKey !== null} onOpenChange={(o) => !o && onClose()}>
			<DialogContent>
				{variableKey !== null ? (
					<HistoryBody
						serviceId={serviceId}
						variableKey={variableKey}
						onClose={onClose}
					/>
				) : null}
			</DialogContent>
		</Dialog>
	);
}

function HistoryBody({
	serviceId,
	variableKey,
	onClose,
}: {
	serviceId: string;
	variableKey: string;
	onClose(): void;
}) {
	const history = useQuery(variableHistoryQuery(serviceId, variableKey));
	const { restore } = useVariableMutations(serviceId);
	const entries = history.data ?? [];
	const newest = entries[0]?.version;

	return (
		<>
			<DialogHeader>
				<DialogTitle>History of {variableKey}</DialogTitle>
				<DialogDescription>
					Secret values are not kept: a fingerprint shows whether a secret
					changed, and whether it is the same as an earlier one.
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				{history.isPending ? (
					<div
						role="status"
						aria-label="Loading history"
						className="flex flex-col gap-2"
					>
						<Skeleton className="h-9 w-full" />
						<Skeleton className="h-9 w-full" />
					</div>
				) : history.isError ? (
					<Alert variant="destructive">{errorMessage(history.error)}</Alert>
				) : entries.length === 0 ? (
					<p className="type-secondary">No versions recorded yet.</p>
				) : (
					<ol
						aria-label="Versions"
						className="flex flex-col divide-y divide-line-faint"
					>
						{entries.map((entry) => (
							<li
								key={`${entry.version}-${entry.op}`}
								className="flex items-start gap-3 py-2 text-[12.5px]"
							>
								<span className="w-8 shrink-0 font-medium font-mono text-ink-3 tabular-nums">
									v{entry.version}
								</span>
								<div className="flex min-w-0 flex-1 flex-col gap-0.5">
									<span className="text-ink-1">{describeEntry(entry)}</span>
									<span className="truncate font-mono text-[11.5px] text-ink-3">
										{entry.isSecret
											? `${SECRET_MASK} · fingerprint ${entry.fingerprint}`
											: entry.op === "delete"
												? "—"
												: (entry.value ?? "")}
									</span>
									<span className="text-[11px] text-ink-4">
										{new Date(entry.createdAt).toLocaleString()} ·{" "}
										{entry.actorType}
										{entry.releaseId
											? ` · shipped in release ${entry.releaseId.slice(0, 7)}`
											: " · not deployed yet"}
									</span>
								</div>
								<span className="flex shrink-0 items-center gap-1.5">
									{entry.isSecret ? <Badge>secret</Badge> : null}
									{entry.restorable && entry.version !== newest ? (
										<Button
											type="button"
											variant="ghost"
											size="sm"
											disabled={restore.isPending}
											onClick={() =>
												restore.mutate(
													{ key: variableKey, version: entry.version },
													{
														onSuccess: () => {
															toast.success(
																`Restored v${entry.version} of ${variableKey}`,
																"Staged as a new version.",
															);
															onClose();
														},
													},
												)
											}
										>
											<RotateCcwIcon aria-hidden />
											Restore
										</Button>
									) : null}
								</span>
							</li>
						))}
					</ol>
				)}
				{restore.isError ? (
					<Alert variant="destructive">{errorMessage(restore.error)}</Alert>
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button type="button" variant="ghost" onClick={onClose}>
					Close
				</Button>
			</DialogFooter>
		</>
	);
}
