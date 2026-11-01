import { XIcon } from "lucide-react";
import type { Ref } from "react";
import type { Service } from "#/api/models";
import { Button } from "#/components/ui/button";
import { ServiceIcon } from "#/features/canvas/service-icon";

/** The names of up to `limit` services, then "and N more". */
function names(list: readonly Pick<Service, "name">[], limit = 2): string {
	const shown = list.slice(0, limit).map((service) => service.name);
	const rest = list.length - shown.length;
	return rest > 0
		? `${shown.join(", ")} and ${rest} more`
		: shown.join(" and ");
}

/**
 * A quiet offer after a database is added to an environment that has apps with
 * no database yet. It is not a dialog and takes no focus: Connect opens the
 * same popover a dropped cable does, anchored on the button, and the cross
 * dismisses it for good (for this database, this visit).
 */
export function DatabaseSuggestion({
	database,
	targets,
	connectRef,
	onConnect,
	onDismiss,
}: {
	database: Service;
	targets: readonly Service[];
	connectRef: Ref<HTMLButtonElement>;
	onConnect(): void;
	onDismiss(): void;
}) {
	return (
		<div
			role="status"
			className="surface-pop fixed bottom-5 left-1/2 z-30 flex max-w-[calc(100vw-32px)] -translate-x-1/2 items-center gap-2.5 rounded-card py-1.5 pr-1.5 pl-3 text-xs text-ink-2"
		>
			<ServiceIcon service={database} size={18} />
			<span className="min-w-0 truncate">
				<span className="font-medium text-ink-1">{database.name}</span> is not
				wired in. {names(targets)} can reach it.
			</span>
			<Button ref={connectRef} type="button" size="sm" onClick={onConnect}>
				Connect
			</Button>
			<Button
				type="button"
				variant="ghost"
				size="icon-sm"
				aria-label={`Dismiss the suggestion to connect ${database.name}`}
				onClick={onDismiss}
			>
				<XIcon aria-hidden />
			</Button>
		</div>
	);
}
