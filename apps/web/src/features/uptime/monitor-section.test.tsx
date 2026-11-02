import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { MonitorSection } from "./monitor-section";

const saved = {
	enabled: true,
	alerts: true,
	failureThreshold: 3,
	recoveryThreshold: 2,
	reminderMinutes: 30,
};

const service = { id: "svc-1", name: "api" };

const problem = (status: number, detail: string, code = "bad_request") => ({
	status,
	body: { status, title: "Error", detail, code },
});

const render = () => renderWithProviders(<MonitorSection service={service} />);

const apply = () => screen.getByRole("button", { name: "Apply" });
const discard = () => screen.getByRole("button", { name: "Discard" });

describe("MonitorSection", () => {
	test("shows a loading placeholder, then the saved monitor", async () => {
		stubApi({ "GET /services/:id/uptime-monitor": { body: saved } });
		await render();
		expect(
			screen.queryByRole("switch", { name: "Monitor this service" }),
		).toBeNull();
		expect(
			await screen.findByRole("switch", { name: "Monitor this service" }),
		).toBeChecked();
		expect(screen.getByRole("switch", { name: "Alerts" })).toBeChecked();
		expect(screen.getByLabelText("Failed checks before an outage")).toHaveValue(
			"3",
		);
		expect(screen.getByLabelText("Passing checks to recover")).toHaveValue("2");
		expect(screen.getByText("Monitored")).toBeInTheDocument();
		expect(screen.getByText("Checked every 30 seconds")).toBeInTheDocument();
		expect(screen.getByText("Alert on outages")).toBeInTheDocument();
		expect(screen.getByText("Reminders: Every 30 min")).toBeInTheDocument();
	});

	test("a disabled monitor is marked Off", async () => {
		stubApi({
			"GET /services/:id/uptime-monitor": {
				body: { ...saved, enabled: false, alerts: false, reminderMinutes: 0 },
			},
		});
		await render();
		expect(
			await screen.findByRole("switch", { name: "Monitor this service" }),
		).not.toBeChecked();
		expect(screen.getByRole("heading", { name: /Off$/ })).toBeInTheDocument();
		expect(screen.getByText("Not checked")).toBeInTheDocument();
		expect(screen.getByText("Record only")).toBeInTheDocument();
		expect(screen.getByText("Reminders: Off")).toBeInTheDocument();
	});

	test("explains in time what each threshold means", async () => {
		stubApi({ "GET /services/:id/uptime-monitor": { body: saved } });
		const user = userEvent.setup();
		await render();
		expect(
			await screen.findByText(
				"Checks run every 30 s, so an outage opens after about 1.5 min.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Checks run every 30 s, so it counts as recovered after about 1 min.",
			),
		).toBeInTheDocument();
		const failure = screen.getByLabelText("Failed checks before an outage");
		await user.clear(failure);
		await user.type(failure, "1");
		expect(
			screen.getByText(
				"Checks run every 30 s, so an outage opens after about 30 s.",
			),
		).toBeInTheDocument();
		await user.clear(failure);
		expect(
			screen.getByText("From 1 to 20 failed checks in a row."),
		).toBeInTheDocument();
	});

	test("shows the problem when the monitor did not load", async () => {
		stubApi({
			"GET /services/:id/uptime-monitor": problem(
				500,
				"monitor store down",
				"internal",
			),
		});
		await render();
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"monitor store down",
		);
		expect(
			screen.queryByRole("switch", { name: "Monitor this service" }),
		).toBeNull();
	});

	test("nothing is dirty until the form changes, and Apply/Discard stay disabled", async () => {
		stubApi({ "GET /services/:id/uptime-monitor": { body: saved } });
		await render();
		await screen.findByRole("switch", { name: "Monitor this service" });
		expect(apply()).toBeDisabled();
		expect(discard()).toBeDisabled();
		expect(screen.queryByText("Unsaved changes")).toBeNull();
	});

	test("turning the monitor off and applying sends the full body, confirms, and reloads", async () => {
		let current = saved;
		const api = stubApi({
			"GET /services/:id/uptime-monitor": () => Response.json(current),
			"PUT /services/:id/uptime-monitor": async (request) => {
				current = (await request.json()) as typeof saved;
				return Response.json(current);
			},
			"GET /services/:id/uptime": { body: make("ServiceReport", {}) },
			"GET /environments/:id/uptime": { body: { items: [] } },
		});
		const user = userEvent.setup();
		await render();
		await user.click(
			await screen.findByRole("switch", { name: "Monitor this service" }),
		);
		expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
		expect(screen.getByText("Not checked")).toBeInTheDocument();
		await user.click(apply());
		expect(
			await screen.findByText("Uptime monitoring of api applied"),
		).toBeInTheDocument();
		const put = api.calls.find((call) => call.method === "PUT");
		expect(put?.path).toBe("/services/svc-1/uptime-monitor");
		expect(put?.body).toEqual({ ...saved, enabled: false });
		// The saved copy is refetched and the form follows it.
		await waitFor(() =>
			expect(screen.getByRole("heading", { name: /Off$/ })).toBeInTheDocument(),
		);
		expect(screen.queryByText("Unsaved changes")).toBeNull();
		expect(
			screen.getByRole("switch", { name: "Monitor this service" }),
		).not.toBeChecked();
		expect(
			api.calls.filter(
				(call) =>
					call.method === "GET" && call.path.endsWith("/uptime-monitor"),
			).length,
		).toBeGreaterThanOrEqual(2);
		expect(api.unmatched).toEqual([]);
	});

	test("editing thresholds and the reminder preset applies those numbers", async () => {
		const api = stubApi({
			"GET /services/:id/uptime-monitor": { body: saved },
			"PUT /services/:id/uptime-monitor": { body: saved },
		});
		const user = userEvent.setup();
		await render();
		const failure = await screen.findByLabelText(
			"Failed checks before an outage",
		);
		await user.clear(failure);
		await user.type(failure, "5");
		const recovery = screen.getByLabelText("Passing checks to recover");
		await user.clear(recovery);
		await user.type(recovery, " 4 ");
		await user.click(screen.getByLabelText("Remind while down"));
		await user.click(
			await screen.findByRole("option", { name: "Every 2 hours" }),
		);
		await user.click(apply());
		await waitFor(() =>
			expect(api.calls.some((call) => call.method === "PUT")).toBe(true),
		);
		expect(api.calls.find((call) => call.method === "PUT")?.body).toEqual({
			enabled: true,
			alerts: true,
			failureThreshold: 5,
			recoveryThreshold: 4,
			reminderMinutes: 120,
		});
	});

	test("a custom reminder takes minutes and validates them", async () => {
		const api = stubApi({
			"GET /services/:id/uptime-monitor": { body: saved },
			"PUT /services/:id/uptime-monitor": { body: saved },
		});
		const user = userEvent.setup();
		await render();
		await user.click(await screen.findByLabelText("Remind while down"));
		await user.click(await screen.findByRole("option", { name: "Custom" }));
		const minutes = screen.getByLabelText("Reminder interval in minutes");
		expect(minutes).toHaveValue("");
		expect(
			screen.getByText("Minutes must be a whole number from 1 to 1440."),
		).toBeInTheDocument();
		expect(minutes).toHaveAttribute("aria-invalid", "true");
		expect(apply()).toBeDisabled();
		await user.type(minutes, "1441");
		expect(apply()).toBeDisabled();
		await user.clear(minutes);
		await user.type(minutes, "45");
		expect(minutes).toHaveAttribute("aria-invalid", "false");
		await user.click(apply());
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "PUT")).toBe(true),
		);
		expect(api.calls.find((c) => c.method === "PUT")?.body).toMatchObject({
			reminderMinutes: 45,
		});
	});

	test("a saved reminder that is not a preset opens as Custom with its minutes", async () => {
		stubApi({
			"GET /services/:id/uptime-monitor": {
				body: { ...saved, reminderMinutes: 45 },
			},
		});
		await render();
		expect(
			await screen.findByLabelText("Reminder interval in minutes"),
		).toHaveValue("45");
		expect(screen.getByText("Reminders: Every 45 min")).toBeInTheDocument();
	});

	test("an out-of-range threshold shows its error and blocks Apply", async () => {
		const api = stubApi({
			"GET /services/:id/uptime-monitor": { body: saved },
		});
		const user = userEvent.setup();
		await render();
		const failure = await screen.findByLabelText(
			"Failed checks before an outage",
		);
		await user.clear(failure);
		await user.type(failure, "21");
		expect(
			screen.getByText("Failed checks must be between 1 and 20."),
		).toBeInTheDocument();
		expect(apply()).toBeDisabled();
		// A form with a problem still counts as unsaved.
		expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
		expect(api.calls.some((call) => call.method === "PUT")).toBe(false);
	});

	test("an empty recovery threshold is rejected as not a whole number", async () => {
		stubApi({ "GET /services/:id/uptime-monitor": { body: saved } });
		const user = userEvent.setup();
		await render();
		await user.clear(await screen.findByLabelText("Passing checks to recover"));
		expect(
			screen.getByText("Passing checks must be a whole number."),
		).toBeInTheDocument();
		expect(apply()).toBeDisabled();
	});

	test("Discard puts the saved values back", async () => {
		stubApi({ "GET /services/:id/uptime-monitor": { body: saved } });
		const user = userEvent.setup();
		await render();
		const failure = await screen.findByLabelText(
			"Failed checks before an outage",
		);
		await user.clear(failure);
		await user.type(failure, "9");
		await user.click(screen.getByRole("switch", { name: "Alerts" }));
		await user.click(discard());
		expect(failure).toHaveValue("3");
		expect(screen.getByRole("switch", { name: "Alerts" })).toBeChecked();
		expect(screen.queryByText("Unsaved changes")).toBeNull();
		expect(apply()).toBeDisabled();
	});

	test("with alerts off, reminders are unavailable and say why", async () => {
		stubApi({ "GET /services/:id/uptime-monitor": { body: saved } });
		const user = userEvent.setup();
		await render();
		await user.click(await screen.findByRole("switch", { name: "Alerts" }));
		expect(screen.getByText("Needs alerts on.")).toBeInTheDocument();
		expect(screen.getByLabelText("Remind while down")).toBeDisabled();
		expect(screen.getByText("Record only")).toBeInTheDocument();
	});

	test("a failed save shows the server's reason and keeps the edit", async () => {
		stubApi({
			"GET /services/:id/uptime-monitor": { body: saved },
			"PUT /services/:id/uptime-monitor": problem(
				403,
				"Needs the admin scope",
				"forbidden",
			),
		});
		const user = userEvent.setup();
		await render();
		await user.click(
			await screen.findByRole("switch", { name: "Monitor this service" }),
		);
		await user.click(apply());
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Needs the admin scope");
		expect(
			screen.getByRole("switch", { name: "Monitor this service" }),
		).not.toBeChecked();
		expect(apply()).toBeEnabled();
		// Editing again clears the error.
		await user.click(screen.getByRole("switch", { name: "Alerts" }));
		expect(screen.queryByRole("alert")).toBeNull();
		expect(
			within(document.body).getByText("Unsaved changes"),
		).toBeInTheDocument();
	});
});
