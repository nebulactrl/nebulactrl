import { useEffect, useMemo, useState } from "react";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import {
	type TeamService,
	useTeamServices,
} from "#/features/log-drains/use-team-services";
import { count } from "#/lib/format";
import { ServiceChecklist } from "./service-checklist";

/**
 * Pick services from every project and environment of the team to show on the
 * page. Services already on the page are not offered. Nothing changes until
 * Add: the editor's own Save then sends the whole list.
 */
export function ServicePickerDialog({
	open,
	onOpenChange,
	taken,
	room,
	onAdd,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** Service ids already on the page. */
	taken: ReadonlySet<string>;
	/** How many more components the page can hold. */
	room: number;
	onAdd: (services: TeamService[]) => void;
}) {
	const team = useTeamServices(open);
	const [picked, setPicked] = useState<string[]>([]);

	useEffect(() => {
		if (open) setPicked([]);
	}, [open]);

	const chosen = useMemo(
		() => team.services.filter((service) => picked.includes(service.id)),
		[team.services, picked],
	);
	const full = picked.length >= room;

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				<DialogHeader>
					<DialogTitle>Add services to the page</DialogTitle>
					<DialogDescription>
						Visitors see each one under the name you give it here, never the
						service's own name, project or environment.
					</DialogDescription>
				</DialogHeader>
				<DialogBody>
					<ServiceChecklist
						label="Services to add"
						services={team.services}
						pending={team.pending}
						exclude={taken}
						selected={picked}
						maxHeight="max-h-80"
						onToggle={(service, on) =>
							setPicked((current) =>
								on
									? current.includes(service.id) || current.length >= room
										? current
										: [...current, service.id]
									: current.filter((id) => id !== service.id),
							)
						}
					/>
					<p role="status" className="m-0 text-ink-3 text-xs">
						{room === 0
							? "The page already shows the most it can, 100 components."
							: full
								? `The page has room for ${count(room, "more component")}; that is all of them.`
								: picked.length === 0
									? "Tick the services to show."
									: `${count(picked.length, "service")} selected.`}
					</p>
				</DialogBody>
				<DialogFooter>
					<Button
						type="button"
						variant="ghost"
						onClick={() => onOpenChange(false)}
					>
						Cancel
					</Button>
					<Button
						type="button"
						variant={picked.length === 0 ? "secondary" : "primary"}
						disabled={picked.length === 0}
						onClick={() => {
							onAdd(chosen);
							onOpenChange(false);
						}}
					>
						{picked.length === 0
							? "Add services"
							: `Add ${count(picked.length, "service")}`}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
