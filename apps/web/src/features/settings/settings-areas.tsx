import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { meQuery } from "#/api/hooks";
import { DestinationsSection } from "#/features/backups/destinations-section";
import { TeamBuildsSection } from "#/features/build-placement/team-builds-section";
import { DashboardAccessSection } from "#/features/dashboard-access/dashboard-access-section";
import { InstanceBackupSection } from "#/features/instance-backup/instance-backup-section";
import { InstanceServicesSection } from "#/features/instance-services/instance-services-section";
import { LogDrainsSection } from "#/features/log-drains/drains-section";
import { MeshSection } from "#/features/mesh/mesh-section";
import { NetworksSection } from "#/features/networks";
import { NotificationsSection } from "#/features/notifications/notifications-section";
import { PlatformServicesSection } from "#/features/platform/platform-services-section";
import { PlatformSetupHost } from "#/features/platform/setup-host";
import { RegistriesSection } from "#/features/registries/registries-section";
import { GeneralSection } from "#/features/teams/general-section";
import {
	MailRelaySection,
	SignInSection,
} from "#/features/teams/instance-section";
import { MembersSection } from "#/features/teams/members-section";
import { useCurrentTeam } from "#/features/teams/queries";
import { isRole, type Role } from "#/features/teams/roles";
import { AuditSection } from "./audit-section";
import { CreateTokenDialog } from "./create-token-dialog";
import { IntegrationsSection } from "./integrations/integrations-section";
import { McpClientsCard } from "./mcp-card";
import { McpSwitchCard } from "./mcp-switch";
import {
	findPage,
	pagesFor,
	type SettingsArea,
	type SettingsViewer,
	TEAM_MANAGE_SENTENCE,
} from "./nav-model";
import { ProfileSection } from "./profile-section";
import { SettingsNotice } from "./section";
import { SecuritySection } from "./security/security-section";
import { SettingsShell } from "./settings-shell";
import type { PresetId } from "./token-presets";
import { TokensSection } from "./tokens-section";
import { UpdateCard } from "./update-card";

/** Who is looking: whether they run the instance, and their role in this team. */
function useViewer(): {
	viewer: SettingsViewer;
	role: Role | undefined;
	email: string;
} {
	const me = useQuery(meQuery());
	const role = me.data?.principal.role;
	return {
		viewer: { instanceAdmin: me.data?.instanceAdmin === true },
		role: isRole(role) ? role : undefined,
		email: me.data?.email ?? "",
	};
}

/**
 * The pages the person can use for an area, and what stands in for the open
 * page when it is one they cannot (a developer who typed an admin address).
 */
export function usePages(area: SettingsArea, section: string) {
	const { viewer, role, email } = useViewer();
	const pages = pagesFor(area, viewer, role);
	const page = findPage(area, section);
	const usable = page !== undefined && pages.includes(page);
	return { pages, usable, role, email };
}

export type Bodies = Record<string, () => ReactNode>;

export function Body({
	bodies,
	section,
	usable,
}: {
	bodies: Bodies;
	section: string;
	usable: boolean;
}) {
	if (!usable) return <SettingsNotice>{TEAM_MANAGE_SENTENCE}</SettingsNotice>;
	return <>{bodies[section]?.()}</>;
}

/** Account settings: you, in every team. */
export function AccountSettings({ section }: { section: string }) {
	const { pages, usable, email } = usePages("account", section);
	const [tokenOpen, setTokenOpen] = useState(false);
	const [preset, setPreset] = useState<PresetId>("read-only");
	const openTokenDialog = (next: PresetId) => {
		setPreset(next);
		setTokenOpen(true);
	};

	const bodies: Bodies = {
		profile: () => <ProfileSection />,
		security: () => <SecuritySection />,
		tokens: () => (
			<TokensSection onCreate={() => openTokenDialog("read-only")} />
		),
		"mcp-clients": () => (
			<McpClientsCard onCreateToken={() => openTokenDialog("mcp-read")} />
		),
	};

	return (
		<SettingsShell
			area="account"
			title="Account settings"
			scopeLine={
				email === ""
					? "Account · applies to you in every team"
					: `Account · ${email} · applies to you in every team`
			}
			pages={pages}
			active={section}
		>
			<Body bodies={bodies} section={section} usable={usable} />
			<CreateTokenDialog
				open={tokenOpen}
				onOpenChange={setTokenOpen}
				initialPreset={preset}
			/>
		</SettingsShell>
	);
}

/** Team settings: this team, for everyone in it. */
export function TeamSettings({ section }: { section: string }) {
	const { pages, usable } = usePages("team", section);
	const { team } = useCurrentTeam();

	const bodies: Bodies = {
		general: () => <GeneralSection />,
		members: () => <MembersSection />,
		"audit-log": () => <AuditSection />,
		"build-location": () => <TeamBuildsSection />,
		"image-registries": () => <RegistriesSection />,
		integrations: () => <IntegrationsSection />,
		"private-networks": () => <NetworksSection />,
		"server-mesh": () => <MeshSection />,
		headscale: () => <PlatformServicesSection />,
		"backup-storage": () => <DestinationsSection />,
		notifications: () => <NotificationsSection />,
		"log-forwarding": () => <LogDrainsSection />,
	};

	return (
		<SettingsShell
			area="team"
			title="Team settings"
			scopeLine={
				team
					? `Team · ${team.name} · applies to everyone in it`
					: "Team · applies to everyone in it"
			}
			pages={pages}
			active={section}
		>
			<Body bodies={bodies} section={section} usable={usable} />
			<PlatformSetupHost />
		</SettingsShell>
	);
}

/** Admin: this installation. The route admits only the instance admin. */
export function AdminSettings({ section }: { section: string }) {
	const { pages, usable } = usePages("admin", section);

	const bodies: Bodies = {
		updates: () => <UpdateCard />,
		backup: () => <InstanceBackupSection />,
		"built-in-services": () => <InstanceServicesSection />,
		"sign-in": () => <SignInSection />,
		"dashboard-access": () => <DashboardAccessSection />,
		"email-relay": () => <MailRelaySection />,
		mcp: () => <McpSwitchCard />,
	};

	return (
		<SettingsShell
			area="admin"
			title="Admin"
			scopeLine="Instance · applies to every team on this installation"
			pages={pages}
			active={section}
		>
			<Body bodies={bodies} section={section} usable={usable} />
		</SettingsShell>
	);
}
