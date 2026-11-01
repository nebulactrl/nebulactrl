// The config files a service mounts into its container (`config.files`).
// The limits and rules mirror internal/services/config_files.go, so a form
// never offers what the control plane would refuse.

export const MAX_FILES = 16;
export const MAX_FILE_BYTES = 64 * 1024;
const MAX_FILES_TOTAL_BYTES = 256 * 1024;

export interface FileDraft {
	path: string;
	/** Octal like 0644; empty keeps the default. */
	mode: string;
	content: string;
}

export interface FileErrors {
	path?: string;
	mode?: string;
	content?: string;
}

const MODE = /^0?[0-7]{3,4}$/;

const encoder = new TextEncoder();

/** The size the control plane counts: UTF-8 bytes. */
export function byteLength(text: string): number {
	return encoder.encode(text).length;
}

/** `/etc/app/config.toml`: absolute, no `.`/`..`/empty segments, not `/`. */
function isCleanAbsolutePath(path: string): boolean {
	if (!path.startsWith("/") || path === "/" || path.endsWith("/")) return false;
	return !path
		.slice(1)
		.split("/")
		.some((segment) => segment === "" || segment === "." || segment === "..");
}

/** What is wrong with one file, given the other files of the service. */
export function fileErrors(
	file: FileDraft,
	others: readonly FileDraft[],
): FileErrors {
	const errors: FileErrors = {};
	if (!isCleanAbsolutePath(file.path)) {
		errors.path =
			"Use a clean absolute path like /etc/app/config.toml (no .. and no trailing /).";
	} else if (others.some((other) => other.path === file.path)) {
		errors.path = "Another file already uses this path.";
	}
	if (file.mode !== "" && !MODE.test(file.mode)) {
		errors.mode = "Use an octal mode like 0644.";
	}
	if (byteLength(file.content) > MAX_FILE_BYTES) {
		errors.content = `A file holds at most ${MAX_FILE_BYTES / 1024} KiB.`;
	}
	return errors;
}

/** The first problem across all files, or undefined when they can be saved. */
export function validateFiles(files: readonly FileDraft[]): string | undefined {
	if (files.length > MAX_FILES) return `At most ${MAX_FILES} files.`;
	let total = 0;
	for (const [index, file] of files.entries()) {
		const others = files.filter((_, other) => other !== index);
		const errors = fileErrors(file, others);
		const problem = errors.path ?? errors.mode ?? errors.content;
		if (problem) return `${file.path || "A file"}: ${problem}`;
		total += byteLength(file.content);
	}
	if (total > MAX_FILES_TOTAL_BYTES) {
		return `Files hold at most ${MAX_FILES_TOTAL_BYTES / 1024} KiB in total.`;
	}
	return undefined;
}

/** `1.2 KiB`, `64 KiB`, `0 B`: a file's size at a glance. */
export function sizeText(bytes: number): string {
	if (bytes < 1024) return `${bytes} B`;
	const kib = bytes / 1024;
	return `${kib >= 10 ? Math.round(kib) : kib.toFixed(1)} KiB`;
}
