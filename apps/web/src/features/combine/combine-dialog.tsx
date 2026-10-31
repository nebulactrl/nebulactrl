import { useQueryClient } from "@tanstack/react-query";
import { cn } from "cn";
import {
	type ReactNode,
	useEffect,
	useId,
	useMemo,
	useRef,
	useState,
} from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import type { CombinePlan } from "#/api/gen/types.gen";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { ConfirmDialog } from "#/components/common/confirm-dialog";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
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
	useOpenedByKeyboard,
} from "#/components/ui/dialog";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { OptionCard, OptionCardGroup } from "#/components/ui/option-card";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { toast } from "#/components/ui/toast";
import { defaultProcessName } from "#/features/processes/model";
import { SelectRow } from "#/features/service-settings/fields";
import { useStackNavigation } from "#/features/stacks/url";
import {
	buildRequest,
	type CombineReview,
	type CombineStepId,
	chooseFor,
	combineSteps,
	conflictOptionLabel,
	defaultTarget,
	keptAsIs,
	type ProgressStep,
	planBlockers,
	planConflicts,
	processNameProblems,
	progressSteps,
	reviewOf,
	suggestProcessNames,
	unresolvedConflicts,
} from "./model";
import {
	useCombineActions,
	useCombinePlan,
	useCombineProgress,
} from "./use-combine";

const STALE_PLAN =
	"The services changed while you were reviewing. The plan was refreshed: check it again, then combine.";

const STEP_KIND: Record<ProgressStep["state"], StatusKind> = {
	done: "healthy",
	current: "deploying",
	pending: "pending",
	failed: "failed",
};

const STEP_WORDS: Record<ProgressStep["state"], string> = {
	done: "Done",
	current: "In progress",
	pending: "Waiting",
	failed: "Failed",
};

/** "3000 · shop.acme.com": what a service serves, so the one that stays is easy to pick. */
function serviceFacts(service: Service): string {
	const port = service.config.port ?? 0;
	const host = service.domains[0]?.hostname;
	return [port > 0 ? `:${port}` : "no port", host].filter(Boolean).join(" · ");
}

/**
 * "Combine into one service": several Git services of one repository become
 * one service with a process each. One service stays (its name, URL history
 * and release line); the others' commands become processes, their domains and
 * tasks move, and they are deleted once the new release runs. Nothing is
 * deleted until the deployment has succeeded, and a failed deployment can be
 * retried or aborted, which leaves every service as it was. `services` null
 * is closed.
 */
export function CombineDialog({
	services,
	environmentId,
	isProduction,
	onClose,
}: {
	services: readonly Service[] | null;
	environmentId: string;
	isProduction: boolean;
	onClose: () => void;
}) {
	const open = services !== null;
	// Opened with the keyboard (Enter on the menu row, a shortcut): no entrance.
	const instant = useOpenedByKeyboard(open);
	// Keep the last selection while the dialog closes.
	const last = useRef<readonly Service[]>([]);
	if (services !== null) last.current = services;
	return (
		<Dialog open={open} onOpenChange={(next) => !next && onClose()}>
			<DialogContent size="lg" instant={instant}>
				<CombineForm
					services={last.current}
					environmentId={environmentId}
					isProduction={isProduction}
					onClose={onClose}
				/>
			</DialogContent>
		</Dialog>
	);
}

