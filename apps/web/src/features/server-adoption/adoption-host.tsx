import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type {
	AdoptionChoices,
	AdoptionPlan,
	AdoptionResult,
} from "#/api/gen/types.gen";
import { projectsQuery } from "#/api/hooks";
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
import { serverViewQuery } from "#/features/servers/queries";
import { useDebounced } from "#/features/templates/use-debounced";
import { EnvironmentCard } from "./environment-card";
import {
	adoptionBlocker,
	applyLabel,
	buildChoices,
	choicesFingerprint,
	EMPTY_CHOICES,
	leaveAllChoices,
	resultTitle,
	totalsText,
} from "./lib";
import { ResultStep } from "./result-step";
import { closeAdoption, useAdoptionState } from "./store";
import { useAdoptionApply, useAdoptionPlan } from "./use-adoption";

const PLAN_DEBOUNCE_MS = 400;

type Step = "review" | "result";

function PlanSkeleton() {
	return (
		<div
			className="flex flex-col gap-3"
			role="status"
			aria-label="Reading the host"
		>
			<Skeleton className="h-5 w-56" />
			<Skeleton className="h-40 rounded-card" />
			<Skeleton className="h-40 rounded-card" />
		</div>
	);
}

function Notes({ plan }: { plan: AdoptionPlan }) {
	const warnings = plan.warnings ?? [];
	if (warnings.length === 0) return null;
	return (
		<Alert variant="warning" role="note">
			<AlertTitle>
				{warnings.length === 1 ? "A note" : `${warnings.length} notes`}
			</AlertTitle>
			<AlertDescription>
				<ul className="m-0 flex list-disc flex-col gap-1 pl-4">
					{warnings.map((warning) => (
						<li key={warning}>{warning}</li>
					))}
				</ul>
			</AlertDescription>
		</Alert>
	);
}

/** The reasons Apply is off, listed above the footer. */
function Blockers({ blockers }: { blockers: readonly string[] }) {
	if (blockers.length === 0) return null;
	return (
		<Alert variant="destructive" role="alert">
			<AlertTitle>
				{blockers.length === 1
					? "Fix this to continue"
					: `Fix these ${blockers.length} to continue`}
			</AlertTitle>
			<AlertDescription>
				<ul className="m-0 flex list-disc flex-col gap-1 pl-4">
					{blockers.map((blocker) => (
						<li key={blocker}>{blocker}</li>
					))}
				</ul>
			</AlertDescription>
		</Alert>
	);
}

/** The NebulaCtrl projects an environment can be adopted into. */
function useExistingProjects() {
	const projects = useQuery(projectsQuery());
	return useMemo(
		() => (projects.data ?? []).filter((project) => !project.system),
		[projects.data],
	);
}

/**
 * The review of the workloads found on one server. All of its state lives
 * here, so closing the dialog (which unmounts it) drops every choice. The plan
 * is read when the dialog opens and follows the choices a beat after the last
 * change; the answer to the previous request stays on screen until the new
 * one arrives.
 */
