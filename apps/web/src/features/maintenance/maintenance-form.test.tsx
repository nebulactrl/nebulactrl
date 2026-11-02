import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { MaintenanceForm } from "./maintenance-form";
import { toLocalInput } from "./model";

const off = (over: Partial<Service> = {}) =>
	makeService({ id: "svc-m", name: "Storefront", ...over });

const on = (maintenance: Partial<Service["maintenance"]> = {}) =>
	off({
		maintenance: {
			enabled: true,
			title: "Upgrade",
			message: "Back soon",
			eta: null,
			since: new Date(Date.now() - 60_000).toISOString(),
			...maintenance,
		},
	});

/** The service refetch after a change; the stub answers with whatever `current` is. */
function backend(current: () => Service) {
	return stubApi({
		"PUT /services/:id/maintenance": { body: make("ServiceView", {}) },
		"GET /services/:id": () => Response.json(current()),
		"GET /environments/:id/services": () =>
			Response.json({ items: [current()] }),
	});
}

const put = (api: ReturnType<typeof stubApi>) =>
	api.calls.find((call) => call.method === "PUT");

const future = () => {
	const date = new Date(Date.now() + 3 * 24 * 3600_000);
	date.setHours(10, 30, 0, 0);
	return date;
};

describe("MaintenanceForm: turning maintenance on", () => {
	test("sends the page text and turns it on; the empty form sends only enabled", async () => {
		const api = backend(() => on());
		const applied = mock(() => {});
		await renderWithProviders(
			<MaintenanceForm service={off()} onApplied={applied} />,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Turn on maintenance" }));
		expect(
			await screen.findByText("Storefront is in maintenance"),
		).toBeInTheDocument();
		expect(put(api)).toMatchObject({
			path: "/services/svc-m/maintenance",
			body: { enabled: true },
		});
		expect(Object.keys(put(api)?.body as object)).toEqual(["enabled"]);
		await waitFor(() => expect(applied).toHaveBeenCalledTimes(1));
	});

	test("sends a trimmed title and message and a typed return time", async () => {
		const api = backend(() => on());
		await renderWithProviders(<MaintenanceForm service={off()} />);
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Title"));
		await user.paste("  Database upgrade ");
		await user.click(screen.getByLabelText("Message"));
		await user.paste("Back at lunch ");
		const eta = future();
		const [date = "", time = ""] = toLocalInput(eta).split("T");
		await user.type(screen.getByLabelText("Expected back, date"), date);
		await user.type(
			screen.getByLabelText("Expected back, time (24-hour)"),
			time,
		);
		await user.click(
			screen.getByRole("button", { name: "Turn on maintenance" }),
		);
		await screen.findByText("Storefront is in maintenance");
		expect(put(api)?.body).toEqual({
			enabled: true,
			title: "Database upgrade",
			message: "Back at lunch",
			eta: eta.toISOString(),
		});
	});

	test("an expected-back preset fills both fields with a future time; Clear empties them", async () => {
		backend(() => on());
		await renderWithProviders(<MaintenanceForm service={off()} />);
		const user = userEvent.setup();
		const dateField = screen.getByLabelText("Expected back, date");
		expect(
			screen.queryByRole("button", { name: "Clear" }),
		).not.toBeInTheDocument();
		const before = Date.now();
		await user.click(screen.getByRole("button", { name: "4 hours" }));
		const timeField = screen.getByLabelText("Expected back, time (24-hour)");
		expect((dateField as HTMLInputElement).value).toMatch(
			/^\d{4}-\d{2}-\d{2}$/,
		);
		expect((timeField as HTMLInputElement).value).toMatch(/^\d{2}:\d{2}$/);
		const shown = new Date(
			`${(dateField as HTMLInputElement).value}T${(timeField as HTMLInputElement).value}`,
		);
		expect(shown.getTime() - before).toBeGreaterThan(3.9 * 3600_000);
		expect(shown.getTime() - before).toBeLessThan(4.1 * 3600_000);
		await user.click(screen.getByRole("button", { name: "Clear" }));
		expect(dateField).toHaveValue("");
		expect(timeField).toHaveValue("");
		expect(
			screen.queryByRole("button", { name: "Clear" }),
		).not.toBeInTheDocument();
	});

	test("a preset's time reaches the request as that instant", async () => {
		const api = backend(() => on());
		await renderWithProviders(<MaintenanceForm service={off()} />);
		const user = userEvent.setup();
		const before = Date.now();
		await user.click(screen.getByRole("button", { name: "1 hour" }));
		await user.click(
			screen.getByRole("button", { name: "Turn on maintenance" }),
		);
		await screen.findByText("Storefront is in maintenance");
		const request = put(api);
		if (!request) throw new Error("no PUT request was sent");
		const sent = new Date((request.body as { eta: string }).eta).getTime();
		// The form works in whole minutes.
		expect(sent - before).toBeGreaterThan(59 * 60_000 - 1000);
		expect(sent - before).toBeLessThan(61 * 60_000 + 1000);
	});
});

describe("MaintenanceForm: validation", () => {
	test("an over-long title blocks turning on and says why", async () => {
		const api = backend(() => on());
		await renderWithProviders(<MaintenanceForm service={off()} />);
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Title"));
		await user.paste("x".repeat(81));
		expect(
			screen.getByText("The title can be at most 80 characters."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Title")).toBeInvalid();
		const button = screen.getByRole("button", { name: "Turn on maintenance" });
		expect(button).toBeDisabled();
		await user.click(button);
		expect(api.calls).toEqual([]);
	});

	test("an over-long message swaps the hint for the error", async () => {
		backend(() => on());
		await renderWithProviders(<MaintenanceForm service={off()} />);
		expect(screen.getByText(/600 characters at most/)).toBeInTheDocument();
		await userEvent.setup().click(screen.getByLabelText("Message"));
		await userEvent.setup().paste("y".repeat(601));
		expect(
			screen.getByText("The message can be at most 600 characters."),
		).toBeInTheDocument();
		expect(
			screen.queryByText(/600 characters at most/),
		).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Turn on maintenance" }),
		).toBeDisabled();
	});

	test("a partly typed or past return time is refused", async () => {
		backend(() => on());
		await renderWithProviders(<MaintenanceForm service={off()} />);
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Expected back, date"), "2099-01-01");
		expect(
			screen.getByText("Enter a valid date and time."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Turn on maintenance" }),
		).toBeDisabled();
		await user.type(
			screen.getByLabelText("Expected back, time (24-hour)"),
			"10:00",
		);
		expect(
			screen.queryByText("Enter a valid date and time."),
		).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Turn on maintenance" }),
		).toBeEnabled();
		await user.clear(screen.getByLabelText("Expected back, date"));
		await user.type(screen.getByLabelText("Expected back, date"), "2001-01-01");
		expect(
			screen.getByText("The expected return time must be in the future."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Expected back, date")).toBeInvalid();
	});

	test("the zone note names the time zone the times are read in", async () => {
		backend(() => on());
		await renderWithProviders(<MaintenanceForm service={off()} />);
		expect(
			screen.getByText(/Times are shown in .+, 24-hour\./),
		).toBeInTheDocument();
	});
});

describe("MaintenanceForm: preview", () => {
	test("the link carries the unsaved text to the preview page", async () => {
		backend(() => on());
		await renderWithProviders(<MaintenanceForm service={off()} />);
		const link = screen.getByRole("link", { name: /Preview page/ });
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute(
			"href",
			"/api/v1/services/svc-m/maintenance/preview?draft=true",
		);
		await userEvent.setup().type(screen.getByLabelText("Title"), "Upgrade");
		expect(link.getAttribute("href")).toBe(
			"/api/v1/services/svc-m/maintenance/preview?draft=true&title=Upgrade",
		);
	});
});

describe("MaintenanceForm: while maintenance is on", () => {
	test("starts from the live page text; Update page waits for a change", async () => {
		backend(() => on());
		await renderWithProviders(<MaintenanceForm service={on()} />);
		expect(screen.getByLabelText("Title")).toHaveValue("Upgrade");
		expect(screen.getByLabelText("Message")).toHaveValue("Back soon");
		expect(
			screen.queryByRole("button", { name: "Turn on maintenance" }),
		).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Turn off" })).toBeEnabled();
		expect(screen.getByRole("button", { name: "Update page" })).toBeDisabled();
		await userEvent.setup().type(screen.getByLabelText("Title"), "!");
		expect(screen.getByRole("button", { name: "Update page" })).toBeEnabled();
	});

	test("Update page sends the edited text with enabled", async () => {
		const api = backend(() => on({ title: "Upgrade!" }));
		await renderWithProviders(<MaintenanceForm service={on()} />);
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Title"), "!");
		await user.click(screen.getByRole("button", { name: "Update page" }));
		await screen.findByText("Storefront is in maintenance");
		expect(put(api)?.body).toEqual({
			enabled: true,
			title: "Upgrade!",
			message: "Back soon",
		});
	});

	test("submitting with Enter does nothing while the page is unchanged", async () => {
		const api = backend(() => on());
		await renderWithProviders(<MaintenanceForm service={on()} />);
		const user = userEvent.setup();
		await user.click(screen.getByLabelText("Title"));
		await user.keyboard("{Enter}");
		expect(api.calls).toEqual([]);
	});

	test("Turn off sends enabled=false and says the service is back", async () => {
		const api = backend(() => off());
		const applied = mock(() => {});
		await renderWithProviders(
			<MaintenanceForm service={on()} onApplied={applied} />,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Turn off" }));
		expect(await screen.findByText("Storefront is back")).toBeInTheDocument();
		expect(put(api)?.body).toEqual({ enabled: false });
		await waitFor(() => expect(applied).toHaveBeenCalledTimes(1));
	});

	test("a refused change shows the reason, keeps the typing and does not report applied", async () => {
		const applied = mock(() => {});
		stubApi({
			"PUT /services/:id/maintenance": problem(409, {
				title: "Conflict",
				status: 409,
				detail: "Service has no domain",
			}),
		});
		await renderWithProviders(
			<MaintenanceForm service={off()} onApplied={applied} />,
		);
		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Title"), "Upgrade");
		await user.click(
			screen.getByRole("button", { name: "Turn on maintenance" }),
		);
		expect(
			await screen.findByText("Service has no domain"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Title")).toHaveValue("Upgrade");
		expect(applied).not.toHaveBeenCalled();
		// Editing again clears the stale error.
		await user.type(screen.getByLabelText("Title"), "!");
		expect(screen.queryByText("Service has no domain")).not.toBeInTheDocument();
	});
});
