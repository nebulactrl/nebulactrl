// Turns the metrics responses into chart rows. Timestamps become epoch
// milliseconds (the shared numeric x axis), bytes become the units each
// chart prints, and gaps in sampling become real gaps in the line.

import type { MetricPoint, ServiceMetricPoint } from "#/api/gen/types.gen";
import { bytesToMb } from "./chart-format";

/** Response-level fields beside `points`. */
export interface MetricsMeta {
	stepSeconds?: number;
	retentionDays?: number;
}

export interface ServiceChartPoint {
	t: number;
	/** CPU summed over the replicas, in percent of one core. */
	cpu: number | null;
	cpuMax: number | null;
	/** CPU averaged per replica, in percent of one core. */
	cpuPerReplica: number | null;
	/** Replica containers sampled in this bucket; 0 for a gap row. */
	replicaCount: number;
	/** Memory summed over the replicas. */
	memMb: number | null;
	memMaxMb: number | null;
	/** Memory limit summed over the replicas, in MiB; null when none is set. */
	limitMb: number | null;
	/** Memory as a share of the limit, 0–100; null without a limit. */
	memPct: number | null;
}

export interface ServerChartPoint {
	t: number;
	cpu: number | null;
	cpuMax: number | null;
	memPct: number | null;
	memMaxPct: number | null;
	diskPct: number | null;
	memUsed: number;
	memTotal: number;
	diskUsed: number;
	diskTotal: number;
}

export function toServicePoints(
	points: readonly ServiceMetricPoint[] | null | undefined,
): ServiceChartPoint[] {
	return (points ?? []).map((p) => {
		const memMb = bytesToMb(p.memBytes);
		const limitMb = p.memLimitBytes > 0 ? bytesToMb(p.memLimitBytes) : null;
		return {
			t: Date.parse(p.ts),
			cpu: p.cpuPercent,
			cpuMax: p.cpuMax ?? p.cpuPercent,
			cpuPerReplica: p.cpuPerReplica,
			replicaCount: p.replicaCount,
			memMb,
			memMaxMb: bytesToMb(p.memMax ?? p.memBytes),
			limitMb,
			memPct: limitMb ? Math.min(100, (memMb / limitMb) * 100) : null,
		};
	});
}

export function toServerPoints(
	points: readonly MetricPoint[] | null | undefined,
): ServerChartPoint[] {
	return (points ?? []).map((p) => ({
		t: Date.parse(p.ts),
		cpu: p.cpuPercent,
		cpuMax: p.cpuMax ?? p.cpuPercent,
		memPct: p.memPercent,
		memMaxPct:
			p.memTotalBytes > 0
				? ((p.memMax ?? p.memBytes) / p.memTotalBytes) * 100
				: null,
		diskPct: p.diskPercent,
		memUsed: p.memBytes,
		memTotal: p.memTotalBytes,
		diskUsed: p.diskUsedBytes,
		diskTotal: p.diskTotalBytes,
	}));
}

/** True when a series has a peak above its average somewhere (downsampled). */
export function hasPeaks(
	rows: readonly { cpu: number | null; cpuMax: number | null }[],
): boolean {
	return rows.some(
		(r) => r.cpu !== null && r.cpuMax !== null && r.cpuMax > r.cpu + 0.05,
	);
}

/**
 * Inserts a null row wherever two samples are further apart than `factor`
 * steps, so an outage draws as a break instead of a straight line across it.
 * `step` is the response's step in ms, or the median spacing when absent.
 */
export function breakGaps<T extends { t: number }>(
	rows: readonly T[],
	stepMs: number,
	blank: (t: number) => T,
	factor = 3,
): T[] {
	if (rows.length < 2 || stepMs <= 0) return [...rows];
	const out: T[] = [];
	let prev: T | undefined;
	for (const row of rows) {
		if (prev && row.t - prev.t > stepMs * factor) {
			out.push(blank(prev.t + stepMs));
		}
		out.push(row);
		prev = row;
	}
	return out;
}

/** Median spacing between rows, in ms; 0 for fewer than two rows. */
export function medianStepMs(rows: readonly { t: number }[]): number {
	if (rows.length < 2) return 0;
	const gaps: number[] = [];
	let prev: number | undefined;
	for (const row of rows) {
		if (prev !== undefined) gaps.push(row.t - prev);
		prev = row.t;
	}
	gaps.sort((a, b) => a - b);
	return gaps[Math.floor(gaps.length / 2)] ?? 0;
}

/** The newest sample, or undefined when there are none. */
export function latest<T>(rows: readonly T[]): T | undefined {
	return rows[rows.length - 1];
}

/**
 * True when the newest sample is older than `factor` steps: the feed is
 * lagging (agent paused, server offline) and "Live" would be a lie.
 */
