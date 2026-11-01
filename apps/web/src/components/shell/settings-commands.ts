import {
	SETTINGS_PAGES,
	type SettingsArea,
	type SettingsViewer,
	settingsHref,
} from "#/features/settings/nav-model";
import type { Role } from "#/features/teams/roles";

/** One ⌘K row for one settings page, before it gets its icon and its action. */
export interface SettingsCommandEntry {
	id: string;
	label: string;
	hint: string;
	keywords: readonly string[];
	area: SettingsArea;
	href: string;
}

const AREA_LABEL: Record<SettingsArea, string> = {
	account: "Account settings",
	team: "Team settings",
	admin: "Admin",
	project: "Project settings",
	server: "Server settings",
};

/**
 * A command for every settings page this person can use: account, team,
 * admin (the instance admin only) and, when a project is open, that
 * project's. The palette is the settings search, so each page is findable by
 * its title, its area and its keywords ("smtp", "s3", "sso"). A server's pages
 * are not here: they belong to one server, and the palette has no server open.
 */
export function settingsCommandEntries(
	viewer: SettingsViewer,
	role: Role | undefined,
	projectId?: string,
): SettingsCommandEntry[] {
	const entries: SettingsCommandEntry[] = [];
	for (const page of SETTINGS_PAGES) {
		if (page.area === "project" && !projectId) continue;
		if (page.area === "server") continue;
		if (!page.visible(viewer, role)) continue;
		entries.push({
			id: `settings:${page.area}:${page.section}`,
			label: `${AREA_LABEL[page.area]}: ${page.title}`,
			hint: page.description,
			keywords: [...page.keywords, "settings"],
			area: page.area,
			href: settingsHref(
				page.area,
				page.section,
				projectId ? { projectId } : undefined,
			),
		});
	}
	return entries;
}
