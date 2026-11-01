import { cn } from "cn";
import { ExternalLinkIcon } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { Service } from "#/api/models";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button, buttonVariants } from "#/components/ui/button";
import { FieldDescription, FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Label } from "#/components/ui/label";
import { Spinner } from "#/components/ui/spinner";
import { Textarea } from "#/components/ui/textarea";
import {
	buildOnBody,
	DEFAULT_MESSAGE,
	DEFAULT_TITLE,
	ETA_PRESETS,
	etaAfter,
	isDirty,
	joinLocalInput,
	MAX_MESSAGE,
	MAX_TITLE,
	type MaintenanceFormState,
	OFF_BODY,
	previewPath,
	splitLocalInput,
	toFormState,
	validateForm,
	zoneNote,
} from "./model";
import { useSetMaintenance } from "./use-set-maintenance";

export interface MaintenanceFormProps {
	service: Service;
	className?: string;
	/** Called after the server accepted a change. */
	onApplied?: () => void;
	/**
	 * Makes the submit button the surface's one key. Set it where the form is
	 * the whole surface (the dialog); inside Settings the save bar is the key.
	 */
	primary?: boolean;
}

/**
 * The page text, the expected return time and the switch. Like scaling it is
 * operational: applying changes the proxy at once and stages nothing.
 */
export function MaintenanceForm({
	service,
	className,
	onApplied,
	primary = false,
}: MaintenanceFormProps) {
	const id = useId();
	const live = service.maintenance;
	// `null` means untouched: the form follows the service as it refetches.
	const [edited, setEdited] = useState<MaintenanceFormState | null>(null);
	const baseline = useMemo(() => toFormState(live), [live]);
	const state = edited ?? baseline;
	const errors = useMemo(() => validateForm(state), [state]);
	const invalid = Object.keys(errors).length > 0;
	const dirty = isDirty(live, state);
	const eta = splitLocalInput(state.eta);
	const zone = useMemo(() => zoneNote(), []);
	const set = useSetMaintenance(service);

	const update = (patch: Partial<MaintenanceFormState>) => {
		set.reset();
		setEdited({ ...state, ...patch });
	};
	const apply = (on: boolean) => {
		set.mutate(on ? buildOnBody(state) : OFF_BODY, {
			onSuccess: () => {
				setEdited(null);
				onApplied?.();
			},
		});
	};

	return (
		<form
			className={cn("flex flex-col gap-4", className)}
			onSubmit={(submit) => {
				submit.preventDefault();
				if (!invalid && !set.isPending && (!live.enabled || dirty)) {
					apply(true);
				}
			}}
		>
			<div className="flex flex-col gap-1.5">
				<Label htmlFor={`${id}-title`}>Title</Label>
				<Input
					id={`${id}-title`}
					value={state.title}
					maxLength={MAX_TITLE * 2}
					placeholder={DEFAULT_TITLE}
					autoComplete="off"
					aria-invalid={errors.title ? true : undefined}
					aria-describedby={errors.title ? `${id}-title-err` : undefined}
					onChange={(e) => update({ title: e.target.value })}
				/>
				<FieldError id={`${id}-title-err`}>{errors.title}</FieldError>
			</div>
			<div className="flex flex-col gap-1.5">
				<Label htmlFor={`${id}-message`}>Message</Label>
				<Textarea
					id={`${id}-message`}
					rows={3}
					value={state.message}
					placeholder={DEFAULT_MESSAGE}
					aria-invalid={errors.message ? true : undefined}
					aria-describedby={`${id}-message-hint`}
					onChange={(e) => update({ message: e.target.value })}
				/>
				{errors.message ? (
					<FieldError id={`${id}-message-hint`}>{errors.message}</FieldError>
				) : (
					<FieldDescription id={`${id}-message-hint`}>
						Plain text, shown to visitors. {MAX_MESSAGE} characters at most.
					</FieldDescription>
				)}
			</div>
			<div className="flex flex-col gap-1.5">
				<Label htmlFor={`${id}-eta-date`}>Expected back (optional)</Label>
				<div className="flex flex-wrap items-center gap-2">
					<Input
						id={`${id}-eta-date`}
						inputMode="numeric"
						autoComplete="off"
						placeholder="YYYY-MM-DD"
						maxLength={10}
						className="w-[8.5rem] font-mono"
						aria-label="Expected back, date"
						value={eta.date}
						aria-invalid={errors.eta ? true : undefined}
						aria-describedby={`${id}-eta-hint`}
						onChange={(e) =>
							update({ eta: joinLocalInput(e.target.value, eta.time) })
						}
					/>
					<Input
						inputMode="numeric"
						autoComplete="off"
						placeholder="HH:mm"
						maxLength={5}
						className="w-[5.5rem] font-mono"
						aria-label="Expected back, time (24-hour)"
						value={eta.time}
						aria-invalid={errors.eta ? true : undefined}
						aria-describedby={`${id}-eta-hint`}
						onChange={(e) =>
							update({ eta: joinLocalInput(eta.date, e.target.value) })
						}
					/>
					{ETA_PRESETS.map((preset) => (
						<Button
							key={preset.label}
							type="button"
							size="sm"
							variant="secondary"
							onClick={() => update({ eta: etaAfter(preset.minutes) })}
						>
							{preset.label}
						</Button>
					))}
					{state.eta ? (
						<Button
							type="button"
							size="sm"
							variant="ghost"
							onClick={() => update({ eta: "" })}
						>
							Clear
						</Button>
					) : null}
				</div>
				{errors.eta ? (
					<FieldError id={`${id}-eta-hint`}>{errors.eta}</FieldError>
				) : (
					<FieldDescription id={`${id}-eta-hint`}>
						Times are shown in {zone}, 24-hour. Shown on the page and sent to
						clients and crawlers as Retry-After.
					</FieldDescription>
				)}
			</div>

			{set.isError ? (
				<Alert variant="destructive">
					<AlertDescription>{errorMessage(set.error)}</AlertDescription>
				</Alert>
			) : null}

			<div className="flex flex-wrap items-center gap-2">
				<a
					href={previewPath(service.id, state)}
					target="_blank"
					rel="noreferrer"
					className={buttonVariants({ variant: "ghost", size: "sm" })}
				>
					<ExternalLinkIcon aria-hidden />
					Preview page
				</a>
				<span className="ml-auto" />
				{live.enabled ? (
					<>
						<Button
							type="button"
							variant="secondary"
							disabled={set.isPending}
							onClick={() => apply(false)}
						>
							Turn off
						</Button>
						<Button
							type="submit"
							variant={primary ? "primary" : "secondary"}
							disabled={!dirty || invalid || set.isPending}
						>
							{set.isPending ? <Spinner /> : null}
							Update page
						</Button>
					</>
				) : (
					<Button
						type="submit"
						variant={primary ? "primary" : "secondary"}
						disabled={invalid || set.isPending}
					>
						{set.isPending ? <Spinner /> : null}
						Turn on maintenance
					</Button>
				)}
			</div>
		</form>
	);
}
