import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CommandsTab } from "./commands-tab";
import type { DebugCommand } from "./queries";
import { clockWithMs } from "./timeline-model";

function command(overrides: Partial<DebugCommand> = {}): DebugCommand {
	return {
		id: "cmd-1",
		kind: "deploy",
		status: "succeeded",
		outcome: "",
		error: "",
		serverId: "srv-1",
		serverName: "edge-1",
		createdAt: "2026-10-31T10:00:00.000Z",
		sentAt: "2026-10-31T10:00:00.250Z",
		ackedAt: "2026-10-31T10:00:01.000Z",
		finishedAt: "2026-10-31T10:00:03.500Z",
		expiresAt: "2026-10-31T10:15:00.000Z",
		payload: { image: "api:1" },
		result: { containerId: "abc" },
		...overrides,
	};
}

function renderTab(props: Partial<Parameters<typeof CommandsTab>[0]> = {}) {
	return render(
		<CommandsTab
			commands={[]}
			pending={false}
			error={false}
			onRetry={() => {}}
			{...props}
		/>,
	);
}

describe("CommandsTab states", () => {
	test("pending shows a busy placeholder", () => {
		const { container } = renderTab({ commands: undefined, pending: true });
		expect(container.querySelector("[aria-busy]")).not.toBeNull();
	});

	test("an error offers a retry", async () => {
		let retries = 0;
		renderTab({
			commands: undefined,
			error: true,
			onRetry: () => {
				retries += 1;
			},
		});
		expect(screen.getByText("The commands did not load")).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Try again" }));
		expect(retries).toBe(1);
	});

	test("no data and no error is a load failure too", () => {
		renderTab({ commands: undefined });
		expect(screen.getByText("The commands did not load")).toBeInTheDocument();
	});

	test("no commands says nothing was dispatched", () => {
		renderTab({ commands: [] });
		expect(screen.getByText("No agent commands")).toBeInTheDocument();
		expect(
			screen.queryByRole("list", { name: "Agent commands" }),
		).not.toBeInTheDocument();
	});
});

describe("CommandsTab cards", () => {
	test("a command shows its kind, server, id and delivery times with the wait between them", () => {
		renderTab({ commands: [command()] });
		const card = within(
			screen.getByRole("list", { name: "Agent commands" }),
		).getByRole("listitem");

		expect(within(card).getByText("deploy")).toBeInTheDocument();
		expect(within(card).getByText("Succeeded")).toBeInTheDocument();
		expect(within(card).getByText("edge-1")).toBeInTheDocument();
		expect(within(card).getByTitle("Command id")).toHaveTextContent("cmd-1");

		const time = (label: string) =>
			screen.getByText(label, { selector: "dt" }).nextElementSibling
				?.textContent;
		expect(time("Enqueued")).toBe(clockWithMs("2026-10-31T10:00:00.000Z"));
		expect(time("Sent")).toBe(
			`${clockWithMs("2026-10-31T10:00:00.250Z")}+250 ms`,
		);
		expect(time("Acknowledged")).toBe(
			`${clockWithMs("2026-10-31T10:00:01.000Z")}+750 ms`,
		);
		expect(time("Finished")).toBe(
			`${clockWithMs("2026-10-31T10:00:03.500Z")}+2.500 s`,
		);
	});

	test("steps that have not happened show a dash and no wait", () => {
		renderTab({
			commands: [
				command({
					status: "pending",
					sentAt: null,
					ackedAt: null,
					finishedAt: null,
				}),
			],
		});
		expect(screen.getByText("Waiting to be sent")).toBeInTheDocument();
		for (const label of ["Sent", "Acknowledged", "Finished"]) {
			expect(
				screen.getByText(label, { selector: "dt" }).nextElementSibling,
			).toHaveTextContent("—");
		}
	});

	test("the wait is measured from the last step that did happen", () => {
		renderTab({
			commands: [
				command({
					status: "expired",
					sentAt: null,
					ackedAt: null,
					finishedAt: "2026-10-31T10:15:00.000Z",
				}),
			],
		});
		expect(
			screen.getByText("Finished", { selector: "dt" }).nextElementSibling
				?.textContent,
		).toBe(`${clockWithMs("2026-10-31T10:15:00.000Z")}+15 m 00.000 s`);
		expect(screen.getByText("Expired before delivery")).toBeInTheDocument();
	});

	test("each status reads in words", () => {
		renderTab({
			commands: [
				command({ id: "a", status: "sent" }),
				command({ id: "b", status: "acked" }),
				command({ id: "c", status: "failed" }),
				command({ id: "d", status: "canceled" }),
			],
		});
		expect(screen.getByText("Sent, not yet acknowledged")).toBeInTheDocument();
		expect(screen.getByText("Acknowledged, running")).toBeInTheDocument();
		expect(screen.getByText("Failed")).toBeInTheDocument();
		expect(screen.getByText("Canceled")).toBeInTheDocument();
	});

	test("an unknown status is shown as the server sent it", () => {
		renderTab({ commands: [command({ status: "quarantined" })] });
		expect(screen.getByText("quarantined")).toBeInTheDocument();
	});

	test("an outcome different from the status is appended, underscores spelled out", () => {
		renderTab({
			commands: [command({ status: "failed", outcome: "image_pull_failed" })],
		});
		expect(screen.getByText(/Failed · image pull failed/)).toBeInTheDocument();
	});

	test("an outcome equal to the status is not repeated", () => {
		renderTab({
			commands: [command({ status: "succeeded", outcome: "succeeded" })],
		});
		expect(screen.getByText("Succeeded")).toBeInTheDocument();
		expect(screen.queryByText(/Succeeded ·/)).not.toBeInTheDocument();
	});

	test("an agent error is shown in full", () => {
		renderTab({
			commands: [
				command({
					status: "failed",
					error: "pull access denied\nfor registry.local",
				}),
			],
		});
		expect(screen.getByRole("alert")).toHaveTextContent("pull access denied");
		expect(screen.getByRole("alert")).toHaveTextContent("for registry.local");
	});

	test("without an error there is no alert", () => {
		renderTab({ commands: [command()] });
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	test("payload and result open to their JSON on demand", async () => {
		renderTab({
			commands: [
				command({ payload: { image: "api:1" }, result: { ok: true } }),
			],
		});
		const user = userEvent.setup();
		const payload = screen.getByRole("button", {
			name: /Payload \(redacted\)/,
		});
		expect(payload).toHaveAttribute("aria-expanded", "false");

		await user.click(payload);

		expect(payload).toHaveAttribute("aria-expanded", "true");
		const json = await screen.findByRole("textbox", {
			name: "Payload (redacted) as JSON",
		});
		expect(JSON.parse((json as HTMLTextAreaElement).value)).toEqual({
			image: "api:1",
		});
		expect(
			screen.getByRole("button", { name: /Result \(redacted\)/ }),
		).toHaveAttribute("aria-expanded", "false");
	});

	test("a result that does not exist yet cannot be opened", () => {
		renderTab({ commands: [command({ result: null })] });
		const result = screen.getByRole("button", { name: /Result \(redacted\)/ });
		expect(result).toBeDisabled();
		expect(result).toHaveTextContent("none yet");
		expect(
			screen.getByRole("button", { name: /Payload \(redacted\)/ }),
		).toBeEnabled();
	});
});
