import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import { z } from "zod";
import { renderWithProviders } from "#/test/render";
import { processSearch, useProcessParam } from "./url";

function Probe() {
	const process = useProcessParam();
	return <output aria-label="process">{String(process)}</output>;
}

async function readParam(path: string): Promise<string> {
	await renderWithProviders(<Probe />, { path });
	return (await screen.findByLabelText("process")).textContent ?? "";
}

describe("useProcessParam", () => {
	test("reads the process the address names", async () => {
		expect(await readParam("/?process=worker")).toBe("worker");
	});

	test("is undefined without ?process=", async () => {
		expect(await readParam("/")).toBe("undefined");
	});

	test("an empty ?process= names no process", async () => {
		expect(await readParam("/?process=")).toBe("undefined");
	});

	test("a value that is not a string names no process", async () => {
		// The router parses `?process=7` as the number 7.
		expect(await readParam("/?process=7")).toBe("undefined");
	});
});

describe("processSearch", () => {
	const schema = z.object(processSearch);

	test("keeps a string", () => {
		expect(schema.parse({ process: "worker" })).toEqual({ process: "worker" });
	});

	test("drops a malformed value instead of failing the route", () => {
		expect(schema.parse({ process: 7 })).toEqual({ process: undefined });
		expect(schema.parse({ process: { a: 1 } })).toEqual({ process: undefined });
	});

	test("is optional", () => {
		expect(schema.parse({})).toEqual({});
	});
});
