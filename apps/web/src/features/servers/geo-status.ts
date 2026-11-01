import {
	absoluteDate,
	absoluteTime,
	formatBytes,
	relativeTime,
} from "#/lib/format";

/** The GeoIP database as the agent last reported it (the server's `geoStatus`). */
export interface GeoDatabase {
	downloaded: boolean;
	source?: string | undefined;
	level?: string | undefined;
	label?: string | undefined;
	sizeBytes?: number | undefined;
	buildTime?: string | undefined;
	checkedAt?: string | undefined;
	error?: string | undefined;
}

interface GeoStatusRow {
	label: string;
	value: string;
	/** The exact time behind a relative value. */
	title?: string;
}

export type GeoStatusTone = "ok" | "wait" | "bad" | "muted";

export interface GeoStatusView {
	tone: GeoStatusTone;
	/** One plain sentence: where the database stands. */
	headline: string;
	/** What went wrong or what happens next, when there is more to say. */
	detail?: string;
	rows: GeoStatusRow[];
}

const SOURCE_NAMES: Record<string, string> = {
	dbip: "DB-IP Lite",
	maxmind: "MaxMind GeoLite2",
};

/** "DB-IP Lite, country level". */
function databaseName(status: GeoDatabase): string {
	const source = status.source
		? (SOURCE_NAMES[status.source] ?? status.source)
		: "";
	return [source, status.level ? `${status.level} level` : ""]
		.filter(Boolean)
		.join(", ");
}

/** DB-IP ships monthly files ("2026-10 release"); MaxMind dates the build. */
function releaseText(status: GeoDatabase): string {
	if (status.source === "maxmind" && status.buildTime) {
		return `Built ${absoluteDate(status.buildTime)}`;
	}
	return status.label ? `${status.label} release` : "";
}

/**
 * What Settings > Monitoring says about the GeoIP database of one server.
 * `enabled` is the saved setting, `online` whether the control plane sees the
 * agent, `status` what the agent reported last (absent until it does, and
 * while geolocation is off). The database lives on the host, so nothing is
 * known while the agent is away.
 */
export function geoStatusView(input: {
	enabled: boolean;
	online: boolean;
	status: GeoDatabase | undefined;
}): GeoStatusView {
	const { enabled, online, status } = input;
	if (!enabled) {
		return {
			tone: "muted",
			headline: "No database. Geolocation is off, so the server keeps none.",
			rows: [],
		};
	}
	if (!online) {
		return {
			tone: "wait",
			headline: "Unknown while the agent is offline.",
			detail:
				"The database sits on the server; it reports back once it connects.",
			rows: [],
		};
	}
	if (!status) {
		return {
			tone: "wait",
			headline: "Not reported yet.",
			detail:
				"The agent sends it with its next heartbeat. An agent from before this release never does; update it.",
			rows: [],
		};
	}
	if (!status.downloaded) {
		return status.error
			? {
					tone: "bad",
					headline: "Not downloaded.",
					detail: `${status.error}. The agent tries again by itself.`,
					rows: [],
				}
			: {
					tone: "wait",
					headline: "Downloading.",
					detail:
						"Origins appear once the database is on the server and public requests arrive.",
					rows: [],
				};
	}
	const rows: GeoStatusRow[] = [
		{ label: "Database", value: databaseName(status) },
	];
	const release = releaseText(status);
	if (release) rows.push({ label: "Release", value: release });
	if (status.sizeBytes && status.sizeBytes > 0) {
		rows.push({ label: "Size", value: formatBytes(status.sizeBytes) });
	}
	if (status.checkedAt) {
		const ago = relativeTime(status.checkedAt);
		if (ago) {
			rows.push({
				label: "Last refresh",
				value: ago,
				title: absoluteTime(status.checkedAt),
			});
		}
	}
	return status.error
		? {
				tone: "bad",
				headline: "Downloaded, but the last refresh failed.",
				detail: `${status.error}. The database in use is the one above.`,
				rows,
			}
		: { tone: "ok", headline: "Downloaded.", rows };
}
