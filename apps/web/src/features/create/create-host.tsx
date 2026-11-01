import type { Server } from "#/api/models";
import { useProjectContext } from "#/components/shell/use-project-context";
import { useServers } from "#/features/canvas/data";
import { ComposeImportHost } from "#/features/compose-import/compose-import-host";
import { CoolifyImportHost } from "#/features/coolify-import/coolify-import-host";
import { CreateDatabaseDialog } from "#/features/databases/create-database-dialog";
import { RouteCreateHost } from "#/features/proxy/create/route-create-host";
import { CreateStackDialog } from "#/features/stacks/create-stack-dialog";
import { useCreateStackState } from "#/features/stacks/store";
import { CreateServiceDialog, isServiceRequest } from "./create-dialog";
import { useCreateState } from "./store";

const NO_SERVERS: Server[] = [];

/**
 * Mounted once by the canvas screen: shows the create dialog for whatever
 * `useCreateService().open` asked for, in the environment on screen.
 */
export function CreateHost() {
	const environment = useProjectContext()?.environment;
	const servers = useServers();
	const { request } = useCreateState();
	const stack = useCreateStackState();
	if (!environment) return null;
	const database = request?.kind === "database" ? request : null;
	const service = request && isServiceRequest(request) ? request : null;
	return (
		<>
			<CreateServiceDialog
				request={service}
				environment={environment}
				servers={servers.data ?? NO_SERVERS}
			/>
			<CreateDatabaseDialog
				request={database}
				environment={environment}
				servers={servers.data ?? NO_SERVERS}
			/>
			<RouteCreateHost />
			<ComposeImportHost />
			<CoolifyImportHost />
			<CreateStackDialog
				open={stack.open}
				position={stack.position}
				environment={environment}
				servers={servers.data ?? NO_SERVERS}
			/>
		</>
	);
}
