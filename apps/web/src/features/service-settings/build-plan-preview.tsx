import { useMutation } from "@tanstack/react-query";
import { errorMessage } from "#/api/client-config";
import { previewBuildPlan } from "#/api/gen/sdk.gen";
import type { BuildPlanResult } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { SettingRow } from "./fields";

const METHOD_LABEL: Record<string, string> = {
	dockerfile: "Dockerfile",
	railpack: "Railpack",
	starter: "Starter Dockerfile",
};

/**
 * "Preview build plan": the server that will build the service clones the
 * branch head and says how it would be built (nothing is built). It uses the
 * saved configuration, so unsaved edits ask to be saved first.
 */
export function BuildPlanPreview({
	service,
	dirty,
}: {
	service: Service;
	dirty: boolean;
}) {
	const preview = useMutation({
		mutationFn: async (): Promise<BuildPlanResult> => {
			const { data } = await previewBuildPlan({
				path: { serviceId: service.id },
				throwOnError: true,
			});
			return data;
		},
	});
	const noServer = !service.serverId;
	const blocked = noServer
		? "Assign the service to a server first: the plan is made on the server that will build it."
		: dirty
			? "Save your changes first: the preview uses the saved settings."
			: null;

	return (
		<SettingRow
			label="Build plan"
			description="Clones the branch head on the build server and shows what would be built. Nothing is deployed."
		>
			<div className="flex flex-col gap-2">
				<div className="flex items-center gap-2">
					<Button
						type="button"
						variant="secondary"
						size="md"
						disabled={blocked !== null || preview.isPending}
						onClick={() => preview.mutate()}
					>
						{preview.isPending ? <Spinner aria-hidden /> : null}
						{preview.isPending ? "Planning…" : "Preview build plan"}
					</Button>
					{blocked ? <span className="type-secondary">{blocked}</span> : null}
				</div>
				<div aria-live="polite" className="flex flex-col gap-2">
					{preview.isError ? (
						<Alert variant="destructive">
							<AlertDescription>{errorMessage(preview.error)}</AlertDescription>
						</Alert>
					) : null}
					{preview.data ? <PlanResult plan={preview.data} /> : null}
				</div>
			</div>
		</SettingRow>
	);
}

function PlanResult({ plan }: { plan: BuildPlanResult }) {
	const info = plan.info;
	return (
		<div className="flex flex-col gap-2 rounded-card border border-line bg-surface-well px-3 py-2.5 text-xs leading-[1.5]">
			{info ? (
				<>
					<div className="flex flex-wrap items-center gap-1.5">
						<span className="font-medium text-ink-1 text-sm">
							{METHOD_LABEL[info.method] ?? info.method}
						</span>
						{info.railpackVersion ? (
							<Badge>v{info.railpackVersion}</Badge>
						) : null}
						{info.starter ? <Badge>{info.starter}</Badge> : null}
						{(info.providers ?? []).map((provider) => (
							<Badge key={provider} variant="accent">
								{provider}
							</Badge>
						))}
					</div>
					{info.reason ? (
						<span className="text-ink-3">{info.reason}</span>
					) : null}
				</>
			) : null}
			{plan.outcome !== "succeeded" && plan.message ? (
				<Alert variant="destructive">
					<AlertDescription>{plan.message}</AlertDescription>
				</Alert>
			) : null}
			{info?.startCommand ? (
				<div className="flex flex-col gap-0.5">
					<span className="type-micro">Start command</span>
					<code className="font-mono text-ink-1">{info.startCommand}</code>
				</div>
			) : null}
			{(info?.packages ?? []).length > 0 ? (
				<div className="flex flex-col gap-0.5">
					<span className="type-micro">Toolchain</span>
					<span className="font-mono text-ink-2">
						{(info?.packages ?? [])
							.map((pkg) => `${pkg.name} ${pkg.version}`)
							.join(" · ")}
					</span>
				</div>
			) : null}
			{(info?.steps ?? []).length > 0 ? (
				<details>
					<summary className="cursor-pointer text-ink-3">
						{(info?.steps ?? []).length} build steps
					</summary>
					<ol className="mt-1 flex list-decimal flex-col gap-1 pl-4">
						{(info?.steps ?? []).map((step) => (
							<li key={step.name}>
								<span className="font-mono text-ink-1">{step.name}</span>
								{(step.commands ?? []).map((command) => (
									<code key={command} className="block font-mono text-ink-3">
										$ {command}
									</code>
								))}
							</li>
						))}
					</ol>
				</details>
			) : null}
			{(plan.messages ?? []).map((message) => (
				<span
					key={`${message.level}:${message.msg}`}
					className={
						message.level === "warn" ? "text-warning-ink" : "text-ink-3"
					}
				>
					{message.level === "warn" ? "Warning: " : ""}
					{message.msg}
				</span>
			))}
			{!plan.dockerfileFound ? (
				<span className="text-ink-3">
					No <code className="font-mono">{plan.dockerfilePath}</code> in the
					repository.
					{plan.suggestedStarter
						? ` It looks like a ${plan.suggestedStarter} project; the "${plan.suggestedStarter}" starter Dockerfile is an alternative to Railpack.`
						: ""}
				</span>
			) : null}
			{plan.commitSha ? (
				<span className="text-ink-3">
					Planned at{" "}
					<code className="font-mono">{plan.commitSha.slice(0, 7)}</code>
				</span>
			) : null}
		</div>
	);
}
