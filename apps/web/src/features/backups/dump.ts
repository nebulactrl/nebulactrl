// Restoring a database from a dump file the user picks. The control plane
// detects the format authoritatively from the file's first bytes; this module
// repeats the cheap checks in the browser so a wrong file is refused before a
// new database is created and before gigabytes are sent.

export type DumpFormat = "pg_custom" | "pg_tar" | "sql" | "mongo_archive";

export interface DumpKind {
	/** Null for a gzip file: what is inside is for the control plane to check. */
	format: DumpFormat | null;
	gzip: boolean;
	/** The engine a plain SQL dump says it was written by. */
	dialect?: "postgres" | "mysql";
}

export interface DumpProblem {
	problem: string;
}

/** Bytes of the file's start `sniffDump` needs. */
export const SNIFF_BYTES = 16 * 1024;

const startsWith = (b: Uint8Array, magic: readonly number[], at = 0) =>
	magic.every((m, i) => b[at + i] === m);

const ascii = (s: string) => Array.from(s, (c) => c.charCodeAt(0));

/** What the first bytes of a file say it is. */
export function sniffDump(bytes: Uint8Array): DumpKind | DumpProblem {
	const b = bytes.subarray(0, SNIFF_BYTES);
	if (b.length === 0) return { problem: "The file is empty." };
	if (startsWith(b, [0x1f, 0x8b])) return { format: null, gzip: true };
	if (startsWith(b, ascii("PGDMP")))
		return { format: "pg_custom", gzip: false };
	if (b.length >= 262 && startsWith(b, ascii("ustar"), 257)) {
		return { format: "pg_tar", gzip: false };
	}
	if (startsWith(b, [0x6d, 0xe2, 0x99, 0x81])) {
		return { format: "mongo_archive", gzip: false };
	}
	if (startsWith(b, ascii("REDIS"))) {
		return {
			problem:
				"This is a Redis/Valkey RDB file. Restoring a key-value dump is not supported.",
		};
	}
	if (startsWith(b, [0x28, 0xb5, 0x2f, 0xfd])) {
		return {
			problem: "zstd-compressed dumps are not supported: use gzip or none.",
		};
	}
	if (startsWith(b, [0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00])) {
		return {
			problem: "xz-compressed dumps are not supported: use gzip or none.",
		};
	}
	if (startsWith(b, ascii("BZh"))) {
		return {
			problem: "bzip2-compressed dumps are not supported: use gzip or none.",
		};
	}
	if (startsWith(b, ascii("PK\x03\x04"))) {
		return {
			problem: "Zip archives are not supported: upload the dump file itself.",
		};
	}
	if (startsWith(b, [0xff, 0xfe]) || startsWith(b, [0xfe, 0xff])) {
		return {
			problem:
				"The file is UTF-16 encoded (a PowerShell > redirect does that). Export it as UTF-8.",
		};
	}
	if (b.includes(0)) {
		return {
			problem:
				"Not a pg_dump, mysqldump or mongodump file: it is binary and has no known header.",
		};
	}
	const head = new TextDecoder("utf-8").decode(b).toLowerCase();
	if (head.includes("postgresql database cluster dump")) {
		return {
			problem:
				"This is a pg_dumpall cluster dump. Dump the single database with pg_dump instead.",
		};
	}
	const trimmed = head.trimStart();
	if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
		return { problem: "The file looks like JSON, not a database dump." };
	}
	const kind: DumpKind = { format: "sql", gzip: false };
	if (head.includes("-- postgresql database dump")) kind.dialect = "postgres";
	else if (
		head.includes("-- mysql dump") ||
		head.includes("-- mariadb dump") ||
		head.includes("/*!40101 set @old_character_set_client")
	) {
		kind.dialect = "mysql";
	}
	return kind;
}

const FORMAT_LABEL: Record<DumpFormat, string> = {
	pg_custom: "PostgreSQL custom-format dump",
	pg_tar: "PostgreSQL tar-format dump",
	sql: "SQL dump",
	mongo_archive: "MongoDB archive",
};

export function describeDump(kind: DumpKind): string {
	let label = kind.format ? FORMAT_LABEL[kind.format] : "Compressed dump";
	if (kind.format === "sql" && kind.dialect === "postgres") {
		label = "PostgreSQL plain SQL dump";
	}
	if (kind.format === "sql" && kind.dialect === "mysql") {
		label = "MySQL/MariaDB SQL dump";
	}
	return kind.gzip && kind.format ? `${label}, gzip` : label;
}

/** The formats each engine's client can restore. */
const ACCEPTS: Record<string, readonly DumpFormat[]> = {
	postgres: ["pg_custom", "pg_tar", "sql"],
	mysql: ["sql"],
	mariadb: ["sql"],
	mongodb: ["mongo_archive"],
};

const ENGINE_LABEL: Record<string, string> = {
	postgres: "PostgreSQL",
	mysql: "MySQL",
	mariadb: "MariaDB",
	mongodb: "MongoDB",
};

const EXPECTED: Record<string, string> = {
	postgres: "pg_dump -Fc, -Ft or plain SQL, optionally gzipped",
	mysql: "a plain SQL dump from mysqldump, optionally gzipped",
	mariadb: "a plain SQL dump from mariadb-dump, optionally gzipped",
	mongodb: "mongodump --archive, optionally gzipped",
};

/** Engines whose dump files can be restored. */
export function canRestoreDump(engine: string): boolean {
	return engine in ACCEPTS;
}

/** Why a file cannot be restored into engine, or undefined when it fits. */
export function dumpMismatch(
	engine: string,
	kind: DumpKind | DumpProblem,
): string | undefined {
	if ("problem" in kind) return kind.problem;
	if (kind.format === null) return undefined; // gzip: the control plane looks inside
	const accepts = ACCEPTS[engine];
	if (!accepts) return `Dump files cannot be restored into ${engine}.`;
	const label = ENGINE_LABEL[engine] ?? engine;
	if (!accepts.includes(kind.format)) {
		return `This is a ${describeDump(kind)}; it cannot be restored into a ${label} database (expected ${EXPECTED[engine]}).`;
	}
	if (kind.format === "sql" && kind.dialect) {
		const wrong =
			engine === "postgres"
				? kind.dialect !== "postgres"
				: kind.dialect !== "mysql";
		if (wrong) {
			return `This is a ${describeDump(kind)}; it cannot be restored into a ${label} database.`;
		}
	}
	return undefined;
}

/** Reads the start of a file and checks it against the engine. */
export async function checkDumpFile(
	file: Blob,
	engine: string,
): Promise<{ kind: DumpKind | undefined; problem: string | undefined }> {
	const bytes = new Uint8Array(await file.slice(0, SNIFF_BYTES).arrayBuffer());
	const kind = sniffDump(bytes);
	const problem = dumpMismatch(engine, kind);
	return { kind: "problem" in kind ? undefined : kind, problem };
}

/** Upload progress as a whole percentage (0 when the size is unknown). */
export function uploadPercent(sent: number, size: number): number {
	if (size <= 0) return 0;
	return Math.max(0, Math.min(100, Math.floor((sent / size) * 100)));
}
