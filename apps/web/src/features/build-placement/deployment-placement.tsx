import { useQuery } from "@tanstack/react-query";
import { ArrowRightIcon, ServerIcon } from "lucide-react";
import { serversQuery } from "#/api/hooks";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { builtOnText, placementNoticeTitle } from "./model";

function ServerChip({ label, name }: { label: string; name: string }) {
	return (
		<span className="surface-well inline-flex min-w-0 items-center gap-1.5 rounded-ctl px-2 py-1 text-xs">
			<ServerIcon aria-hidden className="size-3.5 shrink-0 text-ink-3" />
			<span className="text-ink-3">{label}</span>
			<span className="truncate font-mono text-ink-1" title={name}>
				{name}
			</span>
		</span>
	);
}

/**
 * "Built on build-01 → Deployed on web-02": the two servers of a deployment
 * whose image was built somewhere else. Nothing shows for a build on the
 * target, or for an image that was only pulled.
 */
export function BuiltOnChips({
	buildServerId,
	serverId,
}: {
	buildServerId: string | null;
	serverId: string | null;
}) {
	const servers = useQuery(serversQuery());
	if (buildServerId === null || buildServerId === serverId) return null;
	const nameOf = (id: string | null) =>
		id === null
			? null
			: (servers.data?.find((server) => server.id === id)?.name ??
				(servers.isPending ? "…" : "a removed server"));
	const built = nameOf(buildServerId);
	const deployed = nameOf(serverId);
	return (
		<fieldset
			aria-label={builtOnText(built, deployed) ?? "Build and deploy servers"}
			className="m-0 flex min-w-0 flex-wrap items-center gap-2 border-0 p-0"
		>
			<ServerChip label="Built on" name={built ?? "…"} />
			<ArrowRightIcon aria-hidden className="size-3.5 shrink-0 text-ink-4" />
			<ServerChip label="Deployed on" name={deployed ?? "…"} />
		</fieldset>
	);
}

/**
 * The placement notices of a deployment: a fallback ("No build server
 * available; building on web-02") or a wait for a build slot. They are
 * information, so they read as an info alert above the steps and not as a
 * step of their own.
 */
export function PlacementNotices({
	events,
}: {
	events: readonly { id: number | string; message: string }[];
}) {
	if (events.length === 0) return null;
	return (
		<div className="flex flex-col gap-2">
			{events.map((event) => (
				<Alert key={event.id} variant="info">
					<AlertTitle>{placementNoticeTitle(event.message)}</AlertTitle>
					<AlertDescription>{event.message}</AlertDescription>
				</Alert>
			))}
		</div>
	);
}
