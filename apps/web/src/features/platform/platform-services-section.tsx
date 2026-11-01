import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { errorMessage, errorStatus } from "#/api/client-config";
import { serversQuery } from "#/api/hooks";
import { ErrorState } from "#/components/common/error-state";
import { Loading } from "#/components/common/loading";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { kindEntry } from "./model";
import { platformServicesQuery, usableServers } from "./queries";
import { PlatformServiceCard } from "./service-card";

/**
 * Team settings → Private network (Headscale): the tailnet control server
 * NebulaCtrl runs for this team. It is the one service that stays per team,
 * because a team is its own tailnet. Backup storage, the image registry and
 * central logs are instance services, managed by the instance admin; this
 * team's slice of them shows on Backup storage, Image registries and Log
 * forwarding.
 */
export function PlatformServicesSection() {
	const listing = useQuery(platformServicesQuery());
	const servers = useQuery(serversQuery());

	if (listing.isPending) return <Loading label="Loading Headscale" />;
	if (listing.isError || !listing.data) {
		const forbidden = errorStatus(listing.error) === 403;
		return (
			<ErrorState
				frame="raised"
				title={
					forbidden
						? "Only administrators can manage Headscale"
						: "Headscale did not load"
				}
				body={
					forbidden
						? "Ask a team owner or admin to set it up."
						: `${errorMessage(listing.error).replace(/\.?$/, ".")} Nothing was changed.`
				}
				{...(forbidden ? {} : { retry: () => void listing.refetch() })}
			/>
		);
	}

	const data = listing.data;
	const catalog = kindEntry(data, "headscale");
	const count = servers.data ? usableServers(servers.data).length : 1;

	return (
		<div className="flex flex-col gap-4">
			<Alert variant="info">
				<AlertDescription>
					Headscale runs as an ordinary service in a project called NebulaCtrl
					platform, on a server you choose.{" "}
					{data.projectId ? (
						<Link
							to="/p/$projectId/$envSlug"
							params={{ projectId: data.projectId, envSlug: "platform" }}
							className="text-accent-ink underline"
						>
							Open the platform project
						</Link>
					) : null}
				</AlertDescription>
			</Alert>
			{catalog ? (
				<PlatformServiceCard catalog={catalog} usableServers={count} />
			) : null}
		</div>
	);
}
