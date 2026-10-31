import { useQuery } from "@tanstack/react-query";
import { ExternalLinkIcon } from "lucide-react";
import { errorMessage } from "#/api/client-config";
import { listGitConnectionsOptions } from "#/api/gen/@tanstack/react-query.gen";
import type { Environment } from "#/api/models";
import {
	Alert,
	AlertAction,
	AlertDescription,
	AlertTitle,
} from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { configBanner, fileUrl } from "./model";
import { openProjectConfig } from "./open";
import { useCheckBinding, useEnvironmentConfigSource } from "./queries";

function Banner({ environment }: { environment: Environment }) {
	const source = useEnvironmentConfigSource(environment.id);
	const check = useCheckBinding(environment.id);
	const banner = configBanner(source.data);
	const connections = useQuery({
		...listGitConnectionsOptions(),
		enabled: banner?.kind === "invalid",
	});
	if (!banner || !source.data) return null;

	const connection = connections.data?.items?.find(
		(candidate) => candidate.id === source.data?.connectionId,
	);
	const link =
		banner.kind === "invalid" && connection
			? fileUrl({
					provider: connection.provider,
					baseUrl: connection.baseUrl,
					repo: source.data.repo ?? "",
					ref: source.data.commit ?? "",
					path: source.data.resolvedPath || source.data.path || "",
					line: banner.line,
				})
			: undefined;

	const checkAgain = () =>
		check.mutate(undefined, {
			onError: (error) =>
				toast.error("Could not check nebula.toml", errorMessage(error)),
		});

	return (
		<div className="pointer-events-none absolute inset-x-0 top-3 z-20 flex justify-center px-3">
			<Alert
				variant={banner.kind === "invalid" ? "destructive" : "warning"}
				aria-label="nebula.toml"
				className="pointer-events-auto max-w-[620px] sm:pr-3 sm:has-data-[slot=alert-action]:pr-3"
			>
				<AlertTitle>{banner.title}</AlertTitle>
				<AlertDescription>
					{banner.kind === "differs" ? (
						<>
							The file in git and this environment disagree. Review what it
							would change; nothing is applied until you stage it.
						</>
					) : (
						<>
							<span className="font-mono">
								{banner.detail ?? "The file cannot be applied."}
							</span>{" "}
							Nothing was changed. Fix the file and push again.
						</>
					)}
				</AlertDescription>
				<AlertAction className="flex items-center gap-1.5 sm:static sm:translate-y-0">
					{banner.kind === "differs" ? (
						<Button
							size="sm"
							variant="secondary"
							onClick={() => openProjectConfig("review")}
						>
							Review
						</Button>
					) : (
						<>
							{link ? (
								<Button
									size="sm"
									variant="secondary"
									render={
										<a href={link} target="_blank" rel="noopener noreferrer" />
									}
								>
									{banner.line ? `View line ${banner.line}` : "View file"}
									<ExternalLinkIcon aria-hidden />
								</Button>
							) : null}
							<Button
								size="sm"
								variant="ghost"
								disabled={check.isPending}
								onClick={checkAgain}
							>
								{check.isPending ? <Spinner /> : null}
								Check again
							</Button>
						</>
					)}
				</AlertAction>
			</Alert>
		</div>
	);
}

/**
 * Above the canvas: "nebula.toml @3f9c2a1 differs — 3 services" with Review,
 * or the first error of an invalid file with a link to its line. An in-sync
 * file, no file, or an environment that is not linked shows nothing.
 */
export function EnvironmentConfigBanner({
	environment,
}: {
	environment: Environment;
}) {
	if (environment.kind === "preview") return null;
	return <Banner environment={environment} />;
}
