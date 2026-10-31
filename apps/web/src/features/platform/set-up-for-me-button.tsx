import { useQuery } from "@tanstack/react-query";
import { WandSparklesIcon } from "lucide-react";
import { serversQuery } from "#/api/hooks";
import { Button } from "#/components/ui/button";
import { availability, kindEntry } from "./model";
import { platformServicesQuery, usableServers } from "./queries";
import { platformSetup } from "./store";

/**
 * A "Set up for me" button for Headscale, next to the manual connect form of
 * the Tailscale integration. It opens the shared set-up dialog (mounted once
 * by the settings shell). It is absent when Headscale already exists and
 * while the catalog has not loaded; it is disabled, with the reason, when no
 * server is online.
 */
export function SetUpHeadscaleButton({
	label,
	variant = "secondary",
	size,
}: {
	label: string;
	variant?: "primary" | "secondary" | "ghost";
	size?: "sm" | "default";
}) {
	const listing = useQuery(platformServicesQuery());
	const servers = useQuery(serversQuery());
	const entry = kindEntry(listing.data, "headscale");
	if (!listing.data || !entry) return null;
	const state = availability(entry, {
		// Until the servers load, do not flash a disabled button.
		usableServers: servers.data ? usableServers(servers.data).length : 1,
	});
	if (state.state === "exists") return null;
	const reason = state.state === "available" ? undefined : state.reason;
	return (
		<Button
			type="button"
			variant={variant}
			{...(size ? { size } : {})}
			disabled={reason !== undefined}
			title={reason ?? entry.summary}
			onClick={() => platformSetup.open()}
		>
			<WandSparklesIcon aria-hidden />
			{label}
		</Button>
	);
}
