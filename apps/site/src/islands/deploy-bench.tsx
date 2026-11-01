import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import { useReducedMotionPref } from "@nebulactrl/dither/lib/prefs";
import { ArrowCounterClockwiseIcon } from "@phosphor-icons/react/dist/csr/ArrowCounterClockwise";
import { PlayIcon } from "@phosphor-icons/react/dist/csr/Play";
import {
	type CSSProperties,
	useCallback,
	useEffect,
	useRef,
	useState,
} from "react";
import {
	type DeployEvent,
	type DeployPhase,
	type DeployRecording,
	deployFailRecording,
	deployRecording,
	type ReplayClock,
	replayClock,
} from "../content/recordings.ts";
import { MotionKey, Swap } from "./motion/key.tsx";

type PhaseState = "off" | "live" | "done" | "fail";
type Mode = "pass" | "fail";

const STEP_LABEL: Record<string, string> = {
	pull: "Pull image",
	create: "Create container",
	start: "Start container",
	health: "Health check",
	route: "Move the route",
	verify: "Verify through the proxy",
	retire: "Retire previous release",
};

/** The longest a replayed wait may last. The failing run really waited 20 s for its check. */
const WAIT_CAP_SECONDS = 6;

interface Run {
	rec: DeployRecording;
	label: string;
	clock: ReplayClock;
	/** Events worth a line: a step starting or finishing. The status rows only restate them. */
	log: DeployEvent[];
	/** The line the product prints last, after the failure. */
	verdict: string | null;
}

function makeRun(rec: DeployRecording, label: string): Run {
	const last = rec.events[rec.events.length - 1];
	return {
		rec,
		label,
		clock: replayClock(rec, WAIT_CAP_SECONDS),
		log: rec.events.filter(
			(event) => event.step !== "status" && STEP_LABEL[event.step],
		),
		verdict:
			rec.outcome === "failed"
				? (last?.message
						.split("\n")
						.find((line) => /still serving/.test(line)) ?? null)
				: null,
	};
}

const RUNS: Record<Mode, Run> = {
	pass: makeRun(deployRecording, "Healthy release"),
	fail: makeRun(deployFailRecording, "Failing health check"),
};

function phaseState(
	phase: DeployPhase,
	elapsed: number | null,
	failed: boolean,
): PhaseState {
	if (phase.skipped) return "off";
	if (elapsed === null) return "done";
	if (elapsed >= phase.endS)
		return failed && phase.id === "verify" ? "fail" : "done";
	return elapsed >= phase.startS ? "live" : "off";
}

function seconds(value: number): string {
	return `${value.toFixed(1)} s`;
}

