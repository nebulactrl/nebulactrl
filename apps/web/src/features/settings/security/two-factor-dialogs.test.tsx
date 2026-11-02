import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { getMySecurityQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders, testQueryClient } from "#/test/render";
import {
	DisableTwoFactorDialog,
	RegenerateCodesDialog,
} from "./two-factor-dialogs";

const problem = (status: number, detail: string, code = "bad_request") => ({
	status,
	body: { status, title: "Error", detail, code },
});

const RECOVERY = "abcd-efgh-ijkl-mnop";

describe("DisableTwoFactorDialog", () => {
	test("needs a plausible code and the password before Turn off is enabled", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<DisableTwoFactorDialog
				open
				onOpenChange={() => {}}
				hasPassword
				requiredByTeams={[]}
			/>,
		);
		const turnOff = await screen.findByRole("button", { name: "Turn off" });
		expect(turnOff).toBeDisabled();

		const code = screen.getByLabelText("Authenticator or recovery code");
		await user.type(code, "123 45");
		await user.type(screen.getByLabelText("Password"), "hunter2");
		expect(turnOff).toBeDisabled();

		await user.type(code, "6");
		expect(turnOff).toBeEnabled();

		await user.clear(screen.getByLabelText("Password"));
		expect(turnOff).toBeDisabled();
	});

	test("sends the code without spaces and the password, then confirms and closes", async () => {
		const api = stubApi({ "POST /me/2fa/disable": { status: 204 } });
		const onOpenChange = mock((_open: boolean) => {});
		const queryClient = testQueryClient();
		queryClient.setQueryData(getMySecurityQueryKey(), { totpEnabled: true });
		const user = userEvent.setup();
		await renderWithProviders(
			<DisableTwoFactorDialog
				open
				onOpenChange={onOpenChange}
				hasPassword
				requiredByTeams={[]}
			/>,
			{ queryClient },
		);
		await user.type(
			await screen.findByLabelText("Authenticator or recovery code"),
			"123 456",
		);
		await user.type(screen.getByLabelText("Password"), "hunter2");
		await user.click(screen.getByRole("button", { name: "Turn off" }));

		expect(
			await screen.findByText("Two-step verification is off"),
		).toBeInTheDocument();
		expect(api.calls).toEqual([
			{
				method: "POST",
				path: "/me/2fa/disable",
				query: expect.anything(),
				body: { code: "123456", password: "hunter2" },
			},
		]);
		expect(onOpenChange).toHaveBeenCalledWith(false);
		await waitFor(() =>
			expect(
				queryClient.getQueryState(getMySecurityQueryKey())?.isInvalidated,
			).toBe(true),
		);
		expect(api.unmatched).toEqual([]);
	});

	test("an account without a password sends only the code, accepting a recovery code", async () => {
		const api = stubApi({ "POST /me/2fa/disable": { status: 204 } });
		const user = userEvent.setup();
		await renderWithProviders(
			<DisableTwoFactorDialog
				open
				onOpenChange={() => {}}
				hasPassword={false}
				requiredByTeams={[]}
			/>,
		);
		expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
		await user.type(
			await screen.findByLabelText("Authenticator or recovery code"),
			RECOVERY,
		);
		await user.click(screen.getByRole("button", { name: "Turn off" }));
		await screen.findByText("Two-step verification is off");
		expect(api.calls[0]?.body).toEqual({ code: RECOVERY });
	});

	test("shows the server's reason and stays open when the code is wrong", async () => {
		stubApi({
			"POST /me/2fa/disable": problem(401, "That code is not valid."),
		});
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderWithProviders(
			<DisableTwoFactorDialog
				open
				onOpenChange={onOpenChange}
				hasPassword={false}
				requiredByTeams={[]}
			/>,
		);
		await user.type(
			await screen.findByLabelText("Authenticator or recovery code"),
			"000000",
		);
		await user.click(screen.getByRole("button", { name: "Turn off" }));
		expect(
			await screen.findByText("That code is not valid."),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Two-step verification is off"),
		).not.toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
	});

	test("warns which teams require it, singular and plural", async () => {
		const one = await renderWithProviders(
			<DisableTwoFactorDialog
				open
				onOpenChange={() => {}}
				hasPassword={false}
				requiredByTeams={["Platform"]}
			/>,
		);
		expect(await screen.findByText(/Platform\s+requires it\./)).toBeVisible();
		one.unmount();

		await renderWithProviders(
			<DisableTwoFactorDialog
				open
				onOpenChange={() => {}}
				hasPassword={false}
				requiredByTeams={["Platform", "Data"]}
			/>,
		);
		expect(
			await screen.findByText(/Platform, Data\s+require it\./),
		).toBeVisible();
	});

	test("Keep it on closes without a request", async () => {
		const api = stubApi({});
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderWithProviders(
			<DisableTwoFactorDialog
				open
				onOpenChange={onOpenChange}
				hasPassword
				requiredByTeams={[]}
			/>,
		);
		await user.click(await screen.findByRole("button", { name: "Keep it on" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls).toEqual([]);
	});

	test("pressing Enter in an incomplete form sends nothing", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderWithProviders(
			<DisableTwoFactorDialog
				open
				onOpenChange={() => {}}
				hasPassword
				requiredByTeams={[]}
			/>,
		);
		await user.type(
			await screen.findByLabelText("Authenticator or recovery code"),
			"12{Enter}",
		);
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});
});

