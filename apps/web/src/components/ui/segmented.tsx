import { cn } from "cn";
import { type ReactNode, useLayoutEffect, useRef } from "react";
import { ToggleGroup, ToggleGroupItem } from "#/components/ui/toggle-group";
import { fadeMask, scrollTarget, useEdgeFade } from "#/lib/hooks/use-edge-fade";

interface SegmentedOption<T extends string> {
	value: T;
	label: ReactNode;
	disabled?: boolean;
}

export interface SegmentedProps<T extends string> {
	value: T;
	onValueChange: (value: T) => void;
	options: readonly SegmentedOption<T>[];
	/** Names the choice for assistive tech: "Time range", "Filter". */
	"aria-label": string;
	className?: string;
	/** Mono for values (1h, 24h, env names); sans for words (Owner, Admin). */
	font?: "mono" | "sans";
	/** `glide` slides one fill between segments; `instant` fills the pressed one in place. */
	indicator?: "glide" | "instant";
	/** `compact` is the 24px toolbar segment; `regular` the 28px form segment. */
	density?: "compact" | "regular";
}

/**
 * A single choice among two to five short mono options (1h · 24h · 7d,
 * All · Unread). Always exactly one pressed: clicking the pressed segment
 * again keeps it, because an empty segmented control has no meaning.
 *
 * It never grows past its room. When the options do not fit (a phone, a
 * narrow dialog) the strip scrolls sideways, the end that continues fades and
 * the pressed segment is kept in view, as in `Tabs`.
 */
export function Segmented<T extends string>({
	value,
	onValueChange,
	options,
	className,
	font = "mono",
	indicator = "glide",
	density = "compact",
	...props
}: SegmentedProps<T>) {
	const groupRef = useRef<HTMLDivElement>(null);
	const fade = useEdgeFade(groupRef);
	const scrolls = fade.start || fade.end;
	const mask = fadeMask(fade);

	// The pressed segment comes into view when the choice changes (and on
	// first paint), never on every scroll: reading the strip by hand stays put.
	useLayoutEffect(() => {
		const group = groupRef.current;
		const pressed = group?.querySelector<HTMLElement>(
			':scope > [aria-pressed="true"]',
		);
		if (!group || !pressed) return;
		const box = group.getBoundingClientRect();
		const item = pressed.getBoundingClientRect();
		const target = scrollTarget(
			group.scrollLeft,
			{ left: box.left, width: group.clientWidth },
			{ left: item.left, width: item.width },
		);
		if (target !== null) group.scrollTo({ left: target });
	}, [value]);

	return (
		<ToggleGroup
			ref={groupRef}
			variant="segmented"
			size={density === "regular" ? "segmented-lg" : "segmented"}
			indicator={indicator}
			spacing={0.5}
			value={[value]}
			onValueChange={(next) => {
				const picked = next[0] as T | undefined;
				if (picked !== undefined) onValueChange(picked);
			}}
			aria-label={props["aria-label"]}
			style={mask ? { maskImage: mask, WebkitMaskImage: mask } : undefined}
			className={cn(
				"max-w-full shrink-0",
				// Only a strip that overflows clips: one that fits keeps its focus
				// rings and the thumb's shadow.
				scrolls && "no-scrollbar overflow-x-auto",
				className,
			)}
		>
			{options.map((option) => (
				<ToggleGroupItem
					key={option.value}
					value={option.value}
					disabled={option.disabled ?? false}
					className={font === "sans" ? "font-sans" : undefined}
				>
					{option.label}
				</ToggleGroupItem>
			))}
		</ToggleGroup>
	);
}
