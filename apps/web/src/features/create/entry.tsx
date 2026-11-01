import {
	ServiceIcon,
	type ServiceIconSource,
} from "#/features/canvas/service-icon";
import { ComposeMark } from "#/features/compose-import/compose-mark";
import { composeImportActions } from "#/features/compose-import/store";
import { CoolifyMark } from "#/features/coolify-import/coolify-mark";
import { coolifyImportActions } from "#/features/coolify-import/store";
import { routeCreate } from "#/features/proxy/create-store";
import { createStackActions } from "#/features/stacks/store";
import { templatesActions } from "#/features/templates/store";
import { TemplatesMark } from "#/features/templates/template-icon";
import type { MenuEntryId } from "./lib";
import { type CreateOptions, createActions } from "./store";

type FixedMark = "templates" | "compose" | "coolify";

/** What each entry's mark is drawn from: the same icon its service will wear. */
const ENTRY_ICON: Record<Exclude<MenuEntryId, FixedMark>, ServiceIconSource> = {
	git: { sourceType: "git", config: { gitUrl: "https://github.com/" } },
	image: { sourceType: "image", config: { image: "docker" } },
	database: { kind: "database" },
	external: { kind: "route", config: { route: { protocol: "http" } } },
	port: { kind: "route", config: { route: { protocol: "tcp" } } },
	stack: { kind: "stack", sourceType: "compose" },
};

/** An entry's mark; `engine` draws a database engine's own. */
export function EntryMark({
	id,
	engine,
	size,
}: {
	id: MenuEntryId;
	engine?: string | undefined;
	size: number;
}) {
	if (engine) {
		return (
			<ServiceIcon
				service={{ kind: "database", config: { database: { engine } } }}
				size={size}
			/>
		);
	}
	switch (id) {
		case "templates":
			return <TemplatesMark size={size} />;
		case "compose":
			return <ComposeMark size={size} />;
		case "coolify":
			return <CoolifyMark size={size} />;
		default:
			return <ServiceIcon service={ENTRY_ICON[id]} size={size} />;
	}
}

/**
 * Opens the flow behind an entry: the service dialog for a git repository, an
 * image or a database (`options.engine` preselects one), or the entry's own
 * dialog. `options.position` drops the new node there when the flow places
 * one (the canvas menu passes the pointer). Closes the Create menu.
 */
export function openCreateEntry(id: MenuEntryId, options: CreateOptions = {}) {
	switch (id) {
		case "git":
		case "image":
		case "database":
			createActions.open(id, options);
			return;
		case "external":
		case "port":
			createActions.setMenuOpen(false);
			routeCreate.open({
				mode: id,
				...(options.position ? { position: options.position } : {}),
			});
			return;
		case "stack":
			createActions.setMenuOpen(false);
			createStackActions.open(options.position);
			return;
		case "templates":
			createActions.setMenuOpen(false);
			templatesActions.open();
			return;
		case "compose":
			createActions.setMenuOpen(false);
			composeImportActions.open();
			return;
		case "coolify":
			createActions.setMenuOpen(false);
			coolifyImportActions.open();
			return;
	}
}
