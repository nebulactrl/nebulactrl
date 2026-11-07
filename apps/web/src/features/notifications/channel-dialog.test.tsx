import { describe, expect, mock, setDefaultTimeout, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { ChannelDialog } from "./channel-dialog";
import { channel, EVENT_TYPES, fill, problem, project } from "./test-fixtures";

// Every keystroke re-renders the whole form; leave room on a loaded machine.
setDefaultTimeout(30_000);

const CHANNELS = "/notifications/channels";
const SLACK_URL = "https://hooks.slack.com/services/T1/B2/abc";

function routes(extra: Parameters<typeof stubApi>[0] = {}) {
	return {
		"GET /notifications/event-types": { body: { items: EVENT_TYPES } },
		"GET /projects": {
			body: { items: [project("p1", "Checkout"), project("p2", "Billing")] },
		},
		...extra,
	};
}

async function openCreate(extra: Parameters<typeof stubApi>[0] = {}) {
	const api = stubApi(routes(extra));
	const onOpenChange = mock((_open: boolean) => {});
	const user = userEvent.setup();
	await renderWithProviders(
		<ChannelDialog open onOpenChange={onOpenChange} channel={null} />,
	);
	await screen.findByRole("heading", { name: "Add notification channel" });
	return { api, user, onOpenChange };
}

/** The JSON body of the first call with this method. */
function sent<T>(api: ReturnType<typeof stubApi>, method: string): T {
	const call = api.calls.find((c) => c.method === method);
	if (!call) throw new Error(`no ${method} was sent`);
	return call.body as T;
}

const submit = () => screen.getByRole("button", { name: "Add channel" });

/** Waits for the default event selection that follows the catalogue load. */
async function catalogueLoaded() {
	await waitFor(() =>
		expect(
			screen.getByRole("checkbox", { name: /Deployment failed/ }),
		).toBeChecked(),
	);
}

describe("ChannelDialog: creating", () => {
	test("starts on Slack with a disabled Add button that says what is missing", async () => {
		await openCreate();
		expect(submit()).toBeDisabled();
		expect(submit()).toHaveAttribute("title", "Give the channel a name.");
		expect(screen.getByLabelText("Name")).toHaveAttribute(
			"placeholder",
			"Slack alerts",
		);
		expect(screen.getByLabelText("Webhook URL")).toHaveAttribute(
			"type",
			"password",
		);
	});

	test("a new channel starts on failures and alerts once the catalogue has loaded", async () => {
		await openCreate();
		await catalogueLoaded();
		expect(
			screen.getByRole("checkbox", { name: /Server offline/ }),
		).toBeChecked();
		expect(
			screen.getByRole("checkbox", { name: /Deployment succeeded/ }),
		).not.toBeChecked();
	});

	test("creates a Slack channel: sends the trimmed name, the URL as a secret and the rule, then closes", async () => {
		const { api, user, onOpenChange } = await openCreate({
			[`POST ${CHANNELS}`]: { status: 201, body: { channel: channel() } },
		});
		await catalogueLoaded();
		await fill(user, screen.getByLabelText("Name"), "  Ops Slack ");
		await fill(user, screen.getByLabelText("Webhook URL"), SLACK_URL);
		expect(submit()).toBeEnabled();
		await user.click(submit());
		expect(await screen.findByText("Channel added")).toBeInTheDocument();
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.path).toBe(CHANNELS);
		expect(post?.body).toEqual({
			name: "Ops Slack",
			kind: "slack",
			enabled: true,
			secrets: { url: SLACK_URL },
			rule: {
				eventTypes: ["deploy.failed", "server.offline"],
				minSeverity: "info",
				allProjects: true,
				projectIds: [],
			},
		});
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.unmatched).toEqual([]);
	});

	test("choosing Discord asks for a Discord webhook URL and refuses a non-https one", async () => {
		const { user } = await openCreate();
		await user.click(screen.getByRole("radio", { name: /Discord/ }));
		expect(screen.getByLabelText("Name")).toHaveAttribute(
			"placeholder",
			"Discord alerts",
		);
		await fill(user, screen.getByLabelText("Name"), "Dev");
		await fill(
			user,
			screen.getByLabelText("Webhook URL"),
			"http://discord.test/x",
		);
		expect(submit()).toBeDisabled();
		expect(submit()).toHaveAttribute(
			"title",
			"Paste the Discord webhook URL (it starts with https://).",
		);
	});

	test("creates a Telegram channel with the chat in config and the token as a secret", async () => {
		const { api, user } = await openCreate({
			[`POST ${CHANNELS}`]: {
				status: 201,
				body: { channel: channel({ kind: "telegram", name: "TG" }) },
			},
		});
		await catalogueLoaded();
		await user.click(screen.getByRole("radio", { name: /Telegram/ }));
		await fill(user, screen.getByLabelText("Name"), "TG");
		await fill(user, screen.getByLabelText("Bot token"), " 123:abc ");
		expect(submit()).toHaveAttribute(
			"title",
			"Enter the chat id or @channelname the bot posts to.",
		);
		await fill(user, screen.getByLabelText("Chat"), "@ops");
		await user.click(submit());
		await screen.findByText("Channel added");
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.body).toMatchObject({
			kind: "telegram",
			config: { chatId: "@ops" },
			secrets: { botToken: "123:abc" },
		});
	});

	test("creates an email channel: parsed recipients, port, TLS mode and password", async () => {
		const { api, user } = await openCreate({
			[`POST ${CHANNELS}`]: {
				status: 201,
				body: { channel: channel({ kind: "email", name: "Mail" }) },
			},
		});
		await catalogueLoaded();
		await user.click(screen.getByRole("radio", { name: /Email/ }));
		expect(screen.getByLabelText("Name")).toHaveAttribute(
			"placeholder",
			"Ops mailbox",
		);
		await fill(user, screen.getByLabelText("Name"), "Mail");
		await fill(user, screen.getByLabelText("SMTP server"), "smtp.example.com");
		expect(screen.getByLabelText("Port")).toHaveAttribute("placeholder", "587");
		await user.click(screen.getByRole("button", { name: "TLS" }));
		expect(screen.getByLabelText("Port")).toHaveAttribute("placeholder", "465");
		await fill(user, screen.getByLabelText("Port"), "2465");
		await fill(user, screen.getByLabelText("User name"), "robot");
		expect(submit()).toHaveAttribute(
			"title",
			"The sender must be an email address, like alerts@example.com.",
		);
		await fill(
			user,
			screen.getByLabelText("From"),
			"NebulaCtrl <alerts@example.com>",
		);
		await fill(
			user,
			screen.getByLabelText("Recipients"),
			"ops@example.com, oncall@example.com\nOPS@example.com",
		);
		expect(submit()).toHaveAttribute(
			"title",
			"A user name needs its password.",
		);
		await fill(user, screen.getByLabelText("Password"), "hunter2");
		await user.click(submit());
		await screen.findByText("Channel added");
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.body).toMatchObject({
			kind: "email",
			name: "Mail",
			config: {
				provider: "smtp",
				host: "smtp.example.com",
				port: 2465,
				tls: "implicit",
				username: "robot",
				from: "NebulaCtrl <alerts@example.com>",
				to: ["ops@example.com", "oncall@example.com"],
				insecureSkipVerify: false,
			},
			secrets: { password: "hunter2" },
		});
	});

	test("email without encryption warns that the message travels in clear text", async () => {
		const { user } = await openCreate();
		await user.click(screen.getByRole("radio", { name: /Email/ }));
		expect(screen.queryByText("Not encrypted")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "None" }));
		expect(screen.getByText("Not encrypted")).toBeInTheDocument();
		expect(screen.getByLabelText("Port")).toHaveAttribute("placeholder", "25");
		await user.click(screen.getByRole("button", { name: "STARTTLS" }));
		expect(screen.queryByText("Not encrypted")).not.toBeInTheDocument();
	});

	test("a self-signed certificate can be accepted for email", async () => {
		const { api, user } = await openCreate({
			[`POST ${CHANNELS}`]: {
				status: 201,
				body: { channel: channel({ kind: "email", name: "Mail" }) },
			},
		});
		await user.click(screen.getByRole("radio", { name: /Email/ }));
		await fill(user, screen.getByLabelText("Name"), "Mail");
		await fill(user, screen.getByLabelText("SMTP server"), "10.0.0.2");
		await fill(user, screen.getByLabelText("From"), "a@example.com");
		await fill(user, screen.getByLabelText("Recipients"), "b@example.com");
		await user.click(screen.getByRole("checkbox", { name: /self-signed/ }));
		await user.click(submit());
		await screen.findByText("Channel added");
		expect(
			sent<{ config: { insecureSkipVerify: boolean } }>(api, "POST").config
				.insecureSkipVerify,
		).toBe(true);
	});

	test("creates a Resend email channel: provider and API key, no SMTP settings", async () => {
		const { api, user } = await openCreate({
			[`POST ${CHANNELS}`]: {
				status: 201,
				body: { channel: channel({ kind: "email", name: "Mail" }) },
			},
		});
		await catalogueLoaded();
		await user.click(screen.getByRole("radio", { name: /Email/ }));
		await user.click(screen.getByRole("button", { name: "Resend" }));
		expect(screen.queryByLabelText("SMTP server")).not.toBeInTheDocument();
		expect(screen.queryByLabelText("User name")).not.toBeInTheDocument();
		expect(screen.getByLabelText("API key")).toHaveAttribute(
			"type",
			"password",
		);
		await fill(user, screen.getByLabelText("Name"), "Mail");
		await fill(user, screen.getByLabelText("From"), "alerts@example.com");
		await fill(user, screen.getByLabelText("Recipients"), "ops@example.com");
		expect(submit()).toHaveAttribute("title", "Paste the Resend API key.");
		await fill(user, screen.getByLabelText("API key"), " re_secret ");
		await user.click(submit());
		await screen.findByText("Channel added");
		const post = sent<{ config: object; secrets: unknown }>(api, "POST");
		expect(post).toMatchObject({
			kind: "email",
			config: {
				provider: "resend",
				from: "alerts@example.com",
				to: ["ops@example.com"],
			},
			secrets: { apiKey: "re_secret" },
		});
		expect(post.config).not.toHaveProperty("host");
	});

	test("a webhook without a signing secret gets one generated and shown once with verification code", async () => {
		const { api, user, onOpenChange } = await openCreate({
			[`POST ${CHANNELS}`]: {
				status: 201,
				body: {
					channel: channel({ kind: "webhook", name: "Hook" }),
					signingSecret: "generated-secret-0123456789",
				},
			},
		});
		await user.click(screen.getByRole("radio", { name: /Webhook/ }));
		await fill(user, screen.getByLabelText("Name"), "Hook");
		await fill(
			user,
			screen.getByLabelText("URL"),
			"http://localhost:8080/hook",
		);
		await user.click(submit());
		expect(
			await screen.findByText("Copy the signing secret for Hook"),
		).toBeInTheDocument();
		expect(screen.getByTestId("signing-secret")).toHaveTextContent(
			"generated-secret-0123456789",
		);
		expect(screen.getByText("Shown once")).toBeInTheDocument();
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.body).toMatchObject({
			kind: "webhook",
			secrets: { url: "http://localhost:8080/hook" },
		});
		expect(
			sent<{ secrets: Record<string, string> }>(api, "POST").secrets,
		).not.toHaveProperty("signingSecret");
		// The dialog stays open until the secret is acknowledged.
		expect(onOpenChange).not.toHaveBeenCalled();
		await user.click(screen.getByRole("button", { name: "I have copied it" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});

	test("a webhook's own signing secret must be at least 16 characters and is sent as given", async () => {
		const { api, user } = await openCreate({
			[`POST ${CHANNELS}`]: {
				status: 201,
				body: { channel: channel({ kind: "webhook", name: "Hook" }) },
			},
		});
		await user.click(screen.getByRole("radio", { name: /Webhook/ }));
		await fill(user, screen.getByLabelText("Name"), "Hook");
		await fill(user, screen.getByLabelText("URL"), "https://example.com/h");
		await fill(user, screen.getByLabelText("Signing secret"), "short");
		expect(submit()).toBeDisabled();
		expect(submit().getAttribute("title")).toMatch(/at least 16 characters/);
		await fill(
			user,
			screen.getByLabelText("Signing secret"),
			"short-and-long-enough",
		);
		await user.click(submit());
		// A supplied secret is not echoed back, so the dialog just closes.
		await screen.findByText("Channel added");
		expect(
			sent<{ secrets: { signingSecret: string } }>(api, "POST").secrets
				.signingSecret,
		).toBe("short-and-long-enough");
		expect(screen.queryByTestId("signing-secret")).not.toBeInTheDocument();
	});

	test("selected projects are required and sent instead of the whole team", async () => {
		const { api, user } = await openCreate({
			[`POST ${CHANNELS}`]: { status: 201, body: { channel: channel() } },
		});
		await catalogueLoaded();
		await fill(user, screen.getByLabelText("Name"), "Ops");
		await fill(user, screen.getByLabelText("Webhook URL"), SLACK_URL);
		await user.click(screen.getByRole("button", { name: "Selected projects" }));
		expect(submit()).toBeDisabled();
		expect(submit()).toHaveAttribute(
			"title",
			"Choose at least one project, or send for the whole team.",
		);
		expect(
			screen.getByText(/only whole-team channels receive them/),
		).toBeInTheDocument();
		await user.click(await screen.findByRole("checkbox", { name: "Billing" }));
		await user.click(screen.getByRole("checkbox", { name: "Checkout" }));
		await user.click(screen.getByRole("checkbox", { name: "Billing" }));
		await user.click(submit());
		await screen.findByText("Channel added");
		expect(sent<{ rule: unknown }>(api, "POST").rule).toEqual({
			eventTypes: ["deploy.failed", "server.offline"],
			minSeverity: "info",
			allProjects: false,
			projectIds: ["p1"],
		});
	});

	test("an empty team shows that there are no projects to pick", async () => {
		const { user } = await openCreate({
			"GET /projects": { body: { items: [] } },
		});
		await user.click(screen.getByRole("button", { name: "Selected projects" }));
		expect(await screen.findByText("No projects yet.")).toBeInTheDocument();
	});

	test("the event choice and minimum severity are sent as the user set them", async () => {
		const { api, user } = await openCreate({
			[`POST ${CHANNELS}`]: { status: 201, body: { channel: channel() } },
		});
		await catalogueLoaded();
		await fill(user, screen.getByLabelText("Name"), "Ops");
		await fill(user, screen.getByLabelText("Webhook URL"), SLACK_URL);
		await user.click(
			screen.getByRole("checkbox", { name: /Deployment succeeded/ }),
		);
		await user.click(screen.getByRole("button", { name: "Errors only" }));
		await user.click(submit());
		await screen.findByText("Channel added");
		expect(sent<{ rule: unknown }>(api, "POST").rule).toEqual({
			eventTypes: ["deploy.failed", "deploy.succeeded", "server.offline"],
			minSeverity: "error",
			allProjects: true,
			projectIds: [],
		});
	});

	test("events the user already chose are not overwritten when the catalogue arrives late", async () => {
		let release: () => void = () => {};
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		const { user } = await openCreate({
			"GET /notifications/event-types": async () => {
				await gate;
				return Response.json({ items: EVENT_TYPES });
			},
		});
		// Nothing to click in the matrix yet, so choose a preset (counts as touched).
		await user.click(screen.getByRole("button", { name: "Nothing" }));
		release();
		await screen.findByRole("checkbox", { name: /Deployment failed/ });
		await waitFor(() =>
			expect(
				screen.getByRole("checkbox", { name: /Deployment failed/ }),
			).not.toBeChecked(),
		);
	});

	test("a server error is shown in the dialog, which stays open with the form intact", async () => {
		const { user, onOpenChange } = await openCreate({
			[`POST ${CHANNELS}`]: problem(422, "url resolves to a metadata address"),
		});
		await fill(user, screen.getByLabelText("Name"), "Ops");
		await fill(user, screen.getByLabelText("Webhook URL"), SLACK_URL);
		await user.click(submit());
		expect(
			await screen.findByText("url resolves to a metadata address"),
		).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(screen.getByLabelText("Name")).toHaveValue("Ops");
		expect(screen.queryByText("Channel added")).not.toBeInTheDocument();
	});

	test("Cancel closes without a request", async () => {
		const { api, user, onOpenChange } = await openCreate();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls.filter((call) => call.method === "POST")).toEqual([]);
	});

	test("submitting the form with Enter while invalid sends nothing", async () => {
		const { api, user } = await openCreate();
		await user.type(screen.getByLabelText("Name"), "Ops{Enter}");
		expect(api.calls.filter((call) => call.method === "POST")).toEqual([]);
	});
});

