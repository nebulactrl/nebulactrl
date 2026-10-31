import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DownloadIcon, ExternalLinkIcon, RefreshCwIcon } from "lucide-react";
import { useId, useState } from "react";
import { errorMessage, errorStatus } from "#/api/client-config";
import { checkSystemUpdateMutation } from "#/api/gen/@tanstack/react-query.gen";
import { startSystemUpdate } from "#/api/gen/sdk.gen";
import { CodeBlock } from "#/components/common/code-block";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import {
	Alert,
	AlertAction,
	AlertDescription,
	AlertTitle,
} from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { absoluteDate } from "#/lib/format";
import { ReleaseNotes } from "./release-notes";
import { isSafeHref } from "./release-notes-markdown";
import {
	canStart,
	elapsedMs,
	formatElapsed,
	GIVE_UP_MS,
	idleSummary,
	isStalled,
	isWaitingForRestart,
	showsManualCommand,
	type UpdateInfo,
	type UpdateRun,
	versionLabel,
	viewState,
} from "./update-model";
import { useNow } from "./use-now";
import { systemUpdateQuery } from "./use-system-update";

/** Admin → Updates: the installed release, a newer one if the feed announced it, and the one click update. */
export function UpdateCard() {
	const query = useQuery(systemUpdateQuery());
	if (query.isPending) return <Skeleton className="h-48 rounded-card" />;
	if (!query.data) {
		return (
			<ErrorState
				frame="raised"
				title="The version did not load"
				body="The control plane did not answer, so nothing was changed."
				retry={() => void query.refetch()}
			/>
		);
	}
	return (
		<UpdateBody
			info={query.data}
			errorStatus={query.isError ? errorStatus(query.error) : undefined}
		/>
	);
}

