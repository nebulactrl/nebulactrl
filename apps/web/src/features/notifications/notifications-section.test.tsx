import { describe, expect, setDefaultTimeout, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { NotificationsSection } from "./notifications-section";
import {
	channel,
	delivery,
	EVENT_TYPES,
	problem,
	project,
} from "./test-fixtures";

setDefaultTimeout(30_000);

const CHANNELS = "/notifications/channels";
const ALERTS = {
	crashGraceSeconds: 30,
	unhealthyGraceSeconds: 60,
	loopRestarts: 5,
	loopWindowMinutes: 10,
	recoverAfterSeconds: 120,
	flapIncidents: 4,
	flapWindowMinutes: 30,
	backupGraceMinutes: 0,
	logLines: 10,
};

const slack = channel();
const hook = channel({
	id: "c2",
	name: "Billing hook",
	kind: "webhook",
	secrets: { url: "https://receiver.example.com/••••••" },
	rule: {
		eventTypes: ["deploy.failed", "server.offline"],
		minSeverity: "error",
		allProjects: false,
		projectIds: ["p1"],
	},
});

function base(extra: Parameters<typeof stubApi>[0] = {}) {
	return {
		[`GET ${CHANNELS}`]: { body: { items: [slack, hook] } },
		"GET /projects": { body: { items: [project("p1", "Checkout")] } },
		"GET /notifications/alert-settings": { body: ALERTS },
		"GET /notifications/event-types": { body: { items: EVENT_TYPES } },
		...extra,
	};
}

async function open(extra: Parameters<typeof stubApi>[0] = {}) {
	const api = stubApi(base(extra));
	const user = userEvent.setup();
	await renderWithProviders(<NotificationsSection />);
	await screen.findByRole("article", { name: "Ops Slack" });
	return { api, user };
}

const card = (name: string) => screen.getByRole("article", { name });
const mutations = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((call) => call.method !== "GET");

describe("NotificationsSection", () => {
	test("lists every channel with its project-aware rule and a count", async () => {
		const { api } = await open();
		expect(screen.getByText(/2 channels\./)).toBeInTheDocument();
		expect(card("Ops Slack")).toHaveTextContent(
			"1 event · every severity · whole team",
		);
		expect(card("Billing hook")).toHaveTextContent(
			"2 events · errors only · Checkout",
		);
		expect(
			screen.getByRole("form", { name: "Service alerts" }),
		).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("uses the singular for one channel", async () => {
		stubApi(base({ [`GET ${CHANNELS}`]: { body: { items: [slack] } } }));
		await renderWithProviders(<NotificationsSection />);
		expect(await screen.findByText(/1 channel\./)).toBeInTheDocument();
	});

	test("with no channels, invites to add one", async () => {
		stubApi(base({ [`GET ${CHANNELS}`]: { body: { items: null } } }));
		const user = userEvent.setup();
		await renderWithProviders(<NotificationsSection />);
		expect(
			await screen.findByText("Get told when something breaks"),
		).toBeInTheDocument();
		expect(screen.queryByRole("article")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Add channel" }));
		expect(
			await screen.findByRole("heading", { name: "Add notification channel" }),
		).toBeInTheDocument();
	});

	test("explains the admin scope when the channels did not load, and retry recovers", async () => {
		let attempts = 0;
		stubApi(
			base({
				[`GET ${CHANNELS}`]: () => {
					attempts += 1;
					return attempts === 1
						? Response.json(
								{ status: 403, title: "x", detail: "no", code: "forbidden" },
								{ status: 403 },
							)
						: Response.json({ items: [slack] });
				},
			}),
		);
		const user = userEvent.setup();
		await renderWithProviders(<NotificationsSection />);
		expect(
			await screen.findByText("Notification channels did not load"),
		).toBeInTheDocument();
		expect(screen.getByText(/needs the admin scope/)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByRole("article", { name: "Ops Slack" }),
		).toBeInTheDocument();
	});

	test("the switch pauses a channel with a PATCH and confirms", async () => {
		const { api, user } = await open({
			[`PATCH ${CHANNELS}/c1`]: { body: { ...slack, enabled: false } },
		});
		await user.click(screen.getByRole("switch", { name: "Pause Ops Slack" }));
		expect(await screen.findByText("Paused Ops Slack")).toBeInTheDocument();
		expect(
			screen.getByText("Nothing is sent until you resume it."),
		).toBeInTheDocument();
		const patch = mutations(api)[0];
		expect(patch?.method).toBe("PATCH");
		expect(patch?.path).toBe(`${CHANNELS}/c1`);
		expect(patch?.body).toEqual({ enabled: false });
	});

	test("the switch resumes a paused channel", async () => {
		const paused = channel({ enabled: false });
		const api = stubApi(
			base({
				[`GET ${CHANNELS}`]: { body: { items: [paused] } },
				[`PATCH ${CHANNELS}/c1`]: { body: slack },
			}),
		);
		const user = userEvent.setup();
		await renderWithProviders(<NotificationsSection />);
		await user.click(
			await screen.findByRole("switch", { name: "Resume Ops Slack" }),
		);
		expect(await screen.findByText("Resumed Ops Slack")).toBeInTheDocument();
		expect(mutations(api)[0]?.body).toEqual({ enabled: true });
	});

	test("a failed toggle reports the reason", async () => {
		const { user } = await open({
			[`PATCH ${CHANNELS}/c1`]: problem(409, "channel is locked", "conflict"),
		});
		await user.click(screen.getByRole("switch", { name: "Pause Ops Slack" }));
		expect(
			await screen.findByText("Could not change the channel"),
		).toBeInTheDocument();
		expect(screen.getByText("channel is locked")).toBeInTheDocument();
	});

	test("a delivered test says what the receiver answered and refreshes the card", async () => {
		let tested = false;
		const { api, user } = await open({
			[`GET ${CHANNELS}`]: () =>
				Response.json({
					items: [
						tested
							? channel({
									test: { ok: true, at: "2026-10-26T10:00:00Z", message: "" },
								})
							: slack,
						hook,
					],
				}),
			[`POST ${CHANNELS}/c1/test`]: () => {
				tested = true;
				return Response.json(
					delivery({ status: "delivered", responseCode: 204 }),
				);
			},
		});
		expect(
			within(card("Ops Slack")).getByText("Not tested yet"),
		).toBeInTheDocument();
		await user.click(
			within(card("Ops Slack")).getByRole("button", { name: "Send test" }),
		);
		expect(
			await screen.findByText("Test sent to Ops Slack"),
		).toBeInTheDocument();
		expect(screen.getByText("The receiver answered 204.")).toBeInTheDocument();
		expect(
			await within(card("Ops Slack")).findByText("Test message arrived"),
		).toBeInTheDocument();
		expect(mutations(api).map((c) => `${c.method} ${c.path}`)).toEqual([
			`POST ${CHANNELS}/c1/test`,
		]);
	});

	test("a delivered test without a response code says it was accepted", async () => {
		const { user } = await open({
			[`POST ${CHANNELS}/c1/test`]: {
				body: delivery({ status: "delivered" }),
			},
		});
		await user.click(
			within(card("Ops Slack")).getByRole("button", { name: "Send test" }),
		);
		expect(
			await screen.findByText("The message was accepted."),
		).toBeInTheDocument();
	});

	test("a test the receiver rejected shows its error as a failure", async () => {
		const { user } = await open({
			[`POST ${CHANNELS}/c1/test`]: {
				body: delivery({ status: "failed", lastError: "HTTP 410 gone" }),
			},
		});
		await user.click(
			within(card("Ops Slack")).getByRole("button", { name: "Send test" }),
		);
		expect(
			await screen.findByText("Test to Ops Slack failed"),
		).toBeInTheDocument();
		expect(screen.getByText("HTTP 410 gone")).toBeInTheDocument();
		expect(
			screen.queryByText("Test sent to Ops Slack"),
		).not.toBeInTheDocument();
	});

	test("while a test is in flight only that card's button is busy", async () => {
		let release: () => void = () => {};
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		const { user } = await open({
			[`POST ${CHANNELS}/c1/test`]: async () => {
				await gate;
				return Response.json(delivery({ status: "delivered" }));
			},
		});
		await user.click(
			within(card("Ops Slack")).getByRole("button", { name: "Send test" }),
		);
		expect(
			await within(card("Ops Slack")).findByText("Sending…"),
		).toBeInTheDocument();
		expect(
			within(card("Billing hook")).getByRole("button", { name: "Send test" }),
		).toBeEnabled();
		release();
		await waitFor(() =>
			expect(
				within(card("Ops Slack")).getByRole("button", { name: "Send test" }),
			).toBeEnabled(),
		);
	});

	test("a test request that errors out reports it and frees the button", async () => {
		const { user } = await open({
			[`POST ${CHANNELS}/c1/test`]: problem(500, "mailer exploded", "internal"),
		});
		await user.click(
			within(card("Ops Slack")).getByRole("button", { name: "Send test" }),
		);
		expect(
			await screen.findByText("Could not send the test"),
		).toBeInTheDocument();
		expect(screen.getByText("mailer exploded")).toBeInTheDocument();
		expect(
			within(card("Ops Slack")).getByRole("button", { name: "Send test" }),
		).toBeEnabled();
	});

	test("Delivery log opens that channel's log", async () => {
		const { api, user } = await open({
			[`GET ${CHANNELS}/c2/deliveries`]: {
				body: {
					items: [delivery({ channelId: "c2", title: "billing broke" })],
				},
			},
		});
		await user.click(
			within(card("Billing hook")).getByRole("button", {
				name: "Delivery log",
			}),
		);
		expect(
			await screen.findByText("Delivery log · Billing hook"),
		).toBeInTheDocument();
		expect(await screen.findByText("billing broke")).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
		await user.click(screen.getByRole("button", { name: "Done" }));
		await waitFor(() =>
			expect(
				screen.queryByText("Delivery log · Billing hook"),
			).not.toBeInTheDocument(),
		);
	});

	test("Edit opens the dialog for that channel", async () => {
		const { user } = await open();
		await user.click(
			within(card("Billing hook")).getByRole("button", { name: "Edit" }),
		);
		expect(
			await screen.findByRole("heading", { name: "Edit Billing hook" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("Billing hook");
	});

	test("Remove asks first, then deletes the channel and confirms", async () => {
		const { api, user } = await open({
			[`DELETE ${CHANNELS}/c1`]: { status: 204 },
		});
		await user.click(
			within(card("Ops Slack")).getByRole("button", { name: "Remove" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Remove Ops Slack?")).toBeInTheDocument();
		expect(mutations(api)).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Remove channel" }),
		);
		expect(await screen.findByText("Removed Ops Slack")).toBeInTheDocument();
		expect(mutations(api).map((c) => `${c.method} ${c.path}`)).toEqual([
			`DELETE ${CHANNELS}/c1`,
		]);
	});

	test("cancelling the removal leaves the channel alone", async () => {
		const { api, user } = await open();
		await user.click(
			within(card("Ops Slack")).getByRole("button", { name: "Remove" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(mutations(api)).toEqual([]);
		expect(card("Ops Slack")).toBeInTheDocument();
	});

	test("a failed removal keeps the confirm dialog open with the reason", async () => {
		const { user } = await open({
			[`DELETE ${CHANNELS}/c1`]: problem(
				500,
				"database is read-only",
				"internal",
			),
		});
		await user.click(
			within(card("Ops Slack")).getByRole("button", { name: "Remove" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Remove channel" }),
		);
		expect(
			await within(dialog).findByText("database is read-only"),
		).toBeInTheDocument();
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
	});

	test("only a webhook offers Rotate secret; confirming shows the new secret once", async () => {
		const { api, user } = await open({
			[`POST ${CHANNELS}/c2/rotate-secret`]: {
				body: { signingSecret: "rotated-secret-abcdef0123456789" },
			},
		});
		expect(
			within(card("Ops Slack")).queryByRole("button", {
				name: "Rotate secret",
			}),
		).not.toBeInTheDocument();
		await user.click(
			within(card("Billing hook")).getByRole("button", {
				name: "Rotate secret",
			}),
		);
		const confirm = await screen.findByRole("alertdialog");
		expect(
			within(confirm).getByText("Rotate the signing secret of Billing hook?"),
		).toBeInTheDocument();
		expect(mutations(api)).toEqual([]);
		await user.click(
			within(confirm).getByRole("button", { name: "Rotate secret" }),
		);
		expect(
			await screen.findByText("New signing secret for Billing hook"),
		).toBeInTheDocument();
		expect(screen.getByTestId("signing-secret")).toHaveTextContent(
			"rotated-secret-abcdef0123456789",
		);
		expect(mutations(api).map((c) => `${c.method} ${c.path}`)).toEqual([
			`POST ${CHANNELS}/c2/rotate-secret`,
		]);
		await user.click(screen.getByRole("button", { name: "I have copied it" }));
		await waitFor(() =>
			expect(screen.queryByTestId("signing-secret")).not.toBeInTheDocument(),
		);
	});

	test("a failed rotation shows the reason in the confirm dialog and no secret", async () => {
		const { user } = await open({
			[`POST ${CHANNELS}/c2/rotate-secret`]: problem(
				500,
				"kms unavailable",
				"internal",
			),
		});
		await user.click(
			within(card("Billing hook")).getByRole("button", {
				name: "Rotate secret",
			}),
		);
		const confirm = await screen.findByRole("alertdialog");
		await user.click(
			within(confirm).getByRole("button", { name: "Rotate secret" }),
		);
		expect(
			await within(confirm).findByText("kms unavailable"),
		).toBeInTheDocument();
		expect(screen.queryByTestId("signing-secret")).not.toBeInTheDocument();
	});

	test("a mutation refetches the channel list", async () => {
		let listCalls = 0;
		const { user } = await open({
			[`GET ${CHANNELS}`]: () => {
				listCalls += 1;
				return Response.json({ items: [slack] });
			},
			[`PATCH ${CHANNELS}/c1`]: { body: slack },
		});
		const before = listCalls;
		await user.click(screen.getByRole("switch", { name: "Pause Ops Slack" }));
		await screen.findByText("Paused Ops Slack");
		await waitFor(() => expect(listCalls).toBeGreaterThan(before));
	});
});
