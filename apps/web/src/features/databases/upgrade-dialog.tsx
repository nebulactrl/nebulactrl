import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	planDatabaseUpgradeMutation,
	upgradeDatabaseMutation,
} from "#/api/gen/@tanstack/react-query.gen";
import type { DatabaseView, UpgradeResult } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
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
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { useInspectorActions } from "#/features/canvas/inspector/inspector-context";
import { openStagedReview } from "#/features/deploy-button";
import { upgradeTargets, versionLabel } from "./engines";
import { canConfirmUpgrade, UPGRADE_METHOD_LABEL } from "./model";
import {
	DATABASE_POLL_MS,
	databaseQuery,
	invalidateDatabaseAndServices,
} from "./queries";

export interface UpgradeDialogProps {
	service: Pick<Service, "id" | "name" | "slug">;
	database: Pick<DatabaseView, "engine" | "spec">;
	open: boolean;
	onOpenChange(open: boolean): void;
}

/**
 * Upgrade version: pick a newer version, read the plan the server computes
 * (method, steps, downtime, warnings, blockers), and confirm by typing the
 * service's slug. A method that is not automatic (dump and restore) shows its
 * steps and points to Backups; there is nothing to confirm.
 */
export function UpgradeDialog({
	service,
	database,
	open,
	onOpenChange,
}: UpgradeDialogProps) {
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				<UpgradeBody
					service={service}
					database={database}
					onClose={() => onOpenChange(false)}
				/>
			</DialogContent>
		</Dialog>
	);
}

function Block({
	title,
	items,
	ordered = false,
}: {
	title: string;
	items: readonly string[];
	ordered?: boolean;
}) {
	const Tag = ordered ? "ol" : "ul";
	return (
		<section aria-label={title} className="flex flex-col gap-1.5">
			<h3 className="type-label">{title}</h3>
			<Tag
				className={
					ordered
						? "flex list-decimal flex-col gap-1 pl-5 text-[13px] text-ink-2"
						: "flex list-disc flex-col gap-1 pl-5 text-[13px] text-ink-2"
				}
			>
				{items.map((item) => (
					<li key={item}>{item}</li>
				))}
			</Tag>
		</section>
	);
}

