import { cn } from "cn";
import { useLayoutEffect, useRef } from "react";
import { morphText, setMorphText } from "#/lib/transitions/text-morph";

export interface TextMorphProps {
	/** The label. When it changes, only the letters that differ cross-blur. */
	text: string;
	/** Change without motion (a keyboard-started action). */
	instant?: boolean | undefined;
	className?: string | undefined;
}

/**
 * A label that morphs in place when its text changes (transitions.dev 33):
 * "Copy" to "Copied", "Save" to "Saved". The first text lands without motion,
 * and so does every text under reduced motion or when `instant`. The text is
 * plain DOM text once settled, so it reads the same to assistive tech and to
 * a test; pair a change with a live region when it must be announced.
 */
export function TextMorph({
	text,
	instant = false,
	className,
}: TextMorphProps) {
	const ref = useRef<HTMLSpanElement>(null);
	const placed = useRef(false);

	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		if (placed.current && !instant) morphText(el, text);
		else {
			setMorphText(el, text);
			placed.current = true;
		}
	}, [text, instant]);

	return <span ref={ref} className={cn("t-morph", className)} />;
}
