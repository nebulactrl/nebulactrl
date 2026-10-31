import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRightIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	cancelServerDrain,
	planServerDrain,
	startServerDrain,
	undrainServer,
} from "#/api/gen/sdk.gen";
import type { DrainPlan, DrainView, Move } from "#/api/gen/types.gen";
import { serversQuery } from "#/api/hooks";
import { invalidateOperations } from "#/api/invalidate";
import type { Server } from "#/api/models";
import { ToneIcon } from "#/components/common/tone-icon";
import { HoldActionButton } from "#/components/motion/hold-action-button";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { Field, FieldLabel } from "#/components/ui/field";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import {
	Sheet,
	SheetBody,
	SheetContent,
	SheetDescription,
	SheetFooter,
	SheetHeader,
	SheetTitle,
} from "#/components/ui/sheet";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { toast } from "#/components/ui/toast";
import { DrainChip } from "./drain-chip";
import {
	canSubmitDrain,
	deriveDrainProgress,
	drainBar,
	isSchedulable,
	type MoveTone,
	moveAppearance,
	movesFromPlan,
	planReadiness,
	unschedulableReason,
} from "./model";
import { serverDrainQuery } from "./queries";

const AUTO = "auto";

const DRAIN_OPERATIONS = [
	"getServer",
	"listServers",
	"getServerDrain",
	"getFleet",
	"listServices",
] as const;

export interface DrainSheetServer {
	id: string;
	name: string;
	status: Server["status"];
	drainState: Server["drainState"];
	drainMessage: string;
}

const MOVE_KIND: Record<MoveTone, StatusKind> = {
	pending: "pending",
	active: "deploying",
	done: "live",
	failed: "failed",
};

function MoveList({ moves }: { moves: readonly Move[] }) {
	return (
		<ol className="flex flex-col gap-1.5" aria-label="Moves">
			{moves.map((move) => {
				const appearance = moveAppearance(move);
				return (
					<li
						key={move.serviceId}
						className="surface-well flex flex-col gap-1 rounded-card px-3 py-2.5"
					>
						<div className="flex items-center gap-2">
							<StatusIcon kind={MOVE_KIND[appearance.tone]} label="" />
							<span className="min-w-0 flex-1 truncate font-medium text-[13px]">
								{move.serviceName}
							</span>
							{move.stateful ? <Badge tone="warn">stateful</Badge> : null}
						</div>
						<div className="pl-[22px] text-ink-3 text-xs leading-snug">
							{move.projectName} / {move.environmentName} →{" "}
							{move.targetServerName} · {appearance.label}
						</div>
						{move.message ? (
							<p className="pl-[22px] text-destructive-text text-xs leading-snug">
								{move.message}
							</p>
						) : null}
					</li>
				);
			})}
		</ol>
	);
}

function PlanTable({ plan }: { plan: DrainPlan }) {
	const moves = plan.moves ?? [];
	if (moves.length === 0) {
		return (
			<p className="surface-well rounded-card px-3 py-4 text-center text-ink-3 text-sm">
				No services are placed on this server. Draining just closes it to new
				services.
			</p>
		);
	}
	return (
		<ol className="flex flex-col gap-2" aria-label="Planned moves">
			{moves.map((move) => (
				<li
					key={move.serviceId}
					className="surface-well flex flex-col gap-1.5 rounded-card px-3 py-2.5"
				>
					<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
						<span className="font-medium text-[13px]">{move.serviceName}</span>
						<span className="type-mono text-ink-4">
							{move.projectName} / {move.environmentName}
						</span>
						{move.stateful ? <Badge tone="warn">stateful</Badge> : null}
						<span className="ml-auto flex items-center gap-1.5 text-[13px]">
							<ArrowRightIcon className="size-3.5 text-ink-3" />
							{move.targetServerId ? (
								<span className="font-medium">{move.targetServerName}</span>
							) : (
								<span className="text-destructive-text">no target</span>
							)}
						</span>
					</div>
					{(move.blocking ?? []).map((item) => (
						<Alert key={item.code} variant="destructive">
							<AlertDescription>{item.message}</AlertDescription>
						</Alert>
					))}
					{(move.warnings ?? []).map((warning) =>
						move.stateful && warning.startsWith("Volumes are not copied") ? (
							<Alert key={warning} variant="warning">
								<AlertDescription>{warning}</AlertDescription>
							</Alert>
						) : (
							<p key={warning} className="text-ink-3 text-xs leading-snug">
								{warning}
							</p>
						),
					)}
				</li>
			))}
		</ol>
	);
}

