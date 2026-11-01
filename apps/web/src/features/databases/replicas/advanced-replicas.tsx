import { useId, useState } from "react";
import { errorCode } from "#/api/client-config";
import type { Service } from "#/api/models";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { PANEL_TITLE, shouldOpenAdvanced } from "./model";
import { useReplication } from "./queries";
import { ReplicasPanel } from "./replicas-panel";

/**
 * The standby replica panel, tucked into a collapsed "Advanced" disclosure of
 * a managed Postgres database: it is an option for a few setups, not part of
 * every database's first screen. It starts open when the database already has
 * replicas, is one, is a fenced old primary or has a promote running, so
 * existing state is never hidden. Once the reader toggles it, their choice
 * stands. The caller mounts it for Postgres only.
 */
export function AdvancedReplicas({
	service,
	deployed,
}: {
	service: Service;
	deployed: boolean;
}) {
	const id = useId();
	const view = useReplication(service.id);
	const [chosen, setChosen] = useState<boolean | null>(null);

	// An older control plane has no replica API: nothing to tuck away.
	if (view.isError && errorCode(view.error) === "not_found") return null;

	const open = chosen ?? shouldOpenAdvanced(view.data);
	return (
		<div className="surface-raised rounded-card">
			<button
				type="button"
				aria-expanded={open}
				aria-controls={id}
				onClick={() => setChosen(!open)}
				className="flex w-full cursor-pointer items-center gap-2 rounded-card px-3 py-2.5 text-left text-[13px] text-ink-1 outline-none focus-visible:focus-ring"
			>
				<DisclosureChevron
					open={open}
					className="size-3.5 shrink-0 text-ink-3"
				/>
				<span className="min-w-0 font-medium">Advanced</span>
				<span className="min-w-0 truncate text-ink-3 text-xs">
					{PANEL_TITLE}
				</span>
				<span className="ml-auto shrink-0 text-ink-3 text-xs">
					{open ? "Hide" : "Show"}
				</span>
			</button>
			<DisclosurePanel open={open} id={id}>
				<div className="border-line-faint border-t px-3 py-3">
					<ReplicasPanel service={service} deployed={deployed} />
				</div>
			</DisclosurePanel>
		</div>
	);
}
