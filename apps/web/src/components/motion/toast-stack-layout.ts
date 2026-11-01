/** How far each toast behind the front one peeks out above it while collapsed, in px. */
export const STACK_PEEK = 10;
/** The gap between toasts while the stack is expanded, in px. */
export const STACK_GAP = 8;
/** A toast's height before it has been measured: one title and one description line. */
export const DEFAULT_HEIGHT = 58;

export interface ToastSlot<T> {
	item: T;
	y: number;
	scale: number;
	opacity: number;
	/**
	 * The height a collapsed toast behind the front one is forced to: the
	 * front toast's. Toast text wraps, so heights differ, and a taller front
	 * toast would otherwise cover the ones behind it completely, while a
	 * taller one behind would stick out above it. Undefined means the toast
	 * keeps its natural height.
	 */
	forcedHeight: number | undefined;
}

export interface ToastStackLayout<T> {
	slots: ToastSlot<T>[];
	/** The height the stack's container reserves. */
	totalHeight: number;
}

/**
 * Positions a stack of toasts, front first. `heights` holds each toast's
 * natural height as last measured; an unmeasured toast counts as
 * DEFAULT_HEIGHT. Expanded, toasts sit one above another at their own
 * heights; collapsed, the ones behind the front toast take its height and
 * peek out above it, the fourth and later hidden.
 */
export function toastStackLayout<T extends { id: string }>(
	items: readonly T[],
	heights: Readonly<Record<string, number>>,
	expanded: boolean,
): ToastStackLayout<T> {
	const frontHeight = heights[items[0]?.id ?? ""] ?? DEFAULT_HEIGHT;

	if (expanded) {
		let accumulated = 0;
		const slots = items.map((item): ToastSlot<T> => {
			// 0 - x rather than -x: the front toast's offset is 0, not -0.
			const y = 0 - accumulated;
			accumulated += (heights[item.id] ?? DEFAULT_HEIGHT) + STACK_GAP;
			return { item, y, scale: 1, opacity: 1, forcedHeight: undefined };
		});

		return { slots, totalHeight: Math.max(accumulated - STACK_GAP, 0) };
	}

	const slots = items.map((item, index): ToastSlot<T> => {
		const scale = Math.max(0, 1 - index * 0.05);
		// A card shrinks from its bottom edge, so its top drops by its height
		// times the shrink. The design's peek is tuned for a DEFAULT_HEIGHT
		// toast; lifting the card by the shrink of any extra height keeps that
		// peek when the front toast wraps, instead of the cards behind it
		// sinking out of sight.
		const y =
			0 - index * STACK_PEEK - (frontHeight - DEFAULT_HEIGHT) * (1 - scale);
		return {
			item,
			y,
			scale,
			opacity: index > 2 ? 0 : 1,
			forcedHeight: index === 0 ? undefined : frontHeight,
		};
	});

	return {
		slots,
		totalHeight:
			items.length === 0
				? 0
				: frontHeight + Math.min(items.length - 1, 2) * STACK_PEEK,
	};
}