/** Mounts with the dialog, so every opening starts from a clean form. */
function CombineForm({
	services,
	environmentId,
	isProduction,
	onClose,
}: {
	services: readonly Service[];
	environmentId: string;
	isProduction: boolean;
	onClose: () => void;
}) {
	const ids = useId();
	const queryClient = useQueryClient();
	const { show } = useStackNavigation();
	const [targetId, setTargetId] = useState(() => defaultTarget(services));
	const [typed, setTyped] = useState<Record<string, string>>({});
	const [choices, setChoices] = useState<Record<string, string>>({});
	const [stopFirst, setStopFirst] = useState(false);
	const [step, setStep] = useState<CombineStepId>("services");
	const [combineId, setCombineId] = useState<string | undefined>(undefined);
	const [confirming, setConfirming] = useState(false);
	const [notice, setNotice] = useState<string | null>(null);

	const target = services.find((s) => s.id === targetId) ?? services[0];
	const targetName = target?.name ?? "";
	const defaultName = defaultProcessName(target?.config ?? {});
	const serviceIds = useMemo(() => services.map((s) => s.id), [services]);
	const slugOf = (id: string) => services.find((s) => s.id === id)?.slug ?? id;

	// The reader's own names win; the rest follow the target as it changes.
	const suggested = useMemo(
		() => suggestProcessNames(services, targetId, defaultName),
		[services, targetId, defaultName],
	);
	const names = useMemo(
		() =>
			Object.fromEntries(
				Object.entries(suggested).map(([id, name]) => [id, typed[id] ?? name]),
			),
		[suggested, typed],
	);
	const problems = processNameProblems(names, defaultName);

	const plan = useCombinePlan({
		environmentId,
		serviceIds,
		targetServiceId: targetId,
		enabled: combineId === undefined,
	});
	const planData = plan.data;
	// A plan for the previous target stays on screen while the next loads; it must not be acted on.
	const planBusy = plan.isFetching || plan.isPlaceholderData;
	const blockers = planData ? planBlockers(planData) : [];
	const unresolved = planData ? unresolvedConflicts(planData, choices) : [];
	const steps = combineSteps(planData);
	const stepIndex = Math.max(
		0,
		steps.findIndex((s) => s.id === step),
	);
	const planReady =
		planData !== undefined &&
		!planBusy &&
		blockers.length === 0 &&
		Object.keys(problems).length === 0;
	const canContinue =
		planReady && (step !== "variables" || unresolved.length === 0);
	const reviewReady = planReady && unresolved.length === 0;

	const actions = useCombineActions(environmentId);
	const progress = useCombineProgress(combineId);
	const view = progress.data;

	const announced = useRef(false);
	useEffect(() => {
		if (view?.state !== "done" || announced.current) return;
		announced.current = true;
		void invalidateServices(queryClient);
		toast.success(
			"Services combined",
			`${targetName} now runs ${services.length} processes.`,
		);
		onClose();
	}, [view?.state, queryClient, targetName, services.length, onClose]);

	const request = planData
		? buildRequest(
				planData,
				{
					targetServiceId: targetId,
					processNames: names,
					variableChoices: choices,
					stopSourcesFirst: stopFirst,
				},
				defaultName,
			)
		: null;

	const start = async () => {
		if (!request) return;
		try {
			const started = await actions.start.mutateAsync(request);
			setCombineId(started.id);
		} catch (error) {
			if (errorCode(error) === "plan_stale") {
				void plan.refetch();
				setNotice(STALE_PLAN);
				setConfirming(false);
				setStep("review");
				// Throwing keeps the confirmation from advancing to Progress.
				throw new Error(STALE_PLAN);
			}
			throw error;
		}
	};

	const pickTarget = (id: string) => {
		setTargetId(id);
		setNotice(null);
	};
	const move = (by: 1 | -1) => {
		const next = steps[stepIndex + by];
		if (next) setStep(next.id);
	};

	return (
		<>
			<DialogHeader>
				<DialogTitle>Combine into one service</DialogTitle>
				<DialogDescription>
					One build and one release, with a process for each service's command.
				</DialogDescription>
				<ol
					aria-label="Steps"
					className="m-0 mt-1 flex list-none flex-wrap items-center gap-x-2 p-0 text-xs"
				>
					{steps.map((s, index) => (
						<li
							key={s.id}
							aria-current={s.id === step ? "step" : undefined}
							className={cn(
								"flex items-center gap-2",
								s.id === step ? "font-medium text-ink-1" : "text-ink-3",
							)}
						>
							{index > 0 ? <span aria-hidden>·</span> : null}
							{index + 1} {s.label}
						</li>
					))}
				</ol>
			</DialogHeader>
			<DialogBody>
				{notice ? (
					<Alert variant="warning" role="status">
						<AlertDescription>{notice}</AlertDescription>
					</Alert>
				) : null}
				{plan.isError && combineId === undefined ? (
					<Alert variant="destructive">
						<AlertTitle>Could not check these services</AlertTitle>
						<AlertDescription>
							{errorMessage(plan.error)} Close the dialog and try again.
						</AlertDescription>
					</Alert>
				) : null}

				{step === "services" ? (
					<div className="flex flex-col gap-4">
						<Field>
							<FieldLabel id={`${ids}-target`}>Service that stays</FieldLabel>
							<OptionCardGroup
								value={targetId}
								onValueChange={pickTarget}
								aria-labelledby={`${ids}-target`}
								className="flex-col"
							>
								{services.map((service) => (
									<OptionCard
										key={service.id}
										value={service.id}
										title={service.name}
										description={serviceFacts(service)}
										className="w-full"
									/>
								))}
							</OptionCardGroup>
							<FieldDescription>
								{targetName} keeps its name and settings and runs its own
								command as the <span className="font-mono">{defaultName}</span>{" "}
								process.
							</FieldDescription>
						</Field>
						{services
							.filter((service) => service.id !== targetId)
							.map((service) => {
								const problem = problems[service.id];
								return (
									<Field key={service.id}>
										<FieldLabel htmlFor={`${ids}-name-${service.id}`}>
											Process name for {service.name}
										</FieldLabel>
										<Input
											id={`${ids}-name-${service.id}`}
											scale="lg"
											autoComplete="off"
											spellCheck={false}
											className="font-mono"
											value={names[service.id] ?? ""}
											aria-invalid={problem !== undefined}
											onChange={(event) =>
												setTyped((current) => ({
													...current,
													[service.id]: event.target.value,
												}))
											}
										/>
										{problem ? <FieldError>{problem}</FieldError> : null}
									</Field>
								);
							})}
						<Field>
							<CheckLabel
								checked={stopFirst}
								onCheckedChange={setStopFirst}
								className="items-start"
								checkboxClassName="mt-0.5"
							>
								<span className="font-medium text-ink-1 text-sm">
									Stop the others first
								</span>
							</CheckLabel>
							<FieldDescription>
								Without it a scheduler or queue worker runs twice for a few
								seconds, once in its old service and once in the new process,
								while traffic moves. Services with a domain keep serving until
								then.
							</FieldDescription>
						</Field>
						{blockers.length > 0 ? (
							<Alert variant="destructive">
								<AlertTitle>These services cannot be combined yet</AlertTitle>
								<AlertDescription>
									<ul className="m-0 flex list-disc flex-col gap-1 pl-4">
										{blockers.map((text) => (
											<li key={text}>{text}</li>
										))}
									</ul>
								</AlertDescription>
							</Alert>
						) : null}
					</div>
				) : null}

				{step === "variables" && planData ? (
					<div className="@container/section flex flex-col gap-4">
						<div className="flex items-center justify-between gap-3">
							<p className="m-0 text-ink-2 text-sm">
								{planData.variables.sameCount} variables are the same
								everywhere. {planConflicts(planData).length} differ: pick the
								value to keep.
							</p>
							<Button
								variant="secondary"
								size="sm"
								onClick={() =>
									setChoices((current) =>
										chooseFor(planData, targetId, current),
									)
								}
							>
								Use {slugOf(targetId)} for every key
							</Button>
						</div>
						<div className="flex flex-col">
							{planConflicts(planData).map((conflict) => (
								<SelectRow
									key={conflict.key}
									id={`${ids}-var-${conflict.key}`}
									label={<span className="font-mono">{conflict.key}</span>}
									{...(conflict.secret
										? { description: "A secret: the values differ." }
										: {})}
									value={choices[conflict.key] ?? ""}
									placeholder="Choose a value"
									options={(conflict.options ?? []).map((option) => {
										const text = conflictOptionLabel(conflict, option, slugOf);
										return { value: option.serviceId, label: text, text };
									})}
									onChange={(value) =>
										setChoices((current) => ({
											...current,
											[conflict.key]: value,
										}))
									}
								/>
							))}
						</div>
						{[...keptAsIs(planData)].length > 0 ? (
							<div className="flex flex-col gap-1.5">
								<h3 className="m-0 font-medium text-ink-1 text-sm">
									Kept as is
								</h3>
								<ul className="m-0 flex list-none flex-col gap-1 p-0 text-ink-2 text-xs">
									{[...keptAsIs(planData)].map(([serviceId, keys]) => (
										<li key={serviceId}>
											<span className="font-mono text-ink-1">
												{slugOf(serviceId)}
											</span>
											: <span className="font-mono">{keys.join(", ")}</span>
										</li>
									))}
								</ul>
							</div>
						) : null}
					</div>
				) : null}

				{step === "review" && planData ? (
					<Review
						plan={planData}
						review={reviewOf(
							planData,
							{ targetServiceId: targetId, processNames: names },
							defaultName,
						)}
						unresolved={unresolved.length}
						targetName={targetName}
					/>
				) : null}

				{step === "progress" ? (
					<div className="flex flex-col gap-4">
						<ol
							aria-label="Combine progress"
							className="m-0 flex list-none flex-col gap-2.5 p-0"
						>
							{progressSteps(view ?? { state: "staged" }).map((item) => (
								<li key={item.id} className="flex items-center gap-2.5 text-sm">
									<StatusIcon kind={STEP_KIND[item.state]} label="" />
									<span
										className={
											item.state === "pending" ? "text-ink-3" : "text-ink-1"
										}
									>
										{item.label}
									</span>
									<span className="text-ink-3 text-xs">
										{STEP_WORDS[item.state]}
									</span>
									{item.id === "deploying" &&
									view?.deploymentId &&
									item.state !== "pending" ? (
										<Button
											variant="link"
											onClick={() => {
												show({ service: targetId, tab: "deployments" });
												onClose();
											}}
										>
											View deployment
										</Button>
									) : null}
								</li>
							))}
						</ol>
						{view?.state === "failed" ? (
							<Alert variant="destructive">
								<AlertTitle>The combine failed</AlertTitle>
								<AlertDescription>
									<p>{view.error || "No reason was reported."}</p>
									<p>
										Nothing was deleted and no domain moved: your services keep
										serving. Retry to deploy again, or abort to discard the
										staged changes.
									</p>
								</AlertDescription>
							</Alert>
						) : null}
						{view?.state === "aborted" ? (
							<Alert variant="info" role="status">
								<AlertTitle>Combine aborted</AlertTitle>
								<AlertDescription>
									Everything is as it was: the staged changes were discarded and
									stopped services are running again.
								</AlertDescription>
							</Alert>
						) : null}
						{actions.retry.isError || actions.abort.isError ? (
							<Alert variant="destructive">
								<AlertDescription>
									{errorMessage(actions.retry.error ?? actions.abort.error)}
								</AlertDescription>
							</Alert>
						) : null}
						{view && view.state !== "failed" && view.state !== "aborted" ? (
							<p className="m-0 text-ink-3 text-xs">
								Closing this window does not stop it. Follow the deployment
								under {targetName}.
							</p>
						) : null}
					</div>
				) : null}
			</DialogBody>
			<DialogFooter>
				{step === "services" ? (
					<>
						<Button variant="ghost" onClick={onClose}>
							Cancel
						</Button>
						<Button disabled={!canContinue} onClick={() => move(1)}>
							{planBusy ? <Spinner /> : null}
							Continue
						</Button>
					</>
				) : null}
				{step === "variables" ? (
					<>
						<Button variant="ghost" onClick={() => move(-1)}>
							Back
						</Button>
						<Button disabled={!canContinue} onClick={() => move(1)}>
							Continue
						</Button>
					</>
				) : null}
				{step === "review" ? (
					<>
						<Button variant="ghost" onClick={() => move(-1)}>
							Back
						</Button>
						<Button
							variant="danger-solid"
							disabled={!reviewReady}
							onClick={() => setConfirming(true)}
						>
							Combine services…
						</Button>
					</>
				) : null}
				{step === "progress" ? (
					view?.state === "failed" ? (
						<>
							<Button
								variant="ghost"
								disabled={actions.abort.isPending || actions.retry.isPending}
								onClick={() => combineId && actions.abort.mutate(combineId)}
							>
								Abort and keep everything as it was
							</Button>
							<Button
								disabled={actions.abort.isPending || actions.retry.isPending}
								onClick={() => combineId && actions.retry.mutate(combineId)}
							>
								{actions.retry.isPending ? <Spinner /> : null}
								Retry
							</Button>
						</>
					) : (
						<Button variant="ghost" onClick={onClose}>
							Close
						</Button>
					)
				) : null}
			</DialogFooter>
			<ConfirmDialog
				open={confirming}
				onOpenChange={setConfirming}
				onConfirmed={() => setStep("progress")}
				title={`Combine ${services.length} services into ${targetName}?`}
				body={
					<div className="flex flex-col gap-2">
						<p className="m-0">
							{targetName} is deployed with a process for each service. Once it
							is running, the domains and tasks of the others move to it and the
							others are deleted. This cannot be undone after that point.
						</p>
						{isProduction ? (
							<p className="m-0">This environment is production.</p>
						) : null}
					</div>
				}
				cta="Combine services"
				danger
				hold={false}
				guarded={isProduction}
				typeToConfirm={target?.slug ?? ""}
				onConfirm={start}
			/>
		</>
	);
}

