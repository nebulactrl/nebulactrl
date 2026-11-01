import { cn } from "cn";
import { useMemo } from "react";
import { collapseDiff, type DiffLine, diffStats } from "./diff";

const SIGN: Record<DiffLine["kind"], string> = {
	same: " ",
	add: "+",
	remove: "−",
};

const WORD: Record<DiffLine["kind"], string> = {
	same: "unchanged",
	add: "added",
	remove: "removed",
};

const TONE: Record<DiffLine["kind"], string> = {
	same: "text-ink-3",
	add: "bg-status-live-wash text-status-live-on",
	remove: "bg-status-failed-wash text-status-failed-on",
};

/**
 * A staged compose file as a unified line diff: changed lines with two lines
 * of context, the rest folded into "N unchanged lines". The sign and a hidden
 * word carry the meaning, the tint only supports them.
 */
export function ComposeDiff({
	diff,
	className,
}: {
	diff: readonly DiffLine[];
	className?: string;
}) {
	const items = useMemo(() => collapseDiff(diff, 2), [diff]);
	const { added, removed } = useMemo(() => diffStats(diff), [diff]);
	return (
		<section
			aria-label={`Compose file changes: ${added} added, ${removed} removed`}
			className={cn(
				"surface-well max-h-72 overflow-auto overscroll-contain rounded-ctl py-1 font-mono text-[11.5px] leading-[1.5]",
				className,
			)}
		>
			{items.map((item, index) =>
				item.kind === "gap" ? (
					<div
						// biome-ignore lint/suspicious/noArrayIndexKey: the folded gaps have no identity of their own; the list never reorders.
						key={`gap-${index}`}
						className="px-3 py-0.5 font-sans text-2xs text-ink-4"
					>
						{item.hidden} unchanged {item.hidden === 1 ? "line" : "lines"}
					</div>
				) : (
					<div
						key={`${item.oldLine ?? "n"}:${item.newLine ?? "n"}`}
						data-kind={WORD[item.kind]}
						className={cn("flex gap-2 px-3", TONE[item.kind])}
					>
						<span aria-hidden className="w-3 shrink-0 select-none">
							{SIGN[item.kind]}
						</span>
						<span className="sr-only">{WORD[item.kind]}</span>
						<span className="min-w-0 whitespace-pre-wrap break-all">
							{item.text === "" ? " " : item.text}
						</span>
					</div>
				),
			)}
		</section>
	);
}
