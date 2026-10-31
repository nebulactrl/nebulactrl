import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useId, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { promoteDatabaseReplicaMutation } from "#/api/gen/@tanstack/react-query.gen";
import type { PromotePlan } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Field, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { formatBytes } from "#/lib/format";
import {
	canConfirmPromote,
	dataLossText,
	groupConsumers,
	issueMessages,
	promotionLine,
} from "./model";
import { invalidateReplication, useReplication } from "./queries";

/** The line the contract fixes: promoting is always the user's act. */
export const MANUAL_NOTE =
	"Manual. NebulaCtrl does not detect failures or switch automatically.";

export interface PromoteDialogProps {
	/** The replica to promote. */
	service: Pick<Service, "id" | "name" | "slug">;
	open: boolean;
	onOpenChange(open: boolean): void;
}

/**
 * Promote to primary: the dry-run plan (switchover or forced, the lag, the
 * consumers that are rewritten, the replicas that must be rebuilt), a typed
 * confirmation and, for a forced promote, an explicit acceptance of the data
 * loss. After confirming it follows the promotion's steps.
 */
export function PromoteDialog({
	service,
	open,
	onOpenChange,
}: PromoteDialogProps) {
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				<PromoteBody service={service} onClose={() => onOpenChange(false)} />
			</DialogContent>
		</Dialog>
	);
}

function Section({ title, children }: { title: string; children: ReactNode }) {
	return (
		<section aria-label={title} className="flex flex-col gap-1.5">
			<h3 className="type-label">{title}</h3>
			{children}
		</section>
	);
}

function Plan({ plan }: { plan: PromotePlan }) {
	const groups = groupConsumers(plan.consumers);
	const rebuild = plan.rebuildReplicas ?? [];
	const warnings = issueMessages(plan.warnings);
	const blockers = issueMessages(plan.blockers);
	const switchover = plan.mode === "switchover";
	return (
		<>
			<div className="flex flex-wrap items-center gap-2 text-[13px]">
				<Badge variant={switchover ? "accent" : "warning"}>
					{switchover ? "switchover" : "forced"}
				</Badge>
				<span className="text-ink-1">
					{switchover
						? `${plan.oldPrimary.slug} is reachable: it is frozen, stopped and fenced, then ${plan.replica.slug} takes over after catching up. No data is lost.`
						: `${plan.oldPrimary.slug} is not reachable: ${plan.replica.slug} is promoted as it is.`}
				</span>
			</div>

			<p className="text-[13px] text-ink-2" aria-live="polite">
				{plan.lagBytes == null
					? "The replica's lag is not known."
					: plan.lagBytes === 0
						? "The replica is in sync with the primary."
						: `The replica is ${formatBytes(plan.lagBytes)} behind the primary${
								plan.lagObservedAt
									? ` as of ${new Date(plan.lagObservedAt).toLocaleTimeString()}`
									: ""
							}.`}
			</p>

			<Section title="Consumers that are rewritten">
				{groups.length === 0 ? (
					<p className="text-[13px] text-ink-3">
						No service references {plan.oldPrimary.slug}'s writer keys.
					</p>
				) : (
					<ul className="flex flex-col gap-2">
						{groups.map((group) => (
							<li key={group.server} className="flex flex-col gap-1">
								<span className="flex items-center gap-2 font-mono text-2xs text-ink-2">
									{group.server}
									{group.devicesAdded > 0 ? (
										<Badge tone="info">
											adds {group.devicesAdded}{" "}
											{group.devicesAdded === 1
												? "mesh device"
												: "mesh devices"}
										</Badge>
									) : null}
								</span>
								<ul className="flex list-disc flex-col gap-0.5 pl-5 text-[13px] text-ink-2">
									{group.consumers.map((consumer) => (
										<li key={consumer.serviceId}>
											<span className="text-ink-1">{consumer.slug}</span>
											{(consumer.keys ?? []).length > 0 ? (
												<span className="font-mono text-2xs text-ink-3">
													{" "}
													· {(consumer.keys ?? []).join(", ")}
												</span>
											) : null}
											{issueMessages(consumer.warnings).map((message) => (
												<span
													key={message}
													className="block text-2xs text-warning-text"
												>
													{message}
												</span>
											))}
										</li>
									))}
								</ul>
							</li>
						))}
					</ul>
				)}
				{groups.length > 0 ? (
					<p className="text-2xs text-ink-3">
						Their references to {plan.oldPrimary.slug} become references to{" "}
						{plan.replica.slug} as staged changes; nothing is deployed until you
						deploy them.
					</p>
				) : null}
			</Section>

			{rebuild.length > 0 ? (
				<Section title="Replicas that must be rebuilt">
					<ul className="flex list-disc flex-col gap-0.5 pl-5 text-[13px] text-ink-2">
						{rebuild.map((row) => (
							<li key={row.id}>{row.slug}</li>
						))}
					</ul>
					<p className="text-2xs text-ink-3">
						They followed the old primary, so each needs a rebuild after the
						promote.
					</p>
				</Section>
			) : null}

			{plan.schedulesMoving > 0 ? (
				<p className="text-[13px] text-ink-2">
					{plan.schedulesMoving === 1
						? "1 backup schedule moves"
						: `${plan.schedulesMoving} backup schedules move`}{" "}
					to {plan.replica.slug}. Existing backups stay with{" "}
					{plan.oldPrimary.slug}.
				</p>
			) : null}

			<p className="text-[13px] text-ink-2">
				After the promote {plan.oldPrimary.slug} is fenced: stopped, its data
				kept, and never started as a primary again. You can rebuild it as a
				replica of {plan.replica.slug} or delete it.
			</p>

			{warnings.length > 0 ? (
				<Alert variant="warning">
					<AlertTitle>Before you promote</AlertTitle>
					<AlertDescription>
						<ul className="flex list-disc flex-col gap-1 pl-4">
							{warnings.map((message) => (
								<li key={message}>{message}</li>
							))}
						</ul>
					</AlertDescription>
				</Alert>
			) : null}
			{blockers.length > 0 ? (
				<Alert variant="destructive">
					<AlertTitle>It cannot be promoted right now</AlertTitle>
					<AlertDescription>
						<ul className="flex list-disc flex-col gap-1 pl-4">
							{blockers.map((message) => (
								<li key={message}>{message}</li>
							))}
						</ul>
					</AlertDescription>
				</Alert>
			) : null}
		</>
	);
}

