import { describe, expect, it } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { service, view } from "./fixtures";
import { ServicesPlate } from "./services-plate";

describe("ServicesPlate", () => {
	it("lists the three services in order with status, team count and use", async () => {
		await renderWithProviders(<ServicesPlate view={view()} />);
		const items = screen.getAllByRole("listitem");
		expect(items).toHaveLength(3);
		expect(items[0]).toHaveTextContent("Backup storage");
		expect(items[0]).toHaveTextContent("Ready");
		expect(items[0]).toHaveTextContent("2 teams");
		expect(items[0]).toHaveTextContent("3 GiB used");
		expect(items[1]).toHaveTextContent("Image registry");
		expect(items[1]).toHaveTextContent("1 team");
		expect(items[1]).not.toHaveTextContent("1 teams");
		expect(items[1]).not.toHaveTextContent("GiB used");
		expect(items[2]).toHaveTextContent("Central logs");
		expect(items[2]).toHaveTextContent("Starting");
	});

	it("says a never-checked service was not checked yet and shows the failure reason", async () => {
		await renderWithProviders(
			<ServicesPlate
				view={view({
					services: [
						service({
							kind: "registry",
							status: "failed",
							message: "tunnel closed",
						}),
						service({ kind: "loki", status: "ready", message: "stale" }),
					],
				})}
			/>,
		);
		const items = screen.getAllByRole("listitem");
		expect(items).toHaveLength(2);
		expect(items[0]).toHaveTextContent("Not working");
		expect(items[0]).toHaveTextContent("tunnel closed");
		expect(items[0]).toHaveTextContent("not checked yet");
		// The message of a service that works is not shown.
		expect(items[1]).not.toHaveTextContent("stale");
	});

	it("treats a null service list as no rows", async () => {
		await renderWithProviders(
			<ServicesPlate view={view({ services: null })} />,
		);
		expect(screen.queryAllByRole("listitem")).toHaveLength(0);
	});

	it("Check now posts the check and toasts that every service answers", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /admin/instance-services/check": { body: view() },
		});
		await renderWithProviders(<ServicesPlate view={view()} />);
		await user.click(screen.getByRole("button", { name: "Check now" }));
		expect(
			await screen.findByText("Every built-in service answers."),
		).toBeInTheDocument();
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"POST /admin/instance-services/check",
		]);
		expect(api.unmatched).toEqual([]);
	});

	it("Check now names the services that are still failing", async () => {
		const user = userEvent.setup();
		stubApi({
			"POST /admin/instance-services/check": {
				body: view({
					services: [
						service({ kind: "s3", status: "failed", message: "no bucket" }),
						service({ kind: "loki", status: "failed", message: "down" }),
						service({ kind: "registry" }),
					],
				}),
			},
		});
		await renderWithProviders(<ServicesPlate view={view()} />);
		await user.click(screen.getByRole("button", { name: "Check now" }));
		expect(
			await screen.findByText("A built-in service is not working"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Backup storage, Central logs"),
		).toBeInTheDocument();
	});

	it("Check now toasts the server's reason when the check call fails", async () => {
		const user = userEvent.setup();
		stubApi({
			"POST /admin/instance-services/check": problem(500, {
				detail: "check exploded",
				title: "Internal",
			}),
		});
		await renderWithProviders(<ServicesPlate view={view()} />);
		const button = screen.getByRole("button", { name: "Check now" });
		await user.click(button);
		expect(await screen.findByText("Could not check")).toBeInTheDocument();
		expect(screen.getByText("check exploded")).toBeInTheDocument();
		await waitFor(() => expect(button).toBeEnabled());
	});
});
