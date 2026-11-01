import deployJson from "./deploy-recording.json";
import deployFailJson from "./deploy-recording-fail.json";

/**
 * The real recordings the landing page replays, never written by hand:
 * `deploy-recording.json` is one deployment of the `web` service read back from
 * the API (`/deployments/{id}` and its events), `deploy-recording-fail.json`
 * one whose health check never passes.
 */

export interface DeployEvent {
	t: number;
	step: string;
	status: string;
	message: string;
}

export interface DeployPhase {
	id: "build" | "release" | "verify";
	label: string;
	startS: number;
	endS: number;
	skipped: boolean;
}

export interface CanvasService {
	name: string;
	kind: "app" | "database";
	source: string;
	domain: string | null;
	server: string;
	replicas: number;
	port: number;
	strategy: "rolling" | "recreate";
}

export interface DeployRecording {
	version: 1;
	recordedAt: string;
	service: string;
	source: string;
	strategy: "rolling" | "recreate";
	outcome: string;
	totalSeconds: number;
	events: DeployEvent[];
	phases: DeployPhase[];
	/** Second the route moved to the new container; null when the deployment failed before that. */
	routeSwitchS: number | null;
	canvas: CanvasService[];
}

export const deployRecording = deployJson as unknown as DeployRecording;
/** One deployment whose health check never passes (port 8081 answers nothing), read back from the same API. */
export const deployFailRecording = deployFailJson as unknown as DeployRecording;

/**
 * A replay clock that caps long gaps. A real wait (the failing deploy waits 20 s for
 * a health check) would stall a replay, so each gap between two events is cut to
 * `capSeconds`. Every time shown to the visitor stays the recording's own time; only
 * the pace of the replay is shortened, and `skipped` says by how much.
 */
export interface ReplayClock {
	/** Seconds of replay that the whole recording takes. */
	replayTotal: number;
	/** Seconds cut from the recording. */
	skipped: number;
	/** Recording time at a given replay time. */
	toRecording: (replayT: number) => number;
}

export function replayClock(
	rec: DeployRecording,
	capSeconds: number,
): ReplayClock {
	const times = [...new Set(rec.events.map((event) => event.t))].sort(
		(a, b) => a - b,
	);
	if (times[times.length - 1] !== rec.totalSeconds)
		times.push(rec.totalSeconds);
	// Breakpoints of the piecewise linear map: [recording time, replay time].
	const points: [number, number][] = [[0, 0]];
	let replay = 0;
	let prev = 0;
	for (const t of times) {
		if (t <= prev) continue;
		replay += Math.min(t - prev, capSeconds);
		points.push([t, replay]);
		prev = t;
	}
	const toRecording = (replayT: number): number => {
		for (let i = 1; i < points.length; i++) {
			const [t1, r1] = points[i] as [number, number];
			const [t0, r0] = points[i - 1] as [number, number];
			if (replayT <= r1) {
				const span = r1 - r0;
				return span === 0 ? t1 : t0 + ((replayT - r0) / span) * (t1 - t0);
			}
		}
		return rec.totalSeconds;
	};
	return {
		replayTotal: replay,
		skipped: rec.totalSeconds - replay,
		toRecording,
	};
}
