import { useQuery } from "@tanstack/react-query";
import { DatabaseIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Progress } from "#/components/ui/progress";
import { Skeleton } from "#/components/ui/skeleton";
import { StatusIcon } from "#/components/ui/status-icon";
import { teamSliceQuery } from "./queries";
import { backupStateWords, backupUsage, hasBuiltinStorage } from "./team-slice";

/**
 * Team settings → Backup storage: "Built-in backup storage", the team's own
 * space on the control plane host. Read-only: how much of the quota the backups
 * use and whether it works. The team never sees keys, a bucket or an address,
 * and cannot move or delete it; the instance admin sets the quota.
 * Renders nothing on an installation without built-in storage.
 */
export function BuiltinStorageCard({ actions }: { actions?: ReactNode }) {
	const query = useQuery(teamSliceQuery());
	if (query.isPending) return <Skeleton className="h-28 rounded-card" />;
	const slice = query.data?.backup;
	if (!slice || !hasBuiltinStorage(slice)) return null;

	const state = backupStateWords(slice.state);
	const usage = backupUsage(slice);
	return (
		<article
			aria-label="Built-in backup storage"
			data-testid="builtin-storage"
			className="surface-raised flex min-w-0 flex-col gap-3.5 rounded-card p-4"
		>
			<header className="flex flex-wrap items-start justify-between gap-3">
				<div className="flex min-w-0 items-start gap-3">
					<span
						aria-hidden
						className="surface-well grid size-9 shrink-0 place-items-center rounded-ctl text-accent-ink"
					>
						<DatabaseIcon className="size-4" />
					</span>
					<div className="flex min-w-0 flex-col gap-0.5">
						<h3 className="m-0 font-medium text-ink-1 text-sm">
							Built-in backup storage
						</h3>
						<span className="text-ink-3 text-xs">
							Your team's own space on this installation. Managed by your
							instance admin.
						</span>
					</div>
				</div>
				<span className="flex items-center gap-1.5 text-ink-1 text-xs">
					<StatusIcon kind={state.kind} label="" />
					{state.label}
				</span>
			</header>
			<div className="flex flex-col gap-1.5">
				<span className="flex items-baseline justify-between gap-3 font-mono text-xs tabular-nums">
					<span className="text-ink-1">{usage.text}</span>
					<span className="text-ink-3">{usage.percent}%</span>
				</span>
				<Progress
					value={usage.barValue}
					warnAt={80}
					aria-label="Built-in backup storage used"
				/>
			</div>
			{state.detail ? (
				<p className="m-0 text-ink-2 text-xs leading-relaxed">{state.detail}</p>
			) : null}
			{usage.note ? (
				<p className="m-0 text-warning-text text-xs leading-relaxed">
					{usage.note}
				</p>
			) : null}
			<p className="m-0 text-ink-3 text-xs leading-relaxed">
				It lives on the same host as the control plane, so it is not off-site:
				add an external destination too to keep a copy elsewhere.
			</p>
			{actions ? (
				<footer className="-mb-1 flex flex-wrap items-center gap-1 border-line-faint border-t pt-2.5">
					{actions}
				</footer>
			) : null}
		</article>
	);
}
