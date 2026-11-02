import { describe, expect, it } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DefaultQuotaPlate } from "./default-quota-plate";
import { view } from "./fixtures";
import { GIB } from "./model";
import { instanceServicesQuery } from "./queries";

function Harness() {
	const { data } = useQuery(instanceServicesQuery());
	return data ? (
		<DefaultQuotaPlate defaultQuotaBytes={data.defaultQuotaBytes} />
	) : null;
}

const input = () => screen.getByLabelText("Backup storage per team (GiB)");
const save = () => screen.getByRole("button", { name: "Save" });

describe("DefaultQuotaPlate", () => {
	it("shows the current default in GiB with Save disabled until it changes", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<DefaultQuotaPlate defaultQuotaBytes={50 * GIB} />,
		);
		expect(input()).toHaveValue("50");
		expect(save()).toBeDisabled();
		await user.type(input(), "5");
		expect(save()).toBeEnabled();
		await user.clear(input());
		await user.type(input(), "50");
		expect(save()).toBeDisabled();
	});

	it("saves the typed GiB as bytes, caches the answer and confirms", async () => {
		const user = userEvent.setup();
		const saved = view({ defaultQuotaBytes: 75 * GIB });
		const api = stubApi({
			"PUT /admin/instance-services": { body: saved },
		});
		const { queryClient } = await renderWithProviders(
			<DefaultQuotaPlate defaultQuotaBytes={50 * GIB} />,
		);
		await user.clear(input());
		await user.type(input(), "75");
		await user.click(save());
		expect(await screen.findByText("Default quota saved")).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "PUT",
			path: "/admin/instance-services",
			body: { defaultQuotaBytes: 75 * GIB },
		});
		expect(queryClient.getQueryData(instanceServicesQuery().queryKey)).toEqual(
			saved,
		);
	});

	it("rejects a quota under 1 GiB without calling the control plane", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await renderWithProviders(
			<DefaultQuotaPlate defaultQuotaBytes={50 * GIB} />,
		);
		await user.clear(input());
		await user.type(input(), "0.5");
		await user.click(save());
		expect(
			screen.getByText("The smallest quota is 1 GiB."),
		).toBeInTheDocument();
		expect(input()).toHaveAttribute("aria-invalid", "true");
		expect(api.calls).toEqual([]);
		// Typing again clears the complaint.
		await user.type(input(), "1");
		expect(screen.queryByText("The smallest quota is 1 GiB.")).toBeNull();
		expect(screen.getByText(/At least 1 GiB/)).toBeInTheDocument();
	});

	it("rejects text that is not a number", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await renderWithProviders(
			<DefaultQuotaPlate defaultQuotaBytes={50 * GIB} />,
		);
		await user.clear(input());
		await user.type(input(), "lots");
		await user.click(save());
		expect(
			screen.getByText("Enter a number of GiB, like 50 or 12.5."),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	it("shows the server's refusal under the field", async () => {
		const user = userEvent.setup();
		stubApi({
			"PUT /admin/instance-services": problem(422, {
				code: "quota_too_small",
				detail: "Quota is below the usage.",
			}),
		});
		await renderWithProviders(
			<DefaultQuotaPlate defaultQuotaBytes={50 * GIB} />,
		);
		await user.clear(input());
		await user.type(input(), "2");
		await user.click(save());
		expect(
			await screen.findByText("Quota is below the usage."),
		).toBeInTheDocument();
		expect(input()).toHaveAttribute("aria-invalid", "true");
		await waitFor(() => expect(save()).toBeEnabled());
	});

	it("follows the control plane's value after a save and drops unsaved text", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /admin/instance-services": {
				body: view({ defaultQuotaBytes: 50 * GIB }),
			},
			"PUT /admin/instance-services": {
				body: view({ defaultQuotaBytes: 80 * GIB }),
			},
		});
		await renderWithProviders(<Harness />);
		await screen.findByLabelText("Backup storage per team (GiB)");
		await user.clear(input());
		await user.type(input(), "80");
		await user.click(save());
		await screen.findByText("Default quota saved");
		expect(input()).toHaveValue("80");
		expect(save()).toBeDisabled();
	});
});
