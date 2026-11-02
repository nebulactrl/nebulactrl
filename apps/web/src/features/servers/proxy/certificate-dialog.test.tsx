import { describe, expect, mock, test } from "bun:test";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { CertificateDialog } from "./certificate-dialog";

const UPLOAD = "POST /servers/:serverId/certificates";

const CERT = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----";
const KEY = "-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----";
const ENCRYPTED_KEY =
	"-----BEGIN ENCRYPTED PRIVATE KEY-----\nMIIE\n-----END ENCRYPTED PRIVATE KEY-----";

const stored = (name: string) => ({
	id: "cert-1",
	name,
	serverId: "srv-1",
	status: "pending",
	change: "add",
	domains: ["a.example.com"],
	fingerprint: "ABCD",
	issuer: "CA",
	notBefore: "2026-01-01T00:00:00Z",
	notAfter: "2027-01-01T00:00:00Z",
	createdAt: "2026-01-01T00:00:00Z",
	appliedAt: null,
	daysToExpiry: 100,
	statusMessage: "",
});

const problem = (status: number, code: string, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code },
});

async function open(onOpenChange = mock((_open: boolean) => {})) {
	await renderWithProviders(
		<CertificateDialog
			serverId="srv-1"
			serverName="edge-1"
			open
			onOpenChange={onOpenChange}
		/>,
	);
	return onOpenChange;
}

const nameInput = () => screen.getByLabelText("Name");
const certArea = () => screen.getByLabelText("Certificate (PEM)");
const keyArea = () => screen.getByLabelText("Private key (PEM)");
const submit = () => screen.getByRole("button", { name: /Upload certificate/ });

function fill(name: string, cert: string, key: string) {
	fireEvent.change(nameInput(), { target: { value: name } });
	fireEvent.change(certArea(), { target: { value: cert } });
	fireEvent.change(keyArea(), { target: { value: key } });
}

