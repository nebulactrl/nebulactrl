import type { ReactNode } from "react";
import { DocsLink, docTargetFromPath } from "#/components/docs/docs-link";

/**
 * A secondary button that opens a docs page in the help sheet. `path` is
 * relative to `docs/` and written as the file is named: "installing/servers.md".
 */
export function DocsButton({
	path,
	children,
}: {
	path: string;
	children: ReactNode;
}) {
	return (
		<DocsLink to={docTargetFromPath(path)} variant="button">
			{children}
		</DocsLink>
	);
}
