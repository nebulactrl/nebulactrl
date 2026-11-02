import {
	afterEach,
	beforeEach,
	describe,
	expect,
	type Mock,
	spyOn,
	test,
} from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { getMySecurityQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import type { IdentityBody } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { LinkedAccountsCard } from "./linked-accounts-card";

const problem = (status: number, detail: string, code = "bad_request") => ({
	status,
	body: { status, title: "Error", detail, code },
});

const okta = { id: "p-okta", name: "Okta" };
const github = { id: "p-gh", name: "GitHub" };

const identity = (over: Partial<IdentityBody> = {}): IdentityBody => ({
	id: "i1",
	email: "ada@corp.example",
	linkedAt: "2026-01-01T00:00:00Z",
	providerId: "p-okta",
	providerKind: "oidc",
	providerName: "Okta",
	...over,
});

let assign: Mock<Location["assign"]>;

beforeEach(() => {
	assign = spyOn(window.location, "assign").mockImplementation(() => {});
});

afterEach(() => {
	assign.mockRestore();
});

describe("LinkedAccountsCard list", () => {
	test("shows each linked account with its email and last use", async () => {
		await renderWithProviders(
			<LinkedAccountsCard
				identities={[
					identity({ lastLoginAt: "2026-03-04T05:06:07Z" }),
					identity({
						id: "i2",
						providerId: "p-gh",
						providerName: "GitHub",
						email: "ada@users.example",
					}),
				]}
				providers={[okta, github]}
				hasPassword
			/>,
		);
		expect(
			screen.getByText(
				/Accounts at an identity provider that can sign in as you/,
			),
		).toBeInTheDocument();
		const [first, second] = screen.getAllByRole("listitem");
		expect(first).toHaveTextContent("Okta");
		expect(first).toHaveTextContent("ada@corp.example · last used");
		expect(first?.querySelector("time")).toHaveAttribute(
			"datetime",
			"2026-03-04T05:06:07Z",
		);
		expect(second).toHaveTextContent("GitHub");
		expect(second).toHaveTextContent("ada@users.example");
		expect(second).not.toHaveTextContent("last used");
		// Both providers are linked, so there is nothing left to link.
		expect(
			screen.queryByRole("button", { name: /^Link/ }),
		).not.toBeInTheDocument();
	});

	test("with nothing linked and one provider it invites a single link button", async () => {
		await renderWithProviders(
			<LinkedAccountsCard identities={[]} providers={[okta]} hasPassword />,
		);
		expect(
			screen.getByText(
				"Link an account at your identity provider to sign in with it.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Link your SSO account" }),
		).toBeInTheDocument();
	});

	test("offers one button per provider not yet linked, named after it", async () => {
		await renderWithProviders(
			<LinkedAccountsCard
				identities={[identity()]}
				providers={[okta, github, { id: "p-kc", name: "Keycloak" }]}
				hasPassword
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Link GitHub" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Link Keycloak" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Link Okta" }),
		).not.toBeInTheDocument();
	});
});

