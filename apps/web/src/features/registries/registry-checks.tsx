import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FlaskConicalIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { serversQuery } from "#/api/hooks";
import { Relative } from "#/components/common/relative";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { checkRegistry } from "./api";
import {
	checkableServers,
	checkKind,
	checkSummary,
	type Registry,
	type RegistryCheck,
} from "./model";
import { invalidateRegistries } from "./queries";

/**
 * "Test registry": pick a server and ask its agent to log in to the registry,
 * push and pull a probe. A registry is used from two places, a build server
 * (push) and the server that runs the app (pull), so any online server can be
 * tested, build servers first. The last result per server stays on the card.
 */
export function RegistryChecks({ registry }: { registry: Registry }) {
	const queryClient = useQueryClient();
	const servers = useQuery(serversQuery());
	const options = checkableServers(servers.data ?? []);
	const names = new Map((servers.data ?? []).map((s) => [s.id, s.name]));

	const [picked, setPicked] = useState<string | null>(null);
	const serverId =
		picked !== null && options.some((s) => s.id === picked)
			? picked
			: (options[0]?.id ?? "");

	const test = useMutation({
		mutationFn: () => checkRegistry(registry.id, serverId),
		onSettled: () => invalidateRegistries(queryClient),
	});

	const results = Object.entries(registry.lastCheck)
		.map(([id, check]) => ({
			id,
			name: names.get(id) ?? "Removed server",
			check,
		}))
		.sort((a, b) => b.check.at.localeCompare(a.check.at));

	return (
		<div className="flex flex-col gap-2.5">
			<div className="flex flex-wrap items-center gap-2">
				<Select
					value={serverId}
					items={options.map((s) => ({ value: s.id, label: s.name }))}
					onValueChange={(next) => setPicked(String(next ?? ""))}
				>
					<SelectTrigger
						aria-label="Server to test from"
						className="w-[220px]"
						disabled={options.length === 0}
					>
						<SelectValue
							placeholder={
								servers.isPending ? "Loading servers…" : "No online server"
							}
						/>
					</SelectTrigger>
					<SelectContent alignItemWithTrigger={false}>
						{options.map((s) => (
							<SelectItem key={s.id} value={s.id}>
								<span className="flex items-center gap-2">
									{s.name}
									{s.buildEnabled ? (
										<span className="text-2xs text-ink-4">build server</span>
									) : null}
								</span>
							</SelectItem>
						))}
					</SelectContent>
				</Select>
				<Button
					variant="secondary"
					disabled={serverId === "" || test.isPending}
					onClick={() => test.mutate()}
				>
					{test.isPending ? <Spinner /> : <FlaskConicalIcon aria-hidden />}
					Test registry
				</Button>
			</div>

			{test.isError ? (
				<Alert variant="destructive">
					<AlertTitle>The test did not run</AlertTitle>
					<AlertDescription>{errorMessage(test.error)}</AlertDescription>
				</Alert>
			) : null}

			{results.length === 0 ? (
				<p className="m-0 text-ink-3 text-xs">
					Not tested yet. A test logs in from the server you pick, then pushes
					and pulls a tiny probe image.
				</p>
			) : (
				<ul className="m-0 flex list-none flex-col gap-1.5 p-0">
					{results.map(({ id, name, check }) => (
						<CheckRow key={id} name={name} check={check} />
					))}
				</ul>
			)}
		</div>
	);
}

function CheckRow({ name, check }: { name: string; check: RegistryCheck }) {
	return (
		<li className="flex min-w-0 flex-col gap-0.5">
			<span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-ink-2 text-xs">
				<StatusIcon kind={checkKind(check)} label="" />
				<span className="text-ink-1">{name}</span>
				<span>{checkSummary(check)}</span>
				<span className="text-ink-4">
					· <Relative iso={check.at} />
				</span>
			</span>
			{!check.ok &&
			check.error !== "" &&
			check.error !== checkSummary(check) ? (
				<span
					className="truncate pl-[22px] text-destructive-text text-xs"
					title={check.error}
				>
					{check.error}
				</span>
			) : null}
		</li>
	);
}
