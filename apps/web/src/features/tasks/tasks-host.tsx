import { RunCommandDialog } from "./run-command-dialog";
import { RunsDrawer } from "./runs-drawer";

/**
 * Mounted once by the canvas screen: the run-command dialog and the run
 * history drawer that `openRunCommand` / `openRunHistory` open.
 */
export function TasksHost() {
	return (
		<>
			<RunCommandDialog />
			<RunsDrawer />
		</>
	);
}
