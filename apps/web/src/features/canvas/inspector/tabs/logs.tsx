import { LogsTab as LogsPanel } from "#/features/logs/logs-tab";
import type { ServiceTabProps } from "./types";

/** The Logs tab: a virtualised live tail of the running container. */
export function LogsTab({ service }: ServiceTabProps) {
	return <LogsPanel service={service} />;
}