function Review({
	serverId,
	onBusy,
}: {
	serverId: string;
	/** Reports whether an apply is in flight, so the dialog can refuse to close under it. */
	onBusy(busy: boolean): void;
}) {
	const existingProjects = useExistingProjects();
	const server = useQuery(serverViewQuery(serverId));
	const [step, setStep] = useState<Step>("review");
	const [choices, setChoices] = useState<AdoptionChoices>(EMPTY_CHOICES);
	const [shown, setShown] = useState<AdoptionPlan | undefined>();
	const [plannedFor, setPlannedFor] = useState("");
	const [result, setResult] = useState<AdoptionResult | undefined>();

	const plan = useAdoptionPlan(serverId);
	const apply = useAdoptionApply(serverId);

	const fingerprint = choicesFingerprint(choices);
	const settled = useDebounced(fingerprint, PLAN_DEBOUNCE_MS);
	const choicesRef = useRef(choices);
	choicesRef.current = choices;
	const lastPlanned = useRef<string | null>(null);

	const runPlan = (next: AdoptionChoices) => {
		const asked = choicesFingerprint(next);
		lastPlanned.current = asked;
		plan.mutate(
			{ choices: buildChoices(next) },
			{
				onSuccess: (data) => {
					setShown(data);
					setPlannedFor(asked);
				},
			},
		);
	};

	// The first plan goes out when the dialog opens; after that a change of
	// choices re-plans once it has settled. `plan.mutate` aborts the request
	// before it, and only the latest answer is taken.
	// biome-ignore lint/correctness/useExhaustiveDependencies: the plan runs when the settled choices change, not when its callbacks do.
	useEffect(() => {
		if (step !== "review" || settled !== fingerprint) return;
		if (settled === lastPlanned.current) return;
		runPlan(choicesRef.current);
	}, [settled, step]);

	// While an apply is in flight nothing can change: an edit would discard its
	// result, and a second press would send it twice.
	const locked = apply.isPending;
	// biome-ignore lint/correctness/useExhaustiveDependencies: the host is told when the apply starts or ends, not when its callback changes.
	useEffect(() => onBusy(locked), [locked]);

	const onChoices = (update: (current: AdoptionChoices) => AdoptionChoices) => {
		if (locked) return;
		apply.reset();
		setChoices(update);
	};

	const fresh = plannedFor === fingerprint && !plan.isPending;
	const refreshing =
		!plan.isError &&
		(plan.isPending || settled !== fingerprint || plannedFor !== fingerprint);
	const blocker = adoptionBlocker({
		plan: shown,
		fresh,
		failed: plan.isError,
	});
	const blockers = fresh ? (shown?.blockers ?? []) : [];

	const submit = (request: AdoptionChoices) => {
		if (apply.isPending) return;
		apply.mutate(
			{ choices: request },
			{
				onSuccess: (data) => {
					setResult(data);
					setStep("result");
				},
			},
		);
	};

	const name = server.data?.name;
	const held = shown?.held ?? server.data?.adoption?.state === "held";

	if (step === "result" && result) {
		return (
			<>
				<DialogHeader className="gap-3">
					<DialogTitle>{resultTitle(result)}</DialogTitle>
					<DialogDescription>The review is done.</DialogDescription>
				</DialogHeader>
				<DialogBody className="overscroll-contain">
					<ResultStep result={result} onNavigate={closeAdoption} />
				</DialogBody>
				<DialogFooter>
					<Button variant="primary" size="lg" onClick={closeAdoption}>
						Close
					</Button>
				</DialogFooter>
			</>
		);
	}

	return (
		<>
			<DialogHeader className="gap-3">
				<DialogTitle>
					{name ? `Review workloads on ${name}` : "Review workloads"}
				</DialogTitle>
				<DialogDescription>
					{held
						? "This host runs services from an earlier NebulaCtrl. Nothing on it is changed until you apply. Adopting keeps the containers running; nothing is recreated."
						: "These workloads run on this host without being managed. Adopting keeps the containers running; nothing is recreated."}
				</DialogDescription>
			</DialogHeader>
			<DialogBody className="overscroll-contain">
				{shown ? (
					<div className="flex flex-col gap-4">
						<Notes plan={shown} />
						<ul className="m-0 flex list-none flex-col gap-3 p-0">
							{(shown.environments ?? []).map((environment) => (
								<EnvironmentCard
									key={environment.id}
									environment={environment}
									choices={choices}
									existingProjects={existingProjects}
									onChoices={onChoices}
									locked={locked}
								/>
							))}
						</ul>
						<Blockers blockers={blockers} />
						{plan.isError ? (
							<Alert variant="destructive" role="alert">
								<AlertTitle>The host could not be read again</AlertTitle>
								<AlertDescription>
									<p>{errorMessage(plan.error)}</p>
									<Button
										variant="secondary"
										size="sm"
										onClick={() => runPlan(choicesRef.current)}
									>
										Try again
									</Button>
								</AlertDescription>
							</Alert>
						) : null}
						{apply.isError ? (
							<Alert variant="destructive" role="alert">
								<AlertTitle>Nothing was adopted</AlertTitle>
								<AlertDescription>{errorMessage(apply.error)}</AlertDescription>
							</Alert>
						) : null}
					</div>
				) : plan.isError ? (
					<Alert variant="destructive" role="alert">
						<AlertTitle>The host could not be read</AlertTitle>
						<AlertDescription>
							<p>{errorMessage(plan.error)}</p>
							<Button
								variant="secondary"
								size="sm"
								onClick={() => runPlan(choicesRef.current)}
							>
								Try again
							</Button>
						</AlertDescription>
					</Alert>
				) : (
					<PlanSkeleton />
				)}
			</DialogBody>
			<DialogFooter>
				<span
					role="status"
					className="mr-auto min-w-0 text-[12px] text-ink-3 sm:max-w-[45%]"
				>
					{refreshing ? <Spinner data-icon="inline-start" /> : null}
					{blocker && blockers.length === 0
						? blocker
						: shown
							? totalsText(shown)
							: null}
				</span>
				<Button
					variant="ghost"
					size="lg"
					disabled={locked}
					onClick={closeAdoption}
				>
					Cancel
				</Button>
				<Button
					variant="secondary"
					size="lg"
					disabled={!shown || apply.isPending}
					onClick={() => shown && submit(leaveAllChoices(shown, choices))}
				>
					Leave all running unmanaged
				</Button>
				<Button
					variant="primary"
					size="lg"
					disabled={blocker !== null || apply.isPending}
					onClick={() => submit(buildChoices(choices))}
				>
					{apply.isPending ? <Spinner data-icon="inline-start" /> : null}
					{apply.isPending ? "Adopting" : applyLabel(shown)}
				</Button>
			</DialogFooter>
		</>
	);
}

/**
 * Mounted by the servers page and the server's page: the review of the
 * workloads a server runs from an earlier NebulaCtrl, opened with
 * `openAdoption(serverId)`.
 */
export function AdoptionHost() {
	const { open, serverId } = useAdoptionState();
	const [busy, setBusy] = useState(false);
	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (!next && !busy) closeAdoption();
			}}
		>
			<DialogContent width={980} top={5}>
				{serverId ? (
					<Review key={serverId} serverId={serverId} onBusy={setBusy} />
				) : null}
			</DialogContent>
		</Dialog>
	);
}