/** Step 3: what the combined service will be, and what happens to the rest. */
function Review({
	plan,
	review,
	unresolved,
	targetName,
}: {
	plan: CombinePlan;
	review: CombineReview;
	unresolved: number;
	targetName: string;
}) {
	const others = (plan.members ?? []).filter((m) => !m.isTarget);
	return (
		<div className="flex flex-col gap-4">
			{review.blockers.length > 0 ? (
				<Alert variant="destructive">
					<AlertTitle>These services cannot be combined yet</AlertTitle>
					<AlertDescription>
						<ul className="m-0 flex list-disc flex-col gap-1 pl-4">
							{review.blockers.map((text) => (
								<li key={text}>{text}</li>
							))}
						</ul>
					</AlertDescription>
				</Alert>
			) : null}
			{unresolved > 0 ? (
				<Alert variant="warning">
					<AlertDescription>
						{unresolved} variables differ and have no value chosen. Go back to
						Variables and pick one for each.
					</AlertDescription>
				</Alert>
			) : null}
			{review.warnings.length > 0 ? (
				<Alert variant="warning">
					<AlertTitle>Worth knowing</AlertTitle>
					<AlertDescription>
						<ul className="m-0 flex list-disc flex-col gap-1 pl-4">
							{review.warnings.map((text) => (
								<li key={text}>{text}</li>
							))}
						</ul>
					</AlertDescription>
				</Alert>
			) : null}
			<ReviewList title={`Processes of ${targetName}`}>
				{review.processes.map((process) => (
					<li key={process.name}>
						<span className="font-mono text-ink-1">{process.name}</span> runs
						the command of <span className="font-mono">{process.fromSlug}</span>
						{process.isDefault ? " (stays as it is)" : ""}
					</li>
				))}
			</ReviewList>
			{review.domains.length > 0 ? (
				<ReviewList title="Domains that move">
					{review.domains.map((move) => (
						<li key={move.hostname}>
							<span className="font-mono text-ink-1">{move.hostname}</span> goes
							to the <span className="font-mono">{move.process}</span> process
						</li>
					))}
				</ReviewList>
			) : null}
			{review.tasks.length > 0 ? (
				<ReviewList title="Tasks that move">
					{review.tasks.map((task) => (
						<li key={`${task.fromSlug}:${task.name}`}>
							<span className="font-mono text-ink-1">{task.name}</span> from{" "}
							<span className="font-mono">{task.fromSlug}</span>
						</li>
					))}
				</ReviewList>
			) : null}
			{review.ignored.length > 0 ? (
				<ReviewList title={`Settings not carried over (${targetName} wins)`}>
					{review.ignored.map((entry) => (
						<li key={`${entry.fromSlug}:${entry.field}`}>
							<span className="font-mono">{entry.fromSlug}</span>{" "}
							<span className="font-mono text-ink-1">{entry.field}</span>:{" "}
							{entry.note}
						</li>
					))}
				</ReviewList>
			) : null}
			<ReviewList title="Deleted once the deployment succeeds">
				{others.map((member) => (
					<li key={member.serviceId}>
						<span className="font-mono text-ink-1">{member.slug}</span>
					</li>
				))}
			</ReviewList>
		</div>
	);
}

function ReviewList({
	title,
	children,
}: {
	title: string;
	children: ReactNode;
}) {
	return (
		<div className="flex flex-col gap-1.5">
			<h3 className="m-0 font-medium text-ink-1 text-sm">{title}</h3>
			<ul className="m-0 flex list-none flex-col gap-1 p-0 text-ink-2 text-xs leading-[1.5]">
				{children}
			</ul>
		</div>
	);
}
