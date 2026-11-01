import { useQuery } from "@tanstack/react-query";
import {
	ArrowUpRightIcon,
	ChevronsDownUpIcon,
	ChevronsUpDownIcon,
	CombineIcon,
	KeyboardIcon,
	KeyRoundIcon,
	LayersIcon,
	type LucideIcon,
	MaximizeIcon,
	NetworkIcon,
	PanelRightOpenIcon,
	PencilIcon,
	PlusIcon,
	RefreshCwIcon,
	RocketIcon,
	ScalingIcon,
	ScanSearchIcon,
	SearchIcon,
	SquareDashedMousePointerIcon,
	Trash2Icon,
	Unlink2Icon,
	UnlinkIcon,
	XIcon,
} from "lucide-react";
import { Fragment, type ReactNode } from "react";
import type { CanvasGroup, Point, Service } from "#/api/models";
import {
	ContextMenuContent,
	ContextMenuItem,
	ContextMenuLabel,
	ContextMenuSeparator,
	ContextMenuShortcut,
} from "#/components/motion/context-menu";
import { EntryMark } from "#/features/create/entry";
import type { MenuEntryId } from "#/features/create/lib";
import { instanceOfGroup } from "#/features/templates/lib";
import { instancesQuery } from "#/features/templates/queries";
import { templatesActions } from "#/features/templates/store";
import {
	type EdgeMenuInput,
	edgeMenu,
	groupMenu,
	type InternetMenuInput,
	internetMenu,
	type MenuAction,
	type MenuIconName,
	type MenuItem,
	type MenuModel,
	paneMenu,
	type ServiceMenuInput,
	selectionMenu,
	serviceMenu,
} from "./lib/menu";
import { useServiceActions } from "./use-service-actions";

const ICONS: Record<
	Exclude<MenuIconName, `create-${MenuEntryId}`>,
	LucideIcon
> = {
	open: PanelRightOpenIcon,
	deploy: RocketIcon,
	scale: ScalingIcon,
	arrange: NetworkIcon,
	fit: MaximizeIcon,
	search: SearchIcon,
	shortcuts: KeyboardIcon,
	external: ArrowUpRightIcon,
	variables: KeyRoundIcon,
	references: Unlink2Icon,
	unlink: UnlinkIcon,
	plus: PlusIcon,
	rename: PencilIcon,
	collapse: ChevronsDownUpIcon,
	expand: ChevronsUpDownIcon,
	zoom: ScanSearchIcon,
	ungroup: LayersIcon,
	group: SquareDashedMousePointerIcon,
	combine: CombineIcon,
	restart: RefreshCwIcon,
	clear: XIcon,
	trash: Trash2Icon,
};

/** A Create row wears the mark of the entry it opens, as in the Create menu. */
function createEntryOf(name: MenuIconName): MenuEntryId | null {
	return name.startsWith("create-")
		? (name.slice("create-".length) as MenuEntryId)
		: null;
}

/** What a right-click landed on, with the data its menu is drawn from. */
export type MenuTarget =
	| {
			kind: "service";
			service: Service;
			/** The cables the card ends (a cable itself is no tab stop). */
			links: ServiceMenuInput["links"];
	  }
	| { kind: "pane"; at: Point | null }
	| { kind: "internet"; model: InternetMenuInput }
	| { kind: "edge"; model: EdgeMenuInput }
	| { kind: "group"; group: CanvasGroup; memberCount: number }
	| { kind: "selection"; services: readonly Service[] };

export interface CanvasMenuProps {
	target: MenuTarget;
	isProduction: boolean;
	environmentId: string;
	/** The canvas has no services: Arrange, Fit and Search have nothing to act on. */
	empty: boolean;
	/** Runs a row of any menu but the card's own deploy and More actions. */
	onAction(action: MenuAction): void;
}

function Rows({
	model,
	run,
	iconOf,
}: {
	model: MenuModel;
	run(action: MenuAction): void;
	/** A row whose glyph the caller owns (the card's More actions). */
	iconOf?: (item: MenuItem) => LucideIcon | undefined;
}) {
	return model.sections.map((section, index) => (
		// biome-ignore lint/suspicious/noArrayIndexKey: sections are positional and static per menu.
		<Fragment key={index}>
			{index > 0 ? <ContextMenuSeparator /> : null}
			{section.label ? (
				<ContextMenuLabel>{section.label}</ContextMenuLabel>
			) : null}
			{section.items.map((item) => {
				const Own = iconOf?.(item);
				const entry = item.icon ? createEntryOf(item.icon) : null;
				let icon: ReactNode = null;
				if (Own) icon = <Own aria-hidden className="size-3.5" />;
				else if (entry) icon = <EntryMark id={entry} size={16} />;
				else if (item.icon) {
					const Glyph =
						ICONS[item.icon as Exclude<MenuIconName, `create-${MenuEntryId}`>];
					icon = <Glyph aria-hidden className="size-3.5" />;
				}
				return (
					<ContextMenuItem
						key={item.key}
						onSelect={() => run(item.action)}
						disabled={item.disabled ?? false}
						textValue={item.label}
						{...(item.tone ? { tone: item.tone } : {})}
					>
						{icon}
						{item.label}
						{item.shortcut ? (
							<ContextMenuShortcut>{item.shortcut}</ContextMenuShortcut>
						) : null}
					</ContextMenuItem>
				);
			})}
			{section.note ? (
				<p className="px-2.5 pb-1.5 text-[11px] text-ink-3 leading-[1.4]">
					{section.note}
				</p>
			) : null}
		</Fragment>
	));
}

