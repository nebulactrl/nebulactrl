import { FilesSheet } from "./files-sheet";
import { TerminalSheet } from "./terminal-sheet";

/**
 * Mounted once by the canvas screen: the terminal and file-browser panels
 * that `openShell` / `openFiles` open.
 */
export function TerminalHost() {
	return (
		<>
			<TerminalSheet />
			<FilesSheet />
		</>
	);
}
