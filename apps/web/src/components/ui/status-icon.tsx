import { durations } from "@nebulactrl/tokens";
import { cn } from "cn";
import {
	CircleAlert,
	CircleCheck,
	CircleDashed,
	CircleSlash,
	CircleX,
	LoaderCircle,
	type LucideIcon,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";

/**
 * The status vocabulary of live resources: services, servers, deployments,
 * replicas, tunnels and tailnet nodes. Each kind is one Lucide icon in one
 * status colour. There are no dots and no lamps: state is words, plus this icon.
 */
export type StatusKind =
	| "live"
	| "healthy"
	| "building"
	| "deploying"
	| "verifying"
	| "failed"
	| "degraded"
	| "warning"
	| "stopped"
	| "offline"
	| "pending"
	| "unknown";

interface KindSpec {
	icon: LucideIcon;
	/** The status colour token; the icon is the only thing drawn in it. */
	color: string;
	/** Spins while the work is in flight; static under reduced motion. */
	spin: boolean;
	/** The spoken name, used when no `label` is given. */
	label: string;
}

const KINDS: Record<StatusKind, KindSpec> = {
	live: {
		icon: CircleCheck,
		color: "text-status-live",
		spin: false,
		label: "Live",
	},
	healthy: {
		icon: CircleCheck,
		color: "text-status-live",
		spin: false,
		label: "Healthy",
	},
	building: {
		icon: LoaderCircle,
		color: "text-status-building",
		spin: true,
		label: "Building",
	},
	deploying: {
		icon: LoaderCircle,
		color: "text-status-deploying",
		spin: true,
		label: "Deploying",
	},
	verifying: {
		icon: LoaderCircle,
		color: "text-status-deploying",
		spin: true,
		label: "Verifying",
	},
	failed: {
		icon: CircleX,
		color: "text-status-failed",
		spin: false,
		label: "Failed",
	},
	degraded: {
		icon: CircleAlert,
		color: "text-status-degraded",
		spin: false,
		label: "Degraded",
	},
	warning: {
		icon: CircleAlert,
		color: "text-status-staged",
		spin: false,
		label: "Warning",
	},
	stopped: {
		icon: CircleSlash,
		color: "text-status-stopped",
		spin: false,
		label: "Stopped",
	},
	offline: {
		icon: CircleSlash,
		color: "text-status-offline",
		spin: false,
		label: "Offline",
	},
	pending: {
		icon: CircleDashed,
		color: "text-status-staged",
		spin: false,
		label: "Pending",
	},
	unknown: {
		icon: CircleDashed,
		color: "text-status-stopped",
		spin: false,
		label: "Unknown",
	},
};

export interface StatusIconProps {
	kind: StatusKind;
	/** Size and layout only. The default is 14px; a `size-*` class wins. */
	className?: string;
	/**
	 * Spoken name. Defaults to the kind's own ("Building"). Pass `""` when the
	 * state is printed beside the icon, so a screen reader does not say it
	 * twice: the icon then becomes decorative.
	 */
	label?: string;
}

/** Kinds that mean work is in flight; leaving one for a settled kind is a success. */
const IN_FLIGHT: Partial<Record<StatusKind, true>> = {
	building: true,
	deploying: true,
	verifying: true,
};
const SETTLED: Partial<Record<StatusKind, true>> = {
	live: true,
	healthy: true,
};

/** The check's tween plus its stroke delay, then the wrapper is dropped. */
const CHECK_MS = durations.verySlow + durations.micro + 100;

/**
 * A 14px Lucide status icon in the status colour. `LoaderCircle` spins for
 * building, deploying and verifying, and holds still under reduced motion.
 * Always sits beside, or carries, a text name: colour is never the only cue.
 *
 * When a mounted icon goes from an in-flight kind to a settled one (a deploy
 * that has just succeeded) the check plays once: a fade, a small rotate,
 * bob and cross-blur, with the tick drawing itself (transitions.dev 10,
 * `.t-success-check`). The icon and the word "Live" are the cue that stays;
 * an icon that mounts already settled never animates, and reduced motion
 * shows the check at once.
 */
export function StatusIcon({ kind, className, label }: StatusIconProps) {
	const spec = KINDS[kind];
	const Icon = spec.icon;
	const spoken = label ?? spec.label;

	const previous = useRef(kind);
	const [celebrating, setCelebrating] = useState(false);
	useEffect(() => {
		const was = previous.current;
		previous.current = kind;
		if (was === kind) return;
		if (!IN_FLIGHT[was] || !SETTLED[kind]) {
			setCelebrating(false);
			return;
		}
		setCelebrating(true);
		const timer = window.setTimeout(() => setCelebrating(false), CHECK_MS);
		return () => window.clearTimeout(timer);
	}, [kind]);

	return (
		<span
			data-slot="status-icon"
			data-kind={kind}
			className="inline-flex shrink-0 items-center"
		>
			<span
				className={celebrating ? "t-success-check" : "inline-flex"}
				data-state={celebrating ? "in" : undefined}
			>
				<Icon
					aria-hidden="true"
					className={cn(
						"size-3.5 shrink-0",
						spec.color,
						spec.spin && "animate-nc-spin motion-reduce:animate-none",
						className,
					)}
				/>
			</span>
			{spoken ? <span className="sr-only">{spoken}</span> : null}
		</span>
	);
}
