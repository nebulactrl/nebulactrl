import { useQuery } from "@tanstack/react-query";
import { meQuery } from "#/api/hooks";
import { keyAcknowledged } from "#/features/instance-backup/model";
import { instanceBackupQuery } from "#/features/instance-backup/queries";
import { updateWaiting } from "./update-model";
import { systemUpdateQuery } from "./use-system-update";

/**
 * What waits for the instance admin: a newer release, or the reminder to
 * store the instance secret key. Anyone else gets both false and no request
 * is made (both endpoints refuse them). The sidebar's Admin dot reads this.
 */
export function useAdminAttention(): { update: boolean; keyReminder: boolean } {
	const me = useQuery(meQuery());
	const enabled = me.data?.instanceAdmin === true;
	const update = useQuery({ ...systemUpdateQuery(), enabled });
	const backup = useQuery({ ...instanceBackupQuery(), enabled });
	return {
		update: enabled && updateWaiting(update.data),
		keyReminder:
			enabled && backup.data !== undefined && !keyAcknowledged(backup.data),
	};
}
