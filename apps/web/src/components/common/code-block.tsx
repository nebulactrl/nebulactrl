import { cn } from "cn";
import {
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import { COPIED_MS, CopyChipFaces, writeClipboard } from "./copy-button";

export interface CodeBlockProps {
	children: ReactNode;
	/** When set, clicking anywhere in the block copies this exact text. */
	copyValue?: string;
	/** Break long tokens instead of truncating — for a token shown once. */
	breakAll?: boolean;
	/** Keep to one line and ellipsize — for a hint beside prose. */
	singleLine?: boolean;
	className?: string;
}

/**
 * A command, token or log excerpt on the near-black code surface.
 *
 * With `copyValue` the whole block is the copy target, not just the icon in
 * its corner: the thing people want to copy is the command they are looking
 * at, and hunting for a 20px button to avoid selecting three lines of shell
 * by hand is the wrong way round.
 *
 * Selecting text by hand still works. A click that ends a selection inside
 * the block is someone highlighting part of a command, so it is left alone
 * rather than overwriting their clipboard with the whole thing.
 */
export function CodeBlock({
	children,
	copyValue,
	breakAll = false,
	singleLine = false,
	className,
}: CodeBlockProps) {
	const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
	const [instant, setInstant] = useState(false);
	const timeoutRef = useRef<number | undefined>(undefined);
	useEffect(() => () => window.clearTimeout(timeoutRef.current), []);

	const copy = async () => {
		if (copyValue === undefined) return;

		setInstant(lastInputWasKeyboard());
		const ok = await writeClipboard(copyValue);
		setState(ok ? "copied" : "failed");
		window.clearTimeout(timeoutRef.current);
		timeoutRef.current = window.setTimeout(() => setState("idle"), COPIED_MS);
	};

	const interactive = copyValue !== undefined;

	return (
		<div
			data-slot="code-block"
			{...(interactive
				? {
						role: "button" as const,
						tabIndex: 0,
						"aria-label": `Copy to clipboard: ${copyValue}`,
						onClick: () => {
							// An uncollapsed selection means the reader is picking
							// out part of the command themselves.
							if (!window.getSelection()?.isCollapsed) return;
							void copy();
						},
						onKeyDown: (event: KeyboardEvent<HTMLDivElement>) => {
							if (event.key !== "Enter" && event.key !== " ") return;
							event.preventDefault();
							void copy();
						},
					}
				: {})}
			className={cn(
				"group/code surface-well relative rounded-ctl px-4 py-3.5 font-mono text-ink-1 text-xs leading-[1.7]",
				singleLine ? "truncate leading-normal" : "whitespace-pre-wrap",
				breakAll && "break-all",
				interactive &&
					"cursor-pointer pr-[84px] text-left focus-visible:focus-ring",
				className,
			)}
		>
			{children}
			{interactive ? (
				<>
					<span
						aria-hidden
						className="btn-secondary absolute top-2 right-2 grid h-7 min-w-[68px] items-center overflow-hidden rounded-md px-2.5 font-mono text-2xs text-ink-2 leading-none transition-[translate,box-shadow,background-color,color] duration-quick ease-out group-hover/code:text-ink-1 group-active/code:translate-y-(--key-travel) group-active/code:bg-surface-well group-active/code:shadow-pressed motion-reduce:transition-none pointer-coarse:h-9"
					>
						<CopyChipFaces state={state} instant={instant} />
					</span>
					<span className="sr-only" role="status" aria-live="polite">
						{state === "copied" ? "Copied" : ""}
					</span>
				</>
			) : null}
		</div>
	);
}
