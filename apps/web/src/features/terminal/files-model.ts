import type { FileEntry } from "#/api/gen/types.gen";

/** Joins a directory path and an entry name into an absolute container path. */
export function joinPath(dir: string, name: string): string {
	return dir === "/" ? `/${name}` : `${dir.replace(/\/+$/, "")}/${name}`;
}

/** The parent directory; the root is its own parent. */
export function parentPath(path: string): string {
	const trimmed = path.replace(/\/+$/, "");
	const i = trimmed.lastIndexOf("/");
	return i <= 0 ? "/" : trimmed.slice(0, i);
}

export interface Crumb {
	label: string;
	path: string;
}

/** "/var/log" → [/, var, log], each with the path that opens it. */
export function breadcrumbs(path: string): Crumb[] {
	const out: Crumb[] = [{ label: "/", path: "/" }];
	let acc = "";
	for (const part of path.split("/").filter(Boolean)) {
		acc += `/${part}`;
		out.push({ label: part, path: acc });
	}
	return out;
}

/** What a click on an entry does. */
export type EntryAction = "open" | "preview" | "none";

export function entryAction(e: Pick<FileEntry, "type">): EntryAction {
	switch (e.type) {
		case "dir":
			return "open";
		case "link":
			// A link may point at a directory or a file; the listing does not say.
			return "open";
		case "file":
			return "preview";
		default:
			return "none";
	}
}

/** Files the inline preview should not even try (binary by extension). */
const BINARY_EXTENSIONS = new Set([
	"png",
	"jpg",
	"jpeg",
	"gif",
	"webp",
	"ico",
	"pdf",
	"zip",
	"gz",
	"tgz",
	"bz2",
	"xz",
	"7z",
	"tar",
	"jar",
	"so",
	"o",
	"a",
	"exe",
	"bin",
	"wasm",
	"db",
	"sqlite",
	"woff",
	"woff2",
	"ttf",
	"mp3",
	"mp4",
]);

export function looksBinary(name: string): boolean {
	const dot = name.lastIndexOf(".");
	if (dot < 0) return false;
	return BINARY_EXTENSIONS.has(name.slice(dot + 1).toLowerCase());
}

/** True when the preview bytes contain a NUL: not text. */
export function bytesLookBinary(bytes: Uint8Array): boolean {
	const n = Math.min(bytes.length, 8000);
	for (let i = 0; i < n; i++) if (bytes[i] === 0) return true;
	return false;
}

/** A valid upload file name: one path element. */
export function validUploadName(name: string): string | null {
	if (name === "" || name === "." || name === "..")
		return "Choose a file with a name.";
	if (name.includes("/") || name.includes("\0"))
		return "A file name cannot contain a slash.";
	if (name.length > 255) return "That file name is too long.";
	return null;
}

/** Why an upload cannot go ahead, or null. */
export function uploadBlocker(
	file: { name: string; size: number },
	maxBytes: number,
): string | null {
	const name = validUploadName(file.name);
	if (name) return name;
	if (file.size > maxBytes) {
		return `Uploads are limited to ${Math.floor(maxBytes / (1 << 20))} MB.`;
	}
	return null;
}

/** "0644" → "rw-r--r--". */
export function modeString(mode: string | undefined): string {
	if (!mode) return "";
	const bits = Number.parseInt(mode, 8);
	if (Number.isNaN(bits)) return mode;
	const rwx = ["---", "--x", "-w-", "-wx", "r--", "r-x", "rw-", "rwx"] as const;
	const d = (shift: number) => rwx[(bits >> shift) & 7];
	return `${d(6)}${d(3)}${d(0)}`;
}
