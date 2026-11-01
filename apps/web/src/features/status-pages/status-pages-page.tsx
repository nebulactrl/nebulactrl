import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CalendarClockIcon, PlusIcon } from "lucide-react";
import { useMemo, useState } from "react";
import type { StatusPageView } from "#/api/gen/types.gen";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { PageFrame } from "#/components/shell/page-frame";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import { useTeamServices } from "#/features/log-drains/use-team-services";
import { useCurrentTeam } from "#/features/teams/queries";
import { canManageMembers } from "#/features/teams/roles";
import { count } from "#/lib/format";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { StateLabel } from "./badges";
import { Block } from "./block";
import { CreatePageDialog } from "./create-page-dialog";
import { IncidentDialog } from "./incident-dialog";
import { MaintenanceDialog } from "./maintenance-dialog";
import {
	compareIncidents,
	groupWindows,
	isOpen,
	type PageState,
	pageOverall,
	previewBlocker,
} from "./model";
import { PreviewAction } from "./preview-action";
import {
	maintenanceWindowsQuery,
	statusIncidentsQuery,
	statusPagesQuery,
	useServiceStates,
} from "./queries";
import { IncidentRows, MaintenanceRows } from "./rows";
import { useMaintenanceActions } from "./use-maintenance-actions";

const GRID =
	"grid grid-cols-[repeat(auto-fill,minmax(min(100%,340px),1fr))] gap-4";

type IncidentView = "open" | "resolved";
type WindowView = "current" | "past";

/**
 * The note of an empty list section. Where the list could grow (open
 * incidents, upcoming maintenance) it carries the button that adds to it, or
 * says what to create first when the team has no status page yet.
 */
function EmptyNote({
	text,
	hint,
	action,
	canAct,
	hasPages,
}: {
	text: string;
	/** What to do first when there is no page to post to. */
	hint?: string;
	action?: React.ReactNode;
	canAct: boolean;
	hasPages: boolean;
}) {
	return (
		<div className="surface-well flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-card p-3">
			<p className="m-0 text-ink-3 text-xs leading-relaxed">
				{text}
				{canAct && !hasPages && hint ? ` ${hint}` : null}
			</p>
			{canAct && hasPages ? action : null}
		</div>
	);
}

function PageCard({
	page,
	state,
	openIncidents,
	upcoming,
}: {
	page: StatusPageView;
	state: PageState;
	openIncidents: number;
	upcoming: number;
}) {
	const blocker = previewBlocker(page);
	const [explaining, setExplaining] = useState(false);
	return (
		<article
			aria-label={page.title}
			className="surface-raised flex min-w-0 flex-col gap-3 rounded-card p-4"
		>
			<header className="flex items-start justify-between gap-3">
				<div className="flex min-w-0 flex-col gap-0.5">
					<h3 className="m-0 truncate font-medium text-ink-1 text-sm">
						<Link
							to="/status-pages/$pageId"
							params={{ pageId: page.id }}
							className="rounded-sm outline-none hover:underline focus-visible:focus-ring"
						>
							{page.title}
						</Link>
					</h3>
					<span className="truncate font-mono text-2xs text-ink-3">
						{page.hostname ?? "No hostname yet"}
					</span>
				</div>
				{page.published ? (
					<Badge variant="success" shape="capsule">
						Published
					</Badge>
				) : (
					<Badge shape="capsule">Draft</Badge>
				)}
			</header>
			<StateLabel state={state} />
			<p className="m-0 flex flex-wrap gap-x-3 gap-y-1 text-ink-3 text-xs">
				<span>{count((page.components ?? []).length, "component")}</span>
				<span>
					{openIncidents === 0
						? "No open incidents"
						: count(openIncidents, "open incident")}
				</span>
				{upcoming > 0 ? (
					<span>{count(upcoming, "maintenance window")} ahead</span>
				) : null}
			</p>
			<footer className="-mb-1 mt-auto flex flex-wrap items-center gap-1 border-line-faint border-t pt-2.5">
				<Button
					variant="secondary"
					size="sm"
					nativeButton={false}
					render={
						<Link to="/status-pages/$pageId" params={{ pageId: page.id }} />
					}
				>
					Edit
				</Button>
				<PreviewAction
					page={page}
					size="sm"
					variant="ghost"
					explaining={explaining}
					onExplain={() => setExplaining((on) => !on)}
				/>
				{explaining && blocker ? (
					<p
						role="status"
						className="m-0 basis-full px-1 pt-1 text-ink-3 text-xs leading-relaxed"
					>
						{blocker}
					</p>
				) : null}
			</footer>
		</article>
	);
}

