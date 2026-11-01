// Number and time formatting for chart axes, tooltips and KPI tiles. Pure
// and time-zone-injectable so tests do not depend on the host machine.

export interface DualTime {
	local: string;
	utc: string;
	/** The visitor's zone as Intl names it ("Europe/Berlin"), for the tooltip. */
	zone: string;
}

function formatter(
	timeZone: string | undefined,
	withDate: boolean,
	withSeconds: boolean,
): Intl.DateTimeFormat {
	return new Intl.DateTimeFormat("en-GB", {
		...(timeZone ? { timeZone } : {}),
		...(withDate ? { month: "short", day: "numeric" } : {}),
		hour: "2-digit",
		minute: "2-digit",
		...(withSeconds ? { second: "2-digit" } : {}),
		hourCycle: "h23",
	});
}

/**
 * The same instant in the visitor's zone and in UTC, printed together in
 * every chart tooltip because logs and alerts quote UTC. Seconds only when
 * the step is finer than a minute; the date only when the span crosses days.
 */
export function dualTime(
	ms: number,
	opts: { stepSeconds?: number; spanMs?: number; timeZone?: string } = {},
): DualTime {
	const withSeconds = (opts.stepSeconds ?? 60) < 60;
	const withDate = (opts.spanMs ?? 0) > 20 * 3_600_000;
	const date = new Date(ms);
	const clean = (s: string) => s.replace(/,/g, "");
	return {
		local: clean(formatter(opts.timeZone, withDate, withSeconds).format(date)),
		utc: clean(formatter("UTC", withDate, withSeconds).format(date)),
		zone:
			opts.timeZone ??
			Intl.DateTimeFormat().resolvedOptions().timeZone ??
			"local",
	};
}

/**
 * Axis tick label for a bucket: clock time for spans inside a day, a short
 * date once the step is a day or more, date + hour in between.
 */
export function bucketAxisLabel(
	ms: number,
	stepSeconds: number,
	timeZone?: string,
): string {
	const date = new Date(ms);
	if (stepSeconds >= 86_400) {
		return new Intl.DateTimeFormat("en-GB", {
			...(timeZone ? { timeZone } : {}),
			month: "short",
			day: "numeric",
		}).format(date);
	}
	if (stepSeconds >= 3_600) {
		return new Intl.DateTimeFormat("en-GB", {
			...(timeZone ? { timeZone } : {}),
			month: "short",
			day: "numeric",
			hour: "2-digit",
			hourCycle: "h23",
		})
			.format(date)
			.replace(/,|\sat/g, "");
	}
	return formatter(timeZone, false, false).format(date);
}

/** 12403 → "12.4k", 1_200_000 → "1.2M"; below 1000 the exact integer. */
export function compactNumber(n: number): string {
	if (!Number.isFinite(n)) return "–";
	const abs = Math.abs(n);
	if (abs < 1_000) return String(Math.round(n));
	const units: [number, string][] = [
		[1_000_000_000, "B"],
		[1_000_000, "M"],
		[1_000, "k"],
	];
	for (const [size, suffix] of units) {
		if (abs >= size) {
			const value = n / size;
			// One decimal under 100 of the unit ("12.4k"), none above ("124k").
			const text = Math.abs(value) < 100 ? value.toFixed(1) : value.toFixed(0);
			return `${text.replace(/\.0$/, "")}${suffix}`;
		}
	}
	return String(Math.round(n));
}

/** 0.0042 → "0.42%"; keeps two significant decimals for tiny rates. */
export function formatRate(rate: number): string {
	if (!Number.isFinite(rate)) return "–";
	const pct = rate * 100;
	if (pct === 0) return "0%";
	if (pct < 0.01) return "<0.01%";
	if (pct < 10) return `${Number(pct.toFixed(2))}%`;
	return `${Number(pct.toFixed(1))}%`;
}

/** 182 → "182 ms", 1540 → "1.54 s". */
export function formatMs(ms: number): string {
	if (!Number.isFinite(ms)) return "–";
	if (ms < 1) return "<1 ms";
	if (ms < 1_000) return `${Math.round(ms)} ms`;
	return `${Number((ms / 1_000).toFixed(2))} s`;
}

/** 84_300_112 → "80.4 MiB" (binary, to match memory and `docker stats`). */
export function formatByteCount(n: number): string {
	if (!Number.isFinite(n) || n <= 0) return "0 B";
	const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
	const exponent = Math.min(
		Math.floor(Math.log(n) / Math.log(1024)),
		units.length - 1,
	);
	const value = n / 1024 ** exponent;
	const rounded = Math.round(value * 10) / 10;
	return `${rounded.toFixed(exponent > 0 && !Number.isInteger(rounded) ? 1 : 0)} ${units[exponent]}`;
}

/** Memory in whole mebibytes for chart axes: 222_298_112 → 212. */
export function bytesToMb(bytes: number): number {
	return Math.round((bytes / (1024 * 1024)) * 10) / 10;
}
