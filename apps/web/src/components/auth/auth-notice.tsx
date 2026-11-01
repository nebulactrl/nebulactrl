import { cn } from "cn";
import type { ReactNode } from "react";
import { type CalloutTone, ToneIcon } from "#/components/common/tone-icon";

const TONES = {
	// A refusal or a failure.
	destructive: { tone: "danger", text: "text-status-failed-on" },
	// Something ended on its own (an expired session) and nothing was lost.
	warning: { tone: "warning", text: "text-ink-2" },
	success: { tone: "success", text: "text-ink-2" },
} as const satisfies Record<string, { tone: CalloutTone; text: string }>;

/**
 * The inline notice on the screens outside the console: what happened, the
 * current state and what to do next. A tone icon and a subtle tone wash mark
 * the tone and the sentence carries the meaning. `action` sits at the end for a
 * way out that belongs to the notice itself.
 */
export function AuthNotice({
	tone,
	children,
	action,
	className,
}: {
	tone: keyof typeof TONES;
	children: ReactNode;
	action?: ReactNode;
	className?: string;
}) {
	const { tone: calloutTone, text } = TONES[tone];
	return (
		<div
			role={tone === "success" ? "status" : "alert"}
			data-tone={calloutTone}
			className={cn(
				"tone-callout relative flex items-start gap-2.5 rounded-ctl px-3 py-2.5 text-xs leading-[1.45]",
				text,
				className,
			)}
		>
			<ToneIcon tone={calloutTone} className="mt-px" />
			<span className="min-w-0 flex-1 text-pretty">{children}</span>
			{action}
		</div>
	);
}
