import { useNavigate } from "@tanstack/react-router";
import {
	ArrowUpRightIcon,
	EllipsisVerticalIcon,
	Trash2Icon,
} from "lucide-react";
import { Button } from "#/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import type { ServerView } from "./model";
import { useRemoveServer } from "./remove-server";

/**
 * The `⋯` on a server card: open the server, or remove it. Removal lives
 * here, next to the card it acts on; the same action is on the server's
 * Danger zone page. The control-plane host cannot be removed here, so its
 * card has no menu.
 */
export function ServerCardMenu({ server }: { server: ServerView }) {
	const navigate = useNavigate();
	const { hidden, removing, canClean, ask } = useRemoveServer(server);
	if (hidden) return null;
	return (
		<DropdownMenu>
			<DropdownMenuTrigger
				render={
					<Button
						variant="ghost"
						size="icon-sm"
						aria-label={`Actions for ${server.name}`}
						// Quiet until the card is looked at; always there on touch.
						className="absolute top-3 right-3 opacity-0 transition-opacity duration-quick ease-out focus-visible:opacity-100 group-focus-within/card:opacity-100 group-hover/card:opacity-100 data-popup-open:opacity-100 pointer-coarse:opacity-100"
					/>
				}
			>
				<EllipsisVerticalIcon aria-hidden />
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end" className="w-[230px]">
				<DropdownMenuItem
					render={<button type="button" />}
					onClick={() =>
						void navigate({
							to: "/servers/$serverId",
							params: { serverId: server.id },
						})
					}
				>
					<ArrowUpRightIcon aria-hidden />
					Open
				</DropdownMenuItem>
				<DropdownMenuSeparator />
				<DropdownMenuItem
					render={<button type="button" />}
					variant="destructive"
					disabled={removing}
					onClick={() => ask(canClean ? "clean" : "forget")}
				>
					<Trash2Icon aria-hidden />
					{removing ? "Removing…" : "Remove server…"}
				</DropdownMenuItem>
				{canClean && !removing ? (
					<DropdownMenuItem
						render={<button type="button" />}
						variant="destructive"
						onClick={() => ask("forget")}
					>
						<Trash2Icon aria-hidden />
						Remove without cleaning…
					</DropdownMenuItem>
				) : null}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
