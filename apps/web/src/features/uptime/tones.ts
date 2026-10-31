/**
 * Operational is the common case, so it is the quietest thing on screen: the
 * success hue at low strength over the neutral ink, not the full status
 * green. Degraded (amber) and down (red) keep their full colour, so a
 * problem is the only loud cell. The strip and the dithered bars share this
 * one value so a legend swatch matches both.
 */
export const OPERATIONAL_COLOR =
	"color-mix(in oklab, var(--color-status-live) 34%, var(--color-ink-4))";

/** The `colors` of a `DitherUptimeBar` for uptime: only the operational cells are toned down. */
export const BAR_COLORS = { up: OPERATIONAL_COLOR } as const;
