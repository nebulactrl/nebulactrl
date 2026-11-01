/**
 * Pure formatting helpers for the operator console. No dependencies, no
 * side effects — every function here takes a value and returns a string a
 * reader can trust: absent data reads "not reported", never a fabricated
 * "0" or "—". Sizes read in binary units (MiB/GiB), never raw bytes.
 */

const RELATIVE_UNITS: ReadonlyArray<{ suffix: string; seconds: number }> = [
	{ suffix: "y", seconds: 60 * 60 * 24 * 365.2425 },
	{ suffix: "mo", seconds: 60 * 60 * 24 * 30.436875 },
	{ suffix: "w", seconds: 60 * 60 * 24 * 7 },
	{ suffix: "d", seconds: 60 * 60 * 24 },
	{ suffix: "h", seconds: 60 * 60 },
	{ suffix: "m", seconds: 60 },
	{ suffix: "s", seconds: 1 },
];

/**
 * "6m ago". Short units so a right-aligned column of times stacks in a
 * table cell. Picks the largest unit that has fully elapsed — so 59 minutes
 * reads as "59m ago" rather than rounding up to "1h ago", while 25 hours
 * reads as "1d ago" rather than "25h ago". Anything under ten seconds reads
 * "just now"; a future instant reads "in 6m". Absent input reads "".
 */
export function relativeTime(iso: string | null | undefined): string {
	if (!iso) return "";
	const date = new Date(iso);
	const ms = date.getTime();
	if (Number.isNaN(ms)) return "";

	const diffSeconds = (Date.now() - ms) / 1000;
	const absSeconds = Math.abs(diffSeconds);
	if (absSeconds < 10) return "just now";

	const bucket =
		RELATIVE_UNITS.find(({ seconds }) => absSeconds >= seconds) ??
		RELATIVE_UNITS[RELATIVE_UNITS.length - 1];
	if (!bucket) return "";
	const value = Math.floor(absSeconds / bucket.seconds);
	const amount = `${value}${bucket.suffix}`;
	return diffSeconds >= 0 ? `${amount} ago` : `in ${amount}`;
}

/**
 * "2026-09-20 18:42:05 UTC". Always UTC: the control plane, the agent and
 * every timestamp in the API are UTC, and mixing in the browser's local
 * zone would make two readers looking at the same release disagree on when
 * it happened.
 */
export function absoluteTime(iso: string): string {
	const date = new Date(iso);
	const pad = (value: number) => value.toString().padStart(2, "0");
	const datePart = `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}`;
	const timePart = `${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}:${pad(date.getUTCSeconds())}`;
	return `${datePart} ${timePart} UTC`;
}

/** "2026-09-20", the date part of `absoluteTime`: UTC for the same reason. */
export function absoluteDate(iso: string): string {
	return absoluteTime(iso).slice(0, 10);
}

/**
 * "10.6s", "5m 02s". "340ms" under a second, one decimal of seconds under a
 * minute, zero-padded minutes:seconds under an hour, zero-padded
 * hours:minutes beyond that.
 */
export function formatDuration(ms: number): string {
	const value = Math.max(0, ms);
	if (value < 1_000) return `${Math.round(value)}ms`;
	if (value < 60_000) return `${(value / 1_000).toFixed(1)}s`;
	if (value < 3_600_000) {
		const minutes = Math.floor(value / 60_000);
		const seconds = Math.floor((value % 60_000) / 1_000);
		return `${minutes}m ${seconds.toString().padStart(2, "0")}s`;
	}
	const hours = Math.floor(value / 3_600_000);
	const minutes = Math.floor((value % 3_600_000) / 60_000);
	return `${hours}h ${minutes.toString().padStart(2, "0")}m`;
}

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB"] as const;

/**
 * "512 MiB". General-purpose byte size — a log file, a backup archive, a
 * download. Binary units because that is what Kubernetes reports
 * (allocatable/requests/limits are all Ki/Mi/Gi). `0` reads "0 B".
 */
export function formatBytes(n: number): string {
	if (n === 0) return "0 B";
	const exponent = Math.min(
		Math.floor(Math.log(Math.abs(n)) / Math.log(1024)),
		BYTE_UNITS.length - 1,
	);
	const unit = BYTE_UNITS[exponent] ?? BYTE_UNITS[0];
	const value = n / 1024 ** exponent;
	const rounded = Math.round(value * 10) / 10;
	// One decimal only when it carries information (1.5 GiB), never a
	// trailing ".0" on an exact multiple (2 GiB, not 2.0 GiB).
	const decimals = exponent > 0 && !Number.isInteger(rounded) ? 1 : 0;
	return `${rounded.toFixed(decimals)} ${unit}`;
}

/**
 * "212 MiB" under a GiB, "15.7 GiB" from there on (one decimal, never a
 * trailing ".0"). For memory figures that are already in mebibytes, as the
 * metrics API reports them (`memMb`, `limitMb`). Binary units throughout, to
 * match `formatBytes` and `docker stats`.
 */
export function formatMegabytes(mb: number): string {
	const value = Math.max(0, mb);
	if (value < 1024) return `${Math.round(value)} MiB`;
	const gib = Math.round((value / 1024) * 10) / 10;
	return `${Number.isInteger(gib) ? gib.toFixed(0) : gib.toFixed(1)} GiB`;
}

const DIGEST_PATTERN = /sha256:([0-9a-f]{64})/;

/**
 * "sha256:0a1b2c3d" — the algorithm plus the first eight hex characters of
 * a `repo@sha256:<64 hex>` reference. Never throws: a reference that
 * doesn't match (a tag, a future server sending something unexpected) comes
 * back unchanged rather than crashing the row it's rendered in.
 */
export function shortDigest(imageRef: string): string {
	const match = DIGEST_PATTERN.exec(imageRef);
	const digest = match?.[1];
	if (!digest) return imageRef;
	return `sha256:${digest.slice(0, 8)}`;
}

/**
 * "0a1b2c3d…" — the first eight hex characters of a bare 64-character
 * sha256 digest (a volume backup's own `sha256`, never prefixed with
 * `sha256:` the way an image reference is). `null`/`undefined` reads
 * "not reported"; anything that isn't a 64-char lowercase hex string
 * comes back unchanged rather than crashing the row it's rendered in.
 */
const SHA256_PATTERN = /^[0-9a-f]{64}$/;

export function shortSHA256(sha256: string | null | undefined): string {
	if (sha256 === null || sha256 === undefined) return "not reported";
	if (!SHA256_PATTERN.test(sha256)) return sha256;
	return `${sha256.slice(0, 8)}…`;
}

/**
 * The repository portion of a `repo@sha256:...` reference — everything
 * before the `@`. A reference without `@` (malformed input) comes back
 * unchanged.
 */
export function repository(imageRef: string): string {
	const at = imageRef.indexOf("@");
	return at === -1 ? imageRef : imageRef.slice(0, at);
}

/**
 * "1 node", "3 nodes". Pass `plural` for irregular forms; otherwise an "s"
 * is appended.
 */
export function count(n: number, singular: string, plural?: string): string {
	const word = n === 1 ? singular : (plural ?? `${singular}s`);
	return `${n} ${word}`;
}
