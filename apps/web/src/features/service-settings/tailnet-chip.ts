import type { TailnetConfig, TailnetView } from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";

export type TailnetState = TailnetView["state"];

export interface TailnetStateInfo {
	kind: StatusKind;
	/** One or two words; always shown next to the icon, never colour alone. */
	label: string;
}

const STATE_INFO: Record<TailnetState, TailnetStateInfo> = {
	online: { kind: "live", label: "Online" },
	provisioning: { kind: "deploying", label: "Provisioning" },
	offline: { kind: "offline", label: "Offline" },
	needs_attention: { kind: "failed", label: "Needs attention" },
	removing: { kind: "deploying", label: "Removing" },
};

/** The observed node state as a status kind and its words. */
export function tailnetStateInfo(state: TailnetState): TailnetStateInfo {
	return STATE_INFO[state];
}

/** The name without the trailing root dot MagicDNS names carry. */
export function trimFqdn(fqdn: string): string {
	return fqdn.replace(/\.$/, "");
}

export interface TailnetChipInfo {
	kind: StatusKind;
	/** What the chip shows: the node's hostname. */
	label: string;
	/** The state in words, for the tooltip and screen readers. */
	state: string;
	/** Shown as a distinct "Public" mark. */
	isPublic: boolean;
	/** Public domains are not routed while the service is on the tailnet. */
	tailnetOnly: boolean;
	/** The full sentence for `title` and screen readers. */
	tooltip: string;
}

/** Label, status kind and tooltip of a service's tailnet node, for the canvas chip. */
export function tailnetChipInfo(view: TailnetView): TailnetChipInfo {
	const info = tailnetStateInfo(view.state);
	const name = trimFqdn(view.fqdn) || view.hostname;
	const parts = [`Tailnet node ${name || "(unnamed)"}`, info.label];
	if (view.state === "needs_attention" && view.message !== "") {
		parts.push(view.message);
	}
	if (view.funnel) parts.push("Public via Funnel");
	if (view.hidePublic) parts.push("Tailnet only: public domains are paused");
	return {
		kind: info.kind,
		label: view.hostname || name || "tailnet",
		state: info.label,
		isPublic: view.funnel,
		tailnetOnly: view.hidePublic,
		tooltip: parts.join(" · "),
	};
}

type TcpRule = NonNullable<TailnetView["tcp"]>[number];

const tcpKey = (rules: readonly TcpRule[] | null | undefined): string =>
	JSON.stringify(
		[...(rules ?? [])]
			.map((rule) => [
				rule.listen,
				rule.target || rule.listen,
				rule.tls === true,
			])
			.sort((a, b) => Number(a[0]) - Number(b[0])),
	);

/**
 * What the next deploy changes on the tailnet: the saved (staged) config set
 * against the node as it last reported. Empty when they agree.
 */
export function tailnetDifferences(
	config: TailnetConfig | undefined,
	view: TailnetView | null,
): string[] {
	if (!config?.enabled) {
		return view && view.state !== "removing"
			? ["The device is removed from your tailnet"]
			: [];
	}
	if (!view) return ["The device is created"];
	const changes: string[] = [];
	if (config.hostname && config.hostname !== view.hostname) {
		changes.push(`Hostname ${view.hostname} → ${config.hostname}`);
	}
	if ((config.web === true) !== view.web) {
		changes.push(config.web === true ? "Web exposure on" : "Web exposure off");
	}
	if (tcpKey(config.tcp) !== tcpKey(view.tcp)) changes.push("TCP ports");
	if ((config.funnel === true) !== view.funnel) {
		changes.push(config.funnel === true ? "Funnel on" : "Funnel off");
	}
	if ((config.hidePublic === true) !== view.hidePublic) {
		changes.push(
			config.hidePublic === true ? "Tailnet only on" : "Tailnet only off",
		);
	}
	return changes;
}
