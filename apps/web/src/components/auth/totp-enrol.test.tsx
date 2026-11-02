import { describe, expect, it, jest } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { TotpEnrol, TotpEnrolDialog } from "./totp-enrol";

const SETUP = {
	secret: "JBSWY3DPEHPK3PXP",
	uri: "otpauth://totp/NebulaCtrl:a@b.c?secret=JBSWY3DPEHPK3PXP&issuer=NebulaCtrl",
};
const CODES = ["aaaa-1111-bbbb-2222", "cccc-3333-dddd-4444"];

function routes(extra: Parameters<typeof stubApi>[0] = {}) {
	return stubApi({
		"POST /me/2fa/setup": { body: SETUP },
		"POST /me/2fa/enable": { body: { recoveryCodes: CODES } },
		...extra,
	});
}

const codeInput = () => screen.getByLabelText(/Enter the 6-digit code/);

describe("TotpEnrol", () => {
	it("asks the server for a new secret once and shows it grouped, with the QR code", async () => {
		const api = routes();
		await renderWithProviders(
			<TotpEnrol account="a@b.c" onFinished={() => {}} />,
		);
		expect(await screen.findByTestId("totp-secret")).toHaveTextContent(
			"JBSW Y3DP EHPK 3PXP",
		);
		expect(
			screen.getByRole("img", { name: "QR code for your authenticator app" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: /Open in your authenticator app/ }),
		).toHaveAttribute("href", SETUP.uri);
		expect(api.calls.filter((c) => c.path === "/me/2fa/setup")).toHaveLength(1);
	});

	it("keeps the submit locked until the secret has loaded and the code is plausible", async () => {
		routes();
		const user = userEvent.setup();
		await renderWithProviders(
			<TotpEnrol account="a@b.c" onFinished={() => {}} />,
		);
		await screen.findByTestId("totp-secret");
		const submit = screen.getByRole("button", { name: "Verify and turn on" });
		expect(submit).toBeDisabled();
		await user.type(codeInput(), "12345");
		expect(submit).toBeDisabled();
		await user.type(codeInput(), "6");
		expect(submit).toBeEnabled();
	});

	it("shows the load failure with a retry that asks the server again", async () => {
		let attempts = 0;
		const api = stubApi({
			"POST /me/2fa/setup": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ code: "internal", detail: "setup is down", status: 500 },
							{ status: 500 },
						)
					: Response.json(SETUP);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<TotpEnrol account="a@b.c" onFinished={() => {}} />,
		);
		expect(await screen.findByText("setup is down")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(await screen.findByTestId("totp-secret")).toBeInTheDocument();
		expect(api.calls).toHaveLength(2);
	});

	it("enables with the code without spaces, then shows the recovery codes", async () => {
		const api = routes();
		const onCodesShown = jest.fn();
		const user = userEvent.setup();
		await renderWithProviders(
			<TotpEnrol
				account="a@b.c"
				onFinished={() => {}}
				onCodesShown={onCodesShown}
			/>,
		);
		await screen.findByTestId("totp-secret");
		await user.type(codeInput(), "123 456");
		await user.click(
			screen.getByRole("button", { name: "Verify and turn on" }),
		);
		const list = await screen.findByRole("list", { name: "Recovery codes" });
		expect(
			within(list)
				.getAllByRole("listitem")
				.map((li) => li.textContent),
		).toEqual(CODES);
		expect(onCodesShown).toHaveBeenCalledTimes(1);
		expect(api.calls.find((c) => c.path === "/me/2fa/enable")?.body).toEqual({
			code: "123456",
		});
	});

	it("shows the server's reason for a rejected code and stays on the form", async () => {
		routes({
			"POST /me/2fa/enable": {
				status: 400,
				body: {
					code: "invalid_code",
					detail: "That code did not match.",
					status: 400,
				},
			},
		});
		const user = userEvent.setup();
		const onCodesShown = jest.fn();
		await renderWithProviders(
			<TotpEnrol
				account="a@b.c"
				onFinished={() => {}}
				onCodesShown={onCodesShown}
			/>,
		);
		await screen.findByTestId("totp-secret");
		await user.type(codeInput(), "123456");
		await user.click(
			screen.getByRole("button", { name: "Verify and turn on" }),
		);
		expect(
			await screen.findByText("That code did not match."),
		).toBeInTheDocument();
		expect(onCodesShown).not.toHaveBeenCalled();
		expect(
			screen.getByRole("button", { name: "Verify and turn on" }),
		).toBeEnabled();
	});

	it("does not submit an implausible code with Enter", async () => {
		const api = routes();
		const user = userEvent.setup();
		await renderWithProviders(
			<TotpEnrol account="a@b.c" onFinished={() => {}} />,
		);
		await screen.findByTestId("totp-secret");
		await user.type(codeInput(), "12{Enter}");
		expect(api.calls.filter((c) => c.path === "/me/2fa/enable")).toHaveLength(
			0,
		);
	});

	it("refreshes the security status only when the person leaves the recovery codes", async () => {
		const queryClient = testQueryClient();
		const api = routes({
			"GET /me/security": {
				body: make("SecurityBody", { enrollmentNeeded: false }),
			},
		});
		const invalidate = jest.spyOn(queryClient, "invalidateQueries");
		const onFinished = jest.fn();
		const user = userEvent.setup();
		await renderWithProviders(
			<TotpEnrol
				account="a@b.c"
				doneLabel="Continue"
				onFinished={onFinished}
			/>,
			{ queryClient },
		);
		await screen.findByTestId("totp-secret");
		await user.type(codeInput(), "123456");
		await user.click(
			screen.getByRole("button", { name: "Verify and turn on" }),
		);
		await screen.findByRole("list", { name: "Recovery codes" });
		expect(invalidate).not.toHaveBeenCalled();
		expect(onFinished).not.toHaveBeenCalled();

		await user.click(
			screen.getByRole("checkbox", { name: /I stored these codes/ }),
		);
		await user.click(screen.getByRole("button", { name: "Continue" }));
		await waitFor(() => expect(onFinished).toHaveBeenCalledTimes(1));
		expect(invalidate).toHaveBeenCalledTimes(1);
		expect(api.unmatched).toEqual([]);
	});

	it("offers Cancel only when the caller handles it", async () => {
		routes();
		const onCancel = jest.fn();
		const user = userEvent.setup();
		const first = await renderWithProviders(
			<TotpEnrol account="a" onFinished={() => {}} />,
		);
		await screen.findByTestId("totp-secret");
		expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
		first.unmount();
		routes();
		await renderWithProviders(
			<TotpEnrol account="a" onFinished={() => {}} onCancel={onCancel} />,
		);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(onCancel).toHaveBeenCalledTimes(1);
	});
});