function UpdateBody({
	info,
	errorStatus: failedStatus,
}: {
	info: UpdateInfo;
	/** Status of the last failed read; the update's own restart looks like one. */
	errorStatus: number | undefined;
}) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const state = viewState(info);
	const startable = canStart(info);
	const now = useNow(state === "running");

	const start = useMutation({
		mutationFn: async (variables: { version: string; key: string }) => {
			const res = await startSystemUpdate({
				body: { version: variables.version },
				headers: { "Idempotency-Key": variables.key },
			});
			return res.data;
		},
		onSuccess: (data) => {
			if (data) queryClient.setQueryData(systemUpdateQuery().queryKey, data);
		},
		onError: () =>
			queryClient.invalidateQueries({
				queryKey: systemUpdateQuery().queryKey,
			}),
	});

	const check = useMutation({
		...checkSystemUpdateMutation(),
		onSuccess: (data) => {
			queryClient.setQueryData(systemUpdateQuery().queryKey, data);
			if (data.available && data.latest) {
				toast.success(`${versionLabel(data.latest)} is available`);
			} else {
				toast.success("You are on the latest version.");
			}
		},
		onError: (error) => {
			toast.error("Could not check for updates", errorMessage(error));
			// A failed read is recorded as the check error: show it too.
			void queryClient.invalidateQueries({
				queryKey: systemUpdateQuery().queryKey,
			});
		},
	});

	const requestUpdate = async () => {
		const version = info.latest;
		if (!version) return;
		// One key per confirmation: a retry from the same dialog replays, never starts twice.
		const key = crypto.randomUUID();
		await confirm({
			title: `Update to ${versionLabel(version)}?`,
			body: (
				<>
					The dashboard is unavailable for about a minute while the new version
					starts. A backup of the control plane is taken first, and if the new
					version does not start cleanly, NebulaCtrl rolls back to{" "}
					{versionLabel(info.current)} by itself.
				</>
			),
			cta: "Update now",
			onConfirm: async () => {
				await start.mutateAsync({ version, key });
			},
		});
	};

	const run = info.updating;

	return (
		<div className="flex flex-col gap-5" data-testid="system-update">
			<dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-8 gap-y-2 text-xs">
				<dt className="text-ink-3">Installed</dt>
				<dd className="m-0 font-mono text-ink-1">
					{versionLabel(info.current)}
				</dd>
				<dt className="text-ink-3">Latest</dt>
				<dd className="m-0 font-mono text-ink-1">
					{info.latest
						? versionLabel(info.latest)
						: info.checkEnabled
							? "Not checked yet"
							: "Checks are off"}
				</dd>
				{info.checkEnabled ? (
					<>
						<dt className="text-ink-3">Last checked</dt>
						<dd className="m-0 flex flex-wrap items-center gap-3 text-ink-1">
							<Relative iso={info.checkedAt} />
							{info.isAdmin ? (
								<Button
									variant="secondary"
									size="sm"
									disabled={check.isPending}
									onClick={() => check.mutate({})}
								>
									<RefreshCwIcon
										aria-hidden
										className={
											check.isPending
												? "animate-nc-spin motion-reduce:animate-none"
												: undefined
										}
									/>
									Check now
								</Button>
							) : null}
						</dd>
					</>
				) : null}
			</dl>

			{state === "running" && run ? (
				<Progress
					run={run}
					now={now}
					waiting={isWaitingForRestart(info, failedStatus)}
				/>
			) : null}

			{state === "failed" && run ? (
				<Alert variant="destructive">
					<AlertTitle>The update did not complete.</AlertTitle>
					<AlertDescription>
						<p className="m-0">
							{run.error ??
								"No details were reported. Check the installer log on the server."}
						</p>
						<p className="m-0 text-ink-3">
							From {versionLabel(run.fromVersion)} to{" "}
							{versionLabel(run.targetVersion)}.
						</p>
					</AlertDescription>
					{startable ? (
						<AlertAction>
							<Button
								variant="secondary"
								size="sm"
								disabled={start.isPending}
								onClick={() => void requestUpdate()}
							>
								Try again
							</Button>
						</AlertAction>
					) : null}
				</Alert>
			) : null}

			{state === "available" || state === "unavailable" ? (
				<Alert variant="info" role="note">
					<AlertTitle>
						{info.latest ? versionLabel(info.latest) : "A new version"} is
						available
					</AlertTitle>
					<AlertDescription>
						You are on {versionLabel(info.current)}.
						{info.publishedAt
							? ` Published ${absoluteDate(info.publishedAt)}.`
							: ""}
					</AlertDescription>
					{startable ? (
						<AlertAction>
							<Button
								size="sm"
								disabled={start.isPending}
								onClick={() => void requestUpdate()}
							>
								<DownloadIcon aria-hidden />
								Update now
							</Button>
						</AlertAction>
					) : null}
				</Alert>
			) : null}

			{state === "idle" ? (
				<p className="m-0 text-ink-2 text-xs">{idleSummary(info)}</p>
			) : null}

			{state === "checks-off" ? (
				<p className="m-0 text-ink-2 text-xs">
					Update checks are off for this control plane, so it will not tell you
					when a new version is out.
				</p>
			) : null}

			{info.cannotUpdateReason &&
			(state === "unavailable" || (state === "failed" && !startable)) ? (
				<p className="m-0 text-ink-3 text-xs leading-relaxed">
					{info.cannotUpdateReason}
				</p>
			) : null}

			{info.manualCommand && showsManualCommand(state, startable) ? (
				<ManualCommand
					command={info.manualCommand}
					checksOff={state === "checks-off"}
				/>
			) : null}

			{info.latest && (info.notes || info.notesUrl) ? (
				<Notes
					label={`Release notes for ${versionLabel(info.latest)}`}
					notes={info.notes}
					url={info.notesUrl}
				/>
			) : null}

			{info.checkError ? (
				<p className="m-0 text-ink-3 text-xs leading-relaxed">
					The last check for new releases did not work: {info.checkError}
				</p>
			) : null}
		</div>
	);
}

/**
 * The update in flight. The installer reports no steps, so there is one honest
 * state: an update is running, with how long it has been. Only the sentence is
 * a live region; the running clock is not, so a screen reader is not read to
 * every second.
 */
