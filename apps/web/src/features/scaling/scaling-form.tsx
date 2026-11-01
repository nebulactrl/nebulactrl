import { useMutation, useQueryClient } from "@tanstack/react-query";
import { cn } from "cn";
import { type ChangeEvent, useId, useMemo, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { setProcessScaling, setServiceScaling } from "#/api/gen/sdk.gen";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { FieldDescription, FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Label } from "#/components/ui/label";
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { Stepper } from "#/components/ui/stepper";
import { toast } from "#/components/ui/toast";
import {
	appliedToast,
	buildScalingBody,
	MAX_REPLICAS,
	MAX_TARGET_CPU,
	MIN_REPLICAS,
	MIN_TARGET_CPU,
	readinessText,
	type ScalingFormState,
	type ScalingMode,
	scaleEventText,
	scaleSubject,
	scalingOfProcess,
	toFormState,
	usesServiceScaling,
	validateScaling,
} from "./model";
import { ReplicaStrip } from "./replica-strip";

const MODES = [
	{ value: "manual", label: "Manual" },
	{ value: "autoscale", label: "Autoscale" },
] as const;

/** What the server said, in words a person can act on. */
function applyErrorText(error: unknown): string {
	switch (errorCode(error)) {
		case "autoscaling_enabled":
			return "Autoscaling is on, so the replica count is managed for you. Switch to Manual to set it yourself.";
		case "replicas_require_stateless":
			return "Replicas need a stateless service: no volumes and an HTTP port.";
		default:
			return errorMessage(error);
	}
}

export interface ScalingFormProps {
	service: Service;
	/**
	 * The process to scale when the service has several; the default process
	 * (or the service as a whole, without processes) when absent.
	 */
	process?: string | undefined;
	className?: string;
	/** Called after the server accepted the change. */
	onApplied?: () => void;
	/**
	 * Apply is the surface's one key (default). Inside Settings the save bar is
	 * the key, so the section passes false and Apply is a secondary button.
	 */
	primary?: boolean;
}

/**
 * Replica count and CPU autoscaling of one service. Scaling is operational,
 * not configuration: Apply changes it immediately, it is never staged and it
 * does not touch the settings draft.
 */
export function ScalingForm({
	service,
	process,
	className,
	onApplied,
	primary = true,
}: ScalingFormProps) {
	const scaling = scalingOfProcess(service, process);
	const queryClient = useQueryClient();
	const id = useId();
	// `null` means untouched: the form follows the service as it refetches.
	const [edited, setEdited] = useState<ScalingFormState | null>(null);
	const baseline = useMemo(() => toFormState(scaling), [scaling]);
	const state = edited ?? baseline;
	const errors = useMemo(() => validateScaling(state), [state]);
	const invalid = Object.keys(errors).length > 0;
	const body = useMemo(
		() => buildScalingBody(scaling, state),
		[scaling, state],
	);

	const apply = useMutation({
		mutationFn: async () => {
			if (!body) return;
			const headers = { "Idempotency-Key": crypto.randomUUID() };
			if (process === undefined || usesServiceScaling(service, process)) {
				await setServiceScaling({
					path: { serviceId: service.id },
					body,
					headers,
				});
				return;
			}
			await setProcessScaling({
				path: { serviceId: service.id, process },
				body,
				headers,
			});
		},
		onSuccess: async () => {
			toast.success(appliedToast(scaleSubject(service, process), state));
			setEdited(null);
			await invalidateServices(queryClient);
			onApplied?.();
		},
	});

	const update = (patch: Partial<ScalingFormState>) => {
		apply.reset();
		setEdited({ ...state, ...patch });
	};

	if (!scaling.eligible) {
		return (
			<div className={cn("flex flex-col gap-3", className)}>
				<Alert variant="info">
					<AlertTitle>Runs as a single replica</AlertTitle>
					<AlertDescription>
						{scaling.ineligibleReason ??
							"Replicas need a stateless service: no volumes and an HTTP port."}
					</AlertDescription>
				</Alert>
				<p className="type-secondary">
					Databases and services with volumes always run exactly one replica, so
					their data is never written from two containers at once.
				</p>
			</div>
		);
	}

	const converging = scaling.ready !== scaling.replicas;
	const event = scaling.lastScaleEvent;
	const autoscale = state.mode === "autoscale";
	const numberChange =
		(key: "min" | "max" | "targetCpuPercent") =>
		(change: ChangeEvent<HTMLInputElement>) =>
			update({ [key]: change.target.valueAsNumber });
	const numberValue = (value: number) => (Number.isNaN(value) ? "" : value);

	return (
		<form
			className={cn("@container/scaling flex flex-col gap-4", className)}
			onSubmit={(submit) => {
				submit.preventDefault();
				if (body && !invalid && !apply.isPending) apply.mutate();
			}}
		>
			<div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
				<Segmented<ScalingMode>
					aria-label="Scaling mode"
					font="sans"
					density="regular"
					options={MODES}
					value={state.mode}
					onValueChange={(mode) => update({ mode })}
				/>
				<div className="flex flex-col items-end gap-1" aria-live="polite">
					<span className="type-micro">Replicas ready</span>
					<span className="type-metric text-ink-1">
						{scaling.ready} / {scaling.replicas}
					</span>
					<span className="flex items-center gap-2 text-ink-3 text-xs">
						{converging ? <Spinner className="size-3.5" /> : null}
						<span>{readinessText(scaling)}</span>
						{converging ? <span>· Waiting for replicas</span> : null}
					</span>
				</div>
			</div>
			<ReplicaStrip service={service} process={process} />

			{autoscale ? (
				<div className="flex flex-col gap-3">
					<div className="grid grid-cols-1 gap-3 @sm/scaling:grid-cols-3">
						<div className="flex flex-col gap-1.5">
							<Label htmlFor={`${id}-min`}>Minimum replicas</Label>
							<Input
								id={`${id}-min`}
								type="number"
								inputMode="numeric"
								min={MIN_REPLICAS}
								max={MAX_REPLICAS}
								step={1}
								value={numberValue(state.min)}
								aria-invalid={errors.min ? true : undefined}
								aria-describedby={errors.min ? `${id}-min-err` : undefined}
								onChange={numberChange("min")}
							/>
							<FieldError id={`${id}-min-err`}>{errors.min}</FieldError>
						</div>
						<div className="flex flex-col gap-1.5">
							<Label htmlFor={`${id}-max`}>Maximum replicas</Label>
							<Input
								id={`${id}-max`}
								type="number"
								inputMode="numeric"
								min={MIN_REPLICAS}
								max={MAX_REPLICAS}
								step={1}
								value={numberValue(state.max)}
								aria-invalid={errors.max ? true : undefined}
								aria-describedby={errors.max ? `${id}-max-err` : undefined}
								onChange={numberChange("max")}
							/>
							<FieldError id={`${id}-max-err`}>{errors.max}</FieldError>
						</div>
						<div className="flex flex-col gap-1.5">
							<Label htmlFor={`${id}-cpu`}>Target CPU %</Label>
							<Input
								id={`${id}-cpu`}
								type="number"
								inputMode="numeric"
								min={MIN_TARGET_CPU}
								max={MAX_TARGET_CPU}
								step={1}
								value={numberValue(state.targetCpuPercent)}
								aria-invalid={errors.targetCpuPercent ? true : undefined}
								aria-describedby={
									errors.targetCpuPercent ? `${id}-cpu-err` : undefined
								}
								onChange={numberChange("targetCpuPercent")}
							/>
							<FieldError id={`${id}-cpu-err`}>
								{errors.targetCpuPercent}
							</FieldError>
						</div>
					</div>
					<FieldDescription>
						Scales out after 2 min above target, in after 10 min below 35%.
						Target is measured against each replica's CPU limit (one core if
						none is set).
					</FieldDescription>
				</div>
			) : (
				<div className="flex flex-col gap-1.5">
					<span className="type-label">Replicas</span>
					<Stepper
						aria-label="Replicas"
						{...(errors.replicas
							? { "aria-describedby": `${id}-replicas-err` }
							: {})}
						min={MIN_REPLICAS}
						max={MAX_REPLICAS}
						value={Number.isNaN(state.replicas) ? MIN_REPLICAS : state.replicas}
						onValueChange={(replicas) => update({ replicas })}
					/>
					<FieldError id={`${id}-replicas-err`}>{errors.replicas}</FieldError>
					<FieldDescription>
						Traffic is balanced across replicas on the same server. Changing the
						count never rebuilds the service.
					</FieldDescription>
				</div>
			)}

			{scaling.blockedReason ? (
				<Alert variant="warning">
					<AlertTitle>Scaling blocked</AlertTitle>
					<AlertDescription>{scaling.blockedReason}</AlertDescription>
				</Alert>
			) : null}

			{event ? (
				<p className="type-secondary">
					{scaleEventText(event)} · <Relative iso={event.at} />
				</p>
			) : null}

			<div className="flex flex-wrap items-center justify-end gap-3">
				{apply.isError ? (
					<Alert variant="destructive" className="mr-auto min-w-0 flex-1">
						<AlertDescription>{applyErrorText(apply.error)}</AlertDescription>
					</Alert>
				) : null}
				<Button
					type="submit"
					variant={primary ? "primary" : "secondary"}
					disabled={!body || invalid || apply.isPending}
				>
					{apply.isPending ? <Spinner /> : null}
					Apply
				</Button>
			</div>
		</form>
	);
}