export function isDelayed(
	newestMs: number | undefined,
	stepMs: number,
	now: number,
	factor = 3,
): boolean {
	if (newestMs === undefined || stepMs <= 0) return false;
	return now - newestMs > stepMs * factor;
}

/**
 * Series colours for replicas, by index. Tokens only; the "replica N" label
 * always sits beside the swatch, so colour is never the only cue. Past the
 * palette the colours repeat (a service has at most ten replicas).
 */
export const REPLICA_COLORS = [
	"var(--color-accent-ink)",
	"var(--color-status-live)",
	"var(--color-info)",
	"var(--color-warn)",
	"var(--color-status-degraded)",
	"var(--color-bad)",
] as const;

export function replicaColor(index: number): string {
	const n = REPLICA_COLORS.length;
	return REPLICA_COLORS[((index % n) + n) % n] ?? REPLICA_COLORS[0];
}

/** One replica's usage at the newest sample. */
export interface ReplicaNow {
	index: number;
	/** Percent of one core. */
	cpuPercent: number;
	memMb: number;
}

/**
 * Per-replica usage at the newest sample, sorted by replica index. Only the
 * last point of a response carries it (the API returns no per-replica
 * history), so this reads that point and nothing older; empty when absent.
 */
export function latestReplicas(
	points: readonly ServiceMetricPoint[] | null | undefined,
): ReplicaNow[] {
	const last = points?.[points.length - 1];
	return [...(last?.replicas ?? [])]
		.sort((a, b) => a.index - b.index)
		.map((r) => ({
			index: r.index,
			cpuPercent: r.cpuPercent,
			memMb: bytesToMb(r.memBytes),
		}));
}

/** A replica's bar fills, as fractions of 0–1 (never past full). */
export interface ReplicaBar extends ReplicaNow {
	cpuFrac: number;
	/** Share of the per-replica memory limit; null when no limit is set. */
	memFrac: number | null;
}

/**
 * Bar fractions for the per-replica list. `cpuCap` is what a full CPU bar
 * means (the per-replica quota, or one core = 100); `memLimitMb` is the
 * per-replica memory limit, or null when unlimited.
 */
export function replicaBars(
	replicas: readonly ReplicaNow[],
	cpuCap: number,
	memLimitMb: number | null,
): ReplicaBar[] {
	const clamp = (v: number) => Math.min(1, Math.max(0, v));
	return replicas.map((r) => ({
		...r,
		cpuFrac: cpuCap > 0 ? clamp(r.cpuPercent / cpuCap) : 0,
		memFrac: memLimitMb && memLimitMb > 0 ? clamp(r.memMb / memLimitMb) : null,
	}));
}

/**
 * The replicas to list under the CPU chart: all of them, or only the picked
 * ones. An empty pick means "all"; picks for replicas that no longer exist
 * are ignored, and if none of the picks exist the list falls back to all.
 */
export function visibleReplicas<T extends { index: number }>(
	replicas: readonly T[],
	picked: ReadonlySet<number>,
): T[] {
	if (picked.size === 0) return [...replicas];
	const shown = replicas.filter((r) => picked.has(r.index));
	return shown.length > 0 ? shown : [...replicas];
}

/**
 * The query of a service metrics request: the range, plus the process when
 * the view is narrowed to one. Without `process` the series is the service
 * total (every process summed).
 */
export function serviceMetricsParams<R extends string>(
	range: R,
	process?: string,
): { range: R; process?: string } {
	return process ? { range, process } : { range };
}

/** What a process' memory and CPU limits are, with inheritance from the service applied. */
export interface ProcessResources {
	memoryMb: number | undefined;
	cpus: number | undefined;
}

interface ProcessLimits {
	memoryMb?: number | undefined;
	cpus?: number | undefined;
}

/**
 * The limits that apply to the view. One process: its own, else the
 * service's. The total of a service with processes has no single limit (its
 * processes may differ), so nothing is assumed there. A service without
 * processes: the service's.
 */
export function processResources(
	config: ProcessLimits & {
		processes: readonly (ProcessLimits & { name: string })[];
	},
	process: string | undefined,
): ProcessResources {
	if (process === undefined) {
		return config.processes.length > 0
			? { memoryMb: undefined, cpus: undefined }
			: { memoryMb: config.memoryMb, cpus: config.cpus };
	}
	const own = config.processes.find((candidate) => candidate.name === process);
	return {
		memoryMb: own?.memoryMb || config.memoryMb,
		cpus: own?.cpus || config.cpus,
	};
}

/**
 * Whether the per-replica list is shown: replicas are numbered inside their
 * process, so a service with processes lists them only for one process.
 */
export function showReplicaList(
	hasProcesses: boolean,
	process: string | undefined,
	replicaCount: number,
): boolean {
	return replicaCount > 1 && (!hasProcesses || process !== undefined);
}
