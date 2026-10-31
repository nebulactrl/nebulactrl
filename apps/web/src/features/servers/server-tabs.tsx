import { Link } from "@tanstack/react-router";
import { Badge } from "#/components/ui/badge";
import { NavTab, NavTabs } from "#/components/ui/nav-tabs";
import type { ServerTab } from "./tabs-model";

/**
 * The server page's tabs: the host overview, its traffic, its reverse proxy,
 * its security, its OS updates and its settings. Each is a link to its own
 * address, so a tab can be opened in a new window and shared. They are a nav
 * of links with `aria-current="page"`, not an ARIA tablist, because every one
 * is a page of its own and none has a panel in this one.
 */
export function ServerTabs({
	serverId,
	value,
	staged,
	updatesBadge,
	showSettings,
}: {
	serverId: string;
	value: ServerTab;
	/** Server-level proxy changes wait for a deploy. */
	staged: boolean;
	/** A short word for the Updates tab when something needs attention ("Reboot", "2 security"). */
	updatesBadge?: string | null;
	/** Only owners and admins have settings to open. */
	showSettings: boolean;
}) {
	const params = { serverId };
	return (
		<NavTabs aria-label="Server sections">
			<NavTab
				active={value === "overview"}
				// Every other tab lives under this address, so only an exact match
				// is Overview: the router must not call it current on Traffic.
				render={
					<Link
						to="/servers/$serverId"
						params={params}
						activeOptions={{ exact: true }}
					/>
				}
			>
				Overview
			</NavTab>
			<NavTab
				active={value === "traffic"}
				render={<Link to="/servers/$serverId/traffic" params={params} />}
			>
				Traffic
			</NavTab>
			<NavTab
				active={value === "proxy"}
				render={<Link to="/servers/$serverId/proxy" params={params} />}
			>
				Proxy
				{staged ? (
					<Badge variant="accent" shape="capsule">
						Staged
					</Badge>
				) : null}
			</NavTab>
			<NavTab
				active={value === "security"}
				render={<Link to="/servers/$serverId/security" params={params} />}
			>
				Security
			</NavTab>
			<NavTab
				active={value === "updates"}
				render={<Link to="/servers/$serverId/updates" params={params} />}
			>
				Updates
				{updatesBadge ? (
					<Badge variant="accent" shape="capsule">
						{updatesBadge}
					</Badge>
				) : null}
			</NavTab>
			{showSettings ? (
				<NavTab
					active={value === "settings"}
					render={<Link to="/servers/$serverId/settings" params={params} />}
				>
					Settings
				</NavTab>
			) : null}
		</NavTabs>
	);
}
