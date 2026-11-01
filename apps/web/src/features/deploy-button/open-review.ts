export interface OpenReviewOptions {
	/**
	 * Scope the sheet to one service: its staged lines only, and a key that
	 * deploys that service alone. A service with nothing staged is offered a
	 * redeploy of its current release.
	 */
	serviceId?: string;
}

type Listener = (options: OpenReviewOptions) => void;

const listeners = new Set<Listener>();

/**
 * Opens the top-bar review sheet of the environment on screen, from anywhere
 * (a toast action, the canvas dock, a palette entry). Does nothing outside an
 * environment, where no Deploy button is mounted to listen.
 */
export function openStagedReview(options: OpenReviewOptions = {}): void {
	for (const listener of listeners) listener(options);
}

/** The Deploy button's side: `listener` runs on every `openStagedReview()`. */
export function onOpenStagedReview(listener: Listener): () => void {
	listeners.add(listener);
	return () => listeners.delete(listener);
}
