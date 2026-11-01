import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { cn } from "cn";
import { useEffect, useMemo, useRef, useState } from "react";
import { errorMessage, isApiError } from "#/api/client-config";
import type {
	CoolifyApplyResult,
	CoolifyPlan,
	Project,
} from "#/api/gen/types.gen";
import { projectsQuery } from "#/api/hooks";
import type { Server } from "#/api/models";
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
	type Baseline,
	baselineOf,
	buildApplyRequest,
	buildChoices,
	buildPlanRequest,
	draftSecrets,
	EMPTY_DRAFT,
	EMPTY_SOURCE,
	firstTarget,
	type ImportDraft,
	importBlocker,
	includedText,
	redact,
	requestFingerprint,
	type SourceDraft,
	sourceReady,
	summarizePlan,
	usernameHints,
} from "./lib";
import { ResultStep } from "./result-step";
import { ReviewStep } from "./review-step";
import { SOURCE_FORM_ID, SourceStep } from "./source-step";
import { coolifyImportActions, useCoolifyImportState } from "./store";
import { useCoolifyApply, useCoolifyPlan } from "./use-import";

const NO_SERVERS: Server[] = [];
const PLAN_DEBOUNCE_MS = 400;

type Step = "connect" | "review" | "result";

const STEPS: ReadonlyArray<{ id: Step; label: string }> = [
	{ id: "connect", label: "Connect" },
	{ id: "review", label: "Review" },
	{ id: "result", label: "Result" },
];

const DESCRIPTION: Record<Step, string> = {
	connect: "Read a Coolify instance with an API token. Nothing is changed yet.",
	review:
		"Check what each project, application and database becomes and change what you need.",
	result: "The import is done.",
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
			aria-label="Reading Coolify"
		>
			<Skeleton className="h-5 w-56" />
			<Skeleton className="h-40 rounded-card" />
			<Skeleton className="h-40 rounded-card" />
		</div>
	);
}

/**
 * The three steps of the import. All of its state lives here, so closing the
 * dialog (which unmounts it) drops the Coolify token and every value the
 * reader typed. The plan follows the choices a beat after the last change; the
 * answer to the previous request stays on screen until the new one arrives.
 */
