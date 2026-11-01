import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { cn } from "cn";
import { CheckIcon } from "lucide-react";
import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { errorStatus } from "#/api/client-config";
import type { Team } from "#/api/gen/types.gen";
import { AuthCard } from "#/components/auth/auth-screen";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import {
	elapsedMs,
	formatElapsed,
	versionLabel,
} from "#/features/settings/update-model";
import { useNow } from "#/features/settings/use-now";
import { systemUpdateQuery } from "#/features/settings/use-system-update";
import { leaveTeamState, teamsQuery } from "#/features/teams/queries";
import {
	type DeletionStepRow,
	deletionOutcome,
	deletionStepRows,
	type ThresholdView,
	thresholdView,
} from "./model";

/**
 * What the console shows instead of itself, if anything: the control-plane
 * update's restart, or the deletion of the team the console acts in. Each is
 * the pixel-dithered Eclipse over the halftone floor, the way the sign-in page
 * is, and the progress is told in words only; the field never reacts to it.
 *
 * The app stays mounted underneath but inert, so its state survives a view
 * that ends in a failure; a finished update reloads the page into the new
 * version, and a finished deletion leaves the team's state behind.
 */
export function useThresholdView(): ThresholdView | null {
	const queryClient = useQueryClient();
	const router = useRouter();
	const update = useQuery(systemUpdateQuery());
	const teams = useQuery(teamsQuery());
	const items = teams.data?.items ?? [];
	const running = update.data?.updating?.state === "running";
	const now = useNow(running);

	const view = thresholdView({
		update: update.data,
		updateErrorStatus: update.isError ? errorStatus(update.error) : undefined,
		now,
		teams: items,
	});
	// The deletion this page showed: when it leaves the list the team is gone,
	// and nothing cached belongs to it any more.
	const deleting = view?.kind === "team-deletion" ? view.team : undefined;
	const deletingId = deleting?.id;
	const deletingName = deleting?.name;
	const [watched, setWatched] = useState<{ id: string; name: string }>();
	useEffect(() => {
		if (deletingId !== undefined && deletingName !== undefined)
			setWatched({ id: deletingId, name: deletingName });
	}, [deletingId, deletingName]);
	useEffect(() => {
		if (!watched || !teams.isSuccess) return;
		const outcome = deletionOutcome(watched.id, items);
		if (outcome === "running") return;
		setWatched(undefined);
		if (outcome === "gone") {
			toast.success(`${watched.name} was deleted`);
			void leaveTeamState(queryClient, router);
		}
	}, [watched, teams.isSuccess, items, queryClient, router]);

	return view;
}

/** Full-screen above the app: the view's screen, with the app inert beneath it. */
export function ThresholdGate({ children }: { children: ReactNode }) {
	const view = useThresholdView();
	return (
		<>
			<div inert={view !== null}>{children}</div>
			{view ? <ThresholdScreen view={view} /> : null}
		</>
	);
}

function ThresholdScreen({ view }: { view: ThresholdView }) {
	const headingId = useId();
	const ref = useRef<HTMLDivElement>(null);
	// The app beneath is inert, so focus would fall to the body: take it here
	// once, so a screen reader starts at the heading.
	useEffect(() => ref.current?.focus({ preventScroll: true }), []);
	return (
		<div
			ref={ref}
			role="dialog"
			aria-modal="true"
			aria-labelledby={headingId}
			tabIndex={-1}
			className="fixed inset-0 z-[60] overflow-y-auto outline-none motion-safe:animate-nc-fade"
		>
			<AuthCard width={440}>
				{view.kind === "update" ? (
					<UpdateRestart headingId={headingId} view={view} />
				) : (
					<TeamDeletion headingId={headingId} team={view.team} />
				)}
			</AuthCard>
		</div>
	);
}

function Title({ id, children }: { id: string; children: ReactNode }) {
	return (
		<div className="mt-1.5 mb-1 flex flex-col gap-2">
			<h1 id={id} className="type-page-title text-[24px] text-ink-1">
				{children}
			</h1>
		</div>
	);
}

function UpdateRestart({
	headingId,
	view,
}: {
	headingId: string;
	view: Extract<ThresholdView, { kind: "update" }>;
}) {
	const { run, restarting } = view;
	const now = useNow(true);
	return (
		<div className="flex flex-col gap-4" data-testid="update-restart">
			<Title id={headingId}>
				Updating to {versionLabel(run.targetVersion)}
			</Title>
			<p className="m-0 text-ink-2 text-sm leading-relaxed">
				The dashboard is unavailable for about a minute while the new version
				starts. This page reconnects by itself.
			</p>
			<div
				role="status"
				aria-live="polite"
				className="flex items-center gap-2.5 text-ink-1 text-sm"
			>
				<Spinner aria-hidden role="presentation" className="size-4 shrink-0" />
				<span className="font-medium">
					{restarting
						? "Restarting the control plane"
						: "Installing the new version"}
				</span>
			</div>
			<p className="m-0 text-ink-3 text-xs tabular-nums">
				Running for {formatElapsed(elapsedMs(run.startedAt, now))}, from{" "}
				{versionLabel(run.fromVersion)}
			</p>
		</div>
	);
}

function StepMark({ state }: { state: DeletionStepRow["state"] }) {
	if (state === "current") {
		return (
			<Spinner aria-hidden role="presentation" className="size-4 shrink-0" />
		);
	}
	if (state === "done") {
		return (
			<CheckIcon aria-hidden className="size-4 shrink-0 text-success-text" />
		);
	}
	return (
		<span
			aria-hidden
			className="flex size-4 shrink-0 items-center justify-center"
		>
			<span className="size-1.5 rounded-full bg-line-strong" />
		</span>
	);
}

function TeamDeletion({ headingId, team }: { headingId: string; team: Team }) {
	const rows = deletionStepRows(team.deletion?.step);
	return (
		<div className="flex flex-col gap-4" data-testid="team-deletion">
			<Title id={headingId}>Deleting {team.name}</Title>
			<p className="m-0 text-ink-2 text-sm leading-relaxed">
				Agents can take a few minutes to empty their hosts. The deletion
				continues on the server if you close this tab.
			</p>
			<ol
				role="status"
				aria-live="polite"
				className="m-0 flex list-none flex-col gap-2.5 p-0"
			>
				{rows.map((row) => (
					<li
						key={row.id}
						aria-current={row.state === "current" ? "step" : undefined}
						className={cn(
							"flex items-center gap-2.5 text-sm",
							row.state === "current" ? "font-medium text-ink-1" : "text-ink-3",
						)}
					>
						<StepMark state={row.state} />
						<span>
							{row.label}
							{row.state === "done" ? (
								<span className="sr-only"> (done)</span>
							) : null}
						</span>
					</li>
				))}
			</ol>
		</div>
	);
}
