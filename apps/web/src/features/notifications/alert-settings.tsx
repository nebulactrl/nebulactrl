import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { SirenIcon } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { getServiceAlertSettingsOptions } from "#/api/gen/@tanstack/react-query.gen";
import { updateServiceAlertSettings } from "#/api/gen/sdk.gen";
import { invalidateOperations } from "#/api/invalidate";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { FieldDescription, FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Label } from "#/components/ui/label";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import {
	ALERT_FIELDS,
	type AlertForm,
	type AlertKey,
	isDirty,
	parseForm,
	toForm,
} from "./alert-settings-model";

/**
 * Team settings → Notifications → Service alerts: when a crash, a failing health
 * check, a restart loop or a missed backup becomes a message. A channel picks
 * the events it wants in its own event matrix (Services and Backups groups);
 * these thresholds apply to every channel.
 */
export function AlertSettings() {
	const id = useId();
	const queryClient = useQueryClient();
	const settings = useQuery(getServiceAlertSettingsOptions());
	// `null` means untouched: the form follows the saved settings as they refetch.
	const [edited, setEdited] = useState<AlertForm | null>(null);
	const baseline = useMemo(
		() => (settings.data ? toForm(settings.data) : null),
		[settings.data],
	);
	const form = edited ?? baseline;
	const parsed = useMemo(() => (form ? parseForm(form) : null), [form]);

	const save = useMutation({
		mutationFn: async (body: NonNullable<typeof parsed>["value"]) => {
			if (!body) return;
			await updateServiceAlertSettings({ body });
		},
		onSuccess: async () => {
			toast.success(
				"Alert thresholds saved",
				"They apply to every channel within a minute.",
			);
			setEdited(null);
			await invalidateOperations(queryClient, ["getServiceAlertSettings"]);
		},
		onError: (e) =>
			toast.error("Could not save the thresholds", errorMessage(e)),
	});

	if (settings.isPending) return <Skeleton className="h-40 rounded-card" />;
	if (!settings.data || !form || !parsed) {
		return (
			<Alert variant="destructive">
				<AlertDescription>
					{settings.isError
						? errorMessage(settings.error)
						: "The alert thresholds could not be loaded."}
				</AlertDescription>
			</Alert>
		);
	}
	const dirty = isDirty(settings.data, form);
	const set = (key: AlertKey, value: string) => {
		save.reset();
		setEdited({ ...form, [key]: value });
	};
	return (
		<form
			aria-label="Service alerts"
			className="surface-raised flex flex-col gap-4 rounded-card p-4"
			onSubmit={(submit) => {
				submit.preventDefault();
				if (parsed.value && dirty && !save.isPending) save.mutate(parsed.value);
			}}
		>
			<header className="flex flex-col gap-1">
				<h3 className="flex items-center gap-2 font-medium text-sm">
					<SirenIcon aria-hidden="true" className="size-4" />
					Service alerts
				</h3>
				<p className="text-ink-3 text-xs">
					A crash, a failing health check or a restart loop is announced once
					per incident, with a recovery message when the service is healthy
					again. A service that keeps failing is announced once and then paused
					until it has been stable. Turn the events on per channel (Services and
					Backups groups).
				</p>
			</header>
			<div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
				{ALERT_FIELDS.map((f) => {
					const error = parsed.errors[f.key];
					return (
						<div key={f.key} className="flex flex-col gap-1.5">
							<Label htmlFor={`${id}-${f.key}`}>{f.label}</Label>
							<div className="flex items-center gap-2">
								<Input
									id={`${id}-${f.key}`}
									inputMode="numeric"
									autoComplete="off"
									className="w-24 font-mono"
									value={form[f.key]}
									aria-invalid={error ? true : undefined}
									aria-describedby={`${id}-${f.key}-hint`}
									onChange={(e) => set(f.key, e.target.value)}
								/>
								<span className="text-ink-3 text-xs">{f.unit}</span>
							</div>
							{error ? (
								<FieldError id={`${id}-${f.key}-hint`}>{error}</FieldError>
							) : (
								<FieldDescription id={`${id}-${f.key}-hint`}>
									{f.hint}
								</FieldDescription>
							)}
						</div>
					);
				})}
			</div>
			{save.isError ? (
				<Alert variant="destructive">
					<AlertDescription>{errorMessage(save.error)}</AlertDescription>
				</Alert>
			) : null}
			<div className="flex items-center justify-end gap-2">
				<Button
					type="button"
					variant="ghost"
					disabled={!dirty || save.isPending}
					onClick={() => setEdited(null)}
				>
					Reset
				</Button>
				<Button
					type="submit"
					variant="primary"
					disabled={!dirty || !parsed.value || save.isPending}
				>
					{save.isPending ? <Spinner /> : null}
					Save thresholds
				</Button>
			</div>
		</form>
	);
}
