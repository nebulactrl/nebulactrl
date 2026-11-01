import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { cn } from "cn";
import { useEffect, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { ComposeChoices, ComposePlan } from "#/api/gen/types.gen";
import type { Environment, Server } from "#/api/models";
import { useProjectContext } from "#/components/shell/use-project-context";
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
import { useServers } from "#/features/canvas/data";
import { defaultServerId } from "#/features/templates/lib";
import { useDebounced } from "#/features/templates/use-debounced";
import {
	buildImportRequest,
	buildPlanRequest,
	effectiveFiles,
	emptySource,
	isIncluded,
	mergeKnownPaths,
	NO_CHOICES,
	type SourceDraft,
	STAGE_STACK_LABEL,
	sourceReady,
	stackPlanOf,
	stackSummaryText,
	stageBlocker,
	stageLabel,
	summarizePlan,
} from "./lib";
import { composePlanQuery } from "./queries";
import { ReviewStep } from "./review-step";
import { SourceStep } from "./source-step";
import { StackReviewStep } from "./stack-review";
import { StageStep } from "./stage-step";
import { composeImportActions, useComposeImportState } from "./store";
import { useComposeImport } from "./use-import";

const NO_SERVERS: Server[] = [];
const PLAN_DEBOUNCE_MS = 300;

type Step = "source" | "review" | "stage";

const STEPS: ReadonlyArray<{ id: Step; label: string }> = [
	{ id: "source", label: "Source" },
	{ id: "review", label: "Review" },
	{ id: "stage", label: "Stage" },
];

const DESCRIPTION: Record<Step, string> = {
	source: "Paste or upload a compose file, or read it from git.",
	review: "Check what each service becomes and change what you need.",
	stage: "Choose a server. The services are staged, not deployed.",
};

/** Appended to the step's line when the plan is a stack. */
const STACK_DESCRIPTION: Record<Exclude<Step, "source">, string> = {
	review: " The file stays as it is and runs as one stack.",
	stage: " The stack is staged as one service, with no group.",
};

function StepList({ step }: { step: Step }) {
	const current = STEPS.findIndex((s) => s.id === step);
	return (
		<ol
			aria-label="Steps"
			className="m-0 flex list-none items-center gap-4 p-0 font-mono text-2xs"
		>
			{STEPS.map((s, index) => (
				<li
					key={s.id}
					aria-current={s.id === step ? "step" : undefined}
					className={cn(
						"flex items-center gap-1.5 border-b-2 border-transparent pb-0.5 text-ink-4",
						index < current && "text-ink-3",
						s.id === step && "border-accent-ink font-semibold text-ink-1",
					)}
				>
					<span className="tabular-nums">{index + 1}</span>
					{s.label}
					{index < current ? <span className="sr-only">(done)</span> : null}
				</li>
			))}
		</ol>
	);
}

function PlanSkeleton() {
	return (
		<div
			className="flex flex-col gap-3"
			role="status"
			aria-label="Reading the Compose file"
		>
			<Skeleton className="h-5 w-56" />
			<Skeleton className="h-40 rounded-card" />
			<Skeleton className="h-40 rounded-card" />
		</div>
	);
}

/**
 * The three steps of the import. All of its state lives here, so closing the
 * dialog (which unmounts it) starts the next import blank. The plan follows
 * the source text and the choices a beat after the last change; the answer to
 * the previous request stays on screen until the new one arrives.
 */
function Wizard({
	environment,
	projectId,
	servers,
}: {
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	projectId: string;
	servers: readonly Server[];
}) {
	const usable = useMemo(
		() =>
			servers.filter(
				(server) => server.status !== "revoked" && server.status !== "pending",
			),
		[servers],
	);
	const [step, setStep] = useState<Step>("source");
	const [draft, setDraft] = useState<SourceDraft>(emptySource);
	const [choices, setChoices] = useState<ComposeChoices>(NO_CHOICES);
	const [knownPaths, setKnownPaths] = useState<string[]>([]);
	const [pickedServer, setPickedServer] = useState("");
	const serverId = pickedServer || defaultServerId(usable);

	const request = useMemo(
		() => buildPlanRequest(draft, choices, serverId, knownPaths),
		[draft, choices, serverId, knownPaths],
	);
	const fingerprint = JSON.stringify(request);
	const settled = useDebounced(fingerprint, PLAN_DEBOUNCE_MS);
	const planned = useMemo(
		() => JSON.parse(settled) as typeof request,
		[settled],
	);
	const plan = useQuery({
		...composePlanQuery(environment.id, planned),
		enabled: step !== "source",
		placeholderData: keepPreviousData,
	});

	// The last answer that worked stays on screen while a newer request fails.
	const [lastGood, setLastGood] = useState<ComposePlan | undefined>();
	useEffect(() => {
		if (plan.data && !plan.isPlaceholderData) setLastGood(plan.data);
	}, [plan.data, plan.isPlaceholderData]);
	const shown = plan.data ?? lastGood;
	useEffect(() => {
		if (!shown) return;
		setKnownPaths((previous) => {
			const next = mergeKnownPaths(previous, shown);
			return next.length === previous.length ? previous : next;
		});
	}, [shown]);

	const fresh =
		settled === fingerprint && !plan.isFetching && !plan.isPlaceholderData;
	const refreshing = step !== "source" && !fresh && !plan.isError;
	const problem = plan.isError ? errorMessage(plan.error) : null;

	const { stage, failure } = useComposeImport({
		environment,
		projectId,
		onImported: composeImportActions.close,
	});

	const onChoices = (update: (current: ComposeChoices) => ComposeChoices) =>
		setChoices(update);
	const files = useMemo(
		() => effectiveFiles(draft, knownPaths),
		[draft, knownPaths],
	);

	const stackShown = stackPlanOf(shown);
	const included = shown
		? (shown.services ?? []).filter((service) => isIncluded(service, choices))
		: [];
	const includedCount = stackShown
		? (stackShown.components ?? []).length
		: included.length;
	const blocker = stageBlocker({
		plan: shown,
		included: includedCount,
		serverId,
		fresh: fresh && !plan.isError,
	});
	const submit = (deploy: boolean) => {
		if (blocker !== null || stage.isPending) return;
		stage.mutate(
			buildImportRequest(draft, choices, serverId, knownPaths, deploy),
		);
	};

	// A mode switch starts the choices over: they are keyed by service or by component.
	const onDraft = (next: SourceDraft) => {
		if (next.importMode !== draft.importMode) setChoices(NO_CHOICES);
		setDraft(next);
	};

	const goReview = () => {
		stage.reset();
		setStep("review");
	};

	return (
		<>
			<DialogHeader className="gap-3">
				<div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2">
					<DialogTitle>Import Docker Compose</DialogTitle>
					<StepList step={step} />
				</div>
				<DialogDescription>
					{DESCRIPTION[step]}
					{step !== "source" && shown?.mode === "stack"
						? STACK_DESCRIPTION[step]
						: ""}
				</DialogDescription>
			</DialogHeader>
			<DialogBody className="overscroll-contain">
				{step === "source" ? (
					<SourceStep
						draft={draft}
						onChange={onDraft}
						knownPaths={knownPaths}
					/>
				) : null}
				{step === "review" ? (
					shown ? (
						stackShown ? (
							<StackReviewStep
								plan={shown}
								choices={choices}
								onChoices={onChoices}
								onEditSource={() => setStep("source")}
								refreshing={refreshing}
								problem={problem}
							/>
						) : (
							<ReviewStep
								plan={shown}
								choices={choices}
								onChoices={onChoices}
								contents={files}
								onFile={(path, content) =>
									setDraft((current) => ({
										...current,
										files: { ...current.files, [path]: content },
									}))
								}
								onEditSource={() => setStep("source")}
								refreshing={refreshing}
								problem={problem}
							/>
						)
					) : plan.isError ? (
						<Alert variant="destructive">
							<AlertTitle>The Compose file could not be read</AlertTitle>
							<AlertDescription>
								<p>{problem} Nothing was changed.</p>
								<Button
									variant="secondary"
									size="xs"
									onClick={() => setStep("source")}
								>
									Edit source
								</Button>
							</AlertDescription>
						</Alert>
					) : (
						<PlanSkeleton />
					)
				) : null}
				{step === "stage" && shown ? (
					<StageStep
						plan={shown}
						servers={usable}
						serverId={serverId}
						onServer={setPickedServer}
						title={draft.name}
						onTitle={(name) => setDraft((current) => ({ ...current, name }))}
						stackName={stackShown?.name}
						includedNames={
							stackShown
								? [draft.name.trim() || stackShown.name]
								: included.map(
										(service) =>
											choices.services?.[service.key]?.name ?? service.name,
									)
						}
						checking={refreshing}
						failure={failure}
						onCheckAgain={() => {
							stage.reset();
							void plan.refetch();
						}}
					/>
				) : null}
			</DialogBody>
			<DialogFooter>
				{step === "source" ? (
					<>
						<Button
							variant="ghost"
							size="lg"
							onClick={composeImportActions.close}
						>
							Cancel
						</Button>
						<Button
							variant="primary"
							size="lg"
							disabled={!sourceReady(draft)}
							onClick={goReview}
						>
							Review
						</Button>
					</>
				) : null}
				{step === "review" ? (
					<>
						<span
							role="status"
							className="mr-auto min-w-0 text-[12px] text-ink-3 sm:max-w-[50%]"
						>
							{shown
								? stackShown
									? stackSummaryText(stackShown)
									: `${summarizePlan(shown, choices).services} of ${(shown.services ?? []).length} services included`
								: null}
						</span>
						<Button variant="ghost" size="lg" onClick={() => setStep("source")}>
							Back
						</Button>
						<Button
							variant="primary"
							size="lg"
							disabled={!shown}
							onClick={() => setStep("stage")}
						>
							Continue
						</Button>
					</>
				) : null}
				{step === "stage" ? (
					<>
						<span
							role="status"
							className="mr-auto min-w-0 text-[12px] text-ink-3 sm:max-w-[46%]"
						>
							{blocker}
						</span>
						<Button variant="ghost" size="lg" onClick={() => setStep("review")}>
							Back
						</Button>
						<Button
							variant="secondary"
							size="lg"
							disabled={blocker !== null || stage.isPending}
							onClick={() => submit(true)}
						>
							{stage.isPending && stage.variables.deploy ? (
								<Spinner data-icon="inline-start" />
							) : null}
							Stage and deploy
						</Button>
						<Button
							variant="primary"
							size="lg"
							disabled={blocker !== null || stage.isPending}
							onClick={() => submit(false)}
						>
							{stage.isPending && !stage.variables.deploy ? (
								<Spinner data-icon="inline-start" />
							) : null}
							{stackShown ? STAGE_STACK_LABEL : stageLabel(included.length)}
						</Button>
					</>
				) : null}
			</DialogFooter>
		</>
	);
}

/**
 * Mounted once by the canvas screen: the Docker Compose import wizard for
 * the environment on screen.
 */
export function ComposeImportHost() {
	const context = useProjectContext();
	const servers = useServers();
	const { open } = useComposeImportState();
	const environment = context?.environment;
	if (!environment || !context) return null;
	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (!next) composeImportActions.close();
			}}
		>
			<DialogContent width={980} top={5}>
				<Wizard
					environment={environment}
					projectId={context.projectId}
					servers={servers.data ?? NO_SERVERS}
				/>
			</DialogContent>
		</Dialog>
	);
}