/**
 * Status pages: the public pages of the team with their state, and the open
 * incidents and the maintenance posted across them. Only the page is public;
 * the dashboard stays private.
 */
export function StatusPagesPage() {
	useDocumentTitle(["Status pages"]);
	const { role } = useCurrentTeam();
	const canManage = canManageMembers(role);
	const [creating, setCreating] = useState(false);
	const [posting, setPosting] = useState(false);
	const [scheduling, setScheduling] = useState(false);
	const [incidentView, setIncidentView] = useState<IncidentView>("open");
	const [windowView, setWindowView] = useState<WindowView>("current");
	const actions = useMaintenanceActions();

	const pages = useQuery(statusPagesQuery());
	const open = useQuery(statusIncidentsQuery({ status: "active" }));
	const resolved = useQuery({
		...statusIncidentsQuery({ status: "resolved", limit: 20 }),
		enabled: incidentView === "resolved",
	});
	const windows = useQuery(maintenanceWindowsQuery());
	const { states } = useServiceStates();
	// Only an empty list needs to know whether there is anything to show on a page.
	const teamServices = useTeamServices(
		pages.isSuccess && pages.data.length === 0,
	);
	const noServices =
		pages.isSuccess &&
		pages.data.length === 0 &&
		!teamServices.pending &&
		teamServices.services.length === 0;

	const items = pages.data ?? [];
	const openIncidents = useMemo(
		() => [...(open.data ?? [])].filter(isOpen).sort(compareIncidents),
		[open.data],
	);
	const groups = useMemo(
		() => groupWindows(windows.data ?? []),
		[windows.data],
	);
	const titles = useMemo(
		() => new Map(items.map((page) => [page.id, page.title])),
		[items],
	);
	const shownIncidents =
		incidentView === "open" ? openIncidents : (resolved.data ?? []);
	const shownWindows =
		windowView === "current"
			? [...groups.active, ...groups.upcoming]
			: groups.past;

	// With one page there is nothing to pick: the dialogs start on it.
	const [only] = items;
	const onlyPage =
		items.length === 1 && only ? { defaultPageIds: [only.id] } : {};

	let body: React.ReactNode;
	if (pages.isPending) {
		body = (
			<div className={GRID} role="status" aria-label="Loading status pages">
				{[0, 1].map((index) => (
					<Skeleton key={index} className="h-[168px] rounded-card" />
				))}
			</div>
		);
	} else if (pages.isError && pages.data === undefined) {
		body = (
			<ErrorState
				frame="raised"
				title="Status pages did not load"
				body="The control plane did not answer. Nothing was changed; check that it is running, then try again."
				retry={() => void pages.refetch()}
			/>
		);
	} else if (items.length === 0) {
		body = (
			<EmptyState
				illustration="radar"
				frame="none"
				size="lg"
				title="No status pages yet"
				body={
					noServices
						? "A status page shows services, and this team has none yet. Deploy one first, then pick it here, choose a hostname and publish."
						: "Pick services, choose a hostname, publish."
				}
				{...(canManage
					? {
							action: (
								<Button variant="primary" onClick={() => setCreating(true)}>
									<PlusIcon />
									New status page
								</Button>
							),
						}
					: {})}
			/>
		);
	} else {
		body = (
			<div className={GRID}>
				{items.map((page) => (
					<PageCard
						key={page.id}
						page={page}
						state={pageOverall(page, states, openIncidents, windows.data ?? [])}
						openIncidents={
							openIncidents.filter((i) => (i.pageIds ?? []).includes(page.id))
								.length
						}
						upcoming={
							groups.upcoming.filter((w) => (w.pageIds ?? []).includes(page.id))
								.length
						}
					/>
				))}
			</div>
		);
	}

	return (
		<PageFrame
			page="status pages"
			title="Status pages"
			description="Public pages for your services, incidents and maintenance."
			actions={
				canManage && items.length > 0 ? (
					<div className="flex flex-wrap items-center gap-2">
						<Button variant="secondary" onClick={() => setScheduling(true)}>
							<CalendarClockIcon />
							Schedule maintenance
						</Button>
						<Button variant="secondary" onClick={() => setPosting(true)}>
							Post incident
						</Button>
						<Button variant="primary" onClick={() => setCreating(true)}>
							<PlusIcon />
							New status page
						</Button>
					</div>
				) : undefined
			}
		>
			<Block
				title="Status pages"
				aside={items.length > 0 ? count(items.length, "page") : undefined}
			>
				{body}
			</Block>

			<Block
				title="Incidents"
				aside={
					<Segmented<IncidentView>
						aria-label="Which incidents"
						font="sans"
						value={incidentView}
						onValueChange={setIncidentView}
						options={[
							{
								value: "open",
								label: `Open${openIncidents.length > 0 ? ` (${openIncidents.length})` : ""}`,
							},
							{ value: "resolved", label: "Resolved" },
						]}
					/>
				}
			>
				{(incidentView === "open" ? open.isPending : resolved.isPending) ? (
					<Skeleton className="h-20 rounded-card" />
				) : shownIncidents.length === 0 ? (
					<EmptyNote
						text={
							incidentView === "open"
								? "No open incidents."
								: "No resolved incidents yet."
						}
						{...(incidentView === "open"
							? {
									hint: "Create a status page first; incidents are posted to a page.",
									action: (
										<Button
											variant="secondary"
											size="sm"
											onClick={() => setPosting(true)}
										>
											Post incident
										</Button>
									),
								}
							: {})}
						canAct={canManage}
						hasPages={items.length > 0}
					/>
				) : (
					<IncidentRows
						label={
							incidentView === "open" ? "Open incidents" : "Resolved incidents"
						}
						incidents={shownIncidents}
						pageTitles={titles}
					/>
				)}
			</Block>

			<Block
				title="Maintenance"
				aside={
					<Segmented<WindowView>
						aria-label="Which maintenance windows"
						font="sans"
						value={windowView}
						onValueChange={setWindowView}
						options={[
							{
								value: "current",
								label: `Upcoming and active${groups.active.length + groups.upcoming.length > 0 ? ` (${groups.active.length + groups.upcoming.length})` : ""}`,
							},
							{ value: "past", label: "Past" },
						]}
					/>
				}
			>
				{windows.isPending ? (
					<Skeleton className="h-20 rounded-card" />
				) : shownWindows.length === 0 ? (
					<EmptyNote
						text={
							windowView === "current"
								? "Nothing scheduled."
								: "No past maintenance."
						}
						{...(windowView === "current"
							? {
									hint: "Create a status page first; maintenance is announced on a page.",
									action: (
										<Button
											variant="secondary"
											size="sm"
											onClick={() => setScheduling(true)}
										>
											<CalendarClockIcon />
											Schedule maintenance
										</Button>
									),
								}
							: {})}
						canAct={canManage}
						hasPages={items.length > 0}
					/>
				) : (
					<MaintenanceRows
						label={
							windowView === "current"
								? "Upcoming and active maintenance"
								: "Past maintenance"
						}
						windows={shownWindows}
						pageTitles={titles}
						canManage={canManage}
						onEdit={actions.onEdit}
						onCancel={actions.onCancel}
						onDelete={actions.onDelete}
					/>
				)}
			</Block>

			<CreatePageDialog open={creating} onOpenChange={setCreating} />
			<IncidentDialog
				open={posting}
				onOpenChange={setPosting}
				incident={null}
				{...onlyPage}
			/>
			<MaintenanceDialog
				open={scheduling}
				onOpenChange={setScheduling}
				maintenance={null}
				{...onlyPage}
			/>
			<MaintenanceDialog
				open={actions.editing !== null}
				onOpenChange={(next) => (next ? null : actions.closeEditor())}
				maintenance={actions.editing}
			/>
		</PageFrame>
	);
}
