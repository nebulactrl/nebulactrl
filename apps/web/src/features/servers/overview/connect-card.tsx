import {
	skipToken,
	useMutation,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { errorMessage } from "#/api/client-config";
import { reissueEnrollmentToken } from "#/api/gen/sdk.gen";
import type { EnrollmentToken } from "#/api/gen/types.gen";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import {
	cacheEnrollment,
	ENROLLMENT_CACHE_MS,
	enrollmentKey,
} from "../enrollment";
import { InstallCommandField, useEnrollmentExpired } from "../install-command";
import type { ServerView } from "../model";
import { serverViewQuery } from "../queries";

/**
 * A server whose agent has not connected yet: its install command and a key
 * to make a new one. The control plane keeps only a token's hash, so the
 * command is shown here while this tab still holds the one it issued; after
 * a reload, or once it expires, "New install command" revokes the old token
 * and issues another. This card goes when the server's status leaves
 * "pending".
 */
export function ConnectCard({ server }: { server: ServerView }) {
	const queryClient = useQueryClient();
	const held = useQuery<EnrollmentToken>({
		queryKey: enrollmentKey(server.id),
		queryFn: skipToken,
		gcTime: ENROLLMENT_CACHE_MS,
	});
	// The agent connecting is what ends this card; poll in case the event
	// stream is not delivering.
	useQuery({ ...serverViewQuery(server.id), refetchInterval: 5_000 });

	const issue = useMutation({
		mutationFn: async () => {
			const { data } = await reissueEnrollmentToken({
				path: { serverId: server.id },
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
			return data;
		},
		onSuccess: (data) => cacheEnrollment(queryClient, data),
	});

	const enrollment = held.data;
	const expired = useEnrollmentExpired(enrollment);
	return (
		<div className="surface-raised flex flex-col gap-4 rounded-card p-4">
			<p className="type-body max-w-[68ch] text-ink-2">
				{server.name} is waiting for its agent. Run the install command on the
				host (it needs Docker and a root shell); this page switches to the
				server's details as soon as the agent connects.
			</p>
			{enrollment && !expired ? (
				<InstallCommandField enrollment={enrollment} />
			) : (
				<p className="text-ink-3 text-xs">
					{enrollment
						? "The last install command has expired. "
						: "An install command is shown once, when it is made, and the control plane keeps only a fingerprint of it. "}
					Make a new one to connect this host.
				</p>
			)}
			{issue.isError ? (
				<Alert variant="destructive">
					<AlertDescription>{errorMessage(issue.error)}</AlertDescription>
				</Alert>
			) : null}
			<div className="flex flex-wrap items-center gap-3">
				<Button
					variant={enrollment && !expired ? "secondary" : "primary"}
					disabled={issue.isPending}
					onClick={() => issue.mutate()}
				>
					{issue.isPending ? <Spinner data-icon="inline-start" /> : null}
					New install command
				</Button>
				{enrollment && !expired ? (
					<span className="text-ink-3 text-xs">
						A new command stops the one above from working.
					</span>
				) : null}
			</div>
		</div>
	);
}
