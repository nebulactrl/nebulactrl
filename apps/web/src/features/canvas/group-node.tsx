import { Handle, type Node, type NodeProps, Position } from "@xyflow/react";
import { cn } from "cn";
import {
	ChevronDownIcon,
	ChevronRightIcon,
	LayersIcon,
	XIcon,
} from "lucide-react";
import { memo, useEffect, useRef, useState } from "react";
import type { CanvasGroup, Service } from "#/api/models";
import { StatusIcon } from "#/components/ui/status-icon";
import { StackActions } from "#/features/templates/stack-actions";
import { DIMMED_OPACITY, useCanvasUi } from "./canvas-context";
import { GROUP_HEADER, GROUP_SUMMARY_HEIGHT, NODE_WIDTH } from "./lib/layout";
import { type NodeStatus, nodeStatus, type StatusGroup } from "./lib/status";

export type GroupFlowNode = Node<
	{ group: CanvasGroup; members: Service[] },
	"cluster"
>;

/** The class the frame's header carries: only it drags an expanded group. */
export const GROUP_DRAG_HANDLE = "nc-group-handle";

const ROLLUP_ORDER: StatusGroup[] = [
	"failed",
	"waiting",
	"in_flight",
	"rolled_back",
	"stopped",
	"deployed",
	"not_deployed",
];

/** The most urgent status among a group's members, for its summary card. */
export function rollupStatus(
	members: readonly Service[],
): NodeStatus | undefined {
	let worst: NodeStatus | undefined;
	for (const member of members) {
		const status = nodeStatus(member);
		if (
			worst === undefined ||
			ROLLUP_ORDER.indexOf(status.group) < ROLLUP_ORDER.indexOf(worst.group)
		) {
			worst = status;
		}
	}
	return worst;
}

/**
 * A group's name. On an expanded frame it is a button that edits on a double
 * click; on a collapsed summary card (`summary`) it is plain text that keeps
 * the card draggable. Either way the context menu's Rename edits it in place.
 */
function GroupName({
	group,
	summary = false,
}: {
	group: CanvasGroup;
	summary?: boolean;
}) {
	const { renameGroup, renamingGroupId, finishRenameRequest } = useCanvasUi();
	const [editing, setEditing] = useState(false);
	const [draft, setDraft] = useState(group.name);
	const inputRef = useRef<HTMLInputElement>(null);

	useEffect(() => {
		if (!editing) return;
		inputRef.current?.focus();
		inputRef.current?.select();
	}, [editing]);

	// The context menu asked for this group's name to be edited.
	useEffect(() => {
		if (renamingGroupId !== group.id) return;
		setDraft(group.name);
		setEditing(true);
		finishRenameRequest();
	}, [renamingGroupId, group.id, group.name, finishRenameRequest]);

	const commit = () => {
		setEditing(false);
		const next = draft.trim();
		if (next && next !== group.name) renameGroup(group.id, next);
		else setDraft(group.name);
	};

	if (editing) {
		return (
			<input
				ref={inputRef}
				value={draft}
				aria-label="Group name"
				maxLength={60}
				onChange={(event) => setDraft(event.target.value)}
				onBlur={commit}
				onKeyDown={(event) => {
					event.stopPropagation();
					if (event.key === "Enter") commit();
					if (event.key === "Escape") {
						setDraft(group.name);
						setEditing(false);
					}
				}}
				className={cn(
					"nodrag min-w-0 flex-1 rounded-sm border border-border-strong bg-background px-1.5 py-0.5 text-foreground outline-none focus-visible:border-primary",
					summary
						? "w-full font-semibold text-[14px]"
						: "font-medium text-[12px]",
				)}
			/>
		);
	}
	if (summary) {
		return (
			<div
				title={group.name}
				className="truncate font-semibold text-[14px] leading-[1.2]"
			>
				{group.name}
			</div>
		);
	}
	return (
		<button
			type="button"
			title="Double-click to rename"
			onDoubleClick={() => {
				setDraft(group.name);
				setEditing(true);
			}}
			className="nodrag min-w-0 flex-1 cursor-text truncate text-left font-medium text-[12px] text-foreground-soft"
		>
			{group.name}
		</button>
	);
}

