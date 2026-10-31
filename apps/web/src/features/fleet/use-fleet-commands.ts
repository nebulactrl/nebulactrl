import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ArrowLeftRightIcon, ServerIcon } from "lucide-react";
import { useMemo } from "react";
import { serversQuery } from "#/api/hooks";
import {
	type PaletteCommand,
	useRegisterCommands,
} from "#/components/shell/palette-registry";
import { canStartDrain } from "./model";

/** ⌘K entries for the fleet: open it, and drain a server (opens its drain sheet). */
export function useFleetCommands() {
	const navigate = useNavigate();
	const servers = useQuery(serversQuery());
	const list = servers.data;

	const commands = useMemo<PaletteCommand[]>(() => {
		const entries: PaletteCommand[] = [
			{
				id: "go:fleet",
				label: "Open fleet",
				group: "Go to",
				hint: "capacity and placement",
				icon: ServerIcon,
				keywords: ["capacity", "servers", "memory", "overview", "placement"],
				run: () => void navigate({ to: "/servers", search: { view: "fleet" } }),
			},
		];
		for (const server of list ?? []) {
			if (!canStartDrain(server)) continue;
			entries.push({
				id: `drain:${server.id}`,
				label: `Drain ${server.name}`,
				group: "Action",
				hint: "move its services elsewhere",
				icon: ArrowLeftRightIcon,
				keywords: ["evacuate", "maintenance", "move", "server"],
				searchOnly: true,
				run: () =>
					void navigate({
						to: "/servers/$serverId",
						params: { serverId: server.id },
						search: { drain: true },
					}),
			});
		}
		return entries;
	}, [list, navigate]);

	useRegisterCommands("fleet", commands);
}
