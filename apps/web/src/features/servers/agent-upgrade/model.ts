import type {
	InstanceUpgradeView,
	ServerUpgradeView,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";

export type Skew = ServerUpgradeView["skew"];

/** The status icon of an agent's version against the control plane's. */
export const SKEW_KIND: Record<Skew, StatusKind> = {
	current: "healthy",
	behind: "warning",
	ahead: "warning",
	unknown: "unknown",
};

/** "Up to date", "Behind 1.5.0", … in words, never colour alone. */
export function skewLabel(skew: Skew, controlPlaneVersion: string): string {
	switch (skew) {
		case "current":
			return "Up to date";
		case "behind":
			return `Behind ${controlPlaneVersion}`;
		case "ahead":
			return "Newer than the control plane";
		default:
			return "Version not comparable";
	}
}

export interface UpgradePresentation {
	kind: StatusKind;
	label: string;
}

/** The state of the latest upgrade of one server, or null when none ran. */
export function upgradePresentation(
	s: Pick<ServerUpgradeView, "state" | "trigger" | "targetVersion">,
): UpgradePresentation | null {
	switch (s.state) {
		case "queued":
			return { kind: "pending", label: `Upgrade to ${s.targetVersion} queued` };
		case "restarting":
			return {
				kind: "deploying",
				label: `Installed ${s.targetVersion}, restarting`,
			};
		case "succeeded":
			return {
				kind: "healthy",
				label: `Upgraded to ${s.targetVersion}${s.trigger === "auto" ? " automatically" : ""}`,
			};
		case "failed":
			return { kind: "failed", label: `Upgrade to ${s.targetVersion} failed` };
		default:
			return null;
	}
}

/** True while an upgrade of this server's agent is running. */
export function upgradeRunning(s: Pick<ServerUpgradeView, "state">): boolean {
	return s.state === "queued" || s.state === "restarting";
}

/** True when any server's upgrade is still running (the page polls then). */
export function anyRunning(v: InstanceUpgradeView | undefined): boolean {
	return v?.servers?.some(upgradeRunning) ?? false;
}

/** "2 agents are behind 1.5.0", or null when none is. */
export function behindSummary(v: InstanceUpgradeView): string | null {
	if (v.behind === 0) return null;
	const noun = v.behind === 1 ? "agent is" : "agents are";
	return `${v.behind} ${noun} behind ${v.controlPlaneVersion}`;
}

/** What the "Upgrade agents" button will do, for its confirmation. */
export function rolloutSummary(v: InstanceUpgradeView): string {
	const n = v.upgradable;
	return `${n} ${n === 1 ? "server" : "servers"}, one at a time in name order. Each agent downloads the new binary from this control plane, checks its SHA-256, replaces itself and restarts; the next server starts once the previous one reconnects. The first failure stops the rollout and leaves the others untouched. Running services are not touched.`;
}

/** The view of one server in the overview, if it is listed. */
export function serverUpgrade(
	v: InstanceUpgradeView | undefined,
	serverId: string,
): ServerUpgradeView | undefined {
	return v?.servers?.find((s) => s.serverId === serverId);
}

/** The line the instance backup section shows about the last upgrade's backup. */
export function preUpgradeBackupLine(
	b: NonNullable<InstanceUpgradeView["preUpgradeBackup"]>,
): { kind: StatusKind; label: string } {
	const move =
		b.fromVersion && b.toVersion
			? ` (${b.fromVersion} → ${b.toVersion})`
			: b.toVersion
				? ` (to ${b.toVersion})`
				: "";
	switch (b.status) {
		case "succeeded":
			return { kind: "healthy", label: `Backed up before the upgrade${move}` };
		case "forced":
			return {
				kind: "warning",
				label: `Upgraded without a backup, forced${move}`,
			};
		case "skipped":
			return {
				kind: "warning",
				label: `No backup before the upgrade: no destination${move}`,
			};
		default:
			return {
				kind: "failed",
				label: `Backup before the upgrade failed${move}`,
			};
	}
}
