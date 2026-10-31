import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { errorMessage } from "#/api/client-config";
import { acknowledgeInstanceBackupKey } from "#/api/gen/sdk.gen";
import {
	Alert,
	AlertAction,
	AlertDescription,
	AlertTitle,
} from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { keyAcknowledged } from "./model";
import { instanceBackupQuery, invalidateInstanceBackup } from "./queries";

/**
 * The reminder to keep NEBULACTRL_SECRET_KEY somewhere other than next to the
 * backups. A backup never contains the key, so a restore without it leaves every
 * variable, git token and registry credential unreadable. The card stays until
 * an admin confirms the key is stored; then it is gone for everyone.
 *
 * Shown only on Admin → Control-plane backup; the sidebar's Admin dot
 * (`useAdminAttention`) is how it reaches the admin on other pages. Renders
 * nothing once acknowledged.
 */
export function SecretKeyReminder() {
	const queryClient = useQueryClient();
	const settings = useQuery(instanceBackupQuery());
	const acknowledge = useMutation({
		mutationFn: () => acknowledgeInstanceBackupKey(),
		onSuccess: async () => {
			await invalidateInstanceBackup(queryClient);
			toast.success("Noted", "The reminder is gone.");
		},
		onError: (e) =>
			toast.error("Could not save the confirmation", errorMessage(e)),
	});
	const data = settings.data;
	if (!data || keyAcknowledged(data)) return null;

	return (
		<Alert variant="warning" data-testid="secret-key-reminder">
			<AlertTitle>Store your instance secret key separately</AlertTitle>
			<AlertDescription>
				<p>
					Backups never contain{" "}
					<code className="type-mono">NEBULACTRL_SECRET_KEY</code>, the key that
					encrypts every variable, git token and registry credential. Without
					it, a restored database holds secrets nobody can read. Keep a copy in
					a password manager or vault, apart from the backups.
				</p>
				<p>
					Its fingerprint is{" "}
					<code className="type-mono text-ink-2">{data.keyFingerprint}</code>.
					Compare it with the one you stored to be sure you saved the right key;
					it does not reveal the key.
				</p>
			</AlertDescription>
			<AlertAction>
				<Button
					variant="secondary"
					size="sm"
					disabled={acknowledge.isPending}
					onClick={() => acknowledge.mutate()}
				>
					{acknowledge.isPending ? <Spinner /> : null}I stored the key
				</Button>
			</AlertAction>
		</Alert>
	);
}
