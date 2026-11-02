import { test as bunTest, describe, expect } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { LogDrainView } from "#/api/gen/types.gen";
import { CredentialFields } from "./credential-fields";
import { type Draft, draftFor, mapFromRows, secretsBody } from "./model";
import { drainView } from "./test-fixtures";
import { DraftHarness, newHost } from "./test-harness";

/** Form tests type into a large dialog; allow for a loaded CI machine. */
const test = (
	name: string,
	fn: (() => void) | (() => Promise<unknown>),
): void => {
	bunTest(name, fn, 30_000);
};

function renderCreds(drain: LogDrainView | null, initial: Partial<Draft> = {}) {
	const host = newHost();
	const base = draftFor(drain);
	render(
		<DraftHarness initial={{ ...base, ...initial }} host={host}>
			{(draft, set) => (
				<CredentialFields draft={draft} set={set} drain={drain} />
			)}
		</DraftHarness>,
	);
	return host;
}

const STORED = drainView({
	kind: "loki",
	secrets: { username: "ac••••", password: "••••••", token: "tk••••" },
	headerNames: ["X-Api-Key", "X-Org"],
});

describe("CredentialFields: a new drain", () => {
	test("syslog takes no credentials, so nothing is rendered", () => {
		const { container } = render(
			<DraftHarness initial={{ kind: "syslog" }} host={newHost()}>
				{(draft, set) => (
					<CredentialFields draft={draft} set={set} drain={null} />
				)}
			</DraftHarness>,
		);
		expect(container).toBeEmptyDOMElement();
	});

	test("Loki asks for basic auth, a token and headers; the password and token are masked", () => {
		renderCreds(null, { kind: "loki" });
		expect(screen.getByLabelText("User name")).not.toHaveAttribute(
			"type",
			"password",
		);
		expect(screen.getByLabelText("Password")).toHaveAttribute(
			"type",
			"password",
		);
		expect(screen.getByLabelText("Bearer token")).toHaveAttribute(
			"type",
			"password",
		);
		expect(screen.getByRole("button", { name: "Add header" })).toBeVisible();
		expect(
			screen.getByText(/Basic auth \(a user name and a password or API key/),
		).toBeVisible();
		// Nothing is stored yet, so there is nothing to remove or keep.
		expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
		expect(screen.queryByText(/leave empty to keep/)).toBeNull();
	});

	test("OTLP takes a token and headers but no basic auth", () => {
		renderCreds(null, { kind: "otlp" });
		expect(screen.queryByLabelText("User name")).toBeNull();
		expect(screen.queryByLabelText("Password")).toBeNull();
		expect(screen.getByLabelText("Bearer token")).toBeVisible();
		expect(
			screen.getByText(/A bearer token, extra headers, or both/),
		).toBeVisible();
	});

	test("HTTP shows its own help line", () => {
		renderCreds(null, { kind: "http" });
		expect(screen.getByText(/Whatever the receiver asks for/)).toBeVisible();
	});

	test("typed credentials and headers end up in the secrets sent", async () => {
		const user = userEvent.setup();
		const host = renderCreds(null, { kind: "loki" });
		await user.type(screen.getByLabelText("User name"), "acme");
		await user.type(screen.getByLabelText("Password"), "hunter2");
		await user.type(screen.getByLabelText("Bearer token"), "tok");
		await user.click(screen.getByRole("button", { name: "Add header" }));
		await user.type(screen.getByLabelText("Headers: name"), "DD-API-KEY");
		await user.type(screen.getByLabelText("Headers: value of DD-API-KEY"), "k");
		expect(secretsBody(host.draft())).toEqual({
			username: "acme",
			password: "hunter2",
			token: "tok",
			headers: { "DD-API-KEY": "k" },
		});
		expect(mapFromRows(host.draft().headers)).toEqual({ "DD-API-KEY": "k" });
	});
});

describe("CredentialFields: editing a stored drain", () => {
	test("shows each stored credential's mask and offers Remove", () => {
		renderCreds(STORED);
		expect(screen.getByLabelText(/^User name/)).toHaveAttribute(
			"placeholder",
			"ac••••",
		);
		expect(screen.getByLabelText(/^Password/)).toHaveAttribute(
			"placeholder",
			"••••••",
		);
		expect(screen.getByLabelText(/^Bearer token/)).toHaveAttribute(
			"placeholder",
			"tk••••",
		);
		expect(
			screen.getByLabelText("Password (leave empty to keep)"),
		).toBeVisible();
		expect(screen.getAllByRole("button", { name: "Remove" })).toHaveLength(3);
	});

	test("leaving a stored field empty sends nothing for it, so the server keeps it", () => {
		const host = renderCreds(STORED);
		expect(secretsBody(host.draft())).toEqual({});
	});

	test("typing replaces a stored value", async () => {
		const user = userEvent.setup();
		const host = renderCreds(STORED);
		await user.type(screen.getByLabelText(/^Bearer token/), "new-token");
		expect(secretsBody(host.draft())).toEqual({ token: "new-token" });
	});

	test("Remove marks the credential to be cleared, Keep it undoes that", async () => {
		const user = userEvent.setup();
		const host = renderCreds(STORED);
		// Remove buttons come in field order: user name, password, token.
		const removes = screen.getAllByRole("button", { name: "Remove" });
		await user.click(removes[2] as HTMLElement);
		expect(host.draft().clear).toEqual(["token"]);
		expect(secretsBody(host.draft()).clear).toEqual(["token"]);
		expect(screen.getByText("Removed when you save.")).toBeVisible();
		expect(screen.queryByLabelText(/^Bearer token/)).toBeNull();
		expect(screen.getByLabelText(/^Password/)).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Keep it" }));
		expect(host.draft().clear).toEqual([]);
		expect(screen.queryByText("Removed when you save.")).toBeNull();
		expect(secretsBody(host.draft())).toEqual({});
	});

	test("shows the names of stored headers and never their values", () => {
		renderCreds(STORED);
		expect(screen.getByText("X-Api-Key, X-Org")).toBeVisible();
		expect(
			screen.getByText(/Their values are never shown again/),
		).toBeVisible();
		expect(screen.queryByLabelText("Headers: name")).toBeNull();
	});

	test("Replace headers swaps the stored set for a blank row and warns to re-enter all", async () => {
		const user = userEvent.setup();
		const host = renderCreds(STORED);
		await user.click(screen.getByRole("button", { name: "Replace headers" }));
		expect(host.draft().headersMode).toBe("replace");
		expect(screen.getByLabelText("Headers: name")).toHaveValue("");
		expect(
			screen.getByText(
				/Saving replaces every stored header \(X-Api-Key, X-Org\)/,
			),
		).toBeVisible();
		await user.type(screen.getByLabelText("Headers: name"), "X-New");
		await user.type(screen.getByLabelText("Headers: value of X-New"), "v");
		expect(secretsBody(host.draft()).headers).toEqual({ "X-New": "v" });
		await user.click(
			screen.getByRole("button", { name: "Keep the stored headers" }),
		);
		expect(host.draft().headersMode).toBe("keep");
		expect(screen.getByText("X-Api-Key, X-Org")).toBeVisible();
	});

	test("Remove all headers clears them on save, Keep them undoes it", async () => {
		const user = userEvent.setup();
		const host = renderCreds(STORED);
		await user.click(
			screen.getByRole("button", { name: "Remove all headers" }),
		);
		expect(
			screen.getByText("All stored headers are removed when you save."),
		).toBeVisible();
		expect(secretsBody(host.draft()).clear).toEqual(["headers"]);
		await user.click(screen.getByRole("button", { name: "Keep them" }));
		expect(host.draft().headersMode).toBe("keep");
		expect(secretsBody(host.draft())).toEqual({});
	});

	test("a drain with no stored headers gets the empty header editor", () => {
		renderCreds(
			drainView({ kind: "otlp", otlp: { url: "https://o.example/v1/logs" } }),
		);
		expect(screen.getByRole("button", { name: "Add header" })).toBeVisible();
		expect(
			screen.getByText(/only their names are shown after saving/),
		).toBeVisible();
	});
});
