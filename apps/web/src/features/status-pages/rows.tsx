import { Link } from "@tanstack/react-router";
import type {
	MaintenanceWindowView,
	StatusIncidentView,
} from "#/api/gen/types.gen";
import { Relative } from "#/components/common/relative";
import { Button } from "#/components/ui/button";
import { count } from "#/lib/format";
import { ImpactBadge, IncidentStatusBadge, PhaseBadge } from "./badges";
import { utcLabel, windowIsLive, windowLength } from "./model";

const RULED = "m-0 flex list-none flex-col divide-y divide-line-faint p-0";

/** "Acme status, API" or the reason there is nothing to list. */
function pagesLine(
	ids: readonly string[] | null,
	titles: ReadonlyMap<string, string>,
): string {
	const names = (ids ?? []).map((id) => titles.get(id)).filter(Boolean);
	if (names.length === 0) return "Not posted to any page";
	return `Posted to ${names.join(", ")}`;
}

/** The incidents as links to their timelines, worst first as they are given. */
export function IncidentRows({
	incidents,
	pageTitles,
	label,
}: {
	incidents: readonly StatusIncidentView[];
	pageTitles: ReadonlyMap<string, string>;
	label: string;
}) {
	return (
		<ul aria-label={label} className={RULED}>
			{incidents.map((incident) => (
				<li key={incident.id}>
					<Link
						to="/status-pages/incidents/$incidentId"
						params={{ incidentId: incident.id }}
						className="flex flex-wrap items-center gap-x-4 gap-y-1.5 rounded-ctl px-1 py-3 outline-none hover:bg-white/[0.03] focus-visible:focus-ring"
					>
						<span className="flex min-w-0 flex-1 basis-60 flex-col gap-0.5">
							<span className="truncate font-medium text-ink-1 text-sm">
								{incident.title}
							</span>
							<span className="truncate text-ink-3 text-xs">
								{pagesLine(incident.pageIds, pageTitles)}
								{(incident.serviceIds ?? []).length > 0
									? ` · ${count((incident.serviceIds ?? []).length, "affected service")}`
									: ""}
							</span>
						</span>
						<span className="flex items-center gap-2">
							<ImpactBadge impact={incident.impact} />
							<IncidentStatusBadge status={incident.status} />
						</span>
						<span className="w-20 text-right text-ink-3 text-xs">
							<Relative iso={incident.updatedAt} />
						</span>
					</Link>
				</li>
			))}
		</ul>
	);
}

const LOCAL = new Intl.DateTimeFormat(undefined, {
	dateStyle: "medium",
	timeStyle: "short",
});

/** "Nov 2, 2026, 1:00 AM to Nov 2, 2026, 2:30 AM" in the browser's time zone. */
function localRange(startsAt: string, endsAt: string): string {
	return `${LOCAL.format(new Date(startsAt))} to ${LOCAL.format(new Date(endsAt))}`;
}

/** Maintenance windows with their times in local time and UTC, and what can be done to each. */
export function MaintenanceRows({
	windows,
	pageTitles,
	label,
	canManage,
	onEdit,
	onCancel,
	onDelete,
}: {
	windows: readonly MaintenanceWindowView[];
	pageTitles: ReadonlyMap<string, string>;
	label: string;
	canManage: boolean;
	onEdit: (window: MaintenanceWindowView) => void;
	onCancel: (window: MaintenanceWindowView) => void;
	onDelete: (window: MaintenanceWindowView) => void;
}) {
	return (
		<ul aria-label={label} className={RULED}>
			{windows.map((window) => {
				const length = windowLength(window.startsAt, window.endsAt);
				const services = (window.serviceIds ?? []).length;
				return (
					<li
						key={window.id}
						className="flex flex-wrap items-center gap-x-4 gap-y-2 px-1 py-3"
					>
						<div className="flex min-w-0 flex-1 basis-72 flex-col gap-0.5">
							<span className="flex flex-wrap items-center gap-2">
								<span className="truncate font-medium text-ink-1 text-sm">
									{window.title}
								</span>
								<PhaseBadge phase={window.phase} />
							</span>
							<span className="text-ink-2 text-xs">
								{localRange(window.startsAt, window.endsAt)}
								{length ? ` (${length})` : ""}
							</span>
							<span className="font-mono text-2xs text-ink-3">
								{utcLabel(window.startsAt)} to {utcLabel(window.endsAt)}
							</span>
							<span className="truncate text-ink-3 text-xs">
								{pagesLine(window.pageIds, pageTitles)} ·{" "}
								{services === 0
									? "no services in maintenance"
									: `${count(services, "service")} in maintenance`}
							</span>
						</div>
						{canManage ? (
							<div className="flex items-center gap-1">
								{windowIsLive(window) ? (
									<>
										<Button
											type="button"
											variant="ghost"
											size="sm"
											onClick={() => onEdit(window)}
										>
											Edit
										</Button>
										<Button
											type="button"
											variant="ghost"
											size="sm"
											onClick={() => onCancel(window)}
										>
											{window.phase === "active" ? "End now" : "Cancel"}
										</Button>
									</>
								) : null}
								<Button
									type="button"
									variant="ghost"
									size="sm"
									className="text-destructive-text"
									onClick={() => onDelete(window)}
								>
									Delete
								</Button>
							</div>
						) : null}
					</li>
				);
			})}
		</ul>
	);
}

/** An instant in the browser's time zone, with its UTC reading as the tooltip. */
export function LocalTime({ iso }: { iso: string }) {
	return (
		<time dateTime={iso} title={utcLabel(iso)} className="tabular-nums">
			{LOCAL.format(new Date(iso))}
		</time>
	);
}
