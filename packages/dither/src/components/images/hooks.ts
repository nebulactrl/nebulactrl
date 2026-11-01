import {
	type RefObject,
	useEffect,
	useState,
	useSyncExternalStore,
} from "react";

/** The device pixel ratio, kept current when the window moves between screens or zooms. */
export function useDevicePixelRatio(): number {
	return useSyncExternalStore(
		(onChange) => {
			let query = matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`);
			const listener = () => {
				query.removeEventListener("change", listener);
				query = matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`);
				query.addEventListener("change", listener);
				onChange();
			};
			query.addEventListener("change", listener);
			return () => query.removeEventListener("change", listener);
		},
		() => window.devicePixelRatio || 1,
		() => 1,
	);
}

export interface BoxSize {
	width: number;
	height: number;
}

/** The element's content box in CSS px, null until measured. Updates on every resize. */
export function useBoxSize(ref: RefObject<Element | null>): BoxSize | null {
	const [size, setSize] = useState<BoxSize | null>(null);
	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		const observer = new ResizeObserver(([entry]) => {
			if (!entry) return;
			const { width, height } = entry.contentRect;
			setSize((prev) =>
				prev && prev.width === width && prev.height === height
					? prev
					: { width, height },
			);
		});
		observer.observe(el);
		return () => observer.disconnect();
	}, [ref]);
	return size;
}

export interface InViewOptions {
	/** Margin around the viewport, like CSS margin. Default "0px". */
	rootMargin?: string;
	/** Fraction of the element that must show. Default 0. */
	threshold?: number;
	/** Stay true after the first time it shows. Default false. */
	once?: boolean;
}

/** Whether the element intersects the viewport. Treated as true without IntersectionObserver. */
export function useInView(
	ref: RefObject<Element | null>,
	options: InViewOptions = {},
): boolean {
	const { rootMargin = "0px", threshold = 0, once = false } = options;
	const [inView, setInView] = useState(false);
	useEffect(() => {
		const el = ref.current;
		if (!el) return;
		if (typeof IntersectionObserver === "undefined") {
			setInView(true);
			return;
		}
		const observer = new IntersectionObserver(
			([entry]) => {
				if (!entry) return;
				if (entry.isIntersecting) {
					setInView(true);
					if (once) observer.disconnect();
				} else if (!once) {
					setInView(false);
				}
			},
			{ rootMargin, threshold },
		);
		observer.observe(el);
		return () => observer.disconnect();
	}, [ref, rootMargin, threshold, once]);
	return inView;
}

/**
 * A counter that bumps when web fonts finish loading, so canvas text that was
 * measured with a fallback face is measured again with the real one.
 */
export function useFontVersion(): number {
	const [version, setVersion] = useState(0);
	useEffect(() => {
		if (typeof document === "undefined" || !document.fonts) return;
		let alive = true;
		const bump = () => alive && setVersion((v) => v + 1);
		document.fonts.ready.then(bump);
		document.fonts.addEventListener("loadingdone", bump);
		return () => {
			alive = false;
			document.fonts.removeEventListener("loadingdone", bump);
		};
	}, []);
	return version;
}
