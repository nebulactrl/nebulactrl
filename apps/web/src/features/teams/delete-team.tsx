import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import {
	getTeamDeletionPlanOptions,
	listTeamsQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import { deleteTeam } from "#/api/gen/sdk.gen";
import type { Team, TeamDeletionPlan } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { SettingsPlate } from "#/features/settings/section";
import { deletionStepLabel } from "#/features/threshold/model";

function plural(n: number, one: string, many = `${one}s`) {
	return `${n} ${n === 1 ? one : many}`;
}

/** The lines of the plan that are not zero, in plain words. */
function planLines(p: TeamDeletionPlan): string[] {
	const lines: string[] = [];
	const add = (n: number, text: string) => n > 0 && lines.push(text);
	add(p.projects, `${plural(p.projects, "project")}`);
	add(p.services, `${plural(p.services, "service")}`);
	add(p.databases, `${plural(p.databases, "database")}`);
	add(p.volumes, `${plural(p.volumes, "volume")} with their data`);
	add(
		p.servers.online,
		`${plural(p.servers.online, "online server")}: the agent empties the host and removes itself`,
	);
	add(
		p.servers.offline,
		`${plural(p.servers.offline, "offline server")}: removed without touching the host, which keeps its containers`,
	);
	add(p.domains, `${plural(p.domains, "domain")}`);
	add(p.statusPages, `${plural(p.statusPages, "status page")}`);
	add(
		p.managedDnsRecords,
		`${plural(p.managedDnsRecords, "DNS record")} managed in Cloudflare`,
	);
	add(
		p.tailnetDevices,
		`${plural(p.tailnetDevices, "tailnet device")} NebulaCtrl created`,
	);
	return lines;
}

/**
 * The team's danger zone: the plan of what deleting removes, a typed confirm
 * with the choice to keep the DNS records, then the progress of the job (the
 * team shows "Deleting…" until it is gone) or its failure with a retry.
 */
export function DeleteTeamDangerZone({ team }: { team: Team }) {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const [keepDns, setKeepDns] = useState(false);
	const deletion = team.deletion;
	const plan = useQuery({
		...getTeamDeletionPlanOptions({ path: { teamId: team.id } }),
		enabled: deletion?.state !== "deleting",
	});

	const start = useMutation({
		mutationFn: () =>
			deleteTeam({
				path: { teamId: team.id },
				query: { confirm: team.slug, keepDnsRecords: keepDns },
			}),
		onSuccess: () =>
			queryClient.invalidateQueries({ queryKey: listTeamsQueryKey() }),
	});

	const ask = () => {
		const p = plan.data;
		const lines = p ? planLines(p) : [];
		void confirm({
			title: `Delete ${team.name}?`,
			body: (
				<div className="flex flex-col gap-2">
					{lines.length > 0 ? (
						<>
							<p>This deletes, for every member, and cannot be undone:</p>
							<ul className="list-disc pl-5">
								{lines.map((l) => (
									<li key={l}>{l}</li>
								))}
								<li>every token, integration, secret and the audit log</li>
							</ul>
						</>
					) : (
						<p>
							This deletes the team for every member and cannot be undone: every
							token, integration, secret and the audit log.
						</p>
					)}
					<p>
						Backups already written to backup destinations are kept. Backups
						stored on a server stay at /var/lib/nebulactrl-backups on its host
						(or in the backup directory you configured outside
						/var/lib/nebulactrl-agent).
						{p?.hostsControlPlane
							? " This team owns the server NebulaCtrl runs on, so it cannot be deleted."
							: ""}
					</p>
					{p && p.managedDnsRecords > 0 ? (
						<label className="flex items-center gap-2">
							<input
								type="checkbox"
								checked={keepDns}
								onChange={(e) => setKeepDns(e.target.checked)}
							/>
							Keep the DNS records in Cloudflare
						</label>
					) : null}
				</div>
			),
			cta: "Delete team",
			danger: true,
			typeToConfirm: team.slug,
			blocked: plan.isPending || plan.data?.hostsControlPlane === true,
			onConfirm: async () => {
				await start.mutateAsync();
			},
		});
	};

	return (
		<SettingsPlate id="delete-team" title="Delete this team" tone="danger">
			<div className="flex flex-wrap items-center justify-between gap-4">
				<p className="min-w-0 flex-1 text-pretty text-ink-3 text-xs leading-relaxed">
					Deletes {team.name} with everything in it. You will be asked to type
					the team's slug.
				</p>
				{deletion?.state === "deleting" ? (
					<span className="inline-flex items-center gap-2 text-ink-2 text-sm">
						<Spinner /> Deleting…
					</span>
				) : (
					<Button
						variant="danger"
						onClick={ask}
						disabled={plan.isPending && deletion?.state !== "failed"}
					>
						{deletion?.state === "failed" ? "Retry deletion" : "Delete team"}
					</Button>
				)}
			</div>

			{deletion?.state === "deleting" ? (
				<p className="text-ink-3 text-xs" role="status">
					{deletionStepLabel(deletion.step)}. Agents can take a few minutes to
					empty their hosts. You can leave this page.
				</p>
			) : null}
			{deletion?.state === "failed" ? (
				<Alert variant="destructive">
					<AlertDescription>
						The deletion stopped: {deletion.error}. Nothing was undone; delete
						again to retry.
					</AlertDescription>
				</Alert>
			) : null}
			{deletion?.forgottenServers && deletion.forgottenServers.length > 0 ? (
				<p className="text-ink-3 text-xs">
					Removed without touching their hosts (clean them up yourself):{" "}
					{(deletion.forgottenServers ?? []).join(", ")}.
				</p>
			) : null}
			{!deletion && plan.isPending ? <Skeleton className="h-4 w-1/2" /> : null}
		</SettingsPlate>
	);
}
