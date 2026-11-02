import { afterEach, beforeEach, describe, expect, mock, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { getMySecurityQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import type { PasskeyBody } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders, testQueryClient } from "#/test/render";
import {
	AddPasskeyDialog,
	RemovePasskeyDialog,
	RenamePasskeyDialog,
} from "./passkey-dialogs";

const problem = (status: number, detail: string, code = "bad_request") => ({
	status,
	body: { status, title: "Error", detail, code },
});

const CHROME_MAC =
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36";

const passkey: PasskeyBody = {
	id: "pk1",
	name: "MacBook",
	createdAt: "2026-01-01T00:00:00Z",
	synced: false,
	transports: null,
};

const CREATION_OPTIONS = {
	publicKey: {
		challenge: "AQID",
		rp: { id: "localhost", name: "NebulaCtrl" },
		user: { id: "BAUG", name: "ada@example.com", displayName: "Ada" },
		pubKeyCredParams: [{ type: "public-key", alg: -7 }],
	},
};

// --- a WebAuthn stand-in: happy-dom has none -------------------------------

class FakePublicKeyCredential {
	id = "cred-id";
	type = "public-key";
	rawId = new Uint8Array([1, 2, 3]).buffer;
	authenticatorAttachment = "platform";
	response = {
		clientDataJSON: new Uint8Array([4, 5, 6]).buffer,
		attestationObject: new Uint8Array([7, 8, 9]).buffer,
		getTransports: () => ["internal"],
	};
	getClientExtensionResults = () => ({});
}

type Create = (options: CredentialCreationOptions) => Promise<unknown>;

const originals: {
	name: string;
	target: object;
	desc: PropertyDescriptor | undefined;
}[] = [];

function define(target: object, name: string, value: unknown) {
	originals.push({
		name,
		target,
		desc: Object.getOwnPropertyDescriptor(target, name),
	});
	Object.defineProperty(target, name, {
		value,
		configurable: true,
		writable: true,
	});
}

/** Makes this page look like a browser that can create passkeys with `create`. */
function stubWebAuthn(create: Create) {
	define(window, "isSecureContext", true);
	define(window, "PublicKeyCredential", FakePublicKeyCredential);
	define(globalThis, "PublicKeyCredential", FakePublicKeyCredential);
	define(navigator, "credentials", { create });
}

beforeEach(() => {
	define(navigator, "userAgent", CHROME_MAC);
});

afterEach(() => {
	for (const { name, target, desc } of originals.splice(0).reverse()) {
		if (desc) Object.defineProperty(target, name, desc);
		else Reflect.deleteProperty(target, name);
	}
});

function renderAdd(
	props: Partial<Parameters<typeof AddPasskeyDialog>[0]> = {},
	queryClient = testQueryClient(),
) {
	return renderWithProviders(
		<AddPasskeyDialog
			open
			onOpenChange={() => {}}
			hasPassword
			account="ada@example.com"
			{...props}
		/>,
		{ queryClient },
	);
}

describe("AddPasskeyDialog", () => {
	test("suggests a name from the browser and needs a password to continue", async () => {
		const user = userEvent.setup();
		await renderAdd();
		const name = await screen.findByLabelText("Name");
		expect(name).toHaveValue("Chrome on macOS");
		const next = screen.getByRole("button", { name: "Continue" });
		expect(next).toBeDisabled();
		await user.type(screen.getByLabelText("Password"), "hunter2");
		expect(next).toBeEnabled();
		await user.clear(name);
		expect(next).toBeDisabled();
		await user.type(name, "   ");
		expect(next).toBeDisabled();
	});

	test("an account without a password is not asked for one", async () => {
		await renderAdd({ hasPassword: false });
		expect(await screen.findByLabelText("Name")).toBeInTheDocument();
		expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled();
	});

	test("runs the ceremony: options, browser prompt, then the encoded credential", async () => {
		const created: CredentialCreationOptions[] = [];
		stubWebAuthn(async (options) => {
			created.push(options);
			return new FakePublicKeyCredential();
		});
		const api = stubApi({
			"POST /me/passkeys/options": { body: CREATION_OPTIONS },
			"POST /me/passkeys": {
				status: 201,
				body: { passkey: { ...passkey, name: "Work laptop" } },
			},
		});
		const onOpenChange = mock((_open: boolean) => {});
		const queryClient = testQueryClient();
		queryClient.setQueryData(getMySecurityQueryKey(), {});
		const user = userEvent.setup();
		await renderAdd({ onOpenChange }, queryClient);

		const name = await screen.findByLabelText("Name");
		await user.clear(name);
		await user.type(name, "  Work laptop ");
		await user.type(screen.getByLabelText("Password"), "hunter2");
		await user.click(screen.getByRole("button", { name: "Continue" }));

		expect(await screen.findByText("Passkey added")).toBeInTheDocument();
		expect(screen.getByText("Work laptop")).toBeInTheDocument();
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"POST /me/passkeys/options",
			"POST /me/passkeys",
		]);
		expect(api.calls[0]?.body).toEqual({ password: "hunter2" });
		expect(api.calls[1]?.body).toEqual({
			name: "Work laptop",
			credential: {
				id: "cred-id",
				rawId: "AQID",
				type: "public-key",
				authenticatorAttachment: "platform",
				clientExtensionResults: {},
				response: {
					clientDataJSON: "BAUG",
					attestationObject: "BwgJ",
					transports: ["internal"],
				},
			},
		});
		// The browser got the server's challenge as bytes.
		const publicKey = created[0]?.publicKey;
		expect(
			Array.from(new Uint8Array(publicKey?.challenge as ArrayBuffer)),
		).toEqual([1, 2, 3]);
		expect(publicKey?.user.name).toBe("ada@example.com");
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(
			queryClient.getQueryState(getMySecurityQueryKey())?.isInvalidated,
		).toBe(true);
	});

	test("without a password the first request has an empty body", async () => {
		stubWebAuthn(async () => new FakePublicKeyCredential());
		const api = stubApi({
			"POST /me/passkeys/options": { body: CREATION_OPTIONS },
			"POST /me/passkeys": { status: 201, body: { passkey } },
		});
		const user = userEvent.setup();
		await renderAdd({ hasPassword: false });
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		await screen.findByText("Passkey added");
		expect(api.calls[0]?.body).toEqual({});
	});

	test("shows the recovery codes once when the server issues them, and will not be dismissed early", async () => {
		stubWebAuthn(async () => new FakePublicKeyCredential());
		stubApi({
			"POST /me/passkeys/options": { body: CREATION_OPTIONS },
			"POST /me/passkeys": {
				status: 201,
				body: { passkey, recoveryCodes: ["code-0001", "code-0002"] },
			},
		});
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderAdd({ hasPassword: false, onOpenChange });
		await user.click(await screen.findByRole("button", { name: "Continue" }));

		expect(await screen.findByText("code-0001")).toBeInTheDocument();
		expect(screen.getByText("code-0002")).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "Passkey added" }),
		).toBeInTheDocument();

		await user.keyboard("{Escape}");
		expect(onOpenChange).not.toHaveBeenCalled();

		const done = screen.getByRole("button", { name: "Done" });
		expect(done).toBeDisabled();
		await user.click(
			screen.getByRole("checkbox", {
				name: "I stored these codes somewhere safe",
			}),
		);
		await user.click(done);
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("closing the browser prompt reports that nothing was added", async () => {
		stubWebAuthn(async () => {
			throw new DOMException("denied", "NotAllowedError");
		});
		const api = stubApi({
			"POST /me/passkeys/options": { body: CREATION_OPTIONS },
		});
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderAdd({ hasPassword: false, onOpenChange });
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		expect(
			await screen.findByText(
				"The passkey prompt was closed, so nothing was added.",
			),
		).toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toEqual(["/me/passkeys/options"]);
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(screen.queryByText("Passkey added")).not.toBeInTheDocument();
	});

	test("explains an authenticator that already holds a passkey", async () => {
		stubWebAuthn(async () => {
			throw new DOMException("dup", "InvalidStateError");
		});
		stubApi({ "POST /me/passkeys/options": { body: CREATION_OPTIONS } });
		const user = userEvent.setup();
		await renderAdd({ hasPassword: false });
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		expect(
			await screen.findByText(
				"This authenticator already holds a passkey for your account.",
			),
		).toBeInTheDocument();
	});

	test("says so when the browser returns no credential", async () => {
		stubWebAuthn(async () => null);
		stubApi({ "POST /me/passkeys/options": { body: CREATION_OPTIONS } });
		const user = userEvent.setup();
		await renderAdd({ hasPassword: false });
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		expect(
			await screen.findByText("The browser returned no passkey."),
		).toBeInTheDocument();
	});

	test("without WebAuthn support the browser is never asked, and the reason is shown", async () => {
		const api = stubApi({
			"POST /me/passkeys/options": { body: CREATION_OPTIONS },
		});
		const user = userEvent.setup();
		await renderAdd({ hasPassword: false });
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		expect(
			await screen.findByText(/cannot use passkeys\. Passkeys need HTTPS/),
		).toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toEqual(["/me/passkeys/options"]);
	});

	test("a wrong password from the server is shown and no prompt opens", async () => {
		const create = mock(async () => new FakePublicKeyCredential());
		stubWebAuthn(create);
		const api = stubApi({
			"POST /me/passkeys/options": problem(
				422,
				"That password is not right.",
				"invalid_password",
			),
		});
		const user = userEvent.setup();
		await renderAdd();
		await user.type(await screen.findByLabelText("Password"), "nope");
		await user.click(screen.getByRole("button", { name: "Continue" }));
		expect(
			await screen.findByText("That password is not right."),
		).toBeInTheDocument();
		expect(create).not.toHaveBeenCalled();
		expect(api.calls).toHaveLength(1);
	});

	test("a failure when saving the credential is shown", async () => {
		stubWebAuthn(async () => new FakePublicKeyCredential());
		stubApi({
			"POST /me/passkeys/options": { body: CREATION_OPTIONS },
			"POST /me/passkeys": problem(409, "That passkey is already registered."),
		});
		const user = userEvent.setup();
		await renderAdd({ hasPassword: false });
		await user.click(await screen.findByRole("button", { name: "Continue" }));
		expect(
			await screen.findByText("That passkey is already registered."),
		).toBeInTheDocument();
	});

	test("Cancel closes without a request", async () => {
		const api = stubApi({});
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderAdd({ onOpenChange });
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls).toEqual([]);
	});

	test("Enter in an incomplete form sends nothing", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderAdd();
		await user.type(await screen.findByLabelText("Name"), "{Enter}");
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});
});

