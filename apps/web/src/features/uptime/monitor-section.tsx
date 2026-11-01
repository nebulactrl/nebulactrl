import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { updateUptimeMonitorMutation } from "#/api/gen/@tanstack/react-query.gen";
import { invalidateOperations } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
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
import {
	Section,
	SectionBody,
	SettingRow,
	TextRow,
} from "#/features/service-settings/fields";
import {
	buildMonitorBody,
	checksSpan,
	type MonitorBody,
	type MonitorForm,
	monitorChanged,
	type ReminderChoice,
	reminderText,
	toMonitorForm,
	validateMonitor,
} from "./model";
import { MONITOR_OPERATIONS, uptimeMonitorQuery } from "./queries";

/** The reminder choices, in the order the menu lists them. */
const REMINDERS: ReadonlyArray<{ value: ReminderChoice; label: string }> = [
	{ value: "off", label: "Off" },
	{ value: "15", label: "Every 15 min" },
	{ value: "30", label: "Every 30 min" },
	{ value: "60", label: "Every hour" },
	{ value: "120", label: "Every 2 hours" },
	{ value: "custom", label: "Custom" },
];

const ID = {
	enabled: "set-uptime-enabled",
	alerts: "set-uptime-alerts",
	failure: "set-uptime-failure",
	recovery: "set-uptime-recovery",
	reminder: "set-uptime-reminder",
	reminderMinutes: "set-uptime-reminder-minutes",
} as const;

/** What a threshold means in time, once it is a valid number. */
function spanText(value: string, then: string): string | undefined {
	const n = Number(value.trim());
	if (!Number.isInteger(n) || n < 1) return undefined;
	return `Checks run every 30 s, so ${then} after about ${checksSpan(n)}.`;
}

function SwitchRow({
	id,
	label,
	description,
	checked,
	text,
	onChange,
}: {
	id: string;
	label: string;
	description: string;
	checked: boolean;
	text: string;
	onChange(next: boolean): void;
}) {
	return (
		<SettingRow label={label} htmlFor={id} description={description}>
			<div className="flex items-center gap-3">
				<Switch id={id} checked={checked} onCheckedChange={onChange} />
				<span className="text-[13px] text-ink-2">{text}</span>
			</div>
		</SettingRow>
	);
}

