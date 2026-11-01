import { PencilIcon, Trash2Icon } from "lucide-react";
import { useState } from "react";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { formatBytes } from "#/lib/format";
import {
	BUILTIN_STATUS_LABEL,
	type BuiltinStatus,
	GC_LABEL,
	gcLine,
	type Registry,
	repositoryRoot,
} from "./model";
import { RegistryChecks } from "./registry-checks";
import { RegistryImages } from "./registry-images";

const BUILTIN_KIND: Record<BuiltinStatus, StatusKind> = {
	ready: "healthy",
	unavailable: "failed",
};

function Fact({
	label,
	children,
}: {
	label: string;
	children: React.ReactNode;
}) {
	return (
		<div className="flex min-w-0 flex-col gap-0.5">
			<dt className="type-micro">{label}</dt>
			<dd className="m-0 min-w-0 truncate text-ink-1 text-xs">{children}</dd>
		</div>
	);
}

/** The credentials line: what is stored, never the secret itself. */
function credentialText(registry: Registry): string {
	const push = registry.hasPushAuth
		? `push as ${registry.pushUsername}`
		: "no push credential";
	const pull = registry.hasPullAuth
		? `pull as ${registry.pullUsername}`
		: "pulls with the push credential";
	return `${push}; ${pull}`;
}

/**
 * One registry: where it is, what it holds, how to reach it, a per-server
 * test and the images pushed to it. The built-in registry is the instance's:
 * it shows whether it answers, this team's size and the last cleanup, and has
 * no address, credential, edit or delete, because the instance admin runs it.
 */
export function RegistryCard({
	registry,
	onEdit,
	onDelete,
}: {
	registry: Registry;
	onEdit: () => void;
	onDelete: () => void;
}) {
	const [showImages, setShowImages] = useState(false);
	const builtin = registry.builtin;
	const ready = builtin === null || builtin.status === "ready";

	return (
		<article
			aria-label={registry.name}
			className="surface-raised flex min-w-0 flex-col gap-4 rounded-card p-[18px]"
		>
			<header className="flex flex-wrap items-start justify-between gap-3">
				<div className="flex min-w-0 flex-col gap-1.5">
					<span className="flex flex-wrap items-center gap-2">
						<h3 className="m-0 truncate font-display font-semibold text-base text-ink-1 leading-[1.15] tracking-[-0.01em]">
							{registry.name}
						</h3>
						<Badge variant={builtin ? "accent" : "outline"}>
							{builtin ? "Built-in" : "External"}
						</Badge>
					</span>
					{builtin ? (
						<span className="text-ink-3 text-xs">
							Your team's own space on the instance's registry. The instance
							admin runs it.
						</span>
					) : (
						<span
							className="type-mono truncate text-ink-3"
							title={repositoryRoot(registry.host, registry.namespace)}
						>
							{repositoryRoot(registry.host, registry.namespace)}
						</span>
					)}
				</div>
				<div className="flex shrink-0 items-center gap-1.5">
					{registry.kind === "external" ? (
						<>
							<Button variant="ghost" size="sm" onClick={onEdit}>
								<PencilIcon aria-hidden />
								Edit
							</Button>
							<Button
								variant="ghost"
								size="sm"
								onClick={onDelete}
								disabled={registry.inUse}
								title={
									registry.inUse
										? "A team or project build setting uses this registry. Change that first."
										: undefined
								}
							>
								<Trash2Icon aria-hidden />
								Delete
							</Button>
						</>
					) : null}
				</div>
			</header>

			<dl className="m-0 grid gap-x-6 gap-y-3 sm:grid-cols-3">
				{builtin ? (
					<Fact label="Status">
						<span className="flex items-center gap-1.5">
							<StatusIcon kind={BUILTIN_KIND[builtin.status]} label="" />
							{BUILTIN_STATUS_LABEL[builtin.status]}
						</span>
					</Fact>
				) : null}
				<Fact label="Images">
					{registry.imageCount} {registry.imageCount === 1 ? "image" : "images"}
				</Fact>
				{builtin ? (
					<Fact label="Size">
						{registry.sizeBytes > 0 ? formatBytes(registry.sizeBytes) : "Empty"}
					</Fact>
				) : null}
				{builtin ? (
					<Fact label="Cleanup">
						<span title={GC_LABEL[builtin.gcState]}>{gcLine(builtin)}</span>
					</Fact>
				) : (
					<Fact label="Credentials">{credentialText(registry)}</Fact>
				)}
			</dl>

			{builtin?.status === "unavailable" && builtin.statusMessage !== "" ? (
				<p className="m-0 text-destructive-text text-xs">
					{builtin.statusMessage}
				</p>
			) : null}

			{ready ? (
				<>
					<RegistryChecks registry={registry} />
					<div className="flex flex-col border-line border-t pt-3">
						<Button
							variant="ghost"
							size="sm"
							className="self-start"
							aria-expanded={showImages}
							onClick={() => setShowImages((open) => !open)}
						>
							<DisclosureChevron open={showImages} className="size-3.5" />
							{showImages ? "Hide images" : "Show images"}
						</Button>
						<DisclosurePanel open={showImages}>
							<div className="pt-3">
								<RegistryImages registryId={registry.id} />
							</div>
						</DisclosurePanel>
					</div>
				</>
			) : (
				<p className="m-0 text-ink-3 text-xs">
					The built-in registry is not answering, so testing and images are
					unavailable. The instance admin can see why under Admin → Built-in
					services.
				</p>
			)}
		</article>
	);
}
