import { useQuery } from "@tanstack/react-query";
import { PlusIcon } from "lucide-react";
import { DocsButton } from "#/components/common/docs-button";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { PageFrame } from "#/components/shell/page-frame";
import { Button } from "#/components/ui/button";
import { Kbd } from "#/components/ui/kbd";
import { Segmented } from "#/components/ui/segmented";
import { Skeleton } from "#/components/ui/skeleton";
import { FleetView } from "#/features/fleet/fleet-view";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { AgentUpgradeBar } from "./agent-upgrade/agent-upgrade";
import { EnrollDialog } from "./enroll-dialog";
import { serverViewsQuery } from "./queries";
import { ServerCard } from "./server-card";

const GRID =
	"grid grid-cols-[repeat(auto-fill,minmax(min(100%,340px),1fr))] gap-4";

type ServersView = "servers" | "fleet";

export interface ServersPageProps {
	/** `?view=fleet` shows capacity per server; the route owns the URL state. */
	view: ServersView;
	onViewChange: (view: ServersView) => void;
	/** `?enroll=1` opens the enrol dialog; the route owns the URL state. */
	enrollOpen: boolean;
	onEnrollOpenChange: (open: boolean) => void;
}

/** The Servers page: every Docker host with its live status and meters. */
export function ServersPage({
	view,
	onViewChange,
	enrollOpen,
	onEnrollOpenChange,
}: ServersPageProps) {
	useDocumentTitle(["Servers"]);
	const servers = useQuery(serverViewsQuery());
	const items = servers.data;
	const open = () => onEnrollOpenChange(true);

	let body: React.ReactNode;
	if (view === "fleet") {
		body = <FleetView />;
	} else if (servers.isPending) {
		body = (
			<div className={GRID} role="status" aria-label="Loading servers">
				{[0, 1, 2].map((index) => (
					<Skeleton key={index} className="h-[236px] rounded-card" />
				))}
			</div>
		);
	} else if (servers.isError && items === undefined) {
		body = (
			<ErrorState
				frame="raised"
				title="Servers did not load"
				body="The control plane did not answer. Nothing was changed; check that it is running, then try again."
				retry={() => void servers.refetch()}
			/>
		);
	} else if (items && items.length === 0) {
		body = (
			<EmptyState
				illustration="rack"
				frame="none"
				size="lg"
				title="Add your first server"
				body="A server is a Docker host running the NebulaCtrl agent. Create an install command, run it on the host, and it appears here once the agent connects."
				action={
					<Button variant="primary" onClick={open}>
						<PlusIcon />
						Add server
					</Button>
				}
				secondaryAction={
					<DocsButton path="installing/servers.md">
						Read the install guide
					</DocsButton>
				}
				hint={
					<>
						<Kbd>⌘K</Kbd> then “Add server” works from any page
					</>
				}
			/>
		);
	} else {
		body = (
			<div className={GRID}>
				{(items ?? []).map((server) => (
					<ServerCard key={server.id} server={server} />
				))}
			</div>
		);
	}

	return (
		<PageFrame
			page="servers"
			title="Servers"
			description={
				view === "fleet"
					? "Capacity and placement across every server."
					: "Docker hosts that run your services, each with its own reverse proxy."
			}
			actions={
				<div className="flex items-center gap-3">
					{view === "fleet" || (items && items.length > 0) ? (
						<Segmented
							aria-label="Servers view"
							font="sans"
							density="regular"
							value={view}
							onValueChange={onViewChange}
							options={[
								{ value: "servers", label: "Servers" },
								{ value: "fleet", label: "Fleet" },
							]}
						/>
					) : null}
					{view === "servers" && items?.length === 0 ? null : (
						<Button variant="primary" onClick={open}>
							<PlusIcon />
							Add server
						</Button>
					)}
				</div>
			}
		>
			{view === "servers" ? <AgentUpgradeBar /> : null}
			{body}
			<EnrollDialog open={enrollOpen} onOpenChange={onEnrollOpenChange} />
		</PageFrame>
	);
}
