import { describe, expect, it, jest } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { AuthNotice } from "./auth-notice";
import { AuthCard } from "./auth-screen";
import { QrCode } from "./qr-code";
import { RecoveryCodesPanel } from "./recovery-codes";
import { SsoButtons } from "./sso-buttons";

describe("AuthNotice", () => {
	it("announces a failure as an alert", () => {
		render(<AuthNotice tone="destructive">Wrong password</AuthNotice>);
		const notice = screen.getByRole("alert");
		expect(notice).toHaveTextContent("Wrong password");
		expect(notice).toHaveAttribute("data-tone", "danger");
	});

	it("announces something that ended on its own as a warning alert", () => {
		render(<AuthNotice tone="warning">Session expired</AuthNotice>);
		expect(screen.getByRole("alert")).toHaveAttribute("data-tone", "warning");
	});

	it("announces a success politely as a status", () => {
		render(<AuthNotice tone="success">Linked</AuthNotice>);
		expect(screen.getByRole("status")).toHaveTextContent("Linked");
		expect(screen.queryByRole("alert")).toBeNull();
	});

	it("shows the notice's own action after the message", () => {
		render(
			<AuthNotice
				tone="destructive"
				action={<button type="button">Back</button>}
			>
				Expired
			</AuthNotice>,
		);
		expect(screen.getByRole("alert")).toContainElement(
			screen.getByRole("button", { name: "Back" }),
		);
	});
});

describe("SsoButtons", () => {
	it("renders nothing without providers or other ways in", () => {
		const { container } = render(<SsoButtons providers={[]} />);
		expect(container).toBeEmptyDOMElement();
	});

	it("links each provider to the server's start route", () => {
		render(
			<SsoButtons
				providers={[
					{ id: "gh", name: "GitHub", kind: "github" },
					{ id: "corp sso", name: "Acme SSO", kind: "oidc" },
				]}
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Continue with GitHub" }),
		).toHaveAttribute("href", "/api/v1/auth/oidc/gh/start");
		expect(
			screen.getByRole("button", { name: "Continue with Acme SSO" }),
		).toHaveAttribute("href", "/api/v1/auth/oidc/corp%20sso/start");
	});

	it("carries an invite through the provider round trip", () => {
		render(
			<SsoButtons
				providers={[{ id: "gh", name: "GitHub", kind: "github" }]}
				invite="inv/1"
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Continue with GitHub" }),
		).toHaveAttribute("href", "/api/v1/auth/oidc/gh/start?invite=inv%2F1");
	});

	it("shows other ways in under the same 'or' even with no providers", () => {
		render(
			<SsoButtons providers={[]}>
				<button type="button">Sign in with a passkey</button>
			</SsoButtons>,
		);
		expect(screen.getByText("or")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Sign in with a passkey" }),
		).toBeInTheDocument();
	});
});

describe("RecoveryCodesPanel", () => {
	const codes = ["aaaa-1111", "bbbb-2222", "cccc-3333"];

	it("lists every code, and the way out is called Done unless named", () => {
		render(
			<RecoveryCodesPanel codes={codes} account="a@b.c" onDone={() => {}} />,
		);
		const list = screen.getByRole("list", { name: "Recovery codes" });
		expect(
			Array.from(list.querySelectorAll("li")).map((li) => li.textContent),
		).toEqual(codes);
		expect(screen.getByRole("button", { name: "Done" })).toBeDisabled();
	});

	it("keeps the way out locked until the codes are acknowledged as stored", async () => {
		const onDone = jest.fn();
		const user = userEvent.setup();
		render(
			<RecoveryCodesPanel
				codes={codes}
				account="a@b.c"
				doneLabel="Continue"
				onDone={onDone}
			/>,
		);
		const done = screen.getByRole("button", { name: "Continue" });
		expect(done).toBeDisabled();
		await user.click(done);
		expect(onDone).not.toHaveBeenCalled();

		await user.click(
			screen.getByRole("checkbox", {
				name: "I stored these codes somewhere safe",
			}),
		);
		expect(done).toBeEnabled();
		await user.click(done);
		expect(onDone).toHaveBeenCalledTimes(1);
	});

	it("copies all codes, one per line", async () => {
		const user = userEvent.setup();
		const write = jest.spyOn(navigator.clipboard, "writeText");
		render(<RecoveryCodesPanel codes={codes} account="a" onDone={() => {}} />);
		await user.click(screen.getByRole("button", { name: /Copy all/ }));
		expect(write).toHaveBeenCalledWith(codes.join("\n"));
		write.mockRestore();
	});

	it("downloads a text file with the codes and the account", async () => {
		const user = userEvent.setup();
		let blob: Blob | undefined;
		const create = jest
			.spyOn(URL, "createObjectURL")
			.mockImplementation((b) => {
				blob = b as Blob;
				return "blob:codes";
			});
		const revoke = jest
			.spyOn(URL, "revokeObjectURL")
			.mockImplementation(() => {});
		const click = jest
			.spyOn(HTMLAnchorElement.prototype, "click")
			.mockImplementation(function (this: HTMLAnchorElement) {
				expect(this.download).toBe("nebulactrl-recovery-codes.txt");
				expect(this.href).toBe("blob:codes");
			});
		render(
			<RecoveryCodesPanel
				codes={codes}
				account="robin@example.com"
				onDone={() => {}}
			/>,
		);
		await user.click(screen.getByRole("button", { name: /Download .txt/ }));
		expect(click).toHaveBeenCalledTimes(1);
		expect(revoke).toHaveBeenCalledWith("blob:codes");
		expect(blob?.type).toBe("text/plain");
		const text = (await blob?.text()) ?? "";
		expect(text).toContain("robin@example.com");
		for (const code of codes) expect(text).toContain(code);
		create.mockRestore();
		revoke.mockRestore();
		click.mockRestore();
	});
});

describe("QrCode", () => {
	it("is a labelled image, and a different value draws a different picture", () => {
		const label = "QR code for your authenticator app";
		const { rerender } = render(<QrCode value="aaa" label={label} />);
		const picture = () =>
			screen
				.getByRole("img", { name: label })
				.querySelector("path")
				?.getAttribute("d");
		const first = picture();
		expect(first).toBeTruthy();
		rerender(<QrCode value="bbb" label={label} />);
		expect(picture()).not.toBe(first);
	});
});

describe("AuthCard", () => {
	it("shows the wordmark above the card content", async () => {
		await renderWithProviders(
			<AuthCard>
				<p>Sign in</p>
			</AuthCard>,
		);
		expect(screen.getByRole("img", { name: "NebulaCtrl" })).toBeInTheDocument();
		expect(screen.getByText("Sign in")).toBeInTheDocument();
		expect(screen.getByRole("main")).toBeInTheDocument();
	});
});
