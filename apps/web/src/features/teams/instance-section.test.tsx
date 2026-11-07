import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { InstanceSettings } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { MailRelaySection, SignInSection } from "./instance-section";

function instance(over: Partial<InstanceSettings> = {}): InstanceSettings {
	return {
		allowTeamCreation: true,
		canManage: true,
		mailConfigured: false,
		mailProvider: "smtp",
		...over,
	};
}

// The sign-in providers card (settings/security) loads its own lists.
const providerRoutes = {
	"GET /auth-providers": { body: { items: [] } },
	"GET /auth-settings": { body: { allowOpenSignup: false } },
};

const patches = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((c) => c.method === "PATCH");

describe("SignInSection", () => {
	test("a non-admin only reads who may change these settings", async () => {
		const api = stubApi({
			"GET /instance": { body: instance({ canManage: false }) },
			...providerRoutes,
		});
		await renderWithProviders(<SignInSection />);

		expect(
			await screen.findByText(
				/Only the instance admin, the first account created here/,
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/auth-providers")).toBe(false);
	});

	test("a failed load offers a retry that asks again", async () => {
		let attempts = 0;
		stubApi({
			"GET /instance": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, code: "internal", title: "Boom" },
							{ status: 500 },
						)
					: Response.json(instance());
			},
			...providerRoutes,
		});
		const user = userEvent.setup();
		await renderWithProviders(<SignInSection />);

		expect(
			await screen.findByText("Instance settings did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /retry|try again/i }));
		expect(
			await screen.findByRole("switch", { name: "Anyone can create a team." }),
		).toBeInTheDocument();
		expect(attempts).toBe(2);
	});

	test("the admin turns team creation off: PATCH allowTeamCreation false and a toast", async () => {
		let current = instance({ allowTeamCreation: true });
		const api = stubApi({
			"GET /instance": () => Response.json(current),
			"PATCH /instance": () => {
				current = instance({ allowTeamCreation: false });
				return Response.json(current);
			},
			...providerRoutes,
		});
		const user = userEvent.setup();
		await renderWithProviders(<SignInSection />);

		const toggle = await screen.findByRole("switch", {
			name: "Anyone can create a team.",
		});
		expect(toggle).toBeChecked();
		await user.click(toggle);

		expect(
			await screen.findByText("Team creation is admin-only"),
		).toBeInTheDocument();
		expect(patches(api)[0]).toMatchObject({
			path: "/instance",
			body: { allowTeamCreation: false },
		});
		// The refetched value relabels the switch.
		expect(
			await screen.findByRole("switch", {
				name: "Only you can create teams. Everyone else joins by invite.",
			}),
		).not.toBeChecked();
		// The providers card is mounted for the admin.
		expect(api.calls.some((c) => c.path === "/auth-providers")).toBe(true);
	});

	test("the admin opens team creation to everyone", async () => {
		let current = instance({ allowTeamCreation: false });
		const api = stubApi({
			"GET /instance": () => Response.json(current),
			"PATCH /instance": () => {
				current = instance({ allowTeamCreation: true });
				return Response.json(current);
			},
			...providerRoutes,
		});
		const user = userEvent.setup();
		await renderWithProviders(<SignInSection />);

		await user.click(
			await screen.findByRole("switch", {
				name: "Only you can create teams. Everyone else joins by invite.",
			}),
		);

		expect(
			await screen.findByText("Anyone can create teams"),
		).toBeInTheDocument();
		expect(patches(api)[0]?.body).toEqual({ allowTeamCreation: true });
	});
});

describe("MailRelaySection", () => {
	const field = (label: string) => screen.findByLabelText(label);

	test("a non-admin cannot see the relay form", async () => {
		stubApi({ "GET /instance": { body: instance({ canManage: false }) } });
		await renderWithProviders(<MailRelaySection />);
		expect(
			await screen.findByText(/Only the instance admin/),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("SMTP host")).not.toBeInTheDocument();
	});

	test("shows the stored relay and says invites are mailed through it", async () => {
		stubApi({
			"GET /instance": {
				body: instance({
					mailConfigured: true,
					smtpHost: "smtp.example.com",
					smtpPort: 465,
					smtpTls: "implicit",
					smtpUsername: "mailer",
					mailFrom: "Nebula <no-reply@example.com>",
					smtpPasswordSet: true,
				}),
			},
		});
		await renderWithProviders(<MailRelaySection />);

		expect(await field("SMTP host")).toHaveValue("smtp.example.com");
		expect(screen.getByLabelText("Port")).toHaveValue("465");
		expect(screen.getByLabelText("User name")).toHaveValue("mailer");
		expect(screen.getByLabelText("Sender")).toHaveValue(
			"Nebula <no-reply@example.com>",
		);
		expect(screen.getByLabelText("Password")).toHaveValue("");
		expect(screen.getByLabelText("Password")).toHaveAttribute(
			"placeholder",
			"•••••••• (set)",
		);
		expect(screen.getByLabelText("Connection security")).toHaveTextContent(
			"TLS on connect (port 465)",
		);
		expect(
			screen.getByText("Invites can be emailed through this relay."),
		).toBeInTheDocument();
	});

	test("an unconfigured relay says invites are link-only and starts on STARTTLS 587", async () => {
		stubApi({ "GET /instance": { body: instance() } });
		await renderWithProviders(<MailRelaySection />);

		expect(await field("SMTP host")).toHaveValue("");
		expect(screen.getByLabelText("Port")).toHaveValue("587");
		expect(screen.getByLabelText("Connection security")).toHaveTextContent(
			"STARTTLS (port 587)",
		);
		expect(
			screen.getByText("Not configured: invites are link-only."),
		).toBeInTheDocument();
	});

	test("saving sends the trimmed fields and omits an empty password", async () => {
		const api = stubApi({
			"GET /instance": { body: instance({ smtpPasswordSet: true }) },
			"PATCH /instance": { body: instance() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<MailRelaySection />);

		await user.type(await field("SMTP host"), "  smtp.example.com ");
		await user.type(screen.getByLabelText("User name"), " mailer ");
		await user.type(screen.getByLabelText("Sender"), " no-reply@example.com ");
		await user.click(screen.getByRole("button", { name: "Save mail relay" }));

		expect(await screen.findByText("Email relay saved")).toBeInTheDocument();
		expect(patches(api)).toHaveLength(1);
		expect(patches(api)[0]?.path).toBe("/instance");
		// No smtpPassword key: the stored password is kept.
		expect(patches(api)[0]?.body).toEqual({
			mailProvider: "smtp",
			mailFrom: "no-reply@example.com",
			smtpHost: "smtp.example.com",
			smtpTls: "starttls",
			smtpUsername: "mailer",
			smtpPort: 587,
		});
	});

	test("a Resend relay saves its provider, sender and key without any SMTP field", async () => {
		const api = stubApi({
			"GET /instance": { body: instance() },
			"PATCH /instance": { body: instance() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<MailRelaySection />);

		await user.click(await screen.findByRole("button", { name: "Resend" }));
		expect(screen.queryByLabelText("SMTP host")).not.toBeInTheDocument();
		await user.type(screen.getByLabelText("Sender"), " no-reply@example.com ");
		await user.type(screen.getByLabelText("API key"), " re_abc ");
		await user.click(screen.getByRole("button", { name: "Save mail relay" }));

		expect(await screen.findByText("Email relay saved")).toBeInTheDocument();
		expect(patches(api)[0]?.body).toEqual({
			mailProvider: "resend",
			mailFrom: "no-reply@example.com",
			resendApiKey: "re_abc",
		});
		await waitFor(() =>
			expect(screen.getByLabelText("API key")).toHaveValue(""),
		);
	});

	test("a stored Resend key is shown as kept and not sent again when left empty", async () => {
		const api = stubApi({
			"GET /instance": {
				body: instance({
					mailProvider: "resend",
					mailFrom: "no-reply@example.com",
					resendApiKeySet: true,
					mailConfigured: true,
				}),
			},
			"PATCH /instance": { body: instance() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<MailRelaySection />);

		const key = await screen.findByLabelText("API key");
		expect(key).toHaveValue("");
		expect(key).toHaveAttribute("placeholder", "Stored — leave empty to keep");
		expect(screen.getByLabelText("Sender")).toHaveValue("no-reply@example.com");
		await user.click(screen.getByRole("button", { name: "Save mail relay" }));

		await screen.findByText("Email relay saved");
		expect(patches(api)[0]?.body).toEqual({
			mailProvider: "resend",
			mailFrom: "no-reply@example.com",
		});
	});

	test("a typed password is sent once and then cleared from the form", async () => {
		const api = stubApi({
			"GET /instance": { body: instance() },
			"PATCH /instance": { body: instance() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<MailRelaySection />);

		await user.type(await field("SMTP host"), "smtp.example.com");
		await user.type(screen.getByLabelText("Password"), "s3cret");
		await user.click(screen.getByRole("button", { name: "Save mail relay" }));

		await screen.findByText("Email relay saved");
		expect(patches(api)[0]?.body).toMatchObject({ smtpPassword: "s3cret" });
		await waitFor(() =>
			expect(screen.getByLabelText("Password")).toHaveValue(""),
		);
	});

	test("the port field keeps digits only and an empty port is not sent", async () => {
		const api = stubApi({
			"GET /instance": { body: instance() },
			"PATCH /instance": { body: instance() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<MailRelaySection />);

		const port = await field("Port");
		await user.clear(port);
		await user.type(port, "2a5b25");
		expect(port).toHaveValue("2525");
		await user.clear(port);
		await user.click(screen.getByRole("button", { name: "Save mail relay" }));

		await screen.findByText("Email relay saved");
		expect(patches(api)[0]?.body).not.toHaveProperty("smtpPort");
	});

	test("choosing a connection security resets the port to its default", async () => {
		const api = stubApi({
			"GET /instance": { body: instance() },
			"PATCH /instance": { body: instance() },
		});
		const user = userEvent.setup();
		await renderWithProviders(<MailRelaySection />);

		await user.click(await field("Connection security"));
		await user.click(
			await screen.findByRole("option", { name: "TLS on connect (port 465)" }),
		);
		expect(screen.getByLabelText("Port")).toHaveValue("465");

		await user.click(screen.getByLabelText("Connection security"));
		await user.click(
			await screen.findByRole("option", {
				name: "None (port 25, trusted networks only)",
			}),
		);
		expect(screen.getByLabelText("Port")).toHaveValue("25");

		await user.click(screen.getByRole("button", { name: "Save mail relay" }));
		await screen.findByText("Email relay saved");
		expect(patches(api)[0]?.body).toMatchObject({
			smtpTls: "none",
			smtpPort: 25,
		});
	});

	test("a rejected save shows the reason and no success toast", async () => {
		stubApi({
			"GET /instance": { body: instance() },
			"PATCH /instance": {
				status: 422,
				body: {
					status: 422,
					code: "invalid_smtp",
					title: "Unprocessable",
					detail: "smtp host is required",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<MailRelaySection />);
		await user.click(
			await screen.findByRole("button", { name: "Save mail relay" }),
		);

		expect(
			await screen.findByText("smtp host is required"),
		).toBeInTheDocument();
		expect(screen.queryByText("Email relay saved")).not.toBeInTheDocument();
	});
});
