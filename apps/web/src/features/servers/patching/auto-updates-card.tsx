import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { getServerPatchesQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { updateServerPatchAuto } from "#/api/gen/sdk.gen";
import type { PatchView } from "#/api/gen/types.gen";
import { ConfirmDialog } from "#/components/common/confirm-dialog";
import { Relative } from "#/components/common/relative";
import { DocsLink } from "#/components/docs/docs-link";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Card, CardContent, CardFooter } from "#/components/ui/card";
import { CheckLabel } from "#/components/ui/check-label";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { KvRow } from "#/components/ui/kv-row";
import { SectionLabel } from "#/components/ui/section-label";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { StringList } from "#/features/service-settings/docker-rows";
import { browserTimezone, timezoneOptions } from "#/features/tasks/model";
import type { ServerView } from "../model";
import {
	type AutoDraft,
	applySuggestion,
	autoBody,
	autoDraftOf,
	autoErrorField,
	autoIssues,
	autoKey,
	autoRunStatusText,
	enableAuto,
	failuresText,
	HOLD_LIMIT,
	hasAutoIssues,
	nativeUpdatesText,
	nextWindowText,
	productionList,
	REBOOT_GUARDS,
	startWarning,
	windowLengthLabel,
	windowLengths,
} from "./patching-model";

/** The id the History card gives a run's row, so the status line can point at it. */
export function runAnchor(runId: string): string {
	return `patch-run-${runId}`;
}

/**
 * Automatic updates of one server: off by default; on, a nightly maintenance
 * window in which the control plane starts the same upgrade as Apply updates.
 * Edits stay in a draft until Save; a server that runs production (or the
 * control plane) asks for an acknowledgement first. Reserved to the instance
 * admin, like Apply.
 */
