import { cn } from "cn";
import type { ReactNode } from "react";
import { type CalloutTone, ToneIcon } from "#/components/common/tone-icon";

export interface StatePanelProps {
	tone: CalloutTone;
	/** What happened. */
	title: ReactNode;
	/** What the state is now and what to do next. */
	body: ReactNode;
	/** One mono line of evidence: the signed-in address, the error text. */
	detail?: ReactNode;
	/** The way out. */
	actions?: ReactNode;
	className?: string;
}

/**
 * The outside-the-shell state (sign-in trouble, no access, not found): a tone
 * icon at the start of the title and a subtle tone wash for what kind of
 * trouble, the sentence, an optional mono detail and the way out.
 */
export function StatePanel({
	tone,
	title,
	body,
	detail,
	actions,
	className,
}: StatePanelProps) {
	return (
		<div
			data-slot="state-panel"
			data-tone={tone}
			className={cn(
				"tone-callout surface-raised relative flex w-full items-start gap-4 rounded-panel p-6",
				className,
			)}
		>
			<div className="min-w-0 max-w-[56ch]">
				<h1 className="type-section-title flex items-start gap-2.5 text-ink-1">
					<ToneIcon tone={tone} className="mt-[3px]" />
					<span className="min-w-0">{title}</span>
				</h1>
				<p className="type-body mt-2 text-ink-3">{body}</p>
				{detail ? (
					<div className="surface-well mt-3.5 break-words rounded-ctl px-3 py-2.5 font-mono text-ink-2 text-xs leading-normal">
						{detail}
					</div>
				) : null}
				{actions ? (
					<div className="mt-3.5 flex flex-wrap gap-2">{actions}</div>
				) : null}
			</div>
		</div>
	);
}
