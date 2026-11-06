import { cn } from "cn";
import { useEffect, useRef, useState } from "react";
import { CopyCheckIcon } from "#/components/icons/animated/copy-check-icon";
import { Label } from "#/components/ui/label";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import { COPIED_MS, toastCopyFailed, writeClipboard } from "./copy-button";

/** The phrase's look where a site does not choose its own. */
const PHRASE = "font-mono text-ink-1";

/**
 * An icon-only button that copies `value`: the glyph morphs to a check for
 * COPIED_MS and "Copied" is announced politely. Where the clipboard is
 * missing (plain http) a toast says how to copy by hand. It never submits
 * the form around it.
 */
function CopyPhraseButton({ value }: { value: string }) {
	const [copied, setCopied] = useState(false);
	const [instant, setInstant] = useState(false);
	const timeoutRef = useRef<number | undefined>(undefined);
	useEffect(() => () => window.clearTimeout(timeoutRef.current), []);

	const copy = async () => {
		setInstant(lastInputWasKeyboard());
		const ok = await writeClipboard(value);
		if (!ok) {
			toastCopyFailed(value);
			return;
		}
		setCopied(true);
		window.clearTimeout(timeoutRef.current);
		timeoutRef.current = window.setTimeout(() => setCopied(false), COPIED_MS);
	};

	return (
		<>
			<button
				type="button"
				aria-label={`Copy ${value}`}
				onClick={() => void copy()}
				className="-my-1 inline-flex size-5 shrink-0 cursor-pointer items-center justify-center rounded-sm text-ink-3 outline-none transition-colors duration-quick ease-out hover:text-ink-1 focus-visible:focus-ring pointer-coarse:size-8"
			>
				<CopyCheckIcon
					copied={copied}
					instant={instant}
					className="size-3.5"
					checkClassName="text-ok"
				/>
			</button>
			<span className="sr-only" role="status" aria-live="polite">
				{copied ? "Copied" : ""}
			</span>
		</>
	);
}

/**
 * The phrase a person must type to confirm something dangerous, with a copy
 * icon right after it so they can paste it into the confirm field. `className`
 * styles the phrase text (default: mono, ink-1).
 */
export function ConfirmPhrase({
	value,
	className,
}: {
	value: string;
	className?: string;
}) {
	return (
		<span className="inline-flex items-center gap-1">
			<code className={className ?? PHRASE}>{value}</code>
			<CopyPhraseButton value={value} />
		</span>
	);
}

/**
 * "Type <phrase> [copy] to confirm" as the label of the confirm field. The
 * copy button sits between two labels of the same field, never inside one:
 * the field's accessible name stays "Type <phrase> to confirm", and pressing
 * the icon does not activate the label and focus the input.
 */
export function ConfirmPrompt({
	htmlFor,
	value,
	tail = "to confirm",
	className,
	phraseClassName,
}: {
	/** The confirm input's id. */
	htmlFor: string;
	value: string;
	/** What follows the phrase. */
	tail?: string;
	className?: string;
	phraseClassName?: string;
}) {
	return (
		<div className={cn("flex flex-wrap items-center gap-x-1", className)}>
			<Label htmlFor={htmlFor} className="block">
				Type <code className={phraseClassName ?? PHRASE}>{value}</code>{" "}
			</Label>
			<CopyPhraseButton value={value} />
			<Label htmlFor={htmlFor} className="block">
				{tail}
			</Label>
		</div>
	);
}