function GroupNodeImpl({ data, selected }: NodeProps<GroupFlowNode>) {
	const { group, members } = data;
	const { matchIds, toggleGroup, ungroup } = useCanvasUi();
	const dimmed =
		matchIds !== null && !members.some((member) => matchIds.has(member.id));
	const count = members.length;

	if (group.collapsed) {
		const rollup = rollupStatus(members);
		return (
			<div
				data-group-id={group.id}
				className="transition-opacity duration-quick"
				style={{ opacity: dimmed ? DIMMED_OPACITY : 1 }}
			>
				<div
					className="group/node surface-raised relative flex items-center gap-2.5 rounded-card border-group-frame! border-dashed px-3 transition-[border-color] duration-quick hover:border-line-strong!"
					style={{
						width: NODE_WIDTH,
						height: GROUP_SUMMARY_HEIGHT,
						outline: selected ? "2px solid var(--color-accent-ink)" : undefined,
						outlineOffset: selected ? 2 : undefined,
					}}
				>
					<LayersIcon
						aria-hidden
						className="size-4 shrink-0 text-foreground-soft"
					/>
					<div className="min-w-0 flex-1">
						<GroupName group={group} summary />
						<div className="mt-1 flex items-center gap-1.5 text-[12px] text-muted-foreground leading-none">
							{rollup ? <StatusIcon kind={rollup.kind} label="" /> : null}
							<span className="truncate">
								{count} {count === 1 ? "service" : "services"}
								{rollup ? ` · ${rollup.label}` : ""}
							</span>
						</div>
					</div>
					<button
						type="button"
						aria-label={`Expand ${group.name}`}
						title="Expand group"
						onClick={() => toggleGroup(group.id)}
						className="pressable nodrag grid size-6 shrink-0 cursor-pointer place-items-center rounded-sm text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:focus-ring"
					>
						<ChevronRightIcon aria-hidden className="size-3.5" />
					</button>
					<Handle
						id="left"
						type="source"
						position={Position.Left}
						className="size-2.5! border! border-line-control! bg-surface-well! opacity-0"
					/>
					<Handle
						id="right"
						type="source"
						position={Position.Right}
						className="size-2.5! border! border-line-control! bg-surface-well! opacity-0"
					/>
					<Handle
						id="top"
						type="target"
						position={Position.Top}
						isConnectable={false}
						className="size-2.5! border! border-line-control! bg-surface-well! opacity-0"
					/>
				</div>
			</div>
		);
	}

	return (
		<div
			data-group-id={group.id}
			className={cn(
				"size-full rounded-panel border-[1.5px] border-group-frame border-dashed bg-group-fill transition-opacity duration-quick",
				selected && "border-accent-ink",
			)}
			style={{ opacity: dimmed ? DIMMED_OPACITY : 1 }}
		>
			<div
				className={cn(
					GROUP_DRAG_HANDLE,
					"flex cursor-grab items-center gap-1.5 px-3",
				)}
				style={{ height: GROUP_HEADER }}
			>
				<button
					type="button"
					aria-label={`Collapse ${group.name}`}
					title="Collapse group"
					onClick={() => toggleGroup(group.id)}
					className="pressable nodrag grid size-6 shrink-0 cursor-pointer place-items-center rounded-sm text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:focus-ring"
				>
					<ChevronDownIcon aria-hidden className="size-3.5" />
				</button>
				<GroupName group={group} />
				<span className="shrink-0 font-mono text-[11px] text-subtle">
					{count}
				</span>
				<StackActions
					groupId={group.id}
					groupName={group.name}
					members={members}
				/>
				<button
					type="button"
					aria-label={`Ungroup ${group.name}`}
					title="Ungroup (keeps the services)"
					onClick={() => ungroup(group.id)}
					className="pressable nodrag grid size-6 shrink-0 cursor-pointer place-items-center rounded-sm text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:focus-ring"
				>
					<XIcon aria-hidden className="size-3.5" />
				</button>
			</div>
			<Handle
				type="target"
				position={Position.Left}
				isConnectable={false}
				className="opacity-0"
			/>
			<Handle
				type="source"
				position={Position.Right}
				isConnectable={false}
				className="opacity-0"
			/>
		</div>
	);
}

export const GroupNode = memo(
	GroupNodeImpl,
	(prev, next) =>
		prev.data.group === next.data.group &&
		prev.data.members === next.data.members &&
		prev.selected === next.selected,
);