describe("LinkedAccountsCard linking", () => {
	test("asks for the password, starts the link and hands the browser to the provider", async () => {
		const api = stubApi({
			"POST /me/identities/link": {
				body: { url: "https://idp.example/authorize?state=abc" },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<LinkedAccountsCard identities={[]} providers={[okta]} hasPassword />,
		);
		await user.click(
			screen.getByRole("button", { name: "Link your SSO account" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Link Okta")).toBeInTheDocument();
		const next = within(dialog).getByRole("button", { name: "Continue" });
		expect(next).toBeDisabled();
		await user.type(within(dialog).getByLabelText("Password"), "hunter2");
		await user.click(next);

		await waitFor(() =>
			expect(assign).toHaveBeenCalledWith(
				"https://idp.example/authorize?state=abc",
			),
		);
		expect(api.calls).toEqual([
			{
				method: "POST",
				path: "/me/identities/link",
				query: expect.anything(),
				body: { providerId: "p-okta", password: "hunter2" },
			},
		]);
	});

	test("an account without a password links without being asked for one", async () => {
		const api = stubApi({
			"POST /me/identities/link": { body: { url: "https://idp.example/go" } },
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<LinkedAccountsCard
				identities={[]}
				providers={[okta, github]}
				hasPassword={false}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Link GitHub" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Link GitHub")).toBeInTheDocument();
		expect(within(dialog).queryByLabelText("Password")).not.toBeInTheDocument();
		await user.click(within(dialog).getByRole("button", { name: "Continue" }));
		await waitFor(() =>
			expect(assign).toHaveBeenCalledWith("https://idp.example/go"),
		);
		expect(api.calls[0]?.body).toEqual({ providerId: "p-gh" });
	});

	test("a wrong password is reported and the browser stays here", async () => {
		stubApi({
			"POST /me/identities/link": problem(
				422,
				"That password is not right.",
				"invalid_password",
			),
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<LinkedAccountsCard identities={[]} providers={[okta]} hasPassword />,
		);
		await user.click(
			screen.getByRole("button", { name: "Link your SSO account" }),
		);
		const dialog = await screen.findByRole("dialog");
		await user.type(within(dialog).getByLabelText("Password"), "nope");
		await user.click(within(dialog).getByRole("button", { name: "Continue" }));
		expect(
			await within(dialog).findByText("That password is not right."),
		).toBeInTheDocument();
		expect(assign).not.toHaveBeenCalled();
	});

	test("Cancel closes the dialog without a request", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderWithProviders(
			<LinkedAccountsCard identities={[]} providers={[okta]} hasPassword />,
		);
		await user.click(
			screen.getByRole("button", { name: "Link your SSO account" }),
		);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		expect(api.calls).toEqual([]);
	});

	test("reopening a link dialog clears the password", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<LinkedAccountsCard identities={[]} providers={[okta]} hasPassword />,
		);
		await user.click(
			screen.getByRole("button", { name: "Link your SSO account" }),
		);
		await user.type(await screen.findByLabelText("Password"), "secret");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		await user.click(
			screen.getByRole("button", { name: "Link your SSO account" }),
		);
		expect(await screen.findByLabelText("Password")).toHaveValue("");
	});
});

describe("LinkedAccountsCard unlinking", () => {
	test("confirms, then DELETEs that identity, announces it and refreshes", async () => {
		const api = stubApi({ "DELETE /me/identities/:id": { status: 204 } });
		const queryClient = testQueryClient();
		queryClient.setQueryData(getMySecurityQueryKey(), {});
		const user = userEvent.setup();
		await renderWithProviders(
			<LinkedAccountsCard
				identities={[
					identity(),
					identity({ id: "i2", providerId: "p-gh", providerName: "GitHub" }),
				]}
				providers={[okta, github]}
				hasPassword
			/>,
			{ queryClient },
		);
		const gh = screen.getAllByRole("listitem")[1] as HTMLElement;
		await user.click(within(gh).getByRole("button", { name: "Unlink" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Unlink GitHub?")).toBeInTheDocument();
		expect(api.calls).toEqual([]);
		await user.click(within(dialog).getByRole("button", { name: "Unlink" }));

		expect(await screen.findByText("Unlinked GitHub")).toBeInTheDocument();
		expect(api.calls).toEqual([
			{
				method: "DELETE",
				path: "/me/identities/i2",
				query: expect.anything(),
				body: undefined,
			},
		]);
		await waitFor(() =>
			expect(
				queryClient.getQueryState(getMySecurityQueryKey())?.isInvalidated,
			).toBe(true),
		);
	});

	test("declining the confirmation sends nothing", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderWithProviders(
			<LinkedAccountsCard
				identities={[identity()]}
				providers={[okta]}
				hasPassword
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Unlink" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls).toEqual([]);
		expect(screen.queryByText("Unlinked Okta")).not.toBeInTheDocument();
	});

	test("a failed unlink raises an error toast with the server's reason", async () => {
		stubApi({
			"DELETE /me/identities/:id": problem(
				409,
				"It is your only way to sign in.",
				"last_login_method",
			),
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<LinkedAccountsCard
				identities={[identity()]}
				providers={[okta]}
				hasPassword={false}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Unlink" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Unlink" }));
		expect(
			await screen.findByText("Could not unlink the account"),
		).toBeInTheDocument();
		// The toast carries the reason (the confirm dialog may repeat it).
		const reasons = await screen.findAllByText(
			"It is your only way to sign in.",
		);
		expect(reasons.length).toBeGreaterThanOrEqual(1);
		expect(screen.queryByText("Unlinked Okta")).not.toBeInTheDocument();
	});
});
