import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { AlertSettings } from "./alert-settings";
import { problem } from "./test-fixtures";

const SETTINGS = {
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
const PATH = "/notifications/alert-settings";

async function open(routes: Parameters<typeof stubApi>[0] = {}) {
	const api = stubApi({ [`GET ${PATH}`]: { body: SETTINGS }, ...routes });
	const user = userEvent.setup();
	await renderWithProviders(<AlertSettings />);
	await screen.findByLabelText("Down for");
	return { api, user };
}

describe("AlertSettings", () => {
	test("fills every threshold from the saved settings with Save and Reset disabled", async () => {
		const { api } = await open();
		expect(screen.getByLabelText("Down for")).toHaveValue("30");
		expect(screen.getByLabelText("Unhealthy for")).toHaveValue("60");
		expect(screen.getByLabelText("Restart loop at")).toHaveValue("5");
		expect(screen.getByLabelText("Restart window")).toHaveValue("10");
		expect(screen.getByLabelText("Healthy for")).toHaveValue("120");
		expect(screen.getByLabelText("Flapping at")).toHaveValue("4");
		expect(screen.getByLabelText("Flap window")).toHaveValue("30");
		expect(screen.getByLabelText("Backup grace")).toHaveValue("0");
		expect(screen.getByLabelText("Log lines")).toHaveValue("10");
		expect(
			screen.getByRole("button", { name: "Save thresholds" }),
		).toBeDisabled();
		expect(screen.getByRole("button", { name: "Reset" })).toBeDisabled();
		expect(api.unmatched).toEqual([]);
	});

	test("shows an error instead of the form when the settings did not load", async () => {
		stubApi({
			[`GET ${PATH}`]: problem(403, "admin scope required", "forbidden"),
		});
		await renderWithProviders(<AlertSettings />);
		expect(await screen.findByText("admin scope required")).toBeInTheDocument();
		expect(screen.queryByLabelText("Down for")).not.toBeInTheDocument();
	});

	test("editing a threshold enables Save, and saving PUTs every setting as numbers and confirms", async () => {
		const { api, user } = await open({
			[`PUT ${PATH}`]: { body: { ...SETTINGS, crashGraceSeconds: 90 } },
		});
		const field = screen.getByLabelText("Down for");
		await user.clear(field);
		await user.type(field, " 90 ");
		await user.click(screen.getByRole("button", { name: "Save thresholds" }));
		expect(
			await screen.findByText("Alert thresholds saved"),
		).toBeInTheDocument();
		const put = api.calls.find((call) => call.method === "PUT");
		expect(put?.path).toBe(PATH);
		expect(put?.body).toEqual({ ...SETTINGS, crashGraceSeconds: 90 });
		expect(api.unmatched).toEqual([]);
	});

	test("after saving, the form follows the refetched settings", async () => {
		let saved = SETTINGS;
		const { user } = await open({
			[`GET ${PATH}`]: () => Response.json(saved),
			[`PUT ${PATH}`]: async (request) => {
				saved = (await request.json()) as typeof SETTINGS;
				return Response.json(saved);
			},
		});
		const field = screen.getByLabelText("Log lines");
		await user.clear(field);
		await user.type(field, "3");
		await user.click(screen.getByRole("button", { name: "Save thresholds" }));
		await screen.findByText("Alert thresholds saved");
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Save thresholds" }),
			).toBeDisabled(),
		);
		expect(screen.getByLabelText("Log lines")).toHaveValue("3");
	});

	test("Reset drops the edits", async () => {
		const { api, user } = await open();
		const field = screen.getByLabelText("Flapping at");
		await user.clear(field);
		await user.type(field, "9");
		await user.click(screen.getByRole("button", { name: "Reset" }));
		expect(screen.getByLabelText("Flapping at")).toHaveValue("4");
		expect(screen.getByRole("button", { name: "Reset" })).toBeDisabled();
		expect(api.calls.filter((call) => call.method !== "GET")).toEqual([]);
	});

	test("a non-number or out-of-range value is flagged and cannot be saved", async () => {
		const { api, user } = await open();
		const loop = screen.getByLabelText("Restart loop at");
		await user.clear(loop);
		await user.type(loop, "1");
		expect(screen.getByText("Between 2 and 50.")).toBeInTheDocument();
		expect(loop).toHaveAttribute("aria-invalid", "true");
		const down = screen.getByLabelText("Down for");
		await user.clear(down);
		await user.type(down, "1.5");
		expect(screen.getAllByText("Enter a whole number.")).toHaveLength(1);
		await user.clear(down);
		expect(screen.getAllByText("Enter a whole number.")).toHaveLength(1);
		const save = screen.getByRole("button", { name: "Save thresholds" });
		expect(save).toBeDisabled();
		await user.type(down, "99999");
		expect(screen.getByText("Between 0 and 3600.")).toBeInTheDocument();
		expect(save).toBeDisabled();
		// Submitting with Enter does not send an invalid form either.
		await user.type(down, "{Enter}");
		expect(api.calls.filter((call) => call.method === "PUT")).toEqual([]);
	});

	test("a rejected save shows the server's reason and keeps the edit", async () => {
		const { user } = await open({
			[`PUT ${PATH}`]: problem(422, "loopWindowMinutes too small"),
		});
		const field = screen.getByLabelText("Restart window");
		await user.clear(field);
		await user.type(field, "20");
		await user.click(screen.getByRole("button", { name: "Save thresholds" }));
		expect(
			await screen.findAllByText("loopWindowMinutes too small"),
		).not.toHaveLength(0);
		expect(
			screen.queryByText("Alert thresholds saved"),
		).not.toBeInTheDocument();
		expect(screen.getByLabelText("Restart window")).toHaveValue("20");
		// Editing again clears the stale error alert.
		await user.type(field, "1");
		await waitFor(() =>
			expect(
				screen.queryByText("loopWindowMinutes too small", {
					selector: "[data-slot=alert-description]",
				}),
			).not.toBeInTheDocument(),
		);
	});
});
