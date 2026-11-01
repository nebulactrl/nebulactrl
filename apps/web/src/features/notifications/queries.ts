import type { QueryClient } from "@tanstack/react-query";
import { invalidateOperations } from "#/api/invalidate";

/** Everything the notifications UI shows; refetched after any channel mutation. */
const NOTIFICATION_OPERATIONS = [
	"listNotificationChannels",
	"getNotificationChannel",
	"listNotificationDeliveries",
] as const;

export function invalidateNotifications(
	queryClient: QueryClient,
): Promise<void> {
	return invalidateOperations(queryClient, NOTIFICATION_OPERATIONS);
}
