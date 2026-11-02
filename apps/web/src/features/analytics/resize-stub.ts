// Test-only: happy-dom has no layout, so every element measures 0×0 and the
// charts and the dot map wait for a size that never comes. This stands in for
// ResizeObserver and reports one fixed size as soon as an element is observed.

export interface BoxSize {
	width: number;
	height: number;
}

const original = globalThis.ResizeObserver;

/** Installs the stub; returns a restore function for afterEach. */
export function stubResizeObserver(size: BoxSize): () => void {
	class FixedResizeObserver {
		private readonly callback: ResizeObserverCallback;
		constructor(callback: ResizeObserverCallback) {
			this.callback = callback;
		}
		observe(target: Element) {
			const rect = {
				x: 0,
				y: 0,
				top: 0,
				left: 0,
				right: size.width,
				bottom: size.height,
				...size,
				toJSON: () => ({}),
			};
			const box = [{ inlineSize: size.width, blockSize: size.height }];
			queueMicrotask(() =>
				this.callback(
					[
						{
							target,
							contentRect: rect,
							borderBoxSize: box,
							contentBoxSize: box,
							devicePixelContentBoxSize: box,
						} as unknown as ResizeObserverEntry,
					],
					this as unknown as ResizeObserver,
				),
			);
		}
		unobserve() {}
		disconnect() {}
	}
	globalThis.ResizeObserver =
		FixedResizeObserver as unknown as typeof ResizeObserver;
	return () => {
		globalThis.ResizeObserver = original;
	};
}
