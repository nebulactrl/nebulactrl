import { describe, expect, mock, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ChannelView } from "#/api/gen/types.gen";
import { ChannelCard } from "./channel-card";
import { channel } from "./test-fixtures";

function setup(
	view: ChannelView,
	{
		testing = false,
		projectNames = new Map<string, string>(),
	}: { testing?: boolean; projectNames?: Map<string, string> } = {},
) {
	const handlers = {
		onToggle: mock((_enabled: boolean) => {}),
		onTest: mock(() => {}),
		onLog: mock(() => {}),
		onEdit: mock(() => {}),
		onRotate: mock(() => {}),
		onRemove: mock(() => {}),
	};
	const { unmount } = render(
		<ChannelCard
			channel={view}
			projectNames={projectNames}
			testing={testing}
			{...handlers}
		/>,
	);
	return { ...handlers, unmount };
}

describe("ChannelCard", () => {
	test("names the channel, its kind and target, and summarises its rule", () => {
		setup(channel({ name: "Ops Slack" }));
		const card = screen.getByRole("article", { name: "Ops Slack" });
		expect(card).toHaveTextContent("Slack · hooks.slack.com");
		expect(card).toHaveTextContent("1 event · every severity · whole team");
		expect(screen.queryByText("paused")).not.toBeInTheDocument();
	});

	test("resolves a single selected project to its name", () => {
		setup(
			channel({
				rule: {
					eventTypes: ["deploy.failed", "deploy.succeeded"],
					minSeverity: "error",
					allProjects: false,
					projectIds: ["p1"],
				},
			}),
			{ projectNames: new Map([["p1", "Checkout"]]) },
		);
		expect(
			screen.getByText("2 events · errors only · Checkout"),
		).toBeInTheDocument();
	});

	test("an email channel shows its first recipient and how many more", () => {
		setup(
			channel({
				kind: "email",
				name: "Mail",
				config: { to: ["ops@example.com", "oncall@example.com"] },
				secrets: {},
			}),
		);
		expect(screen.getByText("Email · ops@example.com +1")).toBeInTheDocument();
	});

	test("a paused channel is badged and its switch offers to resume it", () => {
		setup(channel({ enabled: false }));
		expect(screen.getByText("paused")).toBeInTheDocument();
		expect(
			screen.getByRole("switch", { name: "Resume Ops Slack" }),
		).not.toBeChecked();
	});

	test("the switch reports the new enabled state", async () => {
		const user = userEvent.setup();
		const h = setup(channel());
		const toggle = screen.getByRole("switch", { name: "Pause Ops Slack" });
		expect(toggle).toBeChecked();
		await user.click(toggle);
		expect(h.onToggle).toHaveBeenCalledTimes(1);
		expect(h.onToggle.mock.calls[0]?.[0]).toBe(false);
	});

	test("a channel that was never tested says so", () => {
		setup(channel());
		expect(screen.getByText("Not tested yet")).toBeInTheDocument();
	});

	test("a passing test shows when it arrived and no error", () => {
		setup(
			channel({
				test: { ok: true, at: "2026-10-26T10:00:00Z", message: "" },
			}),
		);
		expect(screen.getByText("Test message arrived")).toBeInTheDocument();
		expect(screen.queryByText("Last test failed")).not.toBeInTheDocument();
		expect(document.querySelector("time")).toHaveAttribute(
			"datetime",
			"2026-10-26T10:00:00Z",
		);
	});

	test("a failed test shows the reason", () => {
		setup(
			channel({
				test: { ok: false, message: "connection refused" },
			}),
		);
		expect(screen.getByText("Last test failed")).toBeInTheDocument();
		expect(screen.getByText("connection refused")).toHaveAttribute(
			"title",
			"connection refused",
		);
		expect(document.querySelector("time")).toBeNull();
	});

	test("the 24h stats say nothing was sent, or count deliveries and failures", () => {
		const { unmount } = setup(channel());
		expect(
			screen.getByText("Nothing sent in the last 24 hours"),
		).toBeInTheDocument();
		unmount();
		setup(channel({ stats: { delivered24h: 12, failed24h: 0 } }));
		expect(
			screen.getByText("12 delivered in the last 24 hours"),
		).toBeInTheDocument();
	});

	test("failures are counted next to deliveries", () => {
		setup(channel({ stats: { delivered24h: 5, failed24h: 2 } }));
		expect(
			screen.getByText("5 delivered, 2 failed in the last 24 hours"),
		).toBeInTheDocument();
	});

	test("only failures still counts as activity", () => {
		setup(channel({ stats: { delivered24h: 0, failed24h: 3 } }));
		expect(
			screen.getByText("0 delivered, 3 failed in the last 24 hours"),
		).toBeInTheDocument();
	});

	test("the footer buttons call their handlers", async () => {
		const user = userEvent.setup();
		const h = setup(channel());
		await user.click(screen.getByRole("button", { name: "Send test" }));
		await user.click(screen.getByRole("button", { name: "Delivery log" }));
		await user.click(screen.getByRole("button", { name: "Edit" }));
		await user.click(screen.getByRole("button", { name: "Remove" }));
		expect(h.onTest).toHaveBeenCalledTimes(1);
		expect(h.onLog).toHaveBeenCalledTimes(1);
		expect(h.onEdit).toHaveBeenCalledTimes(1);
		expect(h.onRemove).toHaveBeenCalledTimes(1);
		expect(h.onRotate).not.toHaveBeenCalled();
	});

	test("only a webhook can rotate its signing secret", async () => {
		const user = userEvent.setup();
		const { unmount } = setup(channel());
		expect(
			screen.queryByRole("button", { name: "Rotate secret" }),
		).not.toBeInTheDocument();
		unmount();
		const h = setup(channel({ kind: "webhook", name: "Hook" }));
		await user.click(screen.getByRole("button", { name: "Rotate secret" }));
		expect(h.onRotate).toHaveBeenCalledTimes(1);
	});

	test("while a test is being sent the button is disabled and reads Sending", () => {
		setup(channel(), { testing: true });
		expect(screen.getByText("Sending…").closest("button")).toBeDisabled();
		expect(
			screen.queryByRole("button", { name: "Send test" }),
		).not.toBeInTheDocument();
	});
});
