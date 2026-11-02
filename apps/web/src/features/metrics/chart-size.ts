import { spyOn } from "bun:test";

/** The element Recharts measures text with (`getStringSize`), by its id. */
const MEASUREMENT_SPAN_ID = "recharts_measurement_span";

/**
 * happy-dom lays nothing out, so every element measures 0 x 0 and Recharts'
 * ResponsiveContainer renders no plot. Test-only: makes every element but the
 * text-measuring span report `width` x `height` until the returned function
 * restores the original. Recharts caches each string's measured size for the
 * whole process, so a text that measured as a full chart here would leave axes
 * with no room for their ticks in every later test.
 */
export function stubChartSize(width = 600, height = 200): () => void {
	const spy = spyOn(
		Element.prototype,
		"getBoundingClientRect",
	).mockImplementation(function (this: Element) {
		const w = this.id === MEASUREMENT_SPAN_ID ? 0 : width;
		const h = this.id === MEASUREMENT_SPAN_ID ? 0 : height;
		return {
			x: 0,
			y: 0,
			top: 0,
			left: 0,
			right: w,
			bottom: h,
			width: w,
			height: h,
			toJSON: () => ({}),
		} as DOMRect;
	});
	return () => spy.mockRestore();
}