describe("RegenerateCodesDialog", () => {
	test("with an authenticator it posts the normalised code and shows the new codes once", async () => {
		const api = stubApi({
			"POST /me/2fa/recovery-codes": {
				body: { recoveryCodes: ["aaaa-1111", "bbbb-2222"] },
			},
		});
		const onOpenChange = mock((_open: boolean) => {});
		const queryClient = testQueryClient();
		queryClient.setQueryData(getMySecurityQueryKey(), { totpEnabled: true });
		const user = userEvent.setup();
		await renderWithProviders(
			<RegenerateCodesDialog
				open
				onOpenChange={onOpenChange}
				account="ada@example.com"
			/>,
			{ queryClient },
		);
		const create = await screen.findByRole("button", {
			name: "Create new codes",
		});
		expect(create).toBeDisabled();
		await user.type(
			screen.getByLabelText("Authenticator or recovery code"),
			"654 321",
		);
		await user.click(create);

		expect(
			await screen.findByText("Your new recovery codes"),
		).toBeInTheDocument();
		expect(api.calls[0]?.method).toBe("POST");
		expect(api.calls[0]?.path).toBe("/me/2fa/recovery-codes");
		expect(api.calls[0]?.body).toEqual({ code: "654321" });
		expect(screen.getByText("aaaa-1111")).toBeInTheDocument();
		expect(screen.getByText("bbbb-2222")).toBeInTheDocument();

		// Escape must not lose codes that are shown only once.
		await user.keyboard("{Escape}");
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(screen.getByText("Your new recovery codes")).toBeInTheDocument();

		const done = screen.getByRole("button", { name: "Done" });
		expect(done).toBeDisabled();
		await user.click(
			screen.getByRole("checkbox", {
				name: "I stored these codes somewhere safe",
			}),
		);
		await user.click(done);
		expect(onOpenChange).toHaveBeenCalledWith(false);
		await waitFor(() =>
			expect(
				queryClient.getQueryState(getMySecurityQueryKey())?.isInvalidated,
			).toBe(true),
		);
	});

	test("a passkey-only account proves itself with its password", async () => {
		const api = stubApi({
			"POST /me/2fa/recovery-codes": { body: { recoveryCodes: ["cccc-3333"] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<RegenerateCodesDialog
				open
				onOpenChange={() => {}}
				account="ada@example.com"
				byPassword
			/>,
		);
		expect(
			screen.queryByLabelText("Authenticator or recovery code"),
		).not.toBeInTheDocument();
		const create = await screen.findByRole("button", {
			name: "Create new codes",
		});
		expect(create).toBeDisabled();
		await user.type(screen.getByLabelText("Password"), "hunter2");
		await user.click(create);
		expect(await screen.findByText("cccc-3333")).toBeInTheDocument();
		expect(api.calls[0]?.body).toEqual({ password: "hunter2" });
	});

	test("shows the server's reason and keeps the form when the request fails", async () => {
		stubApi({
			"POST /me/2fa/recovery-codes": problem(401, "Wrong password."),
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<RegenerateCodesDialog
				open
				onOpenChange={() => {}}
				account="ada@example.com"
				byPassword
			/>,
		);
		await user.type(await screen.findByLabelText("Password"), "nope");
		await user.click(screen.getByRole("button", { name: "Create new codes" }));
		expect(await screen.findByText("Wrong password.")).toBeInTheDocument();
		expect(screen.getByText("Replace recovery codes")).toBeInTheDocument();
	});

	test("Cancel closes while the codes are not yet shown", async () => {
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderWithProviders(
			<RegenerateCodesDialog
				open
				onOpenChange={onOpenChange}
				account="ada@example.com"
			/>,
		);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("reopening starts from an empty form", async () => {
		const user = userEvent.setup();
		const view = await renderWithProviders(
			<RegenerateCodesDialog
				open
				onOpenChange={() => {}}
				account="ada@example.com"
			/>,
		);
		await user.type(
			await screen.findByLabelText("Authenticator or recovery code"),
			"123456",
		);
		expect(
			screen.getByRole("button", { name: "Create new codes" }),
		).toBeEnabled();
		view.unmount();
		await renderWithProviders(
			<RegenerateCodesDialog
				open
				onOpenChange={() => {}}
				account="ada@example.com"
			/>,
		);
		expect(
			await screen.findByLabelText("Authenticator or recovery code"),
		).toHaveValue("");
	});
});
