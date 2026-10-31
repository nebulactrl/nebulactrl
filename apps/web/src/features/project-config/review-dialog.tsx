import { useEffect, useRef, useState } from "react";
import { errorCode, errorMessage, isApiError } from "#/api/client-config";
import type { Environment } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
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
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import {
	type ApplyOutcome,
	applyOutcome,
	needsApproval,
	type PlanLike,
	planFooter,
	planView,
	stalePlan,
	summaryLine,
} from "./model";
import { openProjectConfig } from "./open";
import {
	ChangeList,
	DiagnosticRow,
	FoldedSection,
	PlanSection,
	ReleasedList,
	ServiceChanges,
} from "./plan-view";
import {
	useApplyFromGit,
	useEnvironmentConfigSource,
	usePlanFromGit,
} from "./queries";

const SHARED_TITLE = {
	project: "Shared variables, project",
	environment: "Shared variables, this environment",
} as const;

/** The empty state: no file to review yet, so start from what runs today. */
function NoFile({ onExport }: { onExport(): void }) {
	return (
		<div className="flex flex-col items-start gap-3 py-2">
			<p className="max-w-[46ch] text-ink-2 text-sm">
				This environment has no nebula.toml to review yet. Start from your
				current setup: export it, commit the file to your repository, and it
				shows up here whenever it differs.
			</p>
			<Button variant="secondary" onClick={onExport}>
				Start from your current setup
			</Button>
		</div>
	);
}

function ResultPanel({ outcome }: { outcome: ApplyOutcome }) {
	return (
		<section aria-label="Result" className="flex flex-col gap-3">
			<Alert variant={outcome.tone === "error" ? "destructive" : "warning"}>
				<AlertTitle>{outcome.title}</AlertTitle>
				<AlertDescription>{outcome.detail}</AlertDescription>
			</Alert>
			{outcome.failed.length > 0 ? (
				<ul className="surface-well flex flex-col gap-1.5 rounded-ctl px-3 py-2">
					{outcome.failed.map((step) => (
						<li key={step.step} className="flex flex-col gap-0.5 text-xs">
							<span className="font-mono text-ink-1">{step.step}</span>
							{step.message ? (
								<span className="text-ink-3">{step.message}</span>
							) : null}
						</li>
					))}
				</ul>
			) : null}
		</section>
	);
}

/**
 * "Review nebula.toml": the plan of the environment's linked file, read at
 * its newest commit. Blockers first and the primary key off while any stands;
 * then what the file creates and updates, as diff lines with a staged or
 * "applies at once" pill and why each is there; what it releases; what it
 * leaves alone. Stage changes applies without deploying; Stage and deploy
 * deploys the services it touched.
 */
