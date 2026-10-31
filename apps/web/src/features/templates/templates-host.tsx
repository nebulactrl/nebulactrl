import type { Server } from "#/api/models";
import { useProjectContext } from "#/components/shell/use-project-context";
import { Dialog, DialogContent } from "#/components/ui/dialog";
import { useServers } from "#/features/canvas/data";
import { DeleteStackDialog } from "./delete-stack-dialog";
import { DetailView } from "./detail-view";
import { GalleryView } from "./gallery-view";
import { templatesActions, useTemplatesState } from "./store";
import { UpdateStackDialog } from "./update-dialog";

const NO_SERVERS: Server[] = [];

/**
 * Mounted once by the canvas screen: the template gallery and, behind a
 * card, its plan step, in the environment on screen, plus the delete-stack
 * confirmation and the update-diff dialog. Escape on the plan step goes back to the gallery before it
 * closes anything.
 */
export function TemplatesHost() {
	const context = useProjectContext();
	const environment = context?.environment;
	const servers = useServers();
	const { open, templateId } = useTemplatesState();
	if (!environment || !context) return null;
	return (
		<>
			<Dialog
				open={open}
				onOpenChange={(next, details) => {
					if (next) return;
					if (details.reason === "escape-key" && templateId !== null) {
						templatesActions.select(null);
						return;
					}
					templatesActions.close();
				}}
			>
				<DialogContent width={templateId ? 980 : 900} top={7}>
					{templateId ? (
						<DetailView
							key={templateId}
							templateId={templateId}
							environment={environment}
							projectId={context.projectId}
							servers={servers.data ?? NO_SERVERS}
						/>
					) : (
						<GalleryView />
					)}
				</DialogContent>
			</Dialog>
			<DeleteStackDialog />
			<UpdateStackDialog />
		</>
	);
}
