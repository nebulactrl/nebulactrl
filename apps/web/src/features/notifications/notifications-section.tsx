import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BellIcon, PlusIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { listNotificationChannelsOptions } from "#/api/gen/@tanstack/react-query.gen";
import {
	deleteNotificationChannel,
	rotateNotificationChannelSecret,
	testNotificationChannel,
	updateNotificationChannel,
} from "#/api/gen/sdk.gen";
import type { ChannelView } from "#/api/gen/types.gen";
import { projectsQuery } from "#/api/hooks";
import { useConfirm } from "#/components/common/confirm-dialog";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { AlertSettings } from "./alert-settings";
import { ChannelCard } from "./channel-card";
import { ChannelDialog } from "./channel-dialog";
import { DeliveriesDialog } from "./deliveries-dialog";
import { invalidateNotifications } from "./queries";
import { SigningSecretPanel } from "./signing-secret";

/**
 * Team settings → Notifications: channels (email, Slack, Discord, Telegram, signed
 * webhook), each with its own rule for which events it receives, a test send
 * and a delivery log.
 */
export function NotificationsSection() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const channels = useQuery(listNotificationChannelsOptions());
	const projects = useQuery(projectsQuery());
	const projectNames = new Map(
		(projects.data ?? []).map((p) => [p.id, p.name]),
	);

	const [creating, setCreating] = useState(false);
	const [editing, setEditing] = useState<ChannelView | null>(null);
	const [logOf, setLogOf] = useState<ChannelView | null>(null);
	const [rotated, setRotated] = useState<{
		name: string;
		secret: string;
	} | null>(null);
	const [testingId, setTestingId] = useState<string | null>(null);

	const toggle = useMutation({
		mutationFn: async ({
			channel,
			enabled,
		}: {
			channel: ChannelView;
			enabled: boolean;
		}) => {
			await updateNotificationChannel({
				path: { channelId: channel.id },
				body: { enabled },
			});
			return { channel, enabled };
		},
		onSuccess: async ({ channel, enabled }) => {
			toast.success(
				enabled ? `Resumed ${channel.name}` : `Paused ${channel.name}`,
				enabled
					? "Matching events are sent again."
					: "Nothing is sent until you resume it.",
			);
			await invalidateNotifications(queryClient);
		},
		onError: (e) =>
			toast.error("Could not change the channel", errorMessage(e)),
	});

	const test = useMutation({
		mutationFn: async (channel: ChannelView) => {
			setTestingId(channel.id);
			const { data } = await testNotificationChannel({
				path: { channelId: channel.id },
			});
			return { channel, delivery: data };
		},
		onSuccess: async ({ channel, delivery }) => {
			if (delivery.status === "delivered") {
				toast.success(
					`Test sent to ${channel.name}`,
					delivery.responseCode
						? `The receiver answered ${delivery.responseCode}.`
						: "The message was accepted.",
				);
			} else {
				toast.error(`Test to ${channel.name} failed`, delivery.lastError);
			}
			await invalidateNotifications(queryClient);
		},
		onError: (e) => toast.error("Could not send the test", errorMessage(e)),
		onSettled: () => setTestingId(null),
	});

	const rotate = useMutation({
		mutationFn: async (channel: ChannelView) => {
			const { data } = await rotateNotificationChannelSecret({
				path: { channelId: channel.id },
			});
			return { channel, secret: data.signingSecret };
		},
		onSuccess: async ({ channel, secret }) => {
			setRotated({ name: channel.name, secret });
			await invalidateNotifications(queryClient);
		},
		onError: (e) => toast.error("Could not rotate the secret", errorMessage(e)),
	});

	const remove = useMutation({
		mutationFn: async (channel: ChannelView) => {
			await deleteNotificationChannel({ path: { channelId: channel.id } });
			return channel;
		},
		onSuccess: async (channel) => {
			toast.success(
				`Removed ${channel.name}`,
				"Its delivery log went with it.",
			);
			await invalidateNotifications(queryClient);
		},
		onError: (e) =>
			toast.error("Could not remove the channel", errorMessage(e)),
	});

	if (channels.isPending) return <Skeleton className="h-40 rounded-card" />;
	if (channels.isError && !channels.data) {
		return (
			<ErrorState
				frame="raised"
				title="Notification channels did not load"
				body="Reading them needs the admin scope. Check your session and try again; nothing was changed."
				retry={() => void channels.refetch()}
			/>
		);
	}
	const items = channels.data?.items ?? [];
	const add = (
		<Button variant="primary" onClick={() => setCreating(true)}>
			<PlusIcon />
			Add channel
		</Button>
	);

	return (
		<div className="flex flex-col gap-4">
			{items.length === 0 ? (
				<EmptyState
					illustration="bell"
					title="Get told when something breaks"
					body="Add a channel (email, Slack, Discord, Telegram or a signed webhook) and choose which events it receives."
					action={add}
				/>
			) : (
				<>
					<div className="flex flex-wrap items-center justify-between gap-3">
						<p className="flex items-center gap-2 text-ink-3 text-xs">
							<BellIcon aria-hidden="true" className="size-4" />
							{items.length} {items.length === 1 ? "channel" : "channels"}. A
							failing channel never blocks a deployment.
						</p>
						{add}
					</div>
					<div className="grid gap-3 xl:grid-cols-2">
						{items.map((channel) => (
							<ChannelCard
								key={channel.id}
								channel={channel}
								projectNames={projectNames}
								testing={testingId === channel.id}
								onToggle={(enabled) => toggle.mutate({ channel, enabled })}
								onTest={() => test.mutate(channel)}
								onLog={() => setLogOf(channel)}
								onEdit={() => setEditing(channel)}
								onRotate={() =>
									void confirm({
										title: `Rotate the signing secret of ${channel.name}?`,
										body: "Deliveries are signed with the new secret right away. Update your receiver first or its checks will fail until you do.",
										cta: "Rotate secret",
										danger: false,
										hold: false,
										onConfirm: async () => {
											await rotate.mutateAsync(channel);
										},
									})
								}
								onRemove={() =>
									void confirm({
										title: `Remove ${channel.name}?`,
										body: "It stops receiving notifications and its delivery log is deleted.",
										cta: "Remove channel",
										danger: true,
										hold: false,
										onConfirm: async () => {
											await remove.mutateAsync(channel);
										},
									})
								}
							/>
						))}
					</div>
				</>
			)}
			<AlertSettings />
			<ChannelDialog
				open={creating}
				onOpenChange={setCreating}
				channel={null}
			/>
			<ChannelDialog
				open={editing !== null}
				onOpenChange={(open) => (open ? null : setEditing(null))}
				channel={editing}
			/>
			<DeliveriesDialog channel={logOf} onClose={() => setLogOf(null)} />
			<Dialog
				open={rotated !== null}
				onOpenChange={(open) => (open ? null : setRotated(null))}
			>
				<DialogContent size="lg">
					<DialogHeader>
						<DialogTitle>New signing secret for {rotated?.name}</DialogTitle>
						<DialogDescription>
							The old secret no longer signs deliveries.
						</DialogDescription>
					</DialogHeader>
					<DialogBody>
						{rotated ? <SigningSecretPanel secret={rotated.secret} /> : null}
					</DialogBody>
					<DialogFooter>
						<Button variant="primary" onClick={() => setRotated(null)}>
							I have copied it
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</div>
	);
}
