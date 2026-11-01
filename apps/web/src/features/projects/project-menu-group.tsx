import { useNavigate } from "@tanstack/react-router";
import { CopyIcon, LayersIcon, PencilIcon } from "lucide-react";
import type { Project } from "#/api/models";
import { copyWithToast } from "#/components/common/copy-button";
import {
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
} from "#/components/ui/dropdown-menu";

/**
 * What a project's `⋯` menu on the canvas can do to the whole project, as
 * its own group below the current environment's. Rename only asks: its dialog
 * belongs to the caller, outside the menu that closes. Project settings and
 * the shared variables are in the sidebar, and Delete project is in Project
 * settings → Danger zone only: a destructive row two lines under Redeploy is
 * one slip from the wrong action.
 */
export function ProjectMenuGroup({
	project,
	onRename,
}: {
	project: Pick<Project, "id" | "name">;
	onRename(): void;
}) {
	const navigate = useNavigate();
	return (
		<DropdownMenuGroup>
			<DropdownMenuLabel>Project</DropdownMenuLabel>
			<DropdownMenuItem render={<button type="button" />} onClick={onRename}>
				<PencilIcon aria-hidden />
				Rename project…
			</DropdownMenuItem>
			<DropdownMenuItem
				render={<button type="button" />}
				onClick={() => void copyWithToast(project.id)}
			>
				<CopyIcon aria-hidden />
				Copy project ID
			</DropdownMenuItem>
			<DropdownMenuItem
				render={<button type="button" />}
				onClick={() =>
					void navigate({
						to: "/p/$projectId/settings",
						params: { projectId: project.id },
						hash: "environments",
					})
				}
			>
				<LayersIcon aria-hidden />
				Environments
			</DropdownMenuItem>
		</DropdownMenuGroup>
	);
}