function MonitorFields({
	service,
	saved,
}: {
	service: Pick<Service, "id" | "name">;
	saved: MonitorBody;
}) {
	const queryClient = useQueryClient();
	// `null` means untouched: the form follows the saved monitor as it refetches.
	const [edited, setEdited] = useState<MonitorForm | null>(null);
	const baseline = useMemo(() => toMonitorForm(saved), [saved]);
	const form = edited ?? baseline;
	const errors = useMemo(() => validateMonitor(form), [form]);
	const body = useMemo(() => buildMonitorBody(form), [form]);
	const dirty = edited !== null && monitorChanged(form, saved);

	const save = useMutation({
		...updateUptimeMonitorMutation(),
		onSuccess: async () => {
			toast.success(
				`Uptime monitoring of ${service.name} applied`,
				"The agent picks it up within seconds. No deploy ran.",
			);
			setEdited(null);
			await invalidateOperations(queryClient, MONITOR_OPERATIONS);
		},
	});

	const update = (patch: Partial<MonitorForm>) => {
		save.reset();
		setEdited({ ...form, ...patch });
	};

	return (
		<>
			<SwitchRow
				id={ID.enabled}
				label="Monitor this service"
				description="Off stops the checks, pauses the service's state and closes an open outage without a recovery notice."
				checked={form.enabled}
				text={form.enabled ? "Checked every 30 seconds" : "Not checked"}
				onChange={(enabled) => update({ enabled })}
			/>
			<SwitchRow
				id={ID.alerts}
				label="Alerts"
				description="Send a down and a recovered notice to the channels that subscribe to them. Outages are recorded either way."
				checked={form.alerts}
				text={form.alerts ? "Alert on outages" : "Record only"}
				onChange={(alerts) => update({ alerts })}
			/>
			<TextRow
				id={ID.failure}
				label="Failed checks before an outage"
				description={
					spanText(form.failureThreshold, "an outage opens") ??
					"From 1 to 20 failed checks in a row."
				}
				type="number"
				narrow
				value={form.failureThreshold}
				error={errors.failureThreshold}
				onChange={(failureThreshold) => update({ failureThreshold })}
			/>
			<TextRow
				id={ID.recovery}
				label="Passing checks to recover"
				description={
					spanText(form.recoveryThreshold, "it counts as recovered") ??
					"From 1 to 20 passing checks in a row."
				}
				type="number"
				narrow
				value={form.recoveryThreshold}
				error={errors.recoveryThreshold}
				onChange={(recoveryThreshold) => update({ recoveryThreshold })}
			/>
			<SettingRow
				label="Remind while down"
				htmlFor={ID.reminder}
				description={
					form.alerts
						? "Send the down alert again at this interval while an outage lasts. Not sent during maintenance."
						: "Needs alerts on."
				}
			>
				<Select
					value={form.reminder}
					items={REMINDERS}
					disabled={!form.alerts}
					onValueChange={(next) => update({ reminder: next as ReminderChoice })}
				>
					<SelectTrigger id={ID.reminder} className="w-full max-w-[200px]">
						<SelectValue />
					</SelectTrigger>
					<SelectContent alignItemWithTrigger={false}>
						{REMINDERS.map((choice) => (
							<SelectItem key={choice.value} value={choice.value}>
								{choice.label}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
				{form.reminder === "custom" ? (
					<>
						<div className="flex items-center gap-2">
							<Input
								id={ID.reminderMinutes}
								aria-label="Reminder interval in minutes"
								inputMode="numeric"
								autoComplete="off"
								spellCheck={false}
								disabled={!form.alerts}
								value={form.reminderCustom}
								aria-invalid={errors.reminderCustom !== undefined}
								aria-describedby={
									errors.reminderCustom
										? `${ID.reminderMinutes}-error`
										: undefined
								}
								className="max-w-[120px] font-mono"
								onChange={(event) =>
									update({ reminderCustom: event.target.value })
								}
							/>
							<span className="text-ink-3 text-xs">minutes</span>
						</div>
						<FieldError id={`${ID.reminderMinutes}-error`}>
							{errors.reminderCustom}
						</FieldError>
					</>
				) : null}
			</SettingRow>
			<SectionBody className="flex flex-wrap items-center justify-end gap-3">
				{save.isError ? (
					<Alert variant="destructive" className="mr-auto min-w-0 flex-1">
						<AlertDescription>{errorMessage(save.error)}</AlertDescription>
					</Alert>
				) : (
					<span className="mr-auto text-ink-3 text-xs">
						{dirty
							? "Unsaved changes"
							: `Reminders: ${reminderText(saved.reminderMinutes)}`}
					</span>
				)}
				<Button
					variant="ghost"
					disabled={!dirty || save.isPending}
					onClick={() => {
						save.reset();
						setEdited(null);
					}}
				>
					Discard
				</Button>
				<Button
					variant="secondary"
					disabled={!dirty || body === null || save.isPending}
					onClick={() => {
						if (body) save.mutate({ path: { serviceId: service.id }, body });
					}}
				>
					{save.isPending ? <Spinner /> : null}
					Apply
				</Button>
			</SectionBody>
		</>
	);
}

/**
 * Settings → Uptime monitoring. Like Scaling and Maintenance it is not part of
 * the settings draft: the monitor lives beside the service, not in a release,
 * so Apply acts at once, the agent learns it within seconds and no deploy is
 * needed. Changing it needs a workspace owner or admin.
 */
export function MonitorSection({
	service,
}: {
	service: Pick<Service, "id" | "name">;
}) {
	const monitor = useQuery(uptimeMonitorQuery(service.id));
	return (
		<Section
			title="Uptime monitoring"
			field="uptime"
			description="The agent on this service's server checks it every 30 seconds and the control plane judges the checks. These settings apply immediately, without a deploy."
			aside={monitor.data ? (monitor.data.enabled ? "Monitored" : "Off") : null}
		>
			{monitor.data ? (
				<MonitorFields service={service} saved={monitor.data} />
			) : monitor.isError ? (
				<SectionBody>
					<Alert variant="destructive">
						<AlertDescription>{errorMessage(monitor.error)}</AlertDescription>
					</Alert>
				</SectionBody>
			) : (
				<SectionBody className="flex flex-col gap-3 py-2">
					<Skeleton className="h-8 w-full" />
					<Skeleton className="h-8 w-full" />
					<Skeleton className="h-8 w-3/4" />
				</SectionBody>
			)}
		</Section>
	);
}