export function ReviewDialog({
	environment,
	open,
	onOpenChange,
}: {
	environment: Environment;
	open: boolean;
	onOpenChange(open: boolean): void;
}) {
	const target = {
		projectId: environment.projectId,
		environmentSlug: environment.slug,
	};
	const confirm = useConfirm();
	const source = useEnvironmentConfigSource(environment.id, open);
	const plan = usePlanFromGit(target);
	const apply = useApplyFromGit(target);
	const [current, setCurrent] = useState<PlanLike | undefined>();
	const [notice, setNotice] = useState<string | undefined>();
	const [outcome, setOutcome] = useState<ApplyOutcome | undefined>();
	const [failure, setFailure] = useState<string | undefined>();
	// One key per (plan, deploy) intent: pressing again replays the stored answer.
	const keys = useRef(new Map<string, string>());

	const refresh = (message?: string) => {
		setFailure(undefined);
		setNotice(message);
		plan.mutate(undefined, {
			onSuccess: (data) => setCurrent(data),
			onError: (error) => setFailure(errorMessage(error)),
		});
	};

	// Opening starts from a fresh plan; closing forgets it.
	// biome-ignore lint/correctness/useExhaustiveDependencies: the plan is requested once per opening.
	useEffect(() => {
		if (!open) {
			setCurrent(undefined);
			setNotice(undefined);
			setOutcome(undefined);
			setFailure(undefined);
			keys.current.clear();
			return;
		}
		refresh();
	}, [open]);

	const view = current ? planView(current) : undefined;
	const busy = plan.isPending || apply.isPending;
	const noFile =
		source.data !== undefined &&
		(source.data.mode === "none" || source.data.status === "not_found");
	const approval = current ? needsApproval(current) : false;

	const keyFor = (planId: string, deploy: boolean) => {
		const id = `${planId}:${deploy}`;
		let key = keys.current.get(id);
		if (key === undefined) {
			key = crypto.randomUUID();
			keys.current.set(id, key);
		}
		return key;
	};

	const run = async (deploy: boolean) => {
		if (!current || !view?.canStage || busy) return;
		if (deploy && environment.isProduction) {
			const ok = await confirm({
				title: `Stage and deploy to ${environment.name}?`,
				body: "The file's changes are staged, then every service they touch is deployed to production.",
				cta: "Stage and deploy",
				hold: false,
			});
			if (!ok) return;
		}
		setFailure(undefined);
		apply.mutate(
			{
				planId: current.planId,
				deploy,
				idempotencyKey: keyFor(current.planId, deploy),
			},
			{
				onSuccess: (result) => {
					const done = applyOutcome(result);
					if (done.tone === "success" || done.tone === "info") {
						toast.success(done.title, done.detail);
						onOpenChange(false);
						return;
					}
					setOutcome(done);
				},
				onError: (error) => {
					const code = errorCode(error);
					const fresh = isApiError(error)
						? stalePlan(error.details)
						: undefined;
					if (code === "plan_stale") {
						keys.current.clear();
						if (fresh) {
							setCurrent(fresh);
							setNotice(
								"The file or the environment changed while you were reviewing. The plan below is up to date; check it and stage again.",
							);
						} else {
							refresh(
								"The file or the environment changed while you were reviewing. The plan is refreshed; check it and stage again.",
							);
						}
						return;
					}
					if (code === "config_blocked" && fresh) {
						setCurrent(fresh);
						setNotice(undefined);
						return;
					}
					setFailure(errorMessage(error));
				},
			},
		);
	};

	const exportInstead = () => {
		onOpenChange(false);
		openProjectConfig("export");
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="lg" showCloseButton>
				<DialogHeader>
					<DialogTitle>Review nebula.toml</DialogTitle>
					<DialogDescription>
						{noFile
							? `What ${environment.name} would change when its file is applied.`
							: current
								? `${summaryLine(current)}. Nothing is deployed until you say so.`
								: `Reading the file for ${environment.name}…`}
					</DialogDescription>
				</DialogHeader>
				<DialogBody className="flex flex-col gap-4">
					{outcome ? <ResultPanel outcome={outcome} /> : null}
					{!outcome && noFile && !current ? (
						<NoFile onExport={exportInstead} />
					) : null}
					{!outcome && !noFile && plan.isPending && !current ? (
						<div role="status" aria-label="Reading nebula.toml">
							<Skeleton className="h-24 rounded-ctl" />
						</div>
					) : null}
					{!outcome && !noFile && failure && !current ? (
						<Alert variant="destructive">
							<AlertTitle>Could not plan nebula.toml</AlertTitle>
							<AlertDescription>{failure}</AlertDescription>
						</Alert>
					) : null}
					{!outcome && notice ? (
						<Alert variant="info">
							<AlertDescription>{notice}</AlertDescription>
						</Alert>
					) : null}
					{!outcome && view ? (
						<>
							{view.blockers.length > 0 ? (
								<Alert variant="destructive" aria-label="Blockers">
									<AlertTitle>
										{view.blockers.length === 1
											? "One problem blocks this file"
											: `${view.blockers.length} problems block this file`}
									</AlertTitle>
									<AlertDescription>
										<ul className="mt-1 flex flex-col gap-2">
											{view.blockers.map((diagnostic) => (
												<DiagnosticRow
													key={`${diagnostic.code}:${diagnostic.path ?? ""}:${diagnostic.line ?? 0}`}
													diagnostic={diagnostic}
												/>
											))}
										</ul>
									</AlertDescription>
								</Alert>
							) : null}
							{!current?.valid && view.blockers.length === 0 ? (
								<Alert variant="destructive">
									<AlertTitle>This file cannot be applied</AlertTitle>
									<AlertDescription>
										The control plane did not accept the plan. Check the file
										and try again.
									</AlertDescription>
								</Alert>
							) : null}
							{view.creates.length > 0 ? (
								<PlanSection title="Creates" count={view.creates.length}>
									{view.creates.map((service) => (
										<ServiceChanges key={service.slug} service={service} />
									))}
								</PlanSection>
							) : null}
							{view.updates.length > 0 ? (
								<PlanSection title="Updates" count={view.updates.length}>
									{view.updates.map((service) => (
										<ServiceChanges key={service.slug} service={service} />
									))}
								</PlanSection>
							) : null}
							{view.shared.map((group) => (
								<PlanSection
									key={group.scope}
									title={SHARED_TITLE[group.scope]}
								>
									<ChangeList
										changes={group.changes}
										label={SHARED_TITLE[group.scope]}
									/>
								</PlanSection>
							))}
							{view.released.length > 0 ? (
								<PlanSection title="Released" count={view.released.length}>
									<p className="text-ink-3 text-xs">
										The file no longer names these. They keep their current
										value and stop being managed by it.
									</p>
									<ReleasedList paths={view.released} />
								</PlanSection>
							) : null}
							{view.notInFile.length > 0 ? (
								<FoldedSection
									title="Not in file"
									count={view.notInFile.length}
								>
									<ul className="surface-well flex flex-col gap-1 rounded-ctl px-3 py-2 text-xs">
										{view.notInFile.map((service) => (
											<li key={service.slug} className="flex gap-2">
												<span className="font-mono text-ink-1">
													{service.slug}
												</span>
												<span className="text-ink-3">
													{service.note ??
														"Kept. Remove it in the dashboard if you no longer want it."}
												</span>
											</li>
										))}
									</ul>
								</FoldedSection>
							) : null}
							{view.warnings.length > 0 ? (
								<PlanSection title="Warnings" count={view.warnings.length}>
									<ul className="flex flex-col gap-2">
										{view.warnings.map((diagnostic) => (
											<DiagnosticRow
												key={`${diagnostic.code}:${diagnostic.path ?? ""}:${diagnostic.line ?? 0}`}
												diagnostic={diagnostic}
											/>
										))}
									</ul>
								</PlanSection>
							) : null}
							{!view.hasChanges &&
							view.blockers.length === 0 &&
							current?.valid ? (
								<p className="text-ink-2 text-sm">
									{noFile
										? "There is no file to apply."
										: "This environment already matches the file."}
								</p>
							) : null}
							{failure ? (
								<Alert variant="destructive">
									<AlertDescription>{failure}</AlertDescription>
								</Alert>
							) : null}
						</>
					) : null}
				</DialogBody>
				<DialogFooter className="items-center">
					{current && !outcome ? (
						<span
							className="mr-auto min-w-0 truncate font-mono text-2xs text-ink-4"
							title={planFooter(current).join(" · ")}
						>
							{planFooter(current).join(" · ")}
						</span>
					) : null}
					<Button variant="secondary" onClick={() => onOpenChange(false)}>
						{outcome ? "Close" : "Cancel"}
					</Button>
					{outcome ? null : (
						<>
							{approval ? (
								<span className="max-w-[28ch] text-ink-3 text-xs">
									Needs approval in production.{" "}
									{current?.guard?.reason ??
										"A person has to approve this plan."}
								</span>
							) : (
								<Button
									variant="secondary"
									disabled={busy || !view?.canStage}
									onClick={() => void run(true)}
								>
									Stage and deploy
								</Button>
							)}
							<Button
								variant="primary"
								disabled={busy || !view?.canStage}
								onClick={() => void run(false)}
							>
								{apply.isPending ? <Spinner /> : null}
								Stage changes
							</Button>
						</>
					)}
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
