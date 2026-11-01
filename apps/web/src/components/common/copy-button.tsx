import { cn } from "cn";
import { useEffect, useRef, useState } from "react";
import { CheckIcon } from "#/components/icons/animated/check-icon";
import { CopyCheckIcon } from "#/components/icons/animated/copy-check-icon";
import { TextMorph } from "#/components/motion/text-morph";
import { Button } from "#/components/ui/button";
import { toast } from "#/components/ui/toast";
import { lastInputWasKeyboard } from "#/lib/input-modality";

/** How long a copy confirmation stays before the control reverts. */
export const COPIED_MS = 1_500;

export interface CopyButtonProps {
	value: string;
	/** Visible label; omit for an icon-only button. */
	children?: string;
	/** Accessible name for the icon-only form. */
	label?: string;
	/** `chip` is the design's filled "Copy" chip: label and "Copied" stacked in one cell. */
	variant?: "secondary" | "outline" | "ghost" | "chip";
	size?: "default" | "sm" | "icon-sm" | "icon-xs";
	className?: string;
}

/**
 * Writes to the clipboard, reporting whether it worked rather than
 * throwing. Over plain http the Clipboard API is absent entirely, which is
 * a false return, not an error.
 */
export async function writeClipboard(value: string): Promise<boolean> {
	if (!navigator.clipboard?.writeText) return false;
	try {
		await navigator.clipboard.writeText(value);
		return true;
	} catch {
		return false;
	}
}

/**
 * Copies from a menu, where nothing stays on screen to confirm it: a toast
 * says what was copied, or how to copy it by hand when the browser refuses.
 */
export async function copyWithToast(value: string): Promise<void> {
	if (await writeClipboard(value)) {
		toast.add({ title: "Copied", description: value, type: "success" });
		return;
	}
	toast.add({
		title: "Nothing was copied",
		description: `The browser blocked the clipboard. Select it and copy by hand: ${value}`,
		type: "error",
	});
}

/**
 * The chip's face: the label morphs in place ("Copy" to "Copied", the shared
 * letters stay put, text morph) while the check slides open to its left and
 * draws itself. Green and the word say it, so motion is never the only cue.
 */
export function CopyChipFaces({
	state,
	instant = false,
	children = "Copy",
}: {
	state: "idle" | "copied" | "failed";
	/** The copy came from the keyboard: nothing moves. */
	instant?: boolean;
	children?: string;
}) {
	const copied = state === "copied";
	const text =
		state === "copied"
			? "Copied"
			: state === "failed"
				? "Select and copy"
				: children;
	return (
		<span
			className={cn(
				"col-start-1 row-start-1 flex items-center justify-center whitespace-nowrap",
				copied && "text-status-live-on",
			)}
		>
			<span
				aria-hidden
				className={cn(
					"inline-grid shrink-0 overflow-hidden transition-[width,opacity] duration-fast ease-smooth-out",
					instant && "transition-none",
					copied ? "w-4 opacity-100" : "w-0 opacity-0",
				)}
			>
				<CheckIcon
					size={11}
					strokeWidth={3.2}
					className="block"
					animate={copied && !instant}
				/>
			</span>
			<TextMorph text={text} instant={instant} />
		</span>
	);
}

/**
 * Copies a value and says so in place for 1.5s: the copy glyph morphs to a
 * green check that draws itself, beside the "Copied" text, long enough to
 * register without blocking a second copy. A copy made from the keyboard
 * swaps without the motion. Over plain http the
 * Clipboard API is missing, so the button reports "Select and copy" instead
 * of pretending it worked.
 */
export function CopyButton({
	value,
	children,
	label,
	variant = "secondary",
	size,
	className,
}: CopyButtonProps) {
	const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
	const [instant, setInstant] = useState(false);
	const timeoutRef = useRef<number | undefined>(undefined);
	useEffect(() => () => window.clearTimeout(timeoutRef.current), []);

	const iconOnly = !children;
	const resolvedSize = size ?? (iconOnly ? "icon-sm" : "default");
	const copied = state === "copied";

	const onClick = async () => {
		setInstant(lastInputWasKeyboard());
		const ok = await writeClipboard(value);
		setState(ok ? "copied" : "failed");
		window.clearTimeout(timeoutRef.current);
		timeoutRef.current = window.setTimeout(() => setState("idle"), COPIED_MS);
	};

	if (variant === "chip") {
		return (
			<Button
				type="button"
				variant="secondary"
				onClick={onClick}
				className={cn(
					"grid h-7 min-w-[68px] items-center overflow-hidden rounded-md px-2.5 font-mono text-2xs",
					size === "sm" && "h-[26px] min-w-16 px-[9px]",
					className,
				)}
			>
				<CopyChipFaces state={state} instant={instant}>
					{children ?? "Copy"}
				</CopyChipFaces>
				<span className="sr-only" role="status" aria-live="polite">
					{copied ? "Copied" : ""}
				</span>
			</Button>
		);
	}

	const text = copied
		? "Copied"
		: state === "failed"
			? "Select and copy"
			: children;

	return (
		<Button
			type="button"
			variant={variant}
			size={resolvedSize}
			onClick={onClick}
			aria-label={iconOnly ? (label ?? "Copy") : undefined}
			className={className}
		>
			{iconOnly ? (
				<CopyCheckIcon
					copied={copied}
					instant={instant}
					checkClassName="text-status-live-on"
				/>
			) : (
				<>
					{copied ? (
						<CheckIcon animate={!instant} className="text-status-live-on" />
					) : null}
					<TextMorph
						text={text ?? ""}
						instant={instant}
						className={copied ? "text-status-live-on" : undefined}
					/>
				</>
			)}
			<span className="sr-only" role="status" aria-live="polite">
				{copied ? "Copied" : ""}
			</span>
		</Button>
	);
}
