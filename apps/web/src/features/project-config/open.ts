/** The nebula.toml dialogs of the environment on screen. */
export type ProjectConfigDialog = "review" | "export" | "source";

type Listener = (dialog: ProjectConfigDialog) => void;

const listeners = new Set<Listener>();

/**
 * Opens one of the nebula.toml dialogs from anywhere (the banner, a field
 * mark, a menu). Does nothing outside an environment, where no host is
 * mounted to listen.
 */
export function openProjectConfig(dialog: ProjectConfigDialog): void {
	for (const listener of listeners) listener(dialog);
}

/** The host's side: `listener` runs on every `openProjectConfig()`. */
export function onOpenProjectConfig(listener: Listener): () => void {
	listeners.add(listener);
	return () => listeners.delete(listener);
}