describe("ChannelDialog: editing", () => {
	function openEdit(
		view = channel(),
		extra: Parameters<typeof stubApi>[0] = {},
	) {
		const api = stubApi(routes(extra));
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		return renderWithProviders(
			<ChannelDialog open onOpenChange={onOpenChange} channel={view} />,
		).then(async () => {
			await screen.findByRole("heading", { name: `Edit ${view.name}` });
			return { api, user, onOpenChange };
		});
	}

	test("shows the stored name and rule, hides the type picker, and hints at the stored secret", async () => {
		await openEdit();
		expect(screen.getByLabelText("Name")).toHaveValue("Ops Slack");
		expect(
			screen.queryByRole("radio", { name: /Discord/ }),
		).not.toBeInTheDocument();
		const url = screen.getByLabelText("Webhook URL (leave empty to keep)");
		expect(url).toHaveValue("");
		expect(url).toHaveAttribute(
			"placeholder",
			"https://hooks.slack.com/••••••",
		);
		expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
		expect(
			await screen.findByRole("checkbox", { name: /Deployment failed/ }),
		).toBeChecked();
		// Editing never swaps in the default selection.
		expect(
			screen.getByRole("checkbox", { name: /Server offline/ }),
		).not.toBeChecked();
	});

	test("saving an unchanged channel PATCHes without any secret, so the stored one is kept", async () => {
		const { api, user, onOpenChange } = await openEdit(channel(), {
			[`PATCH ${CHANNELS}/c1`]: { body: channel() },
		});
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(await screen.findByText("Channel saved")).toBeInTheDocument();
		const patch = api.calls.find((call) => call.method === "PATCH");
		expect(patch?.path).toBe(`${CHANNELS}/c1`);
		expect(patch?.body).toEqual({
			name: "Ops Slack",
			enabled: true,
			secrets: {},
			rule: {
				eventTypes: ["deploy.failed"],
				minSeverity: "info",
				allProjects: true,
				projectIds: [],
			},
		});
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
	});

	test("a new URL replaces the stored one", async () => {
		const { api, user } = await openEdit(channel(), {
			[`PATCH ${CHANNELS}/c1`]: { body: channel() },
		});
		await fill(
			user,
			screen.getByLabelText("Webhook URL (leave empty to keep)"),
			SLACK_URL,
		);
		await user.click(screen.getByRole("button", { name: "Save" }));
		await screen.findByText("Channel saved");
		expect(sent<{ secrets: unknown }>(api, "PATCH").secrets).toEqual({
			url: SLACK_URL,
		});
	});

	test("a new URL must still be https for Slack", async () => {
		const { user } = await openEdit();
		await fill(
			user,
			screen.getByLabelText("Webhook URL (leave empty to keep)"),
			"ftp://x",
		);
		expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Save" })).toHaveAttribute(
			"title",
			"The webhook URL must start with https://.",
		);
	});

	test("editing an email channel loads its config and keeps the password hint", async () => {
		const mail = channel({
			kind: "email",
			name: "Mail",
			config: {
				host: "smtp.example.com",
				port: 587,
				tls: "starttls",
				username: "robot",
				from: "alerts@example.com",
				to: ["ops@example.com", "dev@example.com"],
			},
			secrets: { password: "••••" },
		});
		const { api, user } = await openEdit(mail, {
			[`PATCH ${CHANNELS}/c1`]: { body: mail },
		});
		expect(screen.getByLabelText("SMTP server")).toHaveValue(
			"smtp.example.com",
		);
		expect(screen.getByLabelText("Port")).toHaveValue("587");
		expect(screen.getByLabelText("User name")).toHaveValue("robot");
		expect(screen.getByLabelText("Recipients")).toHaveValue(
			"ops@example.com\ndev@example.com",
		);
		expect(
			screen.getByLabelText("Password (leave empty to keep)"),
		).toHaveAttribute("placeholder", "••••");
		await user.click(screen.getByRole("button", { name: "Save" }));
		await screen.findByText("Channel saved");
		const body = api.calls.find((c) => c.method === "PATCH")?.body as {
			config: unknown;
			secrets: unknown;
		};
		expect(body.secrets).toEqual({});
		expect(body.config).toEqual({
			provider: "smtp",
			host: "smtp.example.com",
			port: 587,
			tls: "starttls",
			username: "robot",
			from: "alerts@example.com",
			to: ["ops@example.com", "dev@example.com"],
			insecureSkipVerify: false,
		});
	});

	test("editing a Resend channel shows the stored-key hint, and switching to SMTP shows the SMTP fields", async () => {
		const mail = channel({
			kind: "email",
			name: "Mail",
			config: {
				provider: "resend",
				from: "alerts@example.com",
				to: ["ops@example.com"],
			},
			secrets: { apiKey: "re_••••" },
		});
		const { api, user } = await openEdit(mail, {
			[`PATCH ${CHANNELS}/c1`]: { body: mail },
		});
		const key = screen.getByLabelText("API key (leave empty to keep)");
		expect(key).toHaveValue("");
		expect(key).toHaveAttribute("placeholder", "re_••••");
		expect(screen.queryByLabelText("SMTP server")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();

		await user.click(screen.getByRole("button", { name: "Save" }));
		await screen.findByText("Channel saved");
		const body = sent<{ config: unknown; secrets: unknown }>(api, "PATCH");
		expect(body.secrets).toEqual({});
		expect(body.config).toEqual({
			provider: "resend",
			from: "alerts@example.com",
			to: ["ops@example.com"],
		});

		await user.click(screen.getByRole("button", { name: "SMTP" }));
		expect(screen.getByLabelText("SMTP server")).toHaveValue("");
		expect(screen.queryByLabelText(/API key/)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
	});

	test("editing a webhook names the stored receiver and points to Rotate secret", async () => {
		const hook = channel({
			kind: "webhook",
			name: "Hook",
			secrets: { url: "https://receiver.example.com/••••••" },
		});
		await openEdit(hook);
		expect(
			screen.getByText(/Currently sending to receiver\.example\.com/),
		).toBeInTheDocument();
		expect(screen.getByText(/“Rotate secret” on the card/)).toBeInTheDocument();
		expect(screen.queryByLabelText("Signing secret")).not.toBeInTheDocument();
	});

	test("a failed save keeps the dialog open and shows the reason", async () => {
		const { user, onOpenChange } = await openEdit(channel(), {
			[`PATCH ${CHANNELS}/c1`]: problem(404, "channel not found", "not_found"),
		});
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(await screen.findByText("channel not found")).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
	});

	test("editing a Telegram channel keeps the stored chat and sends no token unless typed", async () => {
		const tg = channel({
			kind: "telegram",
			name: "TG",
			config: { chatId: "-1001" },
			secrets: { botToken: "••••" },
		});
		const { api, user } = await openEdit(tg, {
			[`PATCH ${CHANNELS}/c1`]: { body: tg },
		});
		expect(screen.getByLabelText("Chat")).toHaveValue("-1001");
		await user.click(screen.getByRole("button", { name: "Save" }));
		await screen.findByText("Channel saved");
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toMatchObject({
			config: { chatId: "-1001" },
			secrets: {},
		});
	});
});

function Reopenable() {
	const [open, setOpen] = useState(true);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				reopen
			</button>
			<ChannelDialog open={open} onOpenChange={setOpen} channel={null} />
		</>
	);
}

describe("ChannelDialog: closed", () => {
	test("renders nothing and requests nothing while closed", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<ChannelDialog open={false} onOpenChange={() => {}} channel={null} />,
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("reopening starts from a blank form again", async () => {
		stubApi(routes());
		const user = userEvent.setup();
		await renderWithProviders(<Reopenable />);
		await fill(user, await screen.findByLabelText("Name"), "Draft");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		await user.click(screen.getByRole("button", { name: "reopen" }));
		expect(await screen.findByLabelText("Name")).toHaveValue("");
	});
});