function Menu({
	model,
	run,
	iconOf,
}: {
	model: MenuModel;
	run(action: MenuAction): void;
	iconOf?: (item: MenuItem) => LucideIcon | undefined;
}) {
	return (
		<ContextMenuContent
			ariaLabel={model.ariaLabel}
			{...(model.title ? { title: model.title } : {})}
		>
			<Rows model={model} run={run} {...(iconOf ? { iconOf } : {})} />
		</ContextMenuContent>
	);
}

/**
 * A card's menu: the dock's own rows plus the rest of More. The requests come
 * from `useServiceActions`, so this menu and the dock cannot drift apart.
 */
function ServiceMenu({
	service,
	links,
	isProduction,
	onAction,
}: Pick<CanvasMenuProps, "isProduction" | "onAction"> & {
	service: Service;
	links: ServiceMenuInput["links"];
}) {
	const actions = useServiceActions(service, {
		isProduction,
		onDelete: () =>
			onAction({
				type: "open-service",
				serviceId: service.id,
				section: "settings",
				settingsSection: "danger",
			}),
	});
	const model = serviceMenu({
		service,
		actions: actions.menu,
		busy: actions.busy,
		...(links ? { links } : {}),
	});
	const run = (action: MenuAction) => {
		if (action.type === "service-deploy") actions.deploy();
		else if (action.type === "service-action") {
			actions.menu.find((row) => row.id === action.id)?.run();
		} else onAction(action);
	};
	const iconOf = (item: MenuItem) => {
		const { action } = item;
		if (action.type !== "service-action") return undefined;
		return actions.menu.find((row) => row.id === action.id)?.icon;
	};
	return <Menu model={model} run={run} iconOf={iconOf} />;
}

/** A group's menu; a template stack also offers deleting it as a whole. */
function GroupMenu({
	group,
	memberCount,
	environmentId,
	onAction,
}: Pick<CanvasMenuProps, "environmentId" | "onAction"> & {
	group: CanvasGroup;
	memberCount: number;
}) {
	const instances = useQuery({
		...instancesQuery(environmentId),
		enabled: environmentId !== "",
	});
	const instance = instanceOfGroup(instances.data ?? [], group.id);
	const model = groupMenu({
		groupId: group.id,
		name: group.name,
		collapsed: group.collapsed,
		memberCount,
		isStack: instance !== undefined,
	});
	const run = (action: MenuAction) => {
		if (action.type === "delete-stack") {
			if (instance) templatesActions.requestDelete(instance);
		} else onAction(action);
	};
	return <Menu model={model} run={run} />;
}

/**
 * The right-click menu. Every canvas surface has one: a card (the dock's
 * actions plus More), the empty pane (Create at the cursor and the canvas
 * actions), the Internet node, a cable, a group and a selection of cards.
 * The rows come from the pure builders in `lib/menu.ts`.
 */
export function CanvasMenu(props: CanvasMenuProps) {
	const { target, onAction } = props;
	switch (target.kind) {
		case "service":
			return (
				<ServiceMenu
					service={target.service}
					links={target.links}
					isProduction={props.isProduction}
					onAction={onAction}
				/>
			);
		case "pane":
			return (
				<Menu
					model={paneMenu({ at: target.at, empty: props.empty })}
					run={onAction}
				/>
			);
		case "internet":
			return <Menu model={internetMenu(target.model)} run={onAction} />;
		case "edge":
			return <Menu model={edgeMenu(target.model)} run={onAction} />;
		case "group":
			return (
				<GroupMenu
					group={target.group}
					memberCount={target.memberCount}
					environmentId={props.environmentId}
					onAction={onAction}
				/>
			);
		case "selection":
			return (
				<Menu
					model={selectionMenu({
						services: target.services,
						isProduction: props.isProduction,
					})}
					run={onAction}
				/>
			);
	}
}