function UpgradeBody({
	service,
	database,
	onClose,
}: {
	service: Pick<Service, "id" | "name" | "slug">;
	database: Pick<DatabaseView, "engine" | "spec">;
	onClose(): void;
}) {
	const queryClient = useQueryClient();
	const { openTab } = useInspectorActions();
	const { engine, spec } = database;
	const targets = upgradeTargets(engine, spec.version, spec.flavor);
	const typedId = useId();

	const [target, setTarget] = useState(targets[0]?.id ?? "");
	const [typed, setTyped] = useState("");
	const [started, setStarted] = useState<UpgradeResult | null>(null);
	const plan = useMutation(planDatabaseUpgradeMutation());
	const upgrade = useMutation(upgradeDatabaseMutation());

	// A new target means a new plan.
	// biome-ignore lint/correctness/useExhaustiveDependencies: `plan.mutate` is stable; the plan reruns per target only.
	useEffect(() => {
		if (target === "") return;
		setTyped("");
		plan.mutate({
			path: { serviceId: service.id },
			body: { targetVersion: target },
		});
	}, [target, service.id]);

	// A pg_upgrade runs on the agent; the view says how it ended.
	const view = useQuery({
		...databaseQuery(service.id),
		enabled: started?.status === "pending",
		refetchInterval: DATABASE_POLL_MS,
	});
	const latest = view.data?.upgrade;
	const outcome =
		started?.status === "pending" &&
		latest !== undefined &&
		(started.upgradeId === undefined || latest.id === started.upgradeId)
			? latest.status
			: null;
	useEffect(() => {
		if (outcome === "ready" || outcome === "failed") {
			void invalidateDatabaseAndServices(queryClient);
		}
	}, [outcome, queryClient]);

	const result = plan.data;
	const matches = result !== undefined && result.to === target;
	const current = matches ? result : undefined;
	const warnings = current?.warnings ?? [];
	const blockers = current?.blockers ?? [];
	const steps = current?.steps ?? [];
	const manual =
		current !== undefined && current.kind === "upgrade" && !current.executable;
	const targetLabel = versionLabel(engine, target);

	const confirm = () =>
		upgrade.mutate(
			{
				path: { serviceId: service.id },
				body: { targetVersion: target, confirm: typed },
			},
			{
				onSuccess: (data) => {
					setStarted(data);
					void invalidateDatabaseAndServices(queryClient);
				},
			},
		);

	const ready = started?.status === "staged" || outcome === "ready";
	const failed = outcome === "failed";
	const working = started?.status === "pending" && !ready && !failed;
	const canConfirm =
		started === null &&
		!upgrade.isPending &&
		canConfirmUpgrade(current, typed, service.slug);

	return (
		<>
			<DialogHeader>
				<DialogTitle>Upgrade version · {service.name}</DialogTitle>
				<DialogDescription>
					{engine.label} {spec.version}
					{spec.flavor ? ` (${spec.flavor})` : ""} is running.
					{targets.length === 0
						? ""
						: " Pick a newer version; the plan below is what will happen."}
				</DialogDescription>
			</DialogHeader>
			<DialogBody>
				{targets.length === 0 ? (
					<p className="type-secondary">
						{engine.label} {spec.version} is the newest version offered for this
						database.
					</p>
				) : (
					<Field>
						<FieldLabel htmlFor="upgrade-target">Upgrade to</FieldLabel>
						<Select
							value={target}
							items={targets.map((entry) => ({
								value: entry.id,
								label: entry.label,
							}))}
							disabled={started !== null}
							onValueChange={(next) => setTarget(String(next ?? ""))}
						>
							<SelectTrigger id="upgrade-target" className="w-full">
								<SelectValue placeholder="Choose a version" />
							</SelectTrigger>
							<SelectContent alignItemWithTrigger={false}>
								{targets.map((entry) => (
									<SelectItem key={entry.id} value={entry.id}>
										{entry.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						{targets.find((entry) => entry.id === target)?.notes ? (
							<p className="type-secondary">
								{targets.find((entry) => entry.id === target)?.notes}
							</p>
						) : null}
					</Field>
				)}

				{targets.length > 0 && !matches && !plan.isError ? (
					<div
						className="flex flex-col gap-2"
						role="status"
						aria-label="Planning the upgrade"
					>
						<Skeleton className="h-5 w-1/2" />
						<Skeleton className="h-14 w-full" />
					</div>
				) : null}
				{plan.isError ? (
					<Alert variant="destructive">
						<AlertTitle>The upgrade could not be planned</AlertTitle>
						<AlertDescription>{errorMessage(plan.error)}</AlertDescription>
					</Alert>
				) : null}

				{current && started === null ? (
					<>
						<div className="flex flex-wrap items-center gap-2 text-[13px]">
							<Badge
								variant={current.kind === "upgrade" ? "accent" : "warning"}
							>
								{current.kind}
							</Badge>
							<span className="text-ink-1">
								{UPGRADE_METHOD_LABEL[current.method] ?? current.method}
							</span>
							<span className="text-ink-3">· downtime: {current.downtime}</span>
						</div>
						{steps.length > 0 ? (
							<Block title="Steps" items={steps} ordered />
						) : null}
						{warnings.length > 0 ? (
							<Alert variant="warning">
								<AlertTitle>Before you upgrade</AlertTitle>
								<AlertDescription>
									<ul className="flex list-disc flex-col gap-1 pl-4">
										{warnings.map((warning) => (
											<li key={warning}>{warning}</li>
										))}
									</ul>
								</AlertDescription>
							</Alert>
						) : null}
						{blockers.length > 0 ? (
							<Alert variant="destructive">
								<AlertTitle>It cannot be upgraded right now</AlertTitle>
								<AlertDescription>
									<ul className="flex list-disc flex-col gap-1 pl-4">
										{blockers.map((blocker) => (
											<li key={blocker}>{blocker}</li>
										))}
									</ul>
								</AlertDescription>
							</Alert>
						) : null}
						{manual ? (
							<Alert variant="info">
								<AlertTitle>This upgrade is not automatic</AlertTitle>
								<AlertDescription className="flex flex-col gap-2">
									<span>
										Take a backup, then restore it into a new database on{" "}
										{targetLabel} (Backups → Restore to a new database). Point
										the services that use it at the new one when it is ready.
									</span>
									<span>
										<Button
											variant="secondary"
											size="md"
											onClick={() => {
												onClose();
												openTab("backups");
											}}
										>
											Open Backups
										</Button>
									</span>
								</AlertDescription>
							</Alert>
						) : current.executable && current.kind === "upgrade" ? (
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
						{upgrade.isError ? (
							<Alert variant="destructive">
								<AlertTitle>The upgrade was refused</AlertTitle>
								<AlertDescription>
									{errorMessage(upgrade.error)}
								</AlertDescription>
							</Alert>
						) : null}
					</>
				) : null}

				{working ? (
					<div
						className="flex items-center gap-2.5 text-[13px] text-ink-2"
						role="status"
					>
						<Spinner />
						Converting the data into a new volume. The database is stopped until
						it finishes.
					</div>
				) : null}
				{failed ? (
					<Alert variant="destructive">
						<AlertTitle>The upgrade failed</AlertTitle>
						<AlertDescription>
							{latest?.error ?? "The agent did not report a reason."} The
							previous data is untouched.
						</AlertDescription>
					</Alert>
				) : null}
				{ready ? (
					<Alert variant="success">
						<AlertTitle>Upgrade staged</AlertTitle>
						<AlertDescription>
							Deploy to start {targetLabel}
							{started?.plan.method === "pg_upgrade" && latest?.oldVolume
								? `; the previous volume “${latest.oldVolume}” is kept`
								: ""}
							.
						</AlertDescription>
					</Alert>
				) : null}
			</DialogBody>
			<DialogFooter>
				{ready ? (
					<>
						<Button variant="ghost" onClick={onClose}>
							Close
						</Button>
						<Button
							onClick={() => {
								onClose();
								openStagedReview();
							}}
						>
							Review staged changes
						</Button>
					</>
				) : (
					<>
						<Button variant="ghost" onClick={onClose}>
							{working || failed ? "Close" : "Cancel"}
						</Button>
						{manual || working || failed || targets.length === 0 ? null : (
							<Button disabled={!canConfirm} onClick={confirm}>
								{upgrade.isPending ? <Spinner /> : null}
								Upgrade to {targetLabel}
							</Button>
						)}
					</>
				)}
			</DialogFooter>
		</>
	);
}
