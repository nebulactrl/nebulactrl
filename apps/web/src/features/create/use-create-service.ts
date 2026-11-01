import { useMemo } from "react";
import { type CreateKind, type CreateOptions, createActions } from "./store";

export interface CreateServiceApi {
	/**
	 * With a kind, opens that dialog. Without one, opens the Create menu (or
	 * the Docker image dialog when no Create button is on screen).
	 * `position` drops the node there once created; `engine` preselects a
	 * database engine.
	 */
	open(kind?: CreateKind, options?: CreateOptions): void;
}

/** Opens the create flow from anywhere: the dock, context menu, palette. */
export function useCreateService(): CreateServiceApi {
	return useMemo(() => ({ open: createActions.open }), []);
}
