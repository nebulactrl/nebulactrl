import { useQuery } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import {
	type LucideIcon,
	ServerIcon,
	Settings2Icon,
	ShieldIcon,
	SlidersHorizontalIcon,
	UserIcon,
} from "lucide-react";
import { useMemo } from "react";
import { meQuery } from "#/api/hooks";
import type { SettingsArea } from "#/features/settings/nav-model";
import { isRole } from "#/features/teams/roles";
import { type PaletteCommand, useRegisterCommands } from "./palette-registry";
import { settingsCommandEntries } from "./settings-commands";
import { useProjectContext } from "./use-project-context";

const AREA_ICON: Record<SettingsArea, LucideIcon> = {
	account: UserIcon,
	team: Settings2Icon,
	admin: ShieldIcon,
	project: SlidersHorizontalIcon,
	server: ServerIcon,
};

/** ⌘K: one command per settings page the signed-in person can use. */
export function useSettingsCommands() {
	const router = useRouter();
	const me = useQuery(meQuery());
	const projectId = useProjectContext()?.projectId;
	const instanceAdmin = me.data?.instanceAdmin === true;
	const principalRole = me.data?.principal.role;
	const role = isRole(principalRole) ? principalRole : undefined;

	const commands = useMemo<PaletteCommand[]>(
		() =>
			me.data === undefined
				? []
				: settingsCommandEntries({ instanceAdmin }, role, projectId).map(
						(entry) => ({
							id: entry.id,
							label: entry.label,
							group: "Settings",
							// Findable by typing, never in the empty prompt: twenty-odd
							// pages would bury where to go and what to do.
							searchOnly: true,
							hint: entry.hint,
							keywords: entry.keywords,
							icon: AREA_ICON[entry.area],
							run: () => router.history.push(entry.href),
						}),
					),
		[instanceAdmin, me.data, projectId, role, router],
	);

	useRegisterCommands("shell.settings", commands);
}
