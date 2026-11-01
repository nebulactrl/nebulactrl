import { useQuery } from "@tanstack/react-query";
import { Relative } from "#/components/common/relative";
import { StatusIcon } from "#/components/ui/status-icon";
import { preUpgradeBackupLine } from "#/features/servers/agent-upgrade/model";
import { instanceUpgradeQuery } from "#/features/servers/agent-upgrade/queries";

/**
 * What happened to the backup the control plane takes before it upgrades itself:
 * it backs up first and refuses to migrate when that failed (unless forced).
 * Hidden until the control plane has upgraded once.
 */
export function PreUpgradeBackupNote() {
	const { data } = useQuery(instanceUpgradeQuery());
	const b = data?.preUpgradeBackup;
	if (!b) return null;
	const line = preUpgradeBackupLine(b);
	return (
		<p className="flex flex-col gap-0.5 text-ink-3 text-xs">
			<span className="flex items-center gap-2 text-ink-2">
				<StatusIcon kind={line.kind} label="" />
				{line.label}
				<span className="text-ink-4">
					· <Relative iso={b.at} />
				</span>
			</span>
			{b.message ? <span className="break-words">{b.message}</span> : null}
		</p>
	);
}
