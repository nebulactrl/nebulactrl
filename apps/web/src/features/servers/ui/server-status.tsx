import type { ServerStatus as Status } from "#/api/models";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";

const MAP: Record<Status, { kind: StatusKind; label: string }> = {
	online: { kind: "live", label: "Online" },
	offline: { kind: "offline", label: "Offline" },
	pending: { kind: "pending", label: "Waiting for agent" },
	revoked: { kind: "failed", label: "Revoked" },
};

/**
 * The server's state as an icon plus its word, never colour alone. `pill`
 * sets it in an engraved capsule (headers); the default sits inline on a
 * card or a row. The word carries the state, so the icon is silent.
 */
export function ServerStatus({
	status,
	pill = false,
}: {
	status: Status;
	pill?: boolean;
}) {
	const { kind, label } = MAP[status];
	if (pill) {
		return (
			<span className="surface-well inline-flex h-[22px] w-fit shrink-0 items-center gap-1.5 whitespace-nowrap rounded-full pr-[9px] pl-[7px] font-medium text-ink-1 text-xs leading-none">
				<StatusIcon kind={kind} label="" />
				{label}
			</span>
		);
	}
	return (
		<span className="inline-flex items-center gap-1.5">
			<StatusIcon kind={kind} label="" />
			<span className="font-medium text-ink-1 text-xs leading-none">
				{label}
			</span>
		</span>
	);
}
