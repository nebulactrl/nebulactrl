import { cn } from "cn";
import type { ReactNode } from "react";

type DiffSign = "+" | "−" | "~";

export interface DiffLineProps {
	/** Added, removed or changed. */
	sign: DiffSign;
	children: ReactNode;
	/**
	 * What the row's label sets or deploys, when it reads "label  value":
	 * shown after the label with a gap, ellipsised, the full text on hover.
	 */
	value?: string;
	/**
	 * `plain` colours only the sign, for a staged-changes review where the
	 * reader is still editing. `tinted` colours the whole row, for an approval
	 * where the diff is the thing being judged.
	 */
	variant?: "plain" | "tinted";
	/** Drops this one change; shows an "undo" button at the row's end. */
	onUndo?: () => void;
	/** Said apart from the line, in the row's quiet voice: "set by nebula.toml". */
	note?: string;
	/** Let a long line wrap instead of ellipsising it, where hovering for the rest is no option. */
	wrap?: boolean;
	className?: string;
}

const signTone: Record<DiffSign, string> = {
	"+": "text-ok",
	"−": "text-bad",
	"~": "text-warn",
};

const tintedTone: Record<DiffSign, string> = {
	"+": "bg-status-live-wash text-status-live-on",
	"−": "bg-status-failed-wash text-status-failed-on",
	"~": "bg-status-staged-wash text-status-staged-on",
};

const signName: Record<DiffSign, string> = {
	"+": "added",
	"−": "removed",
	"~": "changed",
};

// A string with a line break in it; anything else is one line.
const hasLineBreak = (text: ReactNode): boolean =>
	typeof text === "string" && /[\r\n]/.test(text);

/** One line of a config diff: `+ KEY=value`, `− KEY`, `~ replicas 4 → 6`. */
export function DiffLine({
	sign,
	children,
	value,
	variant = "plain",
	onUndo,
	note,
	wrap = false,
	className,
}: DiffLineProps) {
	// A value that holds line breaks (a certificate, a JSON blob) is shown
	// whole: its breaks kept, long lines wrapped, nothing ellipsised. A
	// secret never gets here unmasked, the server sends it as dots.
	const labelMultiline = hasLineBreak(children);
	const valueMultiline = hasLineBreak(value);
	const multiline = labelMultiline || valueMultiline;
	return (
		<div
			data-slot="diff-line"
			data-sign={signName[sign]}
			className={cn(
				"flex gap-2.5 font-mono text-xs leading-[1.4]",
				multiline ? "items-start" : "items-center",
				variant === "plain" ? "px-3 py-[5px]" : ["px-4 py-1", tintedTone[sign]],
				className,
			)}
		>
			<span
				className={cn(
					"w-3 shrink-0",
					variant === "plain" ? signTone[sign] : undefined,
				)}
			>
				<span aria-hidden="true">{sign}</span>
				<span className="sr-only">{signName[sign]}</span>
			</span>
			{value === undefined ? (
				<span
					className={cn(
						"min-w-0 flex-1",
						labelMultiline
							? "whitespace-pre-wrap break-words"
							: !wrap && "truncate",
					)}
				>
					{children}
				</span>
			) : (
				// Label and value are separate boxes so the gap between them
				// survives (a doubled space collapses) and only the value, which
				// can be an image reference of any length, gives way to the
				// ellipsis; its title keeps the whole of it readable.
				<span className="flex min-w-0 flex-1 gap-2.5">
					<span
						className={cn(
							"max-w-[45%] shrink-0",
							wrap || labelMultiline ? "break-words" : "truncate",
							labelMultiline && "whitespace-pre-wrap",
						)}
					>
						{children}
					</span>
					<span
						title={valueMultiline ? undefined : value}
						className={cn(
							"min-w-0",
							valueMultiline
								? "whitespace-pre-wrap break-words"
								: wrap
									? "break-words"
									: "truncate",
						)}
					>
						{value}
					</span>
				</span>
			)}
			{note ? (
				<span className="shrink-0 font-sans text-2xs text-ink-4 leading-none">
					{note}
				</span>
			) : null}
			{onUndo ? (
				<button
					type="button"
					onClick={onUndo}
					className="shrink-0 cursor-pointer rounded-xs px-1.5 py-[3px] font-medium font-sans text-2xs text-ink-4 leading-none transition-[background-color,color] duration-quick hover:bg-white/8 hover:text-ink-1 focus-visible:focus-ring"
				>
					undo
				</button>
			) : null}
		</div>
	);
}
