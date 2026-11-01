import type { ReactNode } from "react";
import { ApiError } from "#/api/client-config";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Badge } from "#/components/ui/badge";
import { SectionLabel } from "#/components/ui/section-label";
import { Skeleton } from "#/components/ui/skeleton";
import { absoluteTime } from "#/lib/format";
import { JsonView } from "./json-view";
import type { DebugRelease } from "./queries";

function Facts({ children }: { children: ReactNode }) {
	return (
		<dl className="grid grid-cols-[132px_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-xs">
			{children}
		</dl>
	);
}

function Fact({
	label,
	value,
	mono = true,
}: {
	label: string;
	value: ReactNode;
	mono?: boolean;
}) {
	if (value === null || value === undefined || value === "") return null;
	return (
		<>
			<dt className="text-ink-3">{label}</dt>
			<dd
				className={
					mono
						? "min-w-0 break-all font-mono text-ink-1"
						: "min-w-0 break-words text-ink-1"
				}
			>
				{value}
			</dd>
		</>
	);
}

function Block({ title, children }: { title: string; children: ReactNode }) {
	return (
		<section className="flex flex-col gap-2.5">
			<SectionLabel>{title}</SectionLabel>
			{children}
		</section>
	);
}

/**
 * The release a deployment shipped: what was resolved (image and digest, or
 * the commit), how it was built and where it ran, the configuration frozen
 * into it (secrets masked by the control plane) and the keys of its variables.
 * Never a variable value.
 */
export function ReleaseTab({
	release,
	pending,
	error,
	onRetry,
}: {
	release: DebugRelease | undefined;
	pending: boolean;
	error: unknown;
	onRetry(): void;
}) {
	if (pending) {
		return (
			<div className="flex flex-col gap-3 p-5" aria-busy>
				{["a", "b", "c", "d"].map((key) => (
					<Skeleton key={key} className="h-5 w-full" />
				))}
			</div>
		);
	}
	if (error instanceof ApiError && error.status === 404) {
		return (
			<EmptyState
				illustration="box"
				title="No release yet"
				body="The release is written when the deployment is prepared. This one has none: it may have been canceled while queued."
				frame="none"
				className="p-5"
			/>
		);
	}
	if (error || release === undefined) {
		return (
			<ErrorState
				title="The release did not load"
				body="Nothing was changed. Try again."
				retry={onRetry}
				className="p-5"
			/>
		);
	}

	const { source, build, placement } = release;
	return (
		<div className="flex min-w-0 flex-col gap-6 p-5">
			<Block title="Release">
				<Facts>
					<Fact label="Release" value={release.id} />
					<Fact label="Generation" value={release.generation} />
					<Fact label="Created" value={absoluteTime(release.createdAt)} />
					<Fact label="Image reference" value={release.imageRef} />
				</Facts>
			</Block>

			<Block title="Source">
				<Facts>
					<Fact label="Type" value={source.type} />
					<Fact label="Image" value={source.image} />
					<Fact label="Digest" value={source.digest} />
					<Fact label="Git URL" value={source.gitUrl} />
					<Fact label="Branch" value={source.branch} />
					<Fact label="Commit" value={source.commitSha} />
					<Fact label="Image ID" value={source.imageId} />
					<Fact label="Build hash" value={source.buildHash} />
				</Facts>
			</Block>

			{build ? (
				<Block title="Build">
					<Facts>
						<Fact label="Method" value={build.method} />
						<Fact label="Builder" value={build.builder} />
						<Fact label="Dockerfile" value={build.dockerfile} />
						<Fact label="Why" value={build.reason} mono={false} />
						<Fact label="Start command" value={build.startCommand} />
						<Fact label="Providers" value={build.providers?.join(", ")} />
						<Fact
							label="Packages"
							value={build.packages
								?.map((p) => `${p.name} ${p.version}`)
								.join(", ")}
						/>
					</Facts>
					{build.steps && build.steps.length > 0 ? (
						<ol className="flex flex-col gap-1 font-mono text-[11.5px] text-ink-2">
							{build.steps.map((step, index) => (
								<li
									key={`${step.name}:${(step.commands ?? []).join("|")}`}
									className="flex gap-3"
								>
									<span className="text-ink-4 tabular-nums">{index + 1}</span>
									<span className="flex min-w-0 flex-col gap-0.5">
										<span className="break-all">{step.name}</span>
										{(step.commands ?? []).map((command) => (
											<span key={command} className="break-all text-ink-3">
												{command}
											</span>
										))}
									</span>
								</li>
							))}
						</ol>
					) : null}
				</Block>
			) : null}

			<Block title="Placement">
				<Facts>
					<Fact label="Strategy" value={placement.strategy} />
					<Fact
						label="Target server"
						value={placement.targetServerName}
						mono={false}
					/>
					<Fact
						label="Build server"
						value={placement.buildServerName}
						mono={false}
					/>
					<Fact label="Registry ref" value={placement.registryRef} />
					<Fact label="Platform" value={placement.platform} />
				</Facts>
			</Block>

			<Block title="Configuration snapshot">
				<p className="text-ink-3 text-xs">
					As frozen into the release. Secrets and file contents are masked.
				</p>
				<JsonView
					label="Configuration snapshot as JSON"
					value={release.config}
				/>
			</Block>

			<Block title={`Variables (${release.variables.length})`}>
				{release.variablesPruned ? (
					<p className="text-ink-3 text-xs">
						This release left the rollback window, so its environment was
						dropped and its keys are no longer known.
					</p>
				) : release.variables.length === 0 ? (
					<p className="text-ink-3 text-xs">The release has no variables.</p>
				) : (
					<>
						<p className="text-ink-3 text-xs">
							Keys only: a value is never shown here.
						</p>
						<ul className="flex flex-col">
							{release.variables.map((variable) => (
								<li
									key={variable.key}
									className="flex flex-wrap items-center gap-2 border-line-faint border-b py-1.5 last:border-b-0"
								>
									<span className="min-w-0 break-all font-mono text-ink-1 text-xs">
										{variable.key}
									</span>
									{variable.secret ? (
										<Badge variant="outline" shape="tag">
											secret
										</Badge>
									) : null}
									{variable.reference ? (
										<Badge variant="outline" shape="tag">
											reference
										</Badge>
									) : null}
									{variable.platform ? (
										<Badge variant="outline" shape="tag">
											platform
										</Badge>
									) : null}
									{variable.external ? (
										<Badge variant="outline" shape="tag">
											secret manager
										</Badge>
									) : null}
									{variable.scope === "runtime" ? null : (
										<Badge variant="outline" shape="tag">
											{variable.scope === "build"
												? "build only"
												: "build and runtime"}
										</Badge>
									)}
								</li>
							))}
						</ul>
					</>
				)}
			</Block>
		</div>
	);
}
