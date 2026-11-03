import { RunCommandDialog } from "./run-command-dialog";
import { RunsDrawer } from "./runs-drawer";
import { closeTaskDialog, useTaskDialogTarget } from "./store";
import { TaskDialog } from "./task-dialog";

/**
 * Mounted once by the canvas screen: the run-command dialog, the run history
 * drawer and the "Schedule task" dialog that `openRunCommand` /
 * `openRunHistory` / `openTaskDialog` open.
 */
export function TasksHost() {
	const taskTarget = useTaskDialogTarget();
	return (
		<>
			<RunCommandDialog />
			<RunsDrawer />
			{taskTarget ? (
				<TaskDialog
					open
					onOpenChange={(open) => {
						if (!open) closeTaskDialog();
					}}
					serviceId={taskTarget}
					task={null}
				/>
			) : null}
		</>
	);
}