/** The first line of a failed step's message, without the repeated prefix and the long address list. */
function failureLine(message: string): string {
	return (message.split("\n")[0] ?? "")
		.replace(/^Health check failed:\s*/, "")
		.replace(/\s*\(last failure:.*$/, "");
}

function Led({
	label,
	state,
	skipped,
}: {
	label: string;
	state: PhaseState;
	skipped: boolean;
}) {
	return (
		<div className="flex min-w-[52px] flex-1 flex-col gap-1">
			<span className="phase-strip w-full">
				<i data-s={state} />
			</span>
			<span className="text-ink-3 text-[13px] leading-none">
				{label}
				{skipped ? " (skipped)" : ""}
			</span>
		</div>
	);
}

/**
 * One recorded deployment of `web`, replayed at its real timings (a wait longer
 * than six seconds is shortened). Idle it shows the events of the last
 * recorded run. Deploy web replays one of two deployments: the healthy release
 * moves the route after its check passes; the failing one never moves it, and
 * the old release keeps serving. The phases follow the recorded phases, the
 * Deployment bar is the kit's dithered progress bar and the log prints the
 * recorded events. Reduced motion jumps to the finished state.
 */
export default function DeployBench() {
	const reduced = useReducedMotionPref();
	const [mode, setMode] = useState<Mode>("pass");
	/** Seconds of replay since Deploy was pressed, or null before. */
	const [replayed, setReplayed] = useState<number | null>(null);
	const [ran, setRan] = useState(false);
	const started = useRef(0);
	const timer = useRef<number | undefined>(undefined);
	const logEnd = useRef<HTMLOListElement>(null);
	/** More rows lie below the visible part of the events list, so its bottom edge fades. */
	const [more, setMore] = useState(true);
	const syncMore = useCallback(() => {
		const el = logEnd.current;
		if (el) setMore(el.scrollHeight - el.scrollTop - el.clientHeight > 4);
	}, []);

	const run = RUNS[mode];
	const recording = run.rec;
	const failedRun = recording.outcome === "failed";
	const total = recording.totalSeconds;
	const replayTotal = run.clock.replayTotal;
	const elapsed = replayed === null ? null : run.clock.toRecording(replayed);
	const running = replayed !== null && replayed < replayTotal;

	useEffect(() => () => window.clearInterval(timer.current), []);

	const reset = (next: Mode) => {
		window.clearInterval(timer.current);
		setMode(next);
		setReplayed(null);
		setRan(false);
	};

	const deploy = () => {
		window.clearInterval(timer.current);
		setRan(true);
		if (reduced) {
			setReplayed(replayTotal);
			return;
		}
		started.current = performance.now();
		setReplayed(0);
		timer.current = window.setInterval(() => {
			const next = (performance.now() - started.current) / 1000;
			if (next >= replayTotal) {
				window.clearInterval(timer.current);
				setReplayed(replayTotal);
			} else setReplayed(next);
		}, 60);
	};

	const shown =
		elapsed === null ? [] : run.log.filter((event) => event.t <= elapsed);
	const idle = elapsed === null;
	// biome-ignore lint/correctness/useExhaustiveDependencies: switching mode swaps the log, so the scroll and its edge fade are synced again.
	useEffect(() => {
		const el = logEnd.current;
		if (!el) return;
		if (shown.length) el.scrollTop = el.scrollHeight;
		else if (idle) el.scrollTop = 0;
		syncMore();
	}, [shown.length, idle, mode, syncMore]);

	const routeSwitchS = recording.routeSwitchS;
	const switched =
		elapsed === null || routeSwitchS === null ? false : elapsed >= routeSwitchS;
	const finished = replayed !== null && replayed >= replayTotal;
	const failedNow = finished && failedRun;
	const serving = !ran
		? "Serving the current release."
		: failedNow
			? `Health check failed. ${run.verdict ?? "Previous release is still serving."}`
			: running && !switched
				? "Checking the new container. The old release is serving."
				: finished
					? "The new release is serving."
					: "Check passed. Traffic moved.";

	return (
		<div className="surface-raised relative rounded-2xl p-4 sm:p-6">
			<div className="mb-5 flex flex-wrap items-center gap-x-6 gap-y-3">
				<fieldset className="segmented m-0 border-0 p-0">
					<legend className="sr-only">
						Which recorded deployment to replay
					</legend>
					<span
						aria-hidden
						className="segmented-thumb"
						style={
							{
								"--i": Object.keys(RUNS).indexOf(mode),
								"--n": Object.keys(RUNS).length,
							} as CSSProperties
						}
					/>
					{(Object.keys(RUNS) as Mode[]).map((key) => (
						<button
							key={key}
							type="button"
							aria-pressed={mode === key}
							disabled={running}
							onClick={() => reset(key)}
							className="cursor-pointer disabled:cursor-not-allowed"
						>
							{RUNS[key].label}
						</button>
					))}
				</fieldset>
				<MotionKey
					type="button"
					onClick={deploy}
					disabled={running}
					data-busy={running ? "" : undefined}
					className="btn-primary inline-flex h-11 cursor-pointer items-center gap-2 rounded-lg py-0 pr-5 pl-4 font-semibold text-base outline-none focus-visible:focus-ring disabled:cursor-not-allowed disabled:opacity-60"
				>
					<Swap swapKey={finished ? "again" : "play"}>
						{finished ? (
							<ArrowCounterClockwiseIcon
								aria-hidden
								weight="bold"
								className="size-4"
							/>
						) : (
							<PlayIcon aria-hidden weight="fill" className="ml-px size-4" />
						)}
					</Swap>
					{running
						? "Deploying web"
						: finished
							? "Deploy web again"
							: "Deploy web"}
				</MotionKey>
			</div>

			<div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
				<div className="surface-well flex flex-col gap-5 rounded-xl p-4 lg:col-span-5">
					<DitherProgressBar
						label="Deployment"
						value={elapsed ?? 0}
						max={total}
						valueText={
							elapsed === null
								? "Not running"
								: `${seconds(Math.min(elapsed, total))} of ${seconds(total)}`
						}
						color={failedNow ? "red" : finished ? "green" : "blue"}
						size="lg"
						animate={false}
						activity={running ? "band" : "none"}
						complete={finished}
					/>
					<div
						role="img"
						aria-label={recording.phases
							.map((phase) => {
								const state = phaseState(phase, elapsed, failedNow);
								return `${phase.label} ${phase.skipped ? "skipped" : state === "off" ? "waiting" : state === "live" ? "running" : state === "fail" ? "failed" : "done"}`;
							})
							.join(", ")}
						className="flex gap-[5px]"
					>
						{recording.phases.map((phase) => (
							<Led
								key={phase.id}
								label={phase.label}
								skipped={phase.skipped}
								state={phaseState(phase, elapsed, failedNow)}
							/>
						))}
					</div>
					<p aria-live="polite" className="m-0 text-ink-2 text-sm">
						{serving}
					</p>
				</div>
				<div className="surface-well rounded-xl p-4 lg:col-span-7">
					<div className="mb-3 flex items-baseline justify-between gap-4 text-sm">
						<span className="font-semibold text-ink-1">Events</span>
						<span className="text-[13px] text-ink-3">
							{elapsed === null
								? "Last run"
								: `${shown.length} of ${run.log.length}`}
						</span>
					</div>
					<div className="log-fade" data-more={more ? "" : undefined}>
						<ol
							ref={logEnd}
							onScroll={syncMore}
							className="m-0 flex h-56 list-none flex-col gap-1 overflow-y-auto p-0 text-sm leading-snug"
							style={{ overscrollBehavior: "contain" }}
						>
							{(elapsed === null ? run.log : shown).map((event) => (
								<li
									key={`${event.t}-${event.step}-${event.status}`}
									className="grid grid-cols-[48px_minmax(0,1fr)] gap-x-3"
								>
									<span className="text-right font-mono text-ink-4 tabular-nums">
										{event.t.toFixed(1)}
									</span>
									<span
										className={`min-w-0 ${elapsed === null ? "text-ink-4" : "text-ink-2"}`}
									>
										<span className={elapsed === null ? "" : "text-ink-1"}>
											{STEP_LABEL[event.step]} {event.status}
										</span>
										{event.message && event.status === "started" ? (
											<code
												className={`ml-2 break-all font-mono text-[13px] ${elapsed === null ? "" : "text-ink-3"}`}
											>
												{event.message}
											</code>
										) : null}
										{event.status === "failed" ? (
											<code
												className={`ml-2 break-words font-mono text-[13px] ${elapsed === null ? "" : "text-status-failed-on"}`}
											>
												{failureLine(event.message)}
											</code>
										) : null}
									</span>
								</li>
							))}
						</ol>
					</div>
				</div>
			</div>
		</div>
	);
}
