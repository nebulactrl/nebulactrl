import { afterEach, describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import type * as LatestNote from "./latest-note";

// SITE_URL and VERSION are read from the build environment when the module
// loads, so each case loads its own copy under the environment it wants.
const KEYS = ["VITE_SITE_URL", "VITE_APP_VERSION"] as const;
const saved = Object.fromEntries(KEYS.map((key) => [key, process.env[key]]));
let load = 0;

async function noteWith(env: Partial<Record<(typeof KEYS)[number], string>>) {
	for (const key of KEYS) {
		const value = env[key];
		if (value === undefined) delete process.env[key];
		else process.env[key] = value;
	}
	// A query string makes Bun evaluate the module again under the new environment.
	const module = (await import(
		`./latest-note.tsx?case=${load++}`
	)) as typeof LatestNote;
	return module.LatestDocsNote;
}

afterEach(() => {
	for (const key of KEYS) {
		const value = saved[key];
		if (value === undefined) delete process.env[key];
		else process.env[key] = value;
	}
});

describe("LatestDocsNote", () => {
	test("renders nothing without a public site URL", async () => {
		const LatestDocsNote = await noteWith({ VITE_APP_VERSION: "1.4.0" });
		const { container } = render(<LatestDocsNote />);
		expect(container).toBeEmptyDOMElement();
	});

	test("names the version and links to the latest docs on the site", async () => {
		const LatestDocsNote = await noteWith({
			VITE_SITE_URL: "https://nebula.example///",
			VITE_APP_VERSION: "v1.4.0",
		});
		render(<LatestDocsNote className="note" />);
		const link = screen.getByRole("link", {
			name: "https://nebula.example/docs",
		});
		expect(link).toHaveAttribute("href", "https://nebula.example/docs");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noopener noreferrer");
		const note = link.closest("p");
		expect(note).toHaveClass("note");
		expect(note).toHaveTextContent(
			"Docs for v1.4.0. Latest at https://nebula.example/docs",
		);
	});

	test.each([
		["dev", "dev"],
		["unset", undefined],
	])("calls a %s build 'this build'", async (_name, version) => {
		const LatestDocsNote = await noteWith({
			VITE_SITE_URL: "https://nebula.example",
			...(version ? { VITE_APP_VERSION: version } : {}),
		});
		render(<LatestDocsNote />);
		expect(
			screen.getByText(/Docs for this build\. Latest at/),
		).toBeInTheDocument();
	});
});
