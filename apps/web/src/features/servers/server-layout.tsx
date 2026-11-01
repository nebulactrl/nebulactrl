import { useQuery } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import type { ReactNode } from "react";
import { errorStatus } from "#/api/client-config";
import { meQuery } from "#/api/hooks";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { PageFrame } from "#/components/shell/page-frame";
import { Button } from "#/components/ui/button";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { DrainChip } from "#/features/fleet/drain-chip";
import { DrainSheet } from "#/features/fleet/drain-sheet";
import { pagesFor } from "#/features/settings/nav-model";
import { isRole } from "#/features/teams/roles";
import { HostShellSheet } from "#/features/terminal";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import type { ServerView } from "./model";
import { tabBadge } from "./patching/patching-model";
import { serverViewQuery } from "./queries";
import { ProxyLine } from "./server-card";
import { ServerProvider } from "./server-context";
import { ServerTabs } from "./server-tabs";
import { serverTabOf } from "./tabs-model";
import { ServerStatus } from "./ui/server-status";

/** Status, address, last seen and proxy: the one line under the name. */
function Summary({ server }: { server: ServerView }) {
	return (
		<span className="flex flex-wrap items-center gap-x-4 gap-y-2">
			<ServerStatus status={server.status} pill />
			<DrainChip state={server.drainState} message={server.drainMessage} />
			<span className="type-mono text-ink-3">
				{server.publicIp ?? "no public IP"}
			</span>
			<span className="type-mono text-ink-3">
				{server.lastSeenAt ? (
					<>
						seen <Relative iso={server.lastSeenAt} />
					</>
				) : (
					"never seen"
				)}
			</span>
			<ProxyLine proxy={server.proxy} />
		</span>
	);
}

/** Whether this person has any settings page to open: owners and admins do, nobody else. */
function useCanOpenSettings(): boolean {
	const me = useQuery(meQuery());
	const role = me.data?.principal.role;
	return (
		pagesFor(
			"server",
			{ instanceAdmin: me.data?.instanceAdmin === true },
			isRole(role) ? role : undefined,
		).length > 0
	);
}

/**
 * The server page's frame: the name, the one-line summary, the tabs and the
 * sheets every tab can open (drain, host shell). The tab itself is the route
 * under it, drawn once the server has loaded.
 */
export function ServerLayout({
	serverId,
	drainOpen = false,
	onDrainOpenChange,
	children,
}: {
	serverId: string;
	/** `?drain=1` opens the drain sheet; the route owns the URL state. */
	drainOpen?: boolean;
	onDrainOpenChange?: (open: boolean) => void;
	children: ReactNode;
}) {
	const query = useQuery(serverViewQuery(serverId));
	const server = query.data;
	useDocumentTitle([server?.name ?? "Server"]);
	const pathname = useRouterState({ select: (s) => s.location.pathname });
	const canOpenSettings = useCanOpenSettings();

	let content: ReactNode;
	if (!server) {
		const missing = errorStatus(query.error) === 404;
		content = (
			<ErrorState
				frame="raised"
				title={missing ? "This server does not exist" : "Server did not load"}
				body={
					missing
						? "It may have been removed, or the address is wrong. Go back to the list of servers."
						: "The control plane did not answer. Nothing was changed; check that it is running, then try again."
				}
				{...(missing ? {} : { retry: () => void query.refetch() })}
			/>
		);
	} else {
		content = (
			<ServerProvider
				value={{ server, openDrain: () => onDrainOpenChange?.(true) }}
			>
				<ServerTabs
					serverId={serverId}
					value={serverTabOf(pathname, serverId)}
					staged={server.proxy.generation > server.proxy.appliedGeneration}
					updatesBadge={tabBadge(server.patches)}
					showSettings={canOpenSettings}
				/>
				{children}
				<DrainSheet
					server={server}
					open={drainOpen}
					onOpenChange={(open) => onDrainOpenChange?.(open)}
				/>
				<HostShellSheet />
			</ServerProvider>
		);
	}

	return (
		<PageFrame
			page="servers"
			morph={{ kind: "server", id: serverId }}
			title={server?.name ?? "Server"}
			description={server ? <Summary server={server} /> : undefined}
			actions={
				<Button
					variant="ghost"
					nativeButton={false}
					render={<Link to="/servers" />}
				>
					<ArrowLeftIcon />
					All servers
				</Button>
			}
		>
			<SkeletonReveal
				loading={query.isPending}
				className="flex flex-col gap-5"
				skeleton={
					<div
						className="flex flex-col gap-4"
						role="status"
						aria-label="Loading server"
					>
						<Skeleton className="h-32 rounded-card" />
						<Skeleton className="h-80 rounded-card" />
					</div>
				}
			>
				{() => content}
			</SkeletonReveal>
		</PageFrame>
	);
}