describe("CertificateDialog", () => {
	test("renders nothing while closed", async () => {
		await renderWithProviders(
			<CertificateDialog
				serverId="srv-1"
				serverName="edge-1"
				open={false}
				onOpenChange={() => {}}
			/>,
		);
		expect(screen.queryByText("Upload a certificate")).not.toBeInTheDocument();
	});

	test("explains the upload and names the server it installs on", async () => {
		await open();
		expect(screen.getByText("Upload a certificate")).toBeInTheDocument();
		expect(
			screen.getByText(/It is installed on\s+edge-1 the next time/),
		).toBeInTheDocument();
		expect(nameInput()).toHaveValue("");
		expect(certArea()).toHaveValue("");
		expect(keyArea()).toHaveValue("");
	});

	test("submitting an empty form asks for the missing parts and sends nothing", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await open();
		await user.click(submit());
		expect(
			await screen.findByText("Add a name, the certificate and its key."),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("a name and certificate without a key is still incomplete", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await open();
		fireEvent.change(nameInput(), { target: { value: "wild" } });
		fireEvent.change(certArea(), { target: { value: CERT } });
		await user.click(submit());
		expect(
			await screen.findByText("Add a name, the certificate and its key."),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("rejects text that is not a PEM certificate and marks the field invalid", async () => {
		await open();
		fireEvent.change(certArea(), { target: { value: "hello" } });
		expect(
			screen.getByText("This is not a PEM certificate (BEGIN CERTIFICATE)."),
		).toBeInTheDocument();
		expect(certArea()).toHaveAttribute("aria-invalid", "true");
		fireEvent.change(certArea(), { target: { value: CERT } });
		expect(
			screen.queryByText(/This is not a PEM certificate/),
		).not.toBeInTheDocument();
		expect(certArea()).toHaveAttribute("aria-invalid", "false");
	});

	test("a pasted invalid certificate blocks the upload", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await open();
		fill("wild", "hello", KEY);
		await user.click(submit());
		expect(
			await screen.findByText("Add a name, the certificate and its key."),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("refuses a passphrase-protected key with the fix", async () => {
		await open();
		fireEvent.change(keyArea(), { target: { value: ENCRYPTED_KEY } });
		expect(screen.getByText(/passphrase-protected/)).toBeInTheDocument();
		expect(screen.getByText(/openssl rsa -in key.pem/)).toBeInTheDocument();
		// It is not accepted, so the field stays editable.
		expect(screen.queryByText("Private key added")).not.toBeInTheDocument();
	});

	test("rejects a key that is not a PEM private key", async () => {
		await open();
		fireEvent.change(keyArea(), { target: { value: CERT } });
		expect(
			screen.getByText("This is not a PEM private key (BEGIN PRIVATE KEY)."),
		).toBeInTheDocument();
	});

	test("a valid key is locked away behind a write-only chip, and Replace brings the field back empty", async () => {
		const user = userEvent.setup();
		await open();
		fireEvent.change(keyArea(), { target: { value: KEY } });
		expect(screen.getByText(/Private key added/)).toBeInTheDocument();
		expect(
			screen.getByText("write-only, never shown again"),
		).toBeInTheDocument();
		expect(
			screen.queryByLabelText("Private key (PEM)"),
		).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Private key file")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Replace" }));
		expect(keyArea()).toHaveValue("");
		expect(screen.queryByText(/Private key added/)).not.toBeInTheDocument();
		expect(screen.getByLabelText("Private key file")).toBeInTheDocument();
	});

	test("uploads the trimmed values with an idempotency key, toasts and closes", async () => {
		let key: string | null = null;
		const api = stubApi({
			[UPLOAD]: (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json(stored("wild"), { status: 201 });
			},
		});
		const user = userEvent.setup();
		const onOpenChange = await open();
		fill("  wild  ", `\n${CERT}\n\n`, ` ${KEY} `);
		await user.click(submit());
		expect(
			await screen.findByText("Certificate wild staged"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Deploy the proxy on edge-1 to install it."),
		).toBeInTheDocument();
		expect(onOpenChange).toHaveBeenCalledWith(false);
		const sent = api.calls.filter((c) => c.method === "POST");
		expect(sent).toHaveLength(1);
		expect(sent[0]?.path).toBe("/servers/srv-1/certificates");
		expect(sent[0]?.body).toEqual({ name: "wild", certPem: CERT, keyPem: KEY });
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		expect(api.unmatched).toEqual([]);
	});

	test("shows the control plane's rejection, stays open, and retries with the same idempotency key", async () => {
		const keys: Array<string | null> = [];
		let attempt = 0;
		stubApi({
			[UPLOAD]: (request) => {
				keys.push(request.headers.get("Idempotency-Key"));
				attempt += 1;
				return attempt === 1
					? Response.json(
							{
								status: 422,
								title: "Error",
								code: "certificate_mismatch",
								detail: "The key does not match the certificate",
							},
							{ status: 422 },
						)
					: Response.json(stored("wild"), { status: 201 });
			},
		});
		const user = userEvent.setup();
		const onOpenChange = await open();
		fill("wild", CERT, KEY);
		await user.click(submit());
		expect(
			await screen.findByText("The key does not match the certificate"),
		).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
		await user.click(submit());
		await screen.findByText("Certificate wild staged");
		expect(keys).toHaveLength(2);
		expect(keys[0]).toBe(keys[1]);
		expect(
			screen.queryByText("The key does not match the certificate"),
		).not.toBeInTheDocument();
	});

	test("an agent_too_old answer shows the upgrade banner and no plain error line", async () => {
		stubApi({ [UPLOAD]: problem(422, "agent_too_old", "agent is too old") });
		const user = userEvent.setup();
		await open();
		fill("wild", CERT, KEY);
		await user.click(submit());
		const banner = await screen.findByTestId("agent-too-old");
		expect(banner).toHaveTextContent("The agent on edge-1 is too old for this");
		expect(screen.queryByText("agent is too old")).not.toBeInTheDocument();
	});

	test("Cancel closes without uploading", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		const onOpenChange = await open();
		fill("wild", CERT, KEY);
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls).toEqual([]);
	});

	test("Choose file loads a PEM file into the certificate field", async () => {
		const user = userEvent.setup();
		await open();
		await user.upload(
			screen.getByLabelText("Certificate file"),
			new File([CERT], "site.pem", { type: "text/plain" }),
		);
		await waitFor(() => expect(certArea()).toHaveValue(CERT));
	});

	test("a key file is accepted and locked like a pasted key", async () => {
		const user = userEvent.setup();
		await open();
		await user.upload(
			screen.getByLabelText("Private key file"),
			new File([KEY], "site.key", { type: "text/plain" }),
		);
		expect(await screen.findByText(/Private key added/)).toBeInTheDocument();
	});

	test("an encrypted key file is refused and its text stays in the editable field", async () => {
		const user = userEvent.setup();
		await open();
		await user.upload(
			screen.getByLabelText("Private key file"),
			new File([ENCRYPTED_KEY], "site.key", { type: "text/plain" }),
		);
		await waitFor(() => expect(keyArea()).toHaveValue(ENCRYPTED_KEY));
		expect(screen.getByText(/passphrase-protected/)).toBeInTheDocument();
		expect(screen.queryByText(/Private key added/)).not.toBeInTheDocument();
	});

	test("the file buttons open the matching file picker", async () => {
		const user = userEvent.setup();
		await open();
		const picker = screen.getByLabelText(
			"Certificate file",
		) as HTMLInputElement;
		let clicked = 0;
		picker.addEventListener("click", () => {
			clicked += 1;
		});
		const [certButton] = screen.getAllByRole("button", { name: "Choose file" });
		if (!certButton) throw new Error("no Choose file button");
		await user.click(certButton);
		expect(clicked).toBe(1);
	});
});