function DialogHost({ initiallyOpen = true }: { initiallyOpen?: boolean }) {
	const [open, setOpen] = useState(initiallyOpen);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				Open
			</button>
			<output aria-label="state">{open ? "open" : "closed"}</output>
			<TotpEnrolDialog open={open} onOpenChange={setOpen} account="a@b.c" />
		</>
	);
}

describe("TotpEnrolDialog", () => {
	it("is titled for turning on two-factor authentication", async () => {
		routes();
		await renderWithProviders(<DialogHost />);
		expect(
			await screen.findByRole("dialog", {
				name: "Turn on two-factor authentication",
			}),
		).toBeInTheDocument();
	});

	it("starts no enrolment while closed", async () => {
		const api = routes();
		await renderWithProviders(<DialogHost initiallyOpen={false} />);
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(api.calls).toHaveLength(0);
	});

	it("closes on Cancel before anything was enabled", async () => {
		routes();
		const user = userEvent.setup();
		await renderWithProviders(<DialogHost />);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(screen.getByLabelText("state")).toHaveTextContent("closed");
	});

	it("closes on Escape before anything was enabled", async () => {
		routes();
		const user = userEvent.setup();
		await renderWithProviders(<DialogHost />);
		await screen.findByTestId("totp-secret");
		await user.keyboard("{Escape}");
		await waitFor(() =>
			expect(screen.getByLabelText("state")).toHaveTextContent("closed"),
		);
	});

	it("refuses Escape while the one-time recovery codes are on screen", async () => {
		routes();
		const user = userEvent.setup();
		await renderWithProviders(<DialogHost />);
		await screen.findByTestId("totp-secret");
		await user.type(codeInput(), "123456");
		await user.click(
			screen.getByRole("button", { name: "Verify and turn on" }),
		);
		await screen.findByRole("list", { name: "Recovery codes" });
		await user.keyboard("{Escape}");
		expect(screen.getByLabelText("state")).toHaveTextContent("open");
		expect(
			screen.getByRole("list", { name: "Recovery codes" }),
		).toBeInTheDocument();
	});

	it("closes once the codes are stored and Done is pressed", async () => {
		routes({ "GET /me/security": { body: make("SecurityBody") } });
		const user = userEvent.setup();
		await renderWithProviders(<DialogHost />);
		await screen.findByTestId("totp-secret");
		await user.type(codeInput(), "123456");
		await user.click(
			screen.getByRole("button", { name: "Verify and turn on" }),
		);
		await screen.findByRole("list", { name: "Recovery codes" });
		await user.click(
			screen.getByRole("checkbox", { name: /I stored these codes/ }),
		);
		await user.click(screen.getByRole("button", { name: "Done" }));
		await waitFor(() =>
			expect(screen.getByLabelText("state")).toHaveTextContent("closed"),
		);
	});
});
