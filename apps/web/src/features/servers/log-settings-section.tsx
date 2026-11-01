import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getServerLogSettingsOptions } from "#/api/gen/@tanstack/react-query.gen";
import { updateServerLogSettings } from "#/api/gen/sdk.gen";
import type { LogSettingsView } from "#/api/gen/types.gen";
import { invalidateOperations } from "#/api/invalidate";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Card, CardContent, CardFooter } from "#/components/ui/card";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { KvRow } from "#/components/ui/kv-row";
import { Progress } from "#/components/ui/progress";
import { SectionLabel } from "#/components/ui/section-label";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { formatStamp } from "../logs/history/model";
import {
	CAP_UNITS,
	type CapField,
	draftOf,
	formatCap,
	type LogSettingsDraft,
	RETENTION_LIMITS,
	requestBody,
	shortId,
	topServices,
	usageLine,
	usagePercent,
	validate,
} from "./log-settings-model";
import type { ServerView } from "./model";
import { useServicesOnServer } from "./use-services-on-server";

const LOG_SETTINGS_OPERATIONS = ["getServerLogSettings"] as const;

/** One size: a number beside its unit, as wide as it needs to be. */
function CapInput({
	id,
	label,
	field,
	invalid,
	onChange,
}: {
	id: string;
	label: string;
	field: CapField;
	invalid: boolean;
	onChange(next: CapField): void;
}) {
	return (
		<div className="flex gap-2">
			<Input
				id={id}
				type="number"
				scale="lg"
				min={1}
				inputMode="decimal"
				className="w-[120px]"
				value={field.amount}
				aria-invalid={invalid}
				onChange={(e) => onChange({ ...field, amount: e.target.value })}
			/>
			<Select
				value={field.unit}
				items={[...CAP_UNITS]}
				onValueChange={(unit) => {
					if (unit === "MB" || unit === "GB") onChange({ ...field, unit });
				}}
			>
				<SelectTrigger
					aria-label={`Unit of ${label}`}
					className="data-[size=default]:h-9"
				>
					<SelectValue />
				</SelectTrigger>
				<SelectContent alignItemWithTrigger={false} align="start">
					{CAP_UNITS.map((unit) => (
						<SelectItem key={unit.value} value={unit.value}>
							{unit.label}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
		</div>
	);
}

/**
 * What a server stores of its services' output: whether it keeps it, for how
 * long, how much per service and in total, and how much it uses now (as the
 * agent last reported it). Saving works while the server is offline; the
 * agent picks the settings up when it reconnects.
 */
export function LogSettingsSection({ server }: { server: ServerView }) {
	const query = useQuery(
		getServerLogSettingsOptions({ path: { serverId: server.id } }),
	);
	if (query.isPending) {
		return <Skeleton className="h-64 rounded-card" />;
	}
	if (!query.data) {
		return (
			<Card>
				<CardContent className="py-4 text-ink-3 text-sm">
					Log settings did not load: {errorMessage(query.error)}
				</CardContent>
			</Card>
		);
	}
	return <LogSettingsForm server={server} view={query.data} />;
}

function LogSettingsForm({
	server,
	view,
}: {
	server: ServerView;
	view: LogSettingsView;
}) {
	const queryClient = useQueryClient();
	const [draft, setDraft] = useState<LogSettingsDraft>(() => draftOf(view));
	const baseline = JSON.stringify(draftOf(view));
	// A refetch that changed the settings (someone else saved) resets the form
	// only while it is clean, so typing is never overwritten.
	const lastBaseline = useRef(baseline);
	useEffect(() => {
		if (lastBaseline.current === baseline) return;
		const previous = lastBaseline.current;
		lastBaseline.current = baseline;
		setDraft((current) =>
			JSON.stringify(current) === previous ? draftOf(view) : current,
		);
	}, [baseline, view]);

	const { everywhere, pending } = useServicesOnServer(server.id);
	const names = useMemo(
		() => new Map(everywhere.map((s) => [s.id, s.name])),
		[everywhere],
	);

	const dirty = JSON.stringify(draft) !== baseline;
	const errors = validate(draft);
	const body = requestBody(draft);
	const [touched, setTouched] = useState(false);
	const shown = (key: keyof typeof errors) =>
		touched ? errors[key] : undefined;

	const save = useMutation({
		mutationFn: async () => {
			if (!body) return;
			await updateServerLogSettings({
				path: { serverId: server.id },
				body,
				throwOnError: true,
			});
		},
		onSuccess: async () => {
			setTouched(false);
			toast.success(
				"Log settings saved",
				view.online
					? "The agent applies them now."
					: "Changes apply when the server reconnects.",
			);
			await invalidateOperations(queryClient, LOG_SETTINGS_OPERATIONS);
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (!body || !dirty || save.isPending) return;
		save.mutate();
	};

	const usage = view.usage;
	const rows = topServices(usage, names);

	return (
		<form onSubmit={submit}>
			<Card>
				<CardContent className="py-1">
					<SectionLabel className="pt-4 pb-1">Stored logs</SectionLabel>
					<p className="m-0 pb-3 text-ink-3 text-xs leading-relaxed">
						Logs are stored on this server, compressed, and nowhere else. They
						are not available while the server is offline. When a limit is
						reached, the oldest lines go first.
					</p>
					{view.online ? null : (
						<Alert variant="info" className="mb-3">
							<AlertTitle>{server.name} is offline</AlertTitle>
							<AlertDescription>
								You can still change these settings. Changes apply when the
								server reconnects.
							</AlertDescription>
						</Alert>
					)}
					<KvRow
						label="Keep each service's output on this server"
						description="Off: nothing new is stored; what is stored stays until it expires. Search history and log drains read from it."
						htmlFor="srv-log-capture"
					>
						<Switch
							id="srv-log-capture"
							checked={draft.capture}
							onCheckedChange={(capture) =>
								setDraft((d) => ({ ...d, capture }))
							}
						/>
					</KvRow>
					<KvRow
						label="Keep logs for (days)"
						description={`${RETENTION_LIMITS.min}–${RETENTION_LIMITS.max}. Older lines are deleted.`}
						htmlFor="srv-log-days"
					>
						<div className="flex w-full max-w-[160px] flex-col gap-1.5">
							<Input
								id="srv-log-days"
								type="number"
								scale="lg"
								min={RETENTION_LIMITS.min}
								max={RETENTION_LIMITS.max}
								value={draft.retentionDays}
								aria-invalid={shown("retentionDays") !== undefined}
								onChange={(e) =>
									setDraft((d) => ({ ...d, retentionDays: e.target.value }))
								}
							/>
							{shown("retentionDays") ? (
								<FieldError>{shown("retentionDays")}</FieldError>
							) : null}
						</div>
					</KvRow>
					<KvRow
						label="Limit per service"
						description="The most one service may store. At least 32 MB."
						htmlFor="srv-log-service-cap"
					>
						<div className="flex flex-col gap-1.5">
							<CapInput
								id="srv-log-service-cap"
								label="the per-service limit"
								field={draft.perService}
								invalid={shown("perService") !== undefined}
								onChange={(perService) =>
									setDraft((d) => ({ ...d, perService }))
								}
							/>
							{shown("perService") ? (
								<FieldError>{shown("perService")}</FieldError>
							) : null}
						</div>
					</KvRow>
					<KvRow
						label="Limit for the server"
						description="The most all services together may store. At least 32 MB."
						htmlFor="srv-log-server-cap"
					>
						<div className="flex flex-col gap-1.5">
							<CapInput
								id="srv-log-server-cap"
								label="the server limit"
								field={draft.perServer}
								invalid={shown("perServer") !== undefined}
								onChange={(perServer) => setDraft((d) => ({ ...d, perServer }))}
							/>
							{shown("perServer") ? (
								<FieldError>{shown("perServer")}</FieldError>
							) : null}
						</div>
					</KvRow>

					<SectionLabel className="pt-5 pb-2">Space used</SectionLabel>
					<div className="flex flex-col gap-3 pb-4">
						{usage ? (
							<>
								<div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
									<span className="text-ink-1 text-sm tabular-nums">
										{usageLine(usage.totalBytes, view.maxServerBytes)}
									</span>
									<span className="text-ink-4 text-xs">
										Reported <Relative iso={usage.reportedAt} />
									</span>
								</div>
								<Progress
									value={usagePercent(usage.totalBytes, view.maxServerBytes)}
									warnAt={90}
									aria-label="Space used by stored logs on this server"
								/>
								{rows.length > 0 ? (
									<ul className="m-0 flex list-none flex-col gap-1.5 p-0">
										{rows.map((row) => (
											<li
												key={row.serviceId}
												className="flex flex-wrap items-baseline justify-between gap-x-3 text-xs"
											>
												<span className="min-w-0 truncate text-ink-2">
													{row.name ??
														(pending
															? `Service ${shortId(row.serviceId)}`
															: `Deleted service ${shortId(row.serviceId)}`)}
												</span>
												<span className="font-mono text-ink-3 tabular-nums">
													{formatCap(row.bytes)} · {row.lines.toLocaleString()}{" "}
													{row.lines === 1 ? "line" : "lines"}
													{row.oldest
														? ` · back to ${formatStamp(row.oldest)}`
														: ""}
												</span>
											</li>
										))}
									</ul>
								) : (
									<p className="m-0 text-ink-3 text-xs">
										No service has stored logs yet.
									</p>
								)}
								{!pending && rows.some((row) => row.name === undefined) ? (
									<p className="m-0 text-ink-3 text-xs">
										A deleted service's logs stay on this server until they
										expire.
									</p>
								) : null}
							</>
						) : (
							<p className="m-0 text-ink-3 text-xs">
								No usage reported yet. The agent reports it every few minutes
								once it stores logs.
							</p>
						)}
					</div>
				</CardContent>
				<CardFooter className="justify-between">
					<span className="min-w-0 text-xs">
						{save.isError ? (
							<FieldError>{errorMessage(save.error)}</FieldError>
						) : dirty ? (
							<span className="text-ink-3">
								You have unsaved changes.
								{view.online ? "" : " They apply when the server reconnects."}
							</span>
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
									setDraft(draftOf(view));
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
							disabled={!dirty || save.isPending}
						>
							{save.isPending ? <Spinner /> : null}
							Save log settings
						</Button>
					</span>
				</CardFooter>
			</Card>
		</form>
	);
}
