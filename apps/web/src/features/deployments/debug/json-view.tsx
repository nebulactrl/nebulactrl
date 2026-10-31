import { cn } from "cn";

/** The most rows a JSON view grows to before it scrolls. */
const MAX_ROWS = 18;

/**
 * Pretty-printed JSON in a read-only text area: it is focusable, scrolls with
 * the keyboard, and selects and copies like any text, so no tabindex on a
 * non-interactive element is needed.
 */
export function JsonView({
	value,
	label,
	className,
}: {
	value: unknown;
	label: string;
	className?: string;
}) {
	const text = JSON.stringify(value, null, 2) ?? "null";
	const rows = Math.min(MAX_ROWS, text.split("\n").length);
	return (
		<textarea
			readOnly
			spellCheck={false}
			aria-label={label}
			rows={rows}
			wrap="off"
			value={text}
			className={cn(
				"surface-well block w-[calc(100%-1.5rem)] resize-none overflow-auto overscroll-contain rounded-card p-3 font-mono text-[11.5px] text-ink-2 leading-[1.55] outline-none focus-visible:focus-ring",
				className,
			)}
		/>
	);
}
