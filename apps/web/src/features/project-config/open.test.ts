import { describe, expect, test } from "bun:test";
import { onOpenProjectConfig, openProjectConfig } from "./open";

describe("openProjectConfig", () => {
	test("tells every listener which dialog to open", () => {
		const first: string[] = [];
		const second: string[] = [];
		const offFirst = onOpenProjectConfig((d) => first.push(d));
		const offSecond = onOpenProjectConfig((d) => second.push(d));
		openProjectConfig("review");
		openProjectConfig("export");
		offFirst();
		offSecond();
		expect(first).toEqual(["review", "export"]);
		expect(second).toEqual(["review", "export"]);
	});

	test("a listener that unsubscribed hears nothing more", () => {
		const heard: string[] = [];
		const off = onOpenProjectConfig((d) => heard.push(d));
		openProjectConfig("source");
		off();
		openProjectConfig("review");
		expect(heard).toEqual(["source"]);
	});

	test("does nothing when no host is listening", () => {
		expect(() => openProjectConfig("export")).not.toThrow();
	});
});
