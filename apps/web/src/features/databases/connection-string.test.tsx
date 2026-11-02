import { afterEach, describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ConnectionString } from "./connection-string";
import { type ClipboardStub, problem, stubClipboard } from "./database.fixture";

const REVEAL = "POST /services/:id/database/reveal";
const SECRET = "postgres://app:s3cret@db:5432/app";

const revealed = (expiresInMs = 120_000) => ({
	body: make("Revealed", {
		key: "DATABASE_URL",
		value: SECRET,
		expiresAt: new Date(Date.now() + expiresInMs).toISOString(),
	}),
});

let clipboard: ClipboardStub | undefined;
afterEach(() => clipboard?.restore());

async function mount(children?: React.ReactNode) {
	return renderWithProviders(
		<ConnectionString serviceId="svc-db" exportKey="DATABASE_URL">
			{children}
		</ConnectionString>,
	);
}

describe("ConnectionString", () => {
	test("offers Copy and Show and reveals nothing until asked", async () => {
		const api = stubApi({ [REVEAL]: revealed() });
		await mount(<button type="button">Extra action</button>);
		expect(
			screen.getByRole("button", { name: "Copy connection string" }),
		).toBeEnabled();
		expect(screen.getByRole("button", { name: "Show for 60 s" })).toBeEnabled();
		expect(screen.getByRole("button", { name: "Extra action" })).toBeVisible();
		expect(screen.queryByText(SECRET)).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("Copy reveals the export, writes it to the clipboard and keeps it off the screen", async () => {
		const user = userEvent.setup();
		clipboard = stubClipboard("ok");
		const api = stubApi({ [REVEAL]: revealed() });
		const { queryClient } = await mount();
		await user.click(
			screen.getByRole("button", { name: "Copy connection string" }),
		);

		expect(await screen.findByText("Copied")).toBeVisible();
		expect(
			screen.getByText("Revealed values are audited and not kept."),
		).toBeVisible();
		expect(clipboard.writes).toEqual([SECRET]);
		const call = api.calls[0];
		expect(call?.method).toBe("POST");
		expect(call?.path).toBe("/services/svc-db/database/reveal");
		expect(call?.body).toEqual({ key: "DATABASE_URL" });
		expect(screen.queryByDisplayValue(SECRET)).not.toBeInTheDocument();
		expect(screen.queryByText(SECRET)).not.toBeInTheDocument();
		// The secret does not linger in the mutation cache.
		await waitFor(() =>
			expect(queryClient.getMutationCache().getAll()).toHaveLength(0),
		);
		expect(
			screen.getByRole("button", { name: "Copy connection string" }),
		).toBeEnabled();
	});

	test("Copy says so when the browser blocks the clipboard", async () => {
		const user = userEvent.setup();
		clipboard = stubClipboard("absent");
		stubApi({ [REVEAL]: revealed() });
		await mount();
		await user.click(
			screen.getByRole("button", { name: "Copy connection string" }),
		);
		expect(await screen.findByText("Nothing was copied")).toBeVisible();
		expect(screen.getByText(/Use Show for 60 s instead/)).toBeVisible();
		expect(screen.queryByText("Copied")).not.toBeInTheDocument();
		expect(screen.queryByDisplayValue(SECRET)).not.toBeInTheDocument();
	});

	test("a refused reveal toasts the reason and shows nothing", async () => {
		const user = userEvent.setup();
		clipboard = stubClipboard("ok");
		stubApi({
			[REVEAL]: problem(403, "you may not reveal secrets", "forbidden"),
		});
		await mount();
		await user.click(
			screen.getByRole("button", { name: "Copy connection string" }),
		);
		expect(
			await screen.findByText("Could not reveal the connection string"),
		).toBeVisible();
		expect(screen.getByText("you may not reveal secrets")).toBeVisible();
		expect(clipboard.writes).toEqual([]);
		expect(
			screen.getByRole("button", { name: "Copy connection string" }),
		).toBeEnabled();
	});

	test("Show puts the value in a read-only field with a countdown, and Hide removes it", async () => {
		const user = userEvent.setup();
		stubApi({ [REVEAL]: revealed() });
		await mount();
		await user.click(screen.getByRole("button", { name: "Show for 60 s" }));

		const field = await screen.findByLabelText("DATABASE_URL (revealed)");
		expect(field).toHaveValue(SECRET);
		expect(field).toHaveAttribute("readonly");
		// The server's expiry is later than the client's cap: 60 s at most.
		expect(screen.getByRole("timer")).toHaveTextContent(
			/Hides itself in (60|59) s\. Revealed values are audited and are not kept\./,
		);
		// Show is replaced while the value is on screen.
		expect(
			screen.queryByRole("button", { name: "Show for 60 s" }),
		).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Hide" }));
		expect(screen.queryByDisplayValue(SECRET)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Show for 60 s" })).toBeEnabled();
	});

	test("the countdown follows an earlier server expiry", async () => {
		const user = userEvent.setup();
		stubApi({ [REVEAL]: revealed(20_000) });
		await mount();
		await user.click(screen.getByRole("button", { name: "Show for 60 s" }));
		await screen.findByLabelText("DATABASE_URL (revealed)");
		expect(screen.getByRole("timer")).toHaveTextContent(
			/Hides itself in (20|19) s/,
		);
	});

	test("a value that has already expired is dropped at once", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [REVEAL]: revealed(-5_000) });
		await mount();
		await user.click(screen.getByRole("button", { name: "Show for 60 s" }));
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() =>
			expect(screen.queryByDisplayValue(SECRET)).not.toBeInTheDocument(),
		);
		expect(screen.getByRole("button", { name: "Show for 60 s" })).toBeEnabled();
	});

	test("Copy still works while the value is shown", async () => {
		const user = userEvent.setup();
		clipboard = stubClipboard("ok");
		stubApi({ [REVEAL]: revealed() });
		await mount();
		await user.click(screen.getByRole("button", { name: "Show for 60 s" }));
		await screen.findByLabelText("DATABASE_URL (revealed)");
		await user.click(
			screen.getByRole("button", { name: "Copy connection string" }),
		);
		expect(await screen.findByText("Copied")).toBeVisible();
		expect(clipboard.writes).toEqual([SECRET]);
		expect(screen.getByLabelText("DATABASE_URL (revealed)")).toHaveValue(
			SECRET,
		);
	});

	test("both actions are locked while a reveal is in flight", async () => {
		const gate = Promise.withResolvers<Response>();
		const api = stubApi({ [REVEAL]: () => gate.promise });
		const user = userEvent.setup();
		await mount();
		await user.click(screen.getByRole("button", { name: "Show for 60 s" }));
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(
			screen.getByRole("button", { name: "Copy connection string" }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: /Show for 60 s/ }),
		).toBeDisabled();

		gate.resolve(Response.json(revealed().body));
		expect(await screen.findByLabelText("DATABASE_URL (revealed)")).toHaveValue(
			SECRET,
		);
		expect(
			screen.getByRole("button", { name: "Copy connection string" }),
		).toBeEnabled();
	});
});
