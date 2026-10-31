import { useQuery } from "@tanstack/react-query";
import { LayersIcon } from "lucide-react";
import { useProjectContext } from "#/components/shell/use-project-context";
import { instanceOfService } from "./lib";
import { catalogQuery, instancesQuery } from "./queries";
import { templatesActions } from "./store";

/**
 * "From template Umami": a service has no template field of its own, so the
 * environment's stack list says which stack (if any) lists it. Renders
 * nothing for a service that was not installed from a template.
 */
export function TemplateOrigin({ serviceId }: { serviceId: string }) {
	const environmentId = useProjectContext()?.environment?.id;
	const instances = useQuery({
		...instancesQuery(environmentId ?? ""),
		enabled: environmentId !== undefined,
	});
	const instance = instanceOfService(instances.data ?? [], serviceId);
	const catalog = useQuery({
		...catalogQuery(),
		enabled: instance !== undefined,
	});
	if (!instance) return null;
	const name =
		catalog.data?.items.find((item) => item.id === instance.templateId)?.name ??
		instance.templateId;
	return (
		<p
			data-slot="template-origin"
			className="flex items-center gap-2 text-[12px] text-ink-3"
		>
			<LayersIcon aria-hidden className="size-3.5 shrink-0" />
			<span className="min-w-0 truncate">
				From template <span className="font-medium text-ink-2">{name}</span>
				<span className="font-mono tabular-nums">
					{" "}
					· stack {instance.name} · revision {instance.templateRevision}
					{instance.templateVersion ? ` · ${instance.templateVersion}` : ""}
				</span>
			</span>
			{instance.updateAvailable ? (
				<button
					type="button"
					onClick={() => templatesActions.requestUpdate(instance)}
					className="shrink-0 cursor-pointer rounded-xs px-1.5 py-0.5 font-medium text-accent-ink text-2xs transition-[background-color] duration-100 hover:bg-white/8 focus-visible:focus-ring"
				>
					Update available
				</button>
			) : null}
		</p>
	);
}