export function AutoUpdatesCard({
	server,
	view,
	instanceAdmin,
}: {
	server: ServerView;
	view: PatchView;
	instanceAdmin: boolean;
}) {
	const queryClient = useQueryClient();
	const auto = view.auto;
	const [draft, setDraft] = useState<AutoDraft>(() => autoDraftOf(auto));
	const baseline = autoKey(autoDraftOf(auto));
	// A refetch that changed the saved settings (someone else saved, or three
	// failures switched them off) resets the form only while it is clean.
	const lastBaseline = useRef(baseline);
	useEffect(() => {
		if (lastBaseline.current === baseline) return;
		const previous = lastBaseline.current;
		lastBaseline.current = baseline;
		setDraft((current) =>
			autoKey(current) === previous ? autoDraftOf(auto) : current,
		);
	}, [baseline, auto]);

	const [touched, setTouched] = useState(false);
	const [askAck, setAskAck] = useState(false);
	const [accepted, setAccepted] = useState(false);

	const save = useMutation({
		mutationFn: async (acknowledge: boolean) => {
			await updateServerPatchAuto({
				path: { serverId: server.id },
				body: autoBody(draft, acknowledge),
				throwOnError: true,
			});
		},
		onSuccess: async () => {
			setTouched(false);
			toast.success(
				"Automatic updates saved",
				draft.enabled
					? "The next window is shown under the settings."
					: "Automatic updates are off for this server.",
			);
			await queryClient.invalidateQueries({
				queryKey: getServerPatchesQueryKey({ path: { serverId: server.id } }),
			});
		},
	});

	const zones = useMemo(() => {
		const all = timezoneOptions();
		return all.includes(draft.timezone) ? all : [draft.timezone, ...all];
	}, [draft.timezone]);

	const dirty = autoKey(draft) !== baseline;
	const issues = autoIssues(draft);
	const wrongField = save.isError
		? autoErrorField(errorCode(save.error))
		: null;
	const wrongMessage = save.isError ? errorMessage(save.error) : undefined;
	const needsAck = draft.enabled && auto.requiresAcknowledgement;
	const hint = !instanceAdmin
		? "Only the instance admin can change automatic updates."
		: !view.agentSupported
			? "This agent is too old for automatic updates. Update it first."
			: view.known && !view.supported
				? "Automatic updates need apt or dnf, and this host has neither."
				: null;
	const locked = hint !== null;

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (locked || !dirty || save.isPending || hasAutoIssues(issues)) return;
		if (needsAck) {
			setAccepted(false);
			setAskAck(true);
			return;
		}
		save.mutate(false);
	};

	const shown = touched ? issues : undefined;
	const saved = auto.enabled;
	const next =
		saved && auto.nextWindowStart
			? nextWindowText(auto.nextWindowStart, auto.timezone)
			: null;
	const last = auto.lastScheduledRun;
	const failures = failuresText(auto.consecutiveFailures);
	const native = nativeUpdatesText(auto.nativeAutoUpdates, saved);
	const suggestion = auto.suggestedStart;
	const warning = startWarning(draft.windowStart);
	const lengths = windowLengths(draft.windowMinutes);
	const production = productionList(view.productionServices);
	const holdIssues = {
		listWide:
			shown?.holdPackages ??
			(wrongField === "holdPackages" ? wrongMessage : undefined),
		row: (index: number) => shown?.holdRows[index],
	};

	return (
		<>
			<form onSubmit={submit}>
				<Card>
					<CardContent className="py-1">
						<SectionLabel className="pt-4 pb-1">
							Automatic updates{" "}
							<DocsLink
								to="operations/server-patching#automatic-updates"
								className="ml-2 normal-case tracking-normal"
							/>
						</SectionLabel>
						{auto.disabledReason ? (
							<Alert variant="warning" className="my-3">
								<AlertTitle>Automatic updates were switched off</AlertTitle>
								<AlertDescription>
									{auto.disabledReason} Saving the settings again clears this
									and starts the count over.
								</AlertDescription>
							</Alert>
						) : null}
						<KvRow
							label="Update automatically every night"
							description="Off by default. Docker and containerd are never updated automatically."
							htmlFor="srv-auto-enabled"
						>
							<span className="flex flex-col items-start gap-1.5">
								<Switch
									id="srv-auto-enabled"
									checked={draft.enabled}
									disabled={locked}
									onCheckedChange={(on) =>
										setDraft((d) =>
											on
												? enableAuto(d, auto, browserTimezone())
												: { ...d, enabled: false },
										)
									}
								/>
								{hint ? (
									<span className="text-ink-3 text-xs">{hint}</span>
								) : null}
							</span>
						</KvRow>

						{draft.enabled ? (
							<>
								<KvRow
									label="Install"
									description={
										auto.securityOnlySupported
											? "Security only upgrades just the packages that have a security update."
											: "Security only needs apt or dnf; this host's package manager cannot limit an upgrade to security updates."
									}
								>
									<span className="flex flex-col items-start gap-1.5">
										<Segmented
											aria-label="Install"
											font="sans"
											density="regular"
											value={draft.scope}
											options={[
												{
													value: "security",
													label: "Security only",
													disabled: !auto.securityOnlySupported || locked,
												},
												{
													value: "all",
													label: "All updates",
													disabled: locked,
												},
											]}
											onValueChange={(scope) =>
												setDraft((d) => ({ ...d, scope }))
											}
										/>
										{wrongField === "scope" ? (
											<FieldError>{wrongMessage}</FieldError>
										) : null}
									</span>
								</KvRow>
								<KvRow
									label="Start time"
									description="A window opens once a night at this time and nothing starts in its last 15 minutes. Servers start a little apart, so not all at once."
									htmlFor="srv-auto-start"
								>
									<div className="flex w-full flex-col items-start gap-1.5">
										<div className="flex flex-wrap items-center gap-2">
											<Input
												id="srv-auto-start"
												type="time"
												scale="lg"
												className="w-[120px] font-mono"
												disabled={locked}
												value={draft.windowStart}
												aria-invalid={
													shown?.windowStart !== undefined ||
													wrongField === "windowStart"
												}
												onChange={(e) =>
													setDraft((d) => ({
														...d,
														windowStart: e.target.value,
													}))
												}
											/>
											{suggestion ? (
												<Button
													type="button"
													variant="secondary"
													size="xs"
													disabled={locked}
													onClick={() =>
														setDraft((d) => applySuggestion(d, auto))
													}
												>
													Use suggestion {suggestion} (quietest in the last 14
													days)
												</Button>
											) : null}
										</div>
										{suggestion && auto.timezone !== draft.timezone ? (
											<span className="text-ink-3 text-xs">
												The suggestion is in {auto.timezone}; using it switches
												the time zone to {auto.timezone}.
											</span>
										) : null}
										{warning ? (
											<span className="text-warning-text text-xs">
												{warning}
											</span>
										) : null}
										{shown?.windowStart ? (
											<FieldError>{shown.windowStart}</FieldError>
										) : wrongField === "windowStart" ? (
											<FieldError>{wrongMessage}</FieldError>
										) : null}
									</div>
								</KvRow>
								<KvRow
									label="Window length"
									description="Nothing starts in the last 15 minutes. A run that has started is never stopped when the window ends."
								>
									<Segmented
										aria-label="Window length"
										density="regular"
										value={String(draft.windowMinutes)}
										options={lengths.map((minutes) => ({
											value: String(minutes),
											label: windowLengthLabel(minutes),
											disabled: locked,
										}))}
										onValueChange={(minutes) =>
											setDraft((d) => ({
												...d,
												windowMinutes: Number(minutes),
											}))
										}
									/>
								</KvRow>
								<KvRow
									label="Time zone"
									description="The zone the start time is read in."
									htmlFor="srv-auto-timezone"
								>
									<div className="flex w-full max-w-[320px] flex-col gap-1.5">
										<Select
											value={draft.timezone}
											items={zones.map((zone) => ({
												value: zone,
												label: zone,
											}))}
											disabled={locked}
											onValueChange={(next) =>
												setDraft((d) => ({
													...d,
													timezone: String(next ?? "UTC"),
												}))
											}
										>
											<SelectTrigger id="srv-auto-timezone" className="w-full">
												<SelectValue />
											</SelectTrigger>
											<SelectContent alignItemWithTrigger={false}>
												{zones.map((zone) => (
													<SelectItem key={zone} value={zone}>
														{zone}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
										{shown?.timezone ? (
											<FieldError>{shown.timezone}</FieldError>
										) : wrongField === "timezone" ? (
											<FieldError>{wrongMessage}</FieldError>
										) : null}
									</div>
								</KvRow>
								<KvRow
									label="Reboot"
									description="Never: you are notified when a reboot is pending."
								>
									<div className="flex w-full flex-col gap-1.5">
										<Segmented
											aria-label="Reboot"
											font="sans"
											density="regular"
											value={draft.reboot}
											options={[
												{
													value: "never",
													label: "Never — notify me",
													disabled: locked,
												},
												{
													value: "whenRequired",
													label: "Reboot inside the window when required",
													disabled: locked,
												},
											]}
											onValueChange={(reboot) =>
												setDraft((d) => ({ ...d, reboot }))
											}
										/>
										{draft.reboot === "whenRequired" ? (
											<p className="m-0 text-ink-3 text-xs">{REBOOT_GUARDS}</p>
										) : null}
									</div>
								</KvRow>
								<KvRow
									label="Packages to hold"
									description={`Never upgraded by a scheduled run, up to ${HOLD_LIMIT}. Apply updates still upgrades them.`}
									className="items-start"
								>
									<div className="flex w-full flex-col gap-2">
										<StringList
											label="Packages to hold"
											rowLabel="package"
											values={draft.holdPackages}
											issues={holdIssues}
											max={HOLD_LIMIT}
											placeholder="openssl"
											addLabel="Hold a package"
											onChange={(holdPackages) =>
												setDraft((d) => ({ ...d, holdPackages }))
											}
										/>
										<p className="m-0 text-ink-3 text-xs">
											Docker and containerd are never updated automatically:{" "}
											<span className="font-mono">
												{auto.builtinHolds.join(", ")}
											</span>
											. Upgrade them with Apply updates, where you see the plan.
										</p>
									</div>
								</KvRow>
							</>
						) : null}

						{next || last || failures || native ? (
							<dl className="grid grid-cols-[minmax(96px,150px)_minmax(0,1fr)] items-baseline gap-x-4 gap-y-1.5 border-line border-t py-3.5 text-xs">
								{next ? (
									<>
										<dt className="type-label">Next window</dt>
										<dd className="text-ink-1">{next}</dd>
									</>
								) : null}
								{last ? (
									<>
										<dt className="type-label">Last scheduled run</dt>
										<dd className="flex flex-wrap items-baseline gap-x-2 text-ink-1">
											<span>{autoRunStatusText(last)}</span>
											<Relative iso={last.startedAt} />
											{view.runs.some((r) => r.id === last.id) ? (
												<button
													type="button"
													className="text-accent-text underline-offset-2 hover:underline"
													onClick={() =>
														document
															.getElementById(runAnchor(last.id))
															?.scrollIntoView?.({
																behavior: "smooth",
																block: "center",
															})
													}
												>
													Show in history
												</button>
											) : null}
										</dd>
									</>
								) : null}
								{failures ? (
									<>
										<dt className="type-label">Failures</dt>
										<dd className="text-warning-text">{failures}</dd>
									</>
								) : null}
								{native ? (
									<>
										<dt className="type-label">Host updater</dt>
										<dd className="text-ink-2">{native}</dd>
									</>
								) : null}
							</dl>
						) : null}
					</CardContent>
					<CardFooter className="justify-between">
						<span className="min-w-0 text-xs">
							{save.isError && wrongField === null ? (
								<FieldError>{wrongMessage}</FieldError>
							) : dirty ? (
								<span className="text-ink-3">You have unsaved changes.</span>
							) : (
								<span className="text-ink-3">No unsaved changes.</span>
							)}
						</span>
						<span className="flex items-center gap-2">
							{dirty ? (
								<Button
									type="button"
									variant="ghost"
									onClick={() => {
										setDraft(autoDraftOf(auto));
										setTouched(false);
										save.reset();
									}}
								>
									Discard
								</Button>
							) : null}
							<Button
								type="submit"
								variant="primary"
								disabled={!dirty || locked || save.isPending}
							>
								{save.isPending ? <Spinner /> : null}
								Save automatic updates
							</Button>
						</span>
					</CardFooter>
				</Card>
			</form>
			<ConfirmDialog
				open={askAck}
				onOpenChange={setAskAck}
				title={`Turn on automatic updates for ${server.name}?`}
				body={
					<div className="flex flex-col gap-3">
						<p className="m-0">
							{view.controlPlane ? "This server runs NebulaCtrl itself. " : ""}
							{view.productionServices.length > 0
								? `Production runs on this server: ${production}. `
								: ""}
							Every night inside the window packages are upgraded without anyone
							watching, and services can restart or be briefly unavailable
							{draft.reboot === "whenRequired"
								? ", and the server can reboot"
								: ""}
							.
						</p>
						<CheckLabel
							checked={accepted}
							onCheckedChange={setAccepted}
							checkboxClassName="mt-0.5"
							className="items-start"
						>
							I accept the nightly upgrades on this production server.
						</CheckLabel>
					</div>
				}
				cta="Save automatic updates"
				blocked={!accepted}
				onConfirm={async () => {
					await save.mutateAsync(true);
				}}
			/>
		</>
	);
}
