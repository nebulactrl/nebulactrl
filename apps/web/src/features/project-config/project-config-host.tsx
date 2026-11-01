import { useEffect, useState } from "react";
import type { Environment } from "#/api/models";
import { ExportDialog } from "./export-dialog";
import { onOpenProjectConfig, type ProjectConfigDialog } from "./open";
import { ReviewDialog } from "./review-dialog";
import { SourceDialog } from "./source-dialog";

/**
 * The review, export and "Config from git" dialogs of the environment on
 * screen. Mounted once (in the environment switcher); the banner, the field
 * marks and the menu open them through `openProjectConfig`. A preview
 * environment is never bound to a file, so it gets none.
 */
export function ProjectConfigHost({
	environment,
}: {
	environment: Environment;
}) {
	const [open, setOpen] = useState<ProjectConfigDialog | null>(null);
	useEffect(() => onOpenProjectConfig(setOpen), []);
	if (environment.kind === "preview") return null;
	const onOpenChange = (dialog: ProjectConfigDialog) => (next: boolean) =>
		setOpen((current) => (next ? dialog : current === dialog ? null : current));
	return (
		<>
			<ReviewDialog
				environment={environment}
				open={open === "review"}
				onOpenChange={onOpenChange("review")}
			/>
			<ExportDialog
				environment={environment}
				open={open === "export"}
				onOpenChange={onOpenChange("export")}
			/>
			<SourceDialog
				environment={environment}
				open={open === "source"}
				onOpenChange={onOpenChange("source")}
			/>
		</>
	);
}
