import { cn } from "cn";
import type { ReactNode } from "react";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";

/**
 * The tone vocabulary shared by the pill and the badge: deploy and service
 * states, generic semantics, and `off`. It is a name for a colour and an icon
 * (`statusKindOfTone`); the state is drawn as a `StatusIcon`, never as a dot.
 */
export type LampTone =
	| "live"
	| "building"
	| "deploying"
	| "failed"
	| "stopped"
	| "staged"
	| "offline"
	| "degraded"
	| "ok"
	| "warn"
	| "bad"
	| "info"
	| "off";

const KIND_OF_TONE: Record<LampTone, StatusKind> = {
	live: "live",
	ok: "healthy",
	building: "building",
	deploying: "deploying",
	failed: "failed",
	bad: "failed",
	stopped: "stopped",
	offline: "offline",
	off: "offline",
	staged: "pending",
	warn: "warning",
	degraded: "degraded",
	info: "unknown",
};

/** The status icon a tone is drawn as. */
export function statusKindOfTone(tone: LampTone): StatusKind {
	return KIND_OF_TONE[tone];
}

export interface StatusPillProps {
	/** The status icon to draw. Wins over `tone`. */
	kind?: StatusKind;
	/** A tone, drawn as its status icon. One of `kind` or `tone` is needed. */
	tone?: LampTone;
	/** Kept for call sites; in-flight kinds spin by themselves. */
	pulse?: boolean;
	children: ReactNode;
	className?: string;
}

/** A 22px engraved pill: a status icon and its label. The label carries the state. */
export function StatusPill({
	kind,
	tone,
	children,
	className,
}: StatusPillProps) {
	const resolved = kind ?? statusKindOfTone(tone ?? "off");
	return (
		<span
			data-slot="status-pill"
			data-kind={resolved}
			{...(tone ? { "data-tone": tone } : {})}
			className={cn(
				"surface-well inline-flex h-[22px] w-fit shrink-0 items-center gap-1.5 whitespace-nowrap rounded-full pr-[9px] pl-[6px] font-medium text-ink-1 text-xs leading-none",
				className,
			)}
		>
			<StatusIcon kind={resolved} label="" />
			{children}
		</span>
	);
}
