import { describe, expect, test } from "bun:test";
import {
	canRestoreDump,
	checkDumpFile,
	describeDump,
	dumpMismatch,
	sniffDump,
	uploadPercent,
} from "./dump";
import { inFlight, restorePresentation } from "./model";

const bytes = (s: string) => new TextEncoder().encode(s);

describe("sniffDump", () => {
	test("recognises the dump formats by their first bytes", () => {
		expect(sniffDump(bytes("PGDMP\u0001\u000e"))).toEqual({
			format: "pg_custom",
			gzip: false,
		});
		const tar = new Uint8Array(512);
		tar.set(bytes("toc.dat"), 0);
		tar.set(bytes("ustar"), 257);
		expect(sniffDump(tar)).toEqual({ format: "pg_tar", gzip: false });
		expect(sniffDump(Uint8Array.from([0x6d, 0xe2, 0x99, 0x81, 1, 2]))).toEqual({
			format: "mongo_archive",
			gzip: false,
		});
		expect(sniffDump(Uint8Array.from([0x1f, 0x8b, 8, 0]))).toEqual({
			format: null,
			gzip: true,
		});
	});

	test("plain SQL carries the dialect its header names", () => {
		expect(sniffDump(bytes("--\n-- PostgreSQL database dump\n--\n"))).toEqual({
			format: "sql",
			gzip: false,
			dialect: "postgres",
		});
		expect(sniffDump(bytes("-- MariaDB dump 10.19\n"))).toEqual({
			format: "sql",
			gzip: false,
			dialect: "mysql",
		});
		expect(sniffDump(bytes("CREATE TABLE t (a int);\n"))).toEqual({
			format: "sql",
			gzip: false,
		});
	});

	test("refuses what is not a dump, with a reason", () => {
		for (const [input, word] of [
			[Uint8Array.of(), "empty"],
			[bytes("REDIS0011"), "RDB"],
			[Uint8Array.of(0xff, 0xfe, 0x53, 0), "UTF-16"],
			[Uint8Array.of(0x28, 0xb5, 0x2f, 0xfd, 1), "zstd"],
			[bytes("PK\u0003\u0004zip"), "Zip"],
			[Uint8Array.of(1, 0, 2, 0, 3), "binary"],
			[bytes('{"a":1}'), "JSON"],
			[bytes("-- PostgreSQL database cluster dump\n"), "pg_dumpall"],
		] as const) {
			const k = sniffDump(input);
			expect("problem" in k && k.problem).toContain(word);
		}
	});
});

describe("dumpMismatch", () => {
	const pgc = { format: "pg_custom", gzip: false } as const;
	test("accepts what the engine's client can restore", () => {
		expect(dumpMismatch("postgres", pgc)).toBeUndefined();
		expect(
			dumpMismatch("mysql", { format: "sql", gzip: false, dialect: "mysql" }),
		).toBeUndefined();
		expect(
			dumpMismatch("mongodb", { format: "mongo_archive", gzip: true }),
		).toBeUndefined();
		// A gzip file is judged by the control plane, which can look inside.
		expect(dumpMismatch("postgres", { format: null, gzip: true })).toBe(
			undefined,
		);
	});

	test("names the file and the engine when they do not fit", () => {
		expect(dumpMismatch("mysql", pgc)).toContain("PostgreSQL custom-format");
		expect(dumpMismatch("mysql", pgc)).toContain("MySQL");
		expect(
			dumpMismatch("postgres", {
				format: "sql",
				gzip: false,
				dialect: "mysql",
			}),
		).toContain("MySQL/MariaDB SQL dump");
		expect(dumpMismatch("mongodb", { format: "sql", gzip: false })).toContain(
			"mongodump",
		);
		expect(dumpMismatch("postgres", { problem: "nope" })).toBe("nope");
	});
});

describe("helpers", () => {
	test("describeDump", () => {
		expect(
			describeDump({ format: "sql", gzip: true, dialect: "postgres" }),
		).toBe("PostgreSQL plain SQL dump, gzip");
		expect(describeDump({ format: null, gzip: true })).toBe("Compressed dump");
	});

	test("checkDumpFile reads the start of a File", async () => {
		const ok = await checkDumpFile(
			new Blob([bytes("PGDMP"), new Uint8Array(100)]),
			"postgres",
		);
		expect(ok.problem).toBeUndefined();
		expect(ok.kind?.format).toBe("pg_custom");
		const wrong = await checkDumpFile(new Blob([bytes("PGDMP")]), "mongodb");
		expect(wrong.problem).toContain("cannot be restored into a MongoDB");
	});

	test("engines and progress", () => {
		expect(canRestoreDump("postgres")).toBe(true);
		expect(canRestoreDump("valkey")).toBe(false);
		expect(uploadPercent(50, 200)).toBe(25);
		expect(uploadPercent(500, 200)).toBe(100);
		expect(uploadPercent(1, 0)).toBe(0);
	});

	test("the upload states read as in flight with words", () => {
		for (const s of ["awaiting_upload", "uploading", "restoring"]) {
			expect(inFlight(s)).toBe(true);
		}
		expect(restorePresentation("awaiting_upload").label).toBe(
			"Waiting for the file",
		);
		expect(restorePresentation("uploading").pulse).toBe(true);
	});
});
