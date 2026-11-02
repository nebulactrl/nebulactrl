import { afterEach, describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PasskeyBody, SecurityBody } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { PasskeysCard } from "./passkeys-card";

const saved: {
	name: string;
	target: object;
	desc: PropertyDescriptor | undefined;
}[] = [];

function define(target: object, name: string, value: unknown) {
	saved.push({
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

/** A browser that can create passkeys (happy-dom has no WebAuthn). */
function supportPasskeys() {
	class FakePublicKeyCredential {}
	define(window, "isSecureContext", true);
	define(window, "PublicKeyCredential", FakePublicKeyCredential);
	define(globalThis, "PublicKeyCredential", FakePublicKeyCredential);
	define(navigator, "credentials", { create: async () => null });
}

afterEach(() => {
	for (const { name, target, desc } of saved.splice(0).reverse()) {
		if (desc) Object.defineProperty(target, name, desc);
		else Reflect.deleteProperty(target, name);
	}
});

const key = (over: Partial<PasskeyBody> = {}): PasskeyBody => ({
	id: "pk1",
	name: "MacBook",
	createdAt: "2026-01-01T00:00:00Z",
	synced: true,
	transports: null,
	...over,
});

const security = (over: Partial<SecurityBody> = {}): SecurityBody => ({
	enrollmentNeeded: false,
	hasPassword: true,
	identities: null,
	isInstanceAdmin: false,
	passkeys: null,
	recoveryCodesLeft: 10,
	requiredByTeams: null,
	sessionMfa: false,
	totpEnabled: false,
	totpPending: false,
	...over,
});

const renderCard = (sec: SecurityBody) =>
	renderWithProviders(
		<PasskeysCard security={sec} account="ada@example.com" primary />,
	);

describe("PasskeysCard status", () => {
	test("with none registered it says None and offers Add a passkey", async () => {
		supportPasskeys();
		await renderCard(security());
		expect(screen.getByText("None")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Add a passkey" })).toBeEnabled();
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	test("no recovery-code warning when there is no passkey, even at zero codes", async () => {
		supportPasskeys();
		await renderCard(security({ recoveryCodesLeft: 0 }));
		expect(
			screen.queryByText(/no recovery codes left/),
		).not.toBeInTheDocument();
	});

	test("lists each passkey with its kind and when it was used", async () => {
		supportPasskeys();
		await renderCard(
			security({
				passkeys: [
					key(),
					key({
						id: "pk2",
						name: "YubiKey",
						synced: false,
						lastUsedAt: "2026-02-03T04:05:06Z",
					}),
				],
			}),
		);
		expect(screen.getByText("2 passkeys")).toBeInTheDocument();
		const [mac, yubi] = screen.getAllByRole("listitem");
		expect(mac).toHaveTextContent("MacBook");
		expect(mac).toHaveTextContent("Synced");
		expect(mac).toHaveTextContent("never used");
		expect(yubi).toHaveTextContent("YubiKey");
		expect(yubi).toHaveTextContent("This device only");
		expect(yubi).toHaveTextContent("last used");
		expect(
			yubi?.querySelector('time[datetime="2026-02-03T04:05:06Z"]'),
		).not.toBeNull();
	});

	test("counts a single passkey in the singular", async () => {
		supportPasskeys();
		await renderCard(security({ passkeys: [key()] }));
		expect(screen.getByText("1 passkey")).toBeInTheDocument();
	});

	test("warns when passkeys exist but no recovery code is left", async () => {
		supportPasskeys();
		await renderCard(security({ passkeys: [key()], recoveryCodesLeft: 0 }));
		expect(
			screen.getByText(/losing every passkey locks you out/),
		).toBeInTheDocument();
	});

	test("in a browser without WebAuthn the button is disabled and the reason is shown", async () => {
		await renderCard(security());
		expect(
			screen.getByRole("button", { name: "Add a passkey" }),
		).toBeDisabled();
		expect(
			screen.getByText(/This browser cannot create passkeys here/),
		).toBeInTheDocument();
	});

	test("the server's 'unavailable' reason wins and disables adding", async () => {
		supportPasskeys();
		await renderCard(
			security({ passkeysUnavailable: "Passkeys need a domain name." }),
		);
		expect(
			screen.getByText("Passkeys need a domain name."),
		).toBeInTheDocument();
		expect(
			screen.queryByText(/This browser cannot create passkeys here/),
		).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Add a passkey" }),
		).toBeDisabled();
	});

	test("existing passkeys can still be renamed or removed when adding is unavailable", async () => {
		await renderCard(security({ passkeys: [key()] }));
		expect(screen.getByRole("button", { name: "Rename" })).toBeEnabled();
		expect(screen.getByRole("button", { name: "Remove" })).toBeEnabled();
	});
});

describe("PasskeysCard actions", () => {
	test("Add a passkey opens the add dialog", async () => {
		supportPasskeys();
		const user = userEvent.setup();
		await renderCard(security());
		await user.click(screen.getByRole("button", { name: "Add a passkey" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Add a passkey")).toBeInTheDocument();
		expect(within(dialog).getByLabelText("Password")).toBeInTheDocument();
	});

	test("Rename opens the dialog for that passkey and saves under its id", async () => {
		supportPasskeys();
		const api = stubApi({
			"PATCH /me/passkeys/:id": { status: 204 },
		});
		const user = userEvent.setup();
		await renderCard(
			security({ passkeys: [key(), key({ id: "pk2", name: "YubiKey" })] }),
		);
		const second = screen.getAllByRole("listitem")[1] as HTMLElement;
		await user.click(within(second).getByRole("button", { name: "Rename" }));
		const dialog = await screen.findByRole("dialog");
		const name = within(dialog).getByLabelText("Name");
		expect(name).toHaveValue("YubiKey");
		await user.clear(name);
		await user.type(name, "Work key");
		await user.click(within(dialog).getByRole("button", { name: "Save" }));
		expect(await screen.findByText("Passkey renamed")).toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/me/passkeys/pk2");
		expect(api.calls[0]?.body).toEqual({ name: "Work key" });
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("Cancel in the rename dialog drops it", async () => {
		const user = userEvent.setup();
		await renderCard(security({ passkeys: [key()] }));
		await user.click(screen.getByRole("button", { name: "Rename" }));
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("removing the only passkey with no authenticator app warns it is the last second step", async () => {
		const api = stubApi({ "DELETE /me/passkeys/:id": { status: 204 } });
		const user = userEvent.setup();
		await renderCard(
			security({
				passkeys: [key()],
				requiredByTeams: [{ id: "t1", name: "Platform" }],
			}),
		);
		await user.click(screen.getByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Remove “MacBook”?")).toBeInTheDocument();
		expect(within(dialog).getByText(/last second step/)).toBeInTheDocument();
		expect(
			within(dialog).getByText(/Platform\s+requires two-step verification/),
		).toBeInTheDocument();
		await user.type(within(dialog).getByLabelText("Password"), "hunter2");
		await user.click(within(dialog).getByRole("button", { name: "Remove" }));
		expect(await screen.findByText("Passkey removed")).toBeInTheDocument();
		expect(api.calls[0]?.method).toBe("DELETE");
		expect(api.calls[0]?.path).toBe("/me/passkeys/pk1");
		expect(api.calls[0]?.body).toEqual({ password: "hunter2" });
	});

	test("removing one of several passkeys is not the last second step", async () => {
		const user = userEvent.setup();
		await renderCard(
			security({ passkeys: [key(), key({ id: "pk2", name: "YubiKey" })] }),
		);
		const first = screen.getAllByRole("listitem")[0] as HTMLElement;
		await user.click(within(first).getByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Remove “MacBook”?")).toBeInTheDocument();
		expect(
			within(dialog).queryByText(/last second step/),
		).not.toBeInTheDocument();
	});

	test("removing the only passkey is not the last second step when an authenticator app is on", async () => {
		const user = userEvent.setup();
		await renderCard(security({ passkeys: [key()], totpEnabled: true }));
		await user.click(screen.getByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).queryByText(/last second step/),
		).not.toBeInTheDocument();
	});
});
