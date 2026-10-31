import { useQuery } from "@tanstack/react-query";
import { NetworkIcon } from "lucide-react";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { StatusIcon } from "#/components/ui/status-icon";
import { canPickTailnetDevice } from "../lib/tailnet";
import { primaryAddress } from "../lib/tailscale";
import { serverProxyQuery, tailscaleDevicesQuery } from "../queries";

/**
 * Picks a tailnet device to fill an upstream's host. Shown only when the
 * Tailscale integration is configured and the chosen server reaches a tailnet
 * (managed node or host Tailscale); otherwise it renders nothing.
 */
export function TailnetDevicePicker({
	serverId,
	onPick,
}: {
	serverId: string;
	/** Called with the device's tailnet IPv4 address. */
	onPick(address: string): void;
}) {
	const proxy = useQuery({
		...serverProxyQuery(serverId),
		enabled: serverId !== "",
	});
	const tailnet = proxy.data?.tailnet;
	const available =
		tailnet !== undefined &&
		canPickTailnetDevice({
			integrationConfigured: tailnet.integrationConfigured,
			mode: tailnet.mode,
		});
	const devices = useQuery({ ...tailscaleDevicesQuery(), enabled: available });
	if (!available || devices.isError) return null;
	const items = (devices.data?.items ?? []).filter(
		(device) => primaryAddress(device.addresses) !== "",
	);
	if (items.length === 0 && !devices.isPending) return null;

	return (
		<Select
			value={null}
			items={items.map((device) => ({
				value: device.id,
				label: device.hostname,
			}))}
			onValueChange={(id) => {
				const device = items.find((candidate) => candidate.id === id);
				if (device) onPick(primaryAddress(device.addresses));
			}}
		>
			<SelectTrigger
				size="sm"
				className="w-[190px]"
				aria-label="Pick a tailnet device"
			>
				<NetworkIcon aria-hidden className="size-3.5 text-ink-3" />
				<SelectValue placeholder="Tailnet device" />
			</SelectTrigger>
			<SelectContent alignItemWithTrigger={false}>
				{items.map((device) => (
					<SelectItem key={device.id} value={device.id}>
						<span className="flex items-center gap-2">
							<StatusIcon kind={device.online ? "live" : "offline"} label="" />
							<span className="flex flex-col">
								<span>{device.hostname}</span>
								<span className="font-mono text-2xs text-ink-3">
									{primaryAddress(device.addresses)}
								</span>
							</span>
						</span>
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}
