import { useQuery } from "@tanstack/react-query";
import { DatabaseIcon } from "lucide-react";
import type { Spec } from "#/api/gen/types.gen";
import { flavorOf } from "./engines";
import { enginesQuery } from "./queries";

/**
 * The source line of a managed database's canvas card: the engine's name and
 * a version chip (with the flavor), where other cards show their image. The
 * label comes from the cached catalog; before it loads the engine id stands in.
 */
export function DatabaseLine({ spec }: { spec: Spec }) {
	const catalog = useQuery(enginesQuery());
	const engine = catalog.data?.engines?.find(
		(entry) => entry.id === spec.engine,
	);
	const label = engine?.label ?? spec.engine;
	const flavor = engine ? flavorOf(engine, spec.flavor)?.label : spec.flavor;
	const version = `v${spec.version}${flavor ? ` · ${flavor}` : ""}`;
	return (
		<div
			title={`${label} ${version}`}
			className="mt-[3px] flex items-center gap-1.5 font-mono text-[11px] text-ink-3 leading-none"
		>
			<DatabaseIcon aria-hidden className="size-[11px] shrink-0" />
			<span className="min-w-0 truncate">{label}</span>
			<span className="-my-0.5 shrink-0 rounded-md border border-line-faint bg-white/5 px-1 py-[2px] text-[10px] text-ink-2">
				{version}
			</span>
		</div>
	);
}
