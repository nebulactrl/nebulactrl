import { ServerMeshCard } from "#/features/mesh/server-mesh-card";
import {
	type Bodies,
	Body,
	usePages,
} from "#/features/settings/settings-areas";
import { SettingsShell } from "#/features/settings/settings-shell";
import { CleanupSection } from "./cleanup-section";
import { DangerZone } from "./danger-zone";
import { HostShellCard } from "./host-shell-card";
import { LogSettingsSection } from "./log-settings-section";
import type { ServerView } from "./model";
import { ServerSettingsForm } from "./server-settings-form";

/**
 * A server's Settings tab: the shared settings layout, one page per section,
 * inside the server page's own frame. What each role may open is the nav
 * model's; a page they cannot use says why instead of showing a form.
 */
export function ServerSettings({
	server,
	section,
}: {
	server: ServerView;
	section: string;
}) {
	const { pages, usable } = usePages("server", section);
	const id = server.id;

	const bodies: Bodies = {
		general: () => (
			<ServerSettingsForm
				key={`general-${id}`}
				server={server}
				part="general"
			/>
		),
		scheduling: () => (
			<ServerSettingsForm
				key={`scheduling-${id}`}
				server={server}
				part="scheduling"
			/>
		),
		monitoring: () => (
			<ServerSettingsForm
				key={`monitoring-${id}`}
				server={server}
				part="monitoring"
			/>
		),
		"logs-and-disk": () => (
			<>
				<LogSettingsSection key={`logs-${id}`} server={server} />
				<CleanupSection key={`cleanup-${id}`} server={server} />
			</>
		),
		mesh: () => <ServerMeshCard key={`mesh-${id}`} server={server} />,
		"host-shell": () => <HostShellCard key={`shell-${id}`} server={server} />,
		danger: () => <DangerZone key={`danger-${id}`} server={server} />,
	};

	return (
		<SettingsShell
			area="server"
			title="Server settings"
			scopeLine={`Server · ${server.name} · applies to this server only`}
			pages={pages}
			active={section}
			serverId={id}
			embedded
		>
			<Body bodies={bodies} section={section} usable={usable} />
		</SettingsShell>
	);
}
