// Test-only: happy-dom has no layout, so the virtualised LogView would draw no
// rows. This gives every element a box (the virtualizer measures clientHeight
// and clientWidth) until the returned function restores the real getters.

export function stubLayout(width = 800, height = 400): () => void {
	const saved = {
		width: Object.getOwnPropertyDescriptor(
			HTMLElement.prototype,
			"clientWidth",
		),
		height: Object.getOwnPropertyDescriptor(
			HTMLElement.prototype,
			"clientHeight",
		),
	};
	Object.defineProperty(HTMLElement.prototype, "clientWidth", {
		configurable: true,
		get: () => width,
	});
	Object.defineProperty(HTMLElement.prototype, "clientHeight", {
		configurable: true,
		get: () => height,
	});
	return () => {
		for (const [key, descriptor] of [
			["clientWidth", saved.width],
			["clientHeight", saved.height],
		] as const) {
			if (descriptor)
				Object.defineProperty(HTMLElement.prototype, key, descriptor);
			else Reflect.deleteProperty(HTMLElement.prototype, key);
		}
	};
}
