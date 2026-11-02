import { describe, expect, mock, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "#/components/ui/tooltip";
import { DrainCard } from "./drain-card";
import type { TestOutcome } from "./model";
import { drainView, serverStatus } from "./test-fixtures";

type Props = Parameters<typeof DrainCard>[0];

function renderCard(drain: Props["drain"], props: Partial<Props> = {}) {
	const handlers = {
		onToggle: mock((_enabled: boolean) => {}),
		onTest: mock(() => {}),
		onDismissOutcome: mock(() => {}),
		onEdit: mock(() => {}),
		onRemove: mock(() => {}),
	};
	render(
		<TooltipProvider>
			<DrainCard
				drain={drain}
				projectNames={new Map([["p-web", "Web"]])}
				testing={false}
				outcome={undefined}
				{...handlers}
				{...props}
			/>
		</TooltipProvider>,
	);
	return handlers;
}

const card = () => screen.getByTestId("log-drain");

describe("DrainCard: a team drain", () => {
	test("names the drain, its kind and the host it ships to", () => {
		renderCard(drainView({ name: "Grafana" }));
		expect(screen.getByRole("article", { name: "Grafana" })).toBeVisible();
		expect(screen.getByRole("heading", { name: "Grafana" })).toBeVisible();
		expect(screen.getByText("Loki · logs.grafana.net")).toBeVisible();
	});

	test("shows a syslog drain's address as its target", () => {
		renderCard(
			drainView({
				kind: "syslog",
				loki: undefined,
				syslog: { address: "logs.example.com:6514", protocol: "tcp+tls" },
			}),
		);
		expect(screen.getByText("Syslog · logs.example.com:6514")).toBeVisible();
	});

	test("an enabled drain has no paused badge and offers Pause", () => {
		renderCard(drainView({ enabled: true }));
		expect(screen.queryByText("paused")).toBeNull();
		expect(screen.getByRole("switch", { name: "Pause Grafana" })).toBeChecked();
	});

	test("a paused drain says so and offers Resume", () => {
		renderCard(drainView({ enabled: false }));
		expect(screen.getByText("paused")).toBeVisible();
		expect(
			screen.getByRole("switch", { name: "Resume Grafana" }),
		).not.toBeChecked();
	});

	test("the switch reports the new state", async () => {
		const user = userEvent.setup();
		const h = renderCard(drainView({ enabled: true }));
		await user.click(screen.getByRole("switch", { name: "Pause Grafana" }));
		expect(h.onToggle).toHaveBeenCalledTimes(1);
		expect(h.onToggle.mock.calls[0]?.[0]).toBe(false);
	});

	test("Edit, Test and Delete call their handlers", async () => {
		const user = userEvent.setup();
		const h = renderCard(drainView());
		await user.click(screen.getByRole("button", { name: "Edit" }));
		await user.click(screen.getByRole("button", { name: "Test" }));
		await user.click(screen.getByRole("button", { name: "Delete" }));
		expect(h.onEdit).toHaveBeenCalledTimes(1);
		expect(h.onTest).toHaveBeenCalledTimes(1);
		expect(h.onRemove).toHaveBeenCalledTimes(1);
	});

	test("while testing, the Test button is disabled and says so", () => {
		renderCard(drainView(), { testing: true });
		expect(screen.getByRole("button", { name: /Testing…/ })).toBeDisabled();
		expect(screen.queryByRole("button", { name: /^Test$/ })).toBeNull();
	});

	test("summarises its scope with project names, and the line filter", () => {
		renderCard(
			drainView({
				allProjects: false,
				projectIds: ["p-web"],
				serviceIds: ["a", "b"],
				query: " level:error ",
			}),
		);
		expect(screen.getByText("2 services in Web")).toBeVisible();
		expect(screen.getByText("Only lines matching")).toBeVisible();
		expect(screen.getByText("level:error")).toBeVisible();
	});

	test("says All projects and no filter when unscoped", () => {
		renderCard(drainView());
		expect(screen.getByText("All projects")).toBeVisible();
		expect(screen.queryByText("Only lines matching")).toBeNull();
	});

	test("with no server report, says it is waiting for a server", () => {
		renderCard(drainView({ servers: null }));
		expect(screen.getByText("Waiting for a server")).toBeVisible();
	});

	test("lists each server's state and its delivery numbers", () => {
		renderCard(
			drainView({
				servers: [
					serverStatus({
						serverName: "fra-1",
						sentLines: 1204,
						droppedLines: 3,
						backlogBytes: 2048,
					}),
					serverStatus({
						serverId: "srv-2",
						serverName: "ams-1",
						state: "failing",
						lastError: "connection refused",
						lastErrorAt: "2026-11-01T09:00:00Z",
					}),
				],
			}),
		);
		expect(screen.queryByText("Waiting for a server")).toBeNull();
		const items = within(card()).getAllByRole("listitem");
		expect(items).toHaveLength(2);
		expect(items[0]).toHaveTextContent("fra-1");
		expect(items[0]).toHaveTextContent("Delivering");
		expect(items[0]).toHaveTextContent(
			"1,204 sent · 3 dropped · 2 KiB waiting",
		);
		expect(items[1]).toHaveTextContent("ams-1");
		expect(items[1]).toHaveTextContent("Failing");
		expect(items[1]).toHaveTextContent("connection refused");
	});

	test("a delivering server hides a stale error", () => {
		renderCard(
			drainView({
				servers: [serverStatus({ state: "ok", lastError: "old failure" })],
			}),
		);
		expect(screen.queryByText(/old failure/)).toBeNull();
	});

	test("an error without a timestamp shows just the message", () => {
		renderCard(
			drainView({
				servers: [serverStatus({ state: "retrying", lastError: "timeout" })],
			}),
		);
		const item = within(card()).getByRole("listitem");
		expect(item).toHaveTextContent("Retrying");
		expect(item).toHaveTextContent("timeout");
		expect(item.querySelector("time")).toBeNull();
	});

	test("a never-tested drain says Not tested yet", () => {
		renderCard(drainView({ test: { message: "" } }));
		expect(screen.getByText("Not tested yet")).toBeVisible();
	});

	test("shows a successful last test with its age and no error line", () => {
		renderCard(
			drainView({
				test: { ok: true, at: "2026-11-01T09:00:00Z", message: "" },
			}),
		);
		expect(screen.getByText("Last test line arrived")).toBeVisible();
		expect(screen.queryByText("Not tested yet")).toBeNull();
		expect(card().querySelector("time")).not.toBeNull();
	});

	test("shows a failed last test with the receiver's message", () => {
		renderCard(drainView({ test: { ok: false, message: "401 Unauthorized" } }));
		expect(screen.getByText("Last test failed")).toBeVisible();
		expect(screen.getByText("401 Unauthorized")).toBeVisible();
	});

	test("a failed last test with an empty message shows no detail line", () => {
		renderCard(drainView({ test: { ok: false, message: "" } }));
		expect(screen.getByText("Last test failed")).toBeVisible();
		expect(card().querySelector('[title=""]')).toBeNull();
	});

	test("shows the outcome of the test just run, and Dismiss reports it", async () => {
		const user = userEvent.setup();
		const outcome: TestOutcome = {
			tone: "danger",
			title: "Test line failed",
			detail: "From fra-1 · HTTP 500",
		};
		const h = renderCard(drainView(), { outcome });
		const status = screen.getByRole("status");
		expect(status).toHaveTextContent("Test line failed");
		expect(status).toHaveTextContent("From fra-1 · HTTP 500");
		await user.click(within(status).getByRole("button", { name: "Dismiss" }));
		expect(h.onDismissOutcome).toHaveBeenCalledTimes(1);
	});

	test("an outcome without detail shows only its title", () => {
		renderCard(drainView(), {
			outcome: { tone: "success", title: "Test line delivered", detail: "" },
		});
		expect(screen.getByRole("status")).toHaveTextContent(
			/^Test line deliveredDismiss$/,
		);
	});

	test("no outcome means no status region", () => {
		renderCard(drainView());
		expect(screen.queryByRole("status")).toBeNull();
	});
});

describe("DrainCard: the built-in central logs", () => {
	const builtin = (enabled: boolean) =>
		drainView({
			builtin: true,
			enabled,
			name: "internal",
			kind: "http",
			loki: undefined,
		});

	test("is titled Central logs with its retention line, and cannot be edited or deleted", () => {
		renderCard(builtin(true));
		expect(screen.getByRole("heading", { name: "Central logs" })).toBeVisible();
		expect(screen.getByText("Built-in · kept 7 days")).toBeVisible();
		expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
		expect(screen.getByRole("button", { name: "Test" })).toBeVisible();
	});

	test("when on, explains where lines go and offers to turn it off", () => {
		renderCard(builtin(true));
		expect(
			screen.getByText(/kept for 7 days on the control plane/),
		).toBeVisible();
		expect(screen.queryByText("off")).toBeNull();
		expect(
			screen.getByRole("switch", { name: "Turn off central logs" }),
		).toBeChecked();
	});

	test("when off, says Off, shows the off badge and offers to turn it on", async () => {
		const user = userEvent.setup();
		const h = renderCard(builtin(false));
		expect(screen.getByText("off")).toBeVisible();
		expect(screen.getByText(/^Off\. Turn it on to collect/)).toBeVisible();
		await user.click(
			screen.getByRole("switch", { name: "Turn on central logs" }),
		);
		expect(h.onToggle.mock.calls[0]?.[0]).toBe(true);
	});
});
