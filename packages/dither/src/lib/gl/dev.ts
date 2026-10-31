/**
 * Console output that says each thing once: a field re-rendered every frame or
 * rebuilt after a context restore must not repeat the same message.
 */

const seen = new Set<string>();

/** True outside production builds; bundlers replace `process.env.NODE_ENV`. */
export function isDev(): boolean {
	try {
		return process.env.NODE_ENV !== "production";
	} catch {
		return false;
	}
}

/** `console.warn` once per `key`, in development only. */
export function warnOnce(key: string, message: string): void {
	if (!isDev() || seen.has(key)) return;
	seen.add(key);
	console.warn(message);
}

/** `console.error` once per `key`, in every build: a failed shader is never silent. */
export function errorOnce(key: string, message: string): void {
	if (seen.has(key)) return;
	seen.add(key);
	console.error(message);
}

/** Forget what was logged (tests). */
export function resetLogOnce(): void {
	seen.clear();
}