/**
 * Drain a server: pick where its services go, read the plan (stateful
 * services, capacity, blocking problems), confirm by holding, then follow the
 * moves one by one. Failed and drained servers get their next step here too.
 */
export function DrainSheet({
	server,
	open,
	onOpenChange,
}: {
	server: DrainSheetServer;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const queryClient = useQueryClient();
	const servers = useQuery({ ...serversQuery(), enabled: open });
	const [target, setTarget] = useState(AUTO);
	const [confirmed, setConfirmed] = useState(false);
	const [replan, setReplan] = useState(false);

	const running = server.drainState === "draining";
	const drainQuery = useQuery({
		...serverDrainQuery(server.id, running),
		enabled: open,
	});
	const drain: DrainView | null = drainQuery.data ?? null;

	const phase: "plan" | "progress" | "failed" | "drained" = running
		? "progress"
		: server.drainState === "drained"
			? "drained"
			: server.drainState === "failed" && !replan
				? "failed"
				: "plan";

	// A fresh opening starts from a clean plan.
	const wasOpen = useRef(open);
	useEffect(() => {
		if (open && !wasOpen.current) {
			setTarget(AUTO);
			setConfirmed(false);
			setReplan(false);
		}
		wasOpen.current = open;
	}, [open]);

	const planning = open && phase === "plan";
	const plan = useQuery({
		queryKey: ["drain-plan", server.id, target],
		queryFn: async (): Promise<DrainPlan> => {
			const { data } = await planServerDrain({
				path: { serverId: server.id },
				body: target === AUTO ? {} : { targetServerId: target },
			});
			return data;
		},
		enabled: planning,
		staleTime: 0,
		gcTime: 0,
		retry: false,
	});
	const readiness = plan.data ? planReadiness(plan.data) : null;

	const refresh = () => invalidateOperations(queryClient, DRAIN_OPERATIONS);

	const start = useMutation({
		mutationFn: async () => {
			if (!plan.data) return;
			await startServerDrain({
				path: { serverId: server.id },
				body: {
					moves: movesFromPlan(plan.data.moves ?? []),
					confirmStateful: confirmed,
				},
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
		},
		onSuccess: async () => {
			setReplan(false);
			setConfirmed(false);
			await refresh();
			toast.success(
				`Draining ${server.name}`,
				"Services move one at a time; follow them here.",
			);
		},
	});
	const cancel = useMutation({
		mutationFn: async () => {
			await cancelServerDrain({ path: { serverId: server.id } });
		},
		onSuccess: async () => {
			await refresh();
			toast.success(
				"Cancel requested",
				"The drain stops after the move in flight.",
			);
		},
	});
	const undrain = useMutation({
		mutationFn: async () => {
			await undrainServer({ path: { serverId: server.id } });
		},
		onSuccess: async () => {
			setReplan(false);
			await refresh();
			toast.success(
				`${server.name} accepts services again`,
				"Services that already moved stay where they are.",
			);
		},
	});

	const targets = (servers.data ?? []).filter(
		(candidate) =>
			candidate.id !== server.id &&
			candidate.status !== "revoked" &&
			candidate.status !== "pending",
	);
	const targetItems = [
		{ value: AUTO, label: "Auto: most free memory" },
		...targets.map((candidate) => ({
			value: candidate.id,
			label: candidate.name,
		})),
	];

	const progress = drain ? deriveDrainProgress(drain) : null;
	const drainShape = progress
		? drainBar(progress)
		: { value: 0, max: 1, segments: null };
	const submittable =
		readiness !== null &&
		canSubmitDrain(readiness, confirmed) &&
		!start.isPending;

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="sm:max-w-[620px]">
				<SheetHeader className="pb-4">
					<div className="flex items-center gap-3 pr-10">
						<SheetTitle>Drain {server.name}</SheetTitle>
						<DrainChip state={server.drainState} />
					</div>
					<SheetDescription>
						Moves every service off this server and closes it to new ones. Each
						service is redeployed on its target; the old container is removed
						once the new one is live.
					</SheetDescription>
				</SheetHeader>
				<SheetBody className="flex flex-col gap-4 overscroll-contain">
					{phase === "plan" ? (
						<>
							{server.drainState === "failed" ? (
								<Alert variant="warning">
									<AlertTitle>Planning the rest</AlertTitle>
									<AlertDescription>
										The last drain stopped early. Services that already moved
										are not on this server any more, so the plan covers what is
										left.
									</AlertDescription>
								</Alert>
							) : null}
							<Field>
								<FieldLabel htmlFor="drain-target">Send services to</FieldLabel>
								<Select
									value={target}
									items={targetItems}
									onValueChange={(next) => {
										setTarget(String(next ?? AUTO));
										setConfirmed(false);
									}}
								>
									<SelectTrigger id="drain-target" className="w-full">
										<SelectValue />
									</SelectTrigger>
									<SelectContent alignItemWithTrigger={false}>
										<SelectItem value={AUTO}>
											<span className="font-medium">Auto</span>
											<span className="text-[11px] text-ink-3">
												the online server with the most free memory, per service
											</span>
										</SelectItem>
										{targets.map((candidate) => {
											const reason = unschedulableReason(candidate);
											const closed =
												!isSchedulable(candidate) ||
												candidate.status !== "online";
											return (
												<SelectItem
													key={candidate.id}
													value={candidate.id}
													disabled={closed}
												>
													<span className="font-medium">{candidate.name}</span>
													{closed ? (
														<span className="text-[11px] text-warning-text">
															{reason ?? candidate.status}
														</span>
													) : null}
												</SelectItem>
											);
										})}
									</SelectContent>
								</Select>
							</Field>

							{plan.isPending ? (
								<div
									className="flex flex-col gap-2"
									role="status"
									aria-label="Planning the moves"
								>
									<Skeleton className="h-16 rounded-lg" />
									<Skeleton className="h-16 rounded-lg" />
								</div>
							) : plan.isError ? (
								<Alert variant="destructive">
									<AlertTitle>No plan</AlertTitle>
									<AlertDescription>
										{errorMessage(plan.error)}
									</AlertDescription>
								</Alert>
							) : plan.data ? (
								<>
									<PlanTable plan={plan.data} />
									{plan.data.capacity.targetFits ? (
										(plan.data.capacity.reasons ?? []).length > 0 ? (
											<ul className="text-ink-3 text-xs leading-snug">
												{(plan.data.capacity.reasons ?? []).map((reason) => (
													<li key={reason}>{reason}</li>
												))}
											</ul>
										) : (plan.data.moves ?? []).length > 0 ? (
											<p className="flex items-center gap-1.5 text-success-text text-xs">
												<ToneIcon tone="success" />
												The targets have room for everything.
											</p>
										) : null
									) : (
										<Alert variant="warning">
											<AlertTitle>Not enough memory on a target</AlertTitle>
											<AlertDescription>
												<ul>
													{(plan.data.capacity.reasons ?? []).map((reason) => (
														<li key={reason}>{reason}</li>
													))}
												</ul>
											</AlertDescription>
										</Alert>
									)}
									{readiness?.needsConfirmation ? (
										<CheckLabel
											checked={confirmed}
											onCheckedChange={setConfirmed}
											className="surface-well items-start gap-2 rounded-card border-warning-border bg-warning-tint px-3 py-2.5"
											checkboxClassName="mt-px"
											labelClassName="text-[13px] leading-snug"
										>
											<span className="font-medium">
												Volumes are not copied.
											</span>{" "}
											{readiness.statefulServices.join(", ")}{" "}
											{readiness.statefulServices.length === 1
												? "starts"
												: "start"}{" "}
											with empty volumes on the target. I have moved the data,
											or I accept losing it.
										</CheckLabel>
									) : null}
								</>
							) : null}
							{start.isError ? (
								<Alert variant="destructive">
									<AlertDescription>
										{errorMessage(start.error)}
									</AlertDescription>
								</Alert>
							) : null}
						</>
					) : null}

					{phase === "progress" ? (
						<>
							<div className="flex flex-col gap-2">
								<div className="flex items-center justify-between gap-3 text-[13px]">
									<span className="font-medium">
										{progress?.label ?? "Starting…"}
									</span>
									<span className="type-mono text-ink-3 tabular-nums">
										{progress?.percent ?? 0}%
									</span>
								</div>
								<DitherProgressBar
									label="Drain progress"
									hideLabel
									value={drainShape.value}
									max={drainShape.max}
									valueText={progress?.label ?? "Starting"}
									{...(drainShape.segments === null
										? {}
										: { segments: drainShape.segments })}
								/>
							</div>
							{drain ? (
								<MoveList moves={drain.moves ?? []} />
							) : (
								<Skeleton className="h-24 rounded-card" />
							)}
							{drain?.cancelRequested ? (
								<p className="text-ink-3 text-xs">
									Cancel requested: the drain stops after the move in flight.
								</p>
							) : null}
							{cancel.isError ? (
								<Alert variant="destructive">
									<AlertDescription>
										{errorMessage(cancel.error)}
									</AlertDescription>
								</Alert>
							) : null}
						</>
					) : null}

					{phase === "failed" ? (
						<>
							<Alert variant="destructive">
								<AlertTitle>Drain stopped</AlertTitle>
								<AlertDescription>
									{server.drainMessage || drain?.message || "A move failed."}{" "}
									The remaining services were not touched and keep running here.
								</AlertDescription>
							</Alert>
							{drain ? <MoveList moves={drain.moves ?? []} /> : null}
							{undrain.isError ? (
								<Alert variant="destructive">
									<AlertDescription>
										{errorMessage(undrain.error)}
									</AlertDescription>
								</Alert>
							) : null}
						</>
					) : null}

					{phase === "drained" ? (
						<>
							<Alert variant="success">
								<AlertTitle>{server.name} is drained</AlertTitle>
								<AlertDescription>
									No services run here and it accepts no new ones. Undrain it to
									use it again; the services that moved stay where they are.
								</AlertDescription>
							</Alert>
							{drain && drain.state === "completed" ? (
								<MoveList moves={drain.moves ?? []} />
							) : null}
							{undrain.isError ? (
								<Alert variant="destructive">
									<AlertDescription>
										{errorMessage(undrain.error)}
									</AlertDescription>
								</Alert>
							) : null}
						</>
					) : null}
				</SheetBody>
				<SheetFooter>
					<Button variant="ghost" onClick={() => onOpenChange(false)}>
						Close
					</Button>
					{phase === "plan" ? (
						<HoldActionButton
							type="horizontal"
							holdDuration={1300}
							disabled={!submittable}
							refused={!submittable}
							holdingLabel="Keep holding…"
							completeLabel={
								start.isPending ? (
									<>
										<Spinner data-icon="inline-start" />
										Starting
									</>
								) : (
									"Draining"
								)
							}
							onHoldComplete={() => start.mutate()}
							className="h-[34px] min-w-0 px-4"
						>
							Hold to drain {server.name}
						</HoldActionButton>
					) : null}
					{phase === "progress" ? (
						<Button
							variant="secondary"
							disabled={cancel.isPending || drain?.cancelRequested === true}
							onClick={() => cancel.mutate()}
						>
							{cancel.isPending ? <Spinner data-icon="inline-start" /> : null}
							{drain?.cancelRequested ? "Canceling…" : "Cancel drain"}
						</Button>
					) : null}
					{phase === "failed" ? (
						<>
							<Button variant="secondary" onClick={() => setReplan(true)}>
								Retry (re-plan)
							</Button>
							<Button
								variant="primary"
								disabled={undrain.isPending}
								onClick={() => undrain.mutate()}
							>
								{undrain.isPending ? (
									<Spinner data-icon="inline-start" />
								) : null}
								Undrain
							</Button>
						</>
					) : null}
					{phase === "drained" ? (
						<Button
							variant="primary"
							disabled={undrain.isPending}
							onClick={() => undrain.mutate()}
						>
							{undrain.isPending ? <Spinner data-icon="inline-start" /> : null}
							Undrain
						</Button>
					) : null}
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}