describe("RenamePasskeyDialog", () => {
	test("is closed without a passkey", async () => {
		await renderWithProviders(
			<RenamePasskeyDialog passkey={null} onOpenChange={() => {}} />,
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	test("starts on the current name and only saves a real change", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<RenamePasskeyDialog passkey={passkey} onOpenChange={() => {}} />,
		);
		const name = await screen.findByLabelText("Name");
		expect(name).toHaveValue("MacBook");
		const save = screen.getByRole("button", { name: "Save" });
		expect(save).toBeDisabled();
		await user.type(name, "  ");
		expect(save).toBeDisabled();
		await user.clear(name);
		expect(save).toBeDisabled();
		await user.type(name, "YubiKey");
		expect(save).toBeEnabled();
	});

	test("PATCHes the trimmed name, confirms and refreshes the security status", async () => {
		const api = stubApi({
			"PATCH /me/passkeys/:id": { status: 204 },
		});
		const onOpenChange = mock((_open: boolean) => {});
		const queryClient = testQueryClient();
		queryClient.setQueryData(getMySecurityQueryKey(), {});
		const user = userEvent.setup();
		await renderWithProviders(
			<RenamePasskeyDialog passkey={passkey} onOpenChange={onOpenChange} />,
			{ queryClient },
		);
		const name = await screen.findByLabelText("Name");
		await user.clear(name);
		await user.type(name, " YubiKey ");
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(await screen.findByText("Passkey renamed")).toBeInTheDocument();
		expect(api.calls).toEqual([
			{
				method: "PATCH",
				path: "/me/passkeys/pk1",
				query: expect.anything(),
				body: { name: "YubiKey" },
			},
		]);
		expect(onOpenChange).toHaveBeenCalledWith(false);
		await waitFor(() =>
			expect(
				queryClient.getQueryState(getMySecurityQueryKey())?.isInvalidated,
			).toBe(true),
		);
	});

	test("shows why a rename failed and stays open", async () => {
		stubApi({ "PATCH /me/passkeys/:id": problem(404, "No such passkey.") });
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderWithProviders(
			<RenamePasskeyDialog passkey={passkey} onOpenChange={onOpenChange} />,
		);
		const name = await screen.findByLabelText("Name");
		await user.type(name, "2");
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(await screen.findByText("No such passkey.")).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(screen.queryByText("Passkey renamed")).not.toBeInTheDocument();
	});

	test("Cancel closes", async () => {
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderWithProviders(
			<RenamePasskeyDialog passkey={passkey} onOpenChange={onOpenChange} />,
		);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});
});

describe("RemovePasskeyDialog", () => {
	const renderRemove = (
		props: Partial<Parameters<typeof RemovePasskeyDialog>[0]> = {},
		queryClient = testQueryClient(),
	) =>
		renderWithProviders(
			<RemovePasskeyDialog
				passkey={passkey}
				onOpenChange={() => {}}
				hasPassword
				lastSecondFactor={false}
				requiredByTeams={[]}
				{...props}
			/>,
			{ queryClient },
		);

	test("is closed without a passkey", async () => {
		await renderRemove({ passkey: null });
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	test("names the passkey and needs the password before Remove is enabled", async () => {
		const user = userEvent.setup();
		await renderRemove();
		expect(await screen.findByText("Remove “MacBook”?")).toBeInTheDocument();
		const remove = screen.getByRole("button", { name: "Remove" });
		expect(remove).toBeDisabled();
		await user.type(screen.getByLabelText("Password"), "hunter2");
		expect(remove).toBeEnabled();
	});

	test("DELETEs with the password, confirms by name and refreshes", async () => {
		const api = stubApi({ "DELETE /me/passkeys/:id": { status: 204 } });
		const onOpenChange = mock((_open: boolean) => {});
		const queryClient = testQueryClient();
		queryClient.setQueryData(getMySecurityQueryKey(), {});
		const user = userEvent.setup();
		await renderRemove({ onOpenChange }, queryClient);
		await user.type(await screen.findByLabelText("Password"), "hunter2");
		await user.click(screen.getByRole("button", { name: "Remove" }));
		expect(await screen.findByText("Passkey removed")).toBeInTheDocument();
		expect(api.calls).toEqual([
			{
				method: "DELETE",
				path: "/me/passkeys/pk1",
				query: expect.anything(),
				body: { password: "hunter2" },
			},
		]);
		expect(onOpenChange).toHaveBeenCalledWith(false);
		await waitFor(() =>
			expect(
				queryClient.getQueryState(getMySecurityQueryKey())?.isInvalidated,
			).toBe(true),
		);
	});

	test("an account without a password removes with an empty body and no password field", async () => {
		const api = stubApi({ "DELETE /me/passkeys/:id": { status: 204 } });
		const user = userEvent.setup();
		await renderRemove({ hasPassword: false });
		expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
		await user.click(await screen.findByRole("button", { name: "Remove" }));
		await screen.findByText("Passkey removed");
		expect(api.calls[0]?.body).toEqual({});
	});

	test("warns that removing the last second step also ends the recovery codes", async () => {
		await renderRemove({ lastSecondFactor: true });
		expect(
			await screen.findByText(/last second step, so its recovery codes stop/),
		).toBeInTheDocument();
	});

	test("does not warn about recovery codes while another second step remains", async () => {
		await renderRemove({
			lastSecondFactor: false,
			requiredByTeams: ["Platform"],
		});
		await screen.findByText("Remove “MacBook”?");
		expect(screen.queryByText(/last second step/)).not.toBeInTheDocument();
		expect(screen.queryByText(/requires two-step/)).not.toBeInTheDocument();
	});

	test("names the teams that require two-step verification when it is the last one", async () => {
		const one = await renderRemove({
			lastSecondFactor: true,
			requiredByTeams: ["Platform"],
		});
		expect(
			await screen.findByText(/Platform\s+requires two-step verification/),
		).toBeInTheDocument();
		one.unmount();
		await renderRemove({
			lastSecondFactor: true,
			requiredByTeams: ["Platform", "Data"],
		});
		expect(
			await screen.findByText(/Platform, Data\s+require two-step verification/),
		).toBeInTheDocument();
	});

	test("shows why removal failed and keeps the dialog open", async () => {
		stubApi({
			"DELETE /me/passkeys/:id": problem(
				422,
				"Wrong password.",
				"invalid_password",
			),
		});
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderRemove({ onOpenChange });
		await user.type(await screen.findByLabelText("Password"), "nope");
		await user.click(screen.getByRole("button", { name: "Remove" }));
		expect(await screen.findByText("Wrong password.")).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(screen.queryByText("Passkey removed")).not.toBeInTheDocument();
	});

	test("Keep it closes without a request", async () => {
		const api = stubApi({});
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderRemove({ onOpenChange });
		await user.click(await screen.findByRole("button", { name: "Keep it" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls).toEqual([]);
	});
});