function Wizard({
	servers,
	existingProjects,
}: {
	servers: readonly Server[];
	existingProjects: readonly Project[];
}) {
	const navigate = useNavigate();
	const usable = useMemo(
		() =>
			servers.filter(
				(server) => server.status !== "revoked" && server.status !== "pending",
			),
		[servers],
	);
	const [step, setStep] = useState<Step>("connect");
	const [source, setSource] = useState<SourceDraft>(EMPTY_SOURCE);
	const [draft, setDraft] = useState<ImportDraft>(EMPTY_DRAFT);
	const [pickedServer, setPickedServer] = useState("");
	const [deploy, setDeploy] = useState(false);
	const serverId = pickedServer || defaultServerId(usable);

	// The plan on screen, and the request it answers.
	const [shown, setShown] = useState<CoolifyPlan | undefined>();
	const [plannedFor, setPlannedFor] = useState("");
	const [baseline, setBaseline] = useState<Baseline | undefined>();
	const [baselineUrl, setBaselineUrl] = useState("");
	const [result, setResult] = useState<CoolifyApplyResult | undefined>();

	const plan = useCoolifyPlan();
	const apply = useCoolifyApply();

	const hints = useMemo(() => usernameHints(shown), [shown]);
	const request = useMemo(
		() => buildPlanRequest(source, buildChoices(draft, hints), serverId),
		[source, draft, hints, serverId],
	);
	const fingerprint = requestFingerprint(request);
	const settled = useDebounced(fingerprint, PLAN_DEBOUNCE_MS);
	const requestRef = useRef(request);
	requestRef.current = request;
	const lastPlanned = useRef("");

	const runPlan = (
		next: typeof request,
		onPlan?: (plan: CoolifyPlan) => void,
	) => {
		const asked = requestFingerprint(next);
		lastPlanned.current = asked;
		plan.mutate(next, {
			onSuccess: (data) => {
				setShown(data);
				setPlannedFor(asked);
				onPlan?.(data);
			},
		});
	};

	// A change of choices re-plans a beat after the last change. `plan.mutate`
	// aborts the request before it, and only the latest answer is taken.
	// biome-ignore lint/correctness/useExhaustiveDependencies: the plan runs when the settled request changes, not when its callbacks do.
	useEffect(() => {
		if (step !== "review" || settled === lastPlanned.current) return;
		runPlan(requestRef.current);
	}, [settled, step]);

	const secrets = draftSecrets(source, draft);
	const problemOf = (error: unknown) => redact(errorMessage(error), secrets);

	const onChoices = (update: (current: ImportDraft) => ImportDraft) => {
		apply.reset();
		setDraft(update);
	};

	const read = () => {
		if (!sourceReady(source) || plan.isPending) return;
		const url = source.url.trim();
		const same = baseline !== undefined && baselineUrl === url;
		// A different Coolify starts the choices over: they are keyed by its uuids.
		const nextDraft = same
			? draft
			: { ...EMPTY_DRAFT, gitConnectionId: draft.gitConnectionId };
		if (!same) setDraft(nextDraft);
		apply.reset();
		runPlan(
			buildPlanRequest(
				source,
				buildChoices(nextDraft, same ? hints : {}),
				serverId,
			),
			(data) => {
				if (!same) {
					setBaseline(baselineOf(data));
					setBaselineUrl(url);
				}
				setStep("review");
			},
		);
	};

	const fresh = plannedFor === fingerprint && !plan.isPending;
	const refreshing =
		step === "review" &&
		!plan.isError &&
		(plan.isPending || settled !== fingerprint || plannedFor !== fingerprint);
	const blocker = importBlocker({
		plan: shown,
		serverId,
		fresh,
		failed: plan.isError,
	});
	const summary = shown ? summarizePlan(shown, draft) : undefined;

	const submit = () => {
		if (blocker !== null || apply.isPending) return;
		apply.mutate(
			buildApplyRequest(source, buildChoices(draft, hints), serverId, deploy),
			{
				onSuccess: (data) => {
					setResult(data);
					setStep("result");
				},
			},
		);
	};

	const target = result ? firstTarget(result) : null;
	const open = () => {
		if (!target) return;
		coolifyImportActions.close();
		void navigate({
			to: "/p/$projectId/$envSlug",
			params: { projectId: target.projectId, envSlug: target.envSlug },
		});
	};

	const sourceProblem =
		step === "connect" && plan.isError
			? {
					message: problemOf(plan.error),
					code: isApiError(plan.error) ? plan.error.code : undefined,
				}
			: null;

	return (
		<>
			<DialogHeader className="gap-3">
				<div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2">
					<DialogTitle>Import from Coolify</DialogTitle>
					<StepList step={step} />
				</div>
				<DialogDescription>{DESCRIPTION[step]}</DialogDescription>
			</DialogHeader>
			<DialogBody className="overscroll-contain">
				{step === "connect" ? (
					<SourceStep
						source={source}
						onSource={(next) => {
							if (plan.isError) plan.reset();
							setSource(next);
						}}
						servers={usable}
						serverId={serverId}
						onServer={setPickedServer}
						gitConnectionId={draft.gitConnectionId}
						onGitConnection={(id) =>
							setDraft((current) => ({ ...current, gitConnectionId: id }))
						}
						problem={sourceProblem}
						onSubmit={read}
					/>
				) : null}
				{step === "review" ? (
					shown ? (
						<ReviewStep
							plan={shown}
							draft={draft}
							onDraft={onChoices}
							baseline={baseline}
							existingProjects={existingProjects}
							server={usable.find((s) => s.id === serverId)}
							deploy={deploy}
							onDeploy={(next) => {
								apply.reset();
								setDeploy(next);
							}}
							refreshing={refreshing}
							problem={plan.isError ? problemOf(plan.error) : null}
							applyProblem={apply.isError ? problemOf(apply.error) : null}
							onRetry={() => runPlan(requestRef.current)}
							onEditSource={() => setStep("connect")}
						/>
					) : (
						<PlanSkeleton />
					)
				) : null}
				{step === "result" && result ? (
					<ResultStep result={result} deployed={deploy} />
				) : null}
			</DialogBody>
			<DialogFooter>
				{step === "connect" ? (
					<>
						<Button
							variant="ghost"
							size="lg"
							onClick={coolifyImportActions.close}
						>
							Cancel
						</Button>
						<Button
							type="submit"
							form={SOURCE_FORM_ID}
							variant="primary"
							size="lg"
							disabled={!sourceReady(source) || plan.isPending}
						>
							{plan.isPending ? <Spinner data-icon="inline-start" /> : null}
							{plan.isPending ? "Reading Coolify" : "Read Coolify"}
						</Button>
					</>
				) : null}
				{step === "review" ? (
					<>
						<span
							role="status"
							className="mr-auto min-w-0 text-[12px] text-ink-3 sm:max-w-[50%]"
						>
							{blocker ?? (summary ? includedText(summary) : null)}
						</span>
						<Button
							variant="ghost"
							size="lg"
							onClick={() => setStep("connect")}
						>
							Back
						</Button>
						<Button
							variant="primary"
							size="lg"
							disabled={blocker !== null || apply.isPending}
							onClick={submit}
						>
							{apply.isPending ? <Spinner data-icon="inline-start" /> : null}
							{apply.isPending
								? "Importing"
								: deploy
									? "Import and deploy"
									: "Import"}
						</Button>
					</>
				) : null}
				{step === "result" ? (
					<>
						<Button
							variant="ghost"
							size="lg"
							onClick={coolifyImportActions.close}
						>
							Close
						</Button>
						{target ? (
							<Button variant="primary" size="lg" onClick={open}>
								Open {target.name}
							</Button>
						) : null}
					</>
				) : null}
			</DialogFooter>
		</>
	);
}

/** The NebulaCtrl projects a Coolify project can be merged into. */
function useExistingProjects() {
	const projects = useQuery(projectsQuery());
	return useMemo(
		() => (projects.data ?? []).filter((project) => !project.system),
		[projects.data],
	);
}

/**
 * Mounted once by the canvas screen (through the create host): the import
 * from Coolify wizard.
 */
export function CoolifyImportHost() {
	const servers = useServers();
	const existingProjects = useExistingProjects();
	const { open } = useCoolifyImportState();
	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (!next) coolifyImportActions.close();
			}}
		>
			<DialogContent width={980} top={5}>
				<Wizard
					servers={servers.data ?? NO_SERVERS}
					existingProjects={existingProjects}
				/>
			</DialogContent>
		</Dialog>
	);
}
