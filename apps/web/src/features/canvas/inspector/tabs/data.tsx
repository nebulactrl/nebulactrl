import { useProjectContext } from "#/components/shell/use-project-context";
import { DataPanel } from "#/features/databases/data/data-panel";
import type { ServiceTabProps } from "./types";

/**
 * Data of a managed SQL database: the table browser and the SQL console (see
 * `hasDataTab`). The editor behind it is the heaviest part of the console,
 * which is why this body is a chunk of its own.
 */
export function DataTab({ service }: ServiceTabProps) {
	const production = useProjectContext()?.environment?.isProduction ?? false;
	return (
		<section
			data-tab="data"
			data-service-id={service.id}
			aria-label="Data"
			className="flex min-h-full flex-col p-4"
		>
			<DataPanel service={service} production={production} />
		</section>
	);
}