function Progress({
	run,
	now,
	waiting,
}: {
	run: UpdateRun;
	now: number;
	/** The control plane does not answer, as it does while it restarts. */
	waiting: boolean;
}) {
	const elapsed = elapsedMs(run.startedAt, now);

	if (isStalled(run, now)) {
		return (
			<Alert variant="warning">
				<AlertTitle>The update is taking longer than expected</AlertTitle>
				<AlertDescription>
					<p className="m-0">
						It has been running for {formatElapsed(elapsed)}, past the{" "}
						{formatElapsed(GIVE_UP_MS)} an update should take.{" "}
						{waiting
							? "The control plane has not come back."
							: "The control plane answers, but it has not restarted on the new version."}{" "}
						Check the installer on the server with{" "}
						<code className="type-mono">
							journalctl -u &apos;nebulactrl-update-*&apos;
						</code>
						. This page keeps checking.
					</p>
				</AlertDescription>
			</Alert>
		);
	}

	return (
		<div className="surface-well flex flex-col gap-2 rounded-card p-4">
			<div
				role="status"
				aria-live="polite"
				className="flex items-start gap-2.5"
			>
				<Spinner
					aria-hidden
					role="presentation"
					className="mt-0.5 size-4 shrink-0"
				/>
				<div className="flex flex-col gap-1">
					<p className="m-0 font-medium text-ink-1 text-sm">
						Updating to {versionLabel(run.targetVersion)}. The dashboard is
						unavailable for about a minute.
					</p>
					{waiting ? (
						<p className="m-0 text-ink-3 text-xs">
							The control plane is restarting. This page reconnects by itself.
						</p>
					) : null}
				</div>
			</div>
			<p className="m-0 pl-[26px] text-ink-3 text-xs tabular-nums">
				Running for {formatElapsed(elapsed)}, from{" "}
				{versionLabel(run.fromVersion)}
			</p>
		</div>
	);
}

function ManualCommand({
	command,
	checksOff,
}: {
	command: string;
	checksOff: boolean;
}) {
	return (
		<div className="flex flex-col gap-2">
			<p className="m-0 text-ink-3 text-xs leading-relaxed">
				{checksOff
					? "To update by hand, run this on the server that hosts the control plane. It takes a backup first."
					: "To update by hand, run this on the server that hosts the control plane. It takes a backup first and rolls back if the new version does not start."}
			</p>
			<CodeBlock copyValue={command}>
				<pre className="whitespace-pre-wrap">{command}</pre>
			</CodeBlock>
		</div>
	);
}

/** The release notes: collapsed until asked for, with the full notes page beside them. */
function Notes({
	label,
	notes,
	url,
}: {
	label: string;
	notes: string | undefined;
	url: string | undefined;
}) {
	const [open, setOpen] = useState(false);
	const id = useId();
	return (
		<div className="flex flex-col">
			<div className="flex items-center gap-3">
				{notes ? (
					<button
						type="button"
						aria-expanded={open}
						aria-controls={id}
						onClick={() => setOpen((value) => !value)}
						className="flex cursor-pointer items-center gap-2 rounded-ctl py-1 text-left font-medium text-ink-1 text-xs outline-none focus-visible:focus-ring"
					>
						<DisclosureChevron
							open={open}
							className="size-3.5 shrink-0 text-ink-3"
						/>
						{label}
					</button>
				) : null}
				{url && isSafeHref(url) ? (
					<a
						href={url}
						target="_blank"
						rel="noopener noreferrer"
						className="ml-auto inline-flex items-center gap-1 rounded-ctl text-accent-ink text-xs underline-offset-2 outline-none hover:underline focus-visible:focus-ring"
					>
						Full release notes
						<ExternalLinkIcon aria-hidden className="size-3" />
					</a>
				) : null}
			</div>
			{notes ? (
				<DisclosurePanel open={open} id={id}>
					<div className="pt-2 pl-[22px]">
						<ReleaseNotes markdown={notes} label={label} />
					</div>
				</DisclosurePanel>
			) : null}
		</div>
	);
}
