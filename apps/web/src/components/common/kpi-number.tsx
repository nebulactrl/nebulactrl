import { DitherNumber } from "@nebulactrl/dither/text/dither-number";
import { cn } from "cn";
import { useMemo, useRef, useState } from "react";
import { PopNumber } from "#/features/metrics/ui/pop-number";
import { type KpiParts, kpiFormat, parseKpiText } from "./kpi-number-model";

export interface KpiNumberProps {
	/** Already formatted: "1.2k", "212 MiB", "12.3%". */
	text: string;
	className?: string | undefined;
}

/**
 * A headline figure. The digits resolve out of dither once, when the value
 * first arrives; after that a new value uses the quiet pop-in, so a refetch
 * never replays the resolve. Text that is not one number
 * ("–", "3h 12m", "2/2") is shown as it is, with the same pop-in. The
 * formatted text is always what screen readers get, and reduced motion shows
 * each value at once.
 */
export function KpiNumber({ text, className }: KpiNumberProps) {
	const parts = useMemo(() => parseKpiText(text), [text]);
	if (!parts) {
		return <PopNumber text={text} {...(className ? { className } : {})} />;
	}
	return <Digits parts={parts} className={className} />;
}

function Digits({
	parts,
	className,
}: {
	parts: KpiParts;
	className?: string | undefined;
}) {
	// The first value resolves; once it has changed, every later value ticks.
	const first = useRef(parts.value);
	const [changed, setChanged] = useState(false);
	if (!changed && parts.value !== first.current) setChanged(true);
	const { decimals, grouping } = parts;
	const format = useMemo(
		() => kpiFormat({ decimals, grouping }),
		[decimals, grouping],
	);
	return (
		<DitherNumber
			value={parts.value}
			locales="en"
			format={format}
			prefix={parts.prefix}
			suffix={parts.suffix}
			effect={changed ? "tick" : "resolve"}
			trigger="in-view"
			accent="none"
			className={cn("leading-none", className)}
		/>
	);
}

/** For `DitherCapacityStat`'s `renderValue`: its number resolves once, then pops in. */
export function renderKpi(text: string) {
	return <KpiNumber text={text} />;
}