function PromoteBody({
	service,
	onClose,
}: {
	service: PromoteDialogProps["service"];
	onClose(): void;
}) {
	const queryClient = useQueryClient();
	const typedId = useId();
	const plan = useMutation(promoteDatabaseReplicaMutation());
	const promote = useMutation(promoteDatabaseReplicaMutation());
	const [typed, setTyped] = useState("");
	const [acceptLoss, setAcceptLoss] = useState(false);
	// The id of the promotion this dialog started ("" when the server gave none).
	const [startedId, setStartedId] = useState<string | null>(null);
	const started = startedId !== null;
	// One key per opening: a retry after a lost response replays, not duplicates.
	const [idempotencyKey] = useState(() => crypto.randomUUID());

	// biome-ignore lint/correctness/useExhaustiveDependencies: `plan.mutate` is stable; the plan is read once per opening.
	useEffect(() => {
		plan.mutate({ path: { serviceId: service.id }, body: { dryRun: true } });
	}, [service.id]);

	// Once started, the promotion's own steps come from the replication view.
	const view = useReplication(service.id, started);
	// The view's promotion is the latest one: only the one this dialog started counts.
	const latest = view.data?.promotion;
	const mine =
		latest && (startedId === "" || latest.id === startedId)
			? latest
			: undefined;
	const promotion = promotionLine(mine);
	const finished = mine?.status === "succeeded";
	const current = plan.data?.plan;
	const forced = current?.mode === "forced";
	const canConfirm =
		!started &&
		!promote.isPending &&
		canConfirmPromote(current, typed, service.slug, acceptLoss);

	const ended = finished || mine?.status === "failed";
	useEffect(() => {
		if (ended) void invalidateReplication(queryClient);
	}, [ended, queryClient]);

	const confirm = () =>
		promote.mutate(
			{
				path: { serviceId: service.id },
				body: { confirm: typed, acceptDataLoss: acceptLoss, dryRun: false },
				headers: { "Idempotency-Key": idempotencyKey },
			},
			{
				onSuccess: (result) => {
					setStartedId(result.promotionId ?? "");
					void invalidateReplication(queryClient);
				},
			},
		);

	return (
		<>
			<DialogHeader>
				<DialogTitle>Promote to primary · {service.name}</DialogTitle>
				<DialogDescription>
					Make this read replica the writer of the group. {MANUAL_NOTE}
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				{current === undefined && !plan.isError ? (
					<div
						className="flex flex-col gap-2"
						role="status"
						aria-label="Planning the promote"
					>
						<Skeleton className="h-5 w-2/3" />
						<Skeleton className="h-24 w-full" />
					</div>
				) : null}
				{plan.isError ? (
					<Alert variant="destructive">
						<AlertTitle>The promote could not be planned</AlertTitle>
						<AlertDescription>{errorMessage(plan.error)}</AlertDescription>
					</Alert>
				) : null}

				{current && !started ? (
					<>
						<Plan plan={current} />
						{forced ? (
							<Alert variant="destructive">
								<AlertTitle>Data will be lost</AlertTitle>
								<AlertDescription className="flex flex-col gap-2">
									<span>{dataLossText(current)}</span>
									<span>
										If the old primary's host comes back before NebulaCtrl can
										fence it, Docker may restart it and it can accept writes for
										a short while; those writes are lost too, and there is no
										merge. Rebuilding it as a replica is the only way back.
									</span>
									<CheckLabel
										checked={acceptLoss}
										onCheckedChange={setAcceptLoss}
										className="items-start"
										checkboxClassName="mt-0.5"
									>
										<span className="text-[13px] text-ink-1">
											I accept losing those writes
										</span>
									</CheckLabel>
								</AlertDescription>
							</Alert>
						) : null}
						{(current.blockers ?? []).length === 0 ? (
							<Field>
								<FieldLabel htmlFor={typedId}>
									Type{" "}
									<span className="font-mono text-ink-1">{service.slug}</span>{" "}
									to confirm
								</FieldLabel>
								<Input
									id={typedId}
									value={typed}
									autoComplete="off"
									spellCheck={false}
									className="font-mono"
									onChange={(event) => setTyped(event.target.value)}
									onKeyDown={(event) => {
										if (event.key === "Enter" && canConfirm) {
											event.preventDefault();
											confirm();
										}
									}}
								/>
							</Field>
						) : null}
						{promote.isError ? (
							<Alert variant="destructive">
								<AlertTitle>The promote was refused</AlertTitle>
								<AlertDescription>
									{errorMessage(promote.error)}
								</AlertDescription>
							</Alert>
						) : null}
					</>
				) : null}

				{started ? (
					promotion?.tone === "failed" ? (
						<Alert variant="destructive">
							<AlertTitle>The promote failed</AlertTitle>
							<AlertDescription>{promotion.text}</AlertDescription>
						</Alert>
					) : finished ? (
						<Alert variant="success">
							<AlertTitle>Promoted</AlertTitle>
							<AlertDescription>
								{service.name} is the primary now. Review the staged changes and
								deploy the consumers to point them at it.
							</AlertDescription>
						</Alert>
					) : (
						<div
							className="flex items-center gap-2.5 text-[13px] text-ink-2"
							role="status"
						>
							<Spinner />
							{promotion?.text ?? "Starting the promote…"}
						</div>
					)
				) : null}
			</DialogBody>
			<DialogFooter>
				<Button variant="ghost" onClick={onClose}>
					{started ? "Close" : "Cancel"}
				</Button>
				{started ? null : (
					<Button
						variant={forced ? "danger-solid" : "primary"}
						disabled={!canConfirm}
						onClick={confirm}
					>
						{promote.isPending ? <Spinner /> : null}
						{forced ? "Promote, accepting data loss" : "Promote to primary"}
					</Button>
				)}
			</DialogFooter>
		</>
	);
}
