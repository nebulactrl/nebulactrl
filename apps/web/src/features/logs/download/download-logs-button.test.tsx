import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DownloadLogsButton } from "./download-logs-button";

interface Saved {
	name: string;
	href: string;
	attached: boolean;
}

let saved: Saved[] = [];
let revoked: string[] = [];
const realCreate = URL.createObjectURL;
const realRevoke = URL.revokeObjectURL;
let clickSpy: ReturnType<typeof spyOn>;

beforeEach(() => {
	saved = [];
	revoked = [];
	URL.createObjectURL = () => "blob:logs-1";
	URL.revokeObjectURL = (href: string) => {
		revoked.push(href);
	};
	clickSpy = spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(
		function (this: HTMLAnchorElement) {
			saved.push({
				name: this.download,
				href: this.getAttribute("href") ?? "",
				attached: document.body.contains(this),
			});
		},
	);
});

afterEach(() => {
	clickSpy.mockRestore();
	URL.createObjectURL = realCreate;
	URL.revokeObjectURL = realRevoke;
});

const fileReply = (disposition?: string) => () =>
	new Response("line one\nline two\n", {
		headers: disposition ? { "Content-Disposition": disposition } : {},
	});

async function openMenu() {
	const user = userEvent.setup();
	await user.click(screen.getByRole("button", { name: /Download/ }));
	return user;
}

describe("DownloadLogsButton", () => {
	test("offers a text and an NDJSON file", async () => {
		await renderWithProviders(
			<DownloadLogsButton target={{ kind: "service", id: "s1" }} />,
		);
		await openMenu();
		expect(
			await screen.findByRole("menuitem", { name: "Plain text (.txt)" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("menuitem", { name: "NDJSON (.ndjson)" }),
		).toBeInTheDocument();
	});

	test("downloads a service's log with the range, search, component and process on screen", async () => {
		const api = stubApi({
			"GET /services/:id/logs/download": fileReply(
				'attachment; filename="api-logs.txt"',
			),
		});
		await renderWithProviders(
			<DownloadLogsButton
				target={{ kind: "service", id: "s1" }}
				query="  level:error "
				component="db"
				process="worker"
				range={() => ({
					from: "2026-10-20T10:00:00.000Z",
					to: "2026-10-20T12:00:00.000Z",
				})}
			/>,
		);
		const user = await openMenu();
		await user.click(
			await screen.findByRole("menuitem", { name: "Plain text (.txt)" }),
		);
		await screen.findByRole("button", { name: /Download/ });
		await waitFor(() => expect(saved).toHaveLength(1));
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/services/s1/logs/download");
		expect(Object.fromEntries(api.calls[0]?.query ?? [])).toEqual({
			format: "txt",
			from: "2026-10-20T10:00:00.000Z",
			to: "2026-10-20T12:00:00.000Z",
			q: "level:error",
			component: "db",
			process: "worker",
		});
		expect(saved[0]).toEqual({
			name: "api-logs.txt",
			href: "blob:logs-1",
			attached: true,
		});
		expect(revoked).toEqual(["blob:logs-1"]);
		expect(document.querySelector("a[download]")).toBeNull();
		expect(api.unmatched).toEqual([]);
	});

	test("a deployment log is fetched from the deployment endpoint as NDJSON, with a default name", async () => {
		const api = stubApi({
			"GET /deployments/:id/logs/download": fileReply(),
		});
		await renderWithProviders(
			<DownloadLogsButton target={{ kind: "deployment", id: "d9" }} />,
		);
		const user = await openMenu();
		await user.click(
			await screen.findByRole("menuitem", { name: "NDJSON (.ndjson)" }),
		);
		await waitFor(() => expect(saved).toHaveLength(1));
		expect(api.calls[0]?.path).toBe("/deployments/d9/logs/download");
		expect(Object.fromEntries(api.calls[0]?.query ?? [])).toEqual({
			format: "ndjson",
		});
		expect(saved[0]?.name).toBe("deployment-logs.ndjson");
	});

	test("a failed download shows the server's reason and saves nothing", async () => {
		const api = stubApi({
			"GET /services/:id/logs/download": problem(503, {
				status: 503,
				title: "Unavailable",
				detail: "Server is offline.",
			}),
		});
		await renderWithProviders(
			<DownloadLogsButton target={{ kind: "service", id: "s1" }} />,
		);
		const user = await openMenu();
		await user.click(
			await screen.findByRole("menuitem", { name: "Plain text (.txt)" }),
		);
		expect(
			await screen.findByText("Could not download the log"),
		).toBeInTheDocument();
		expect(await screen.findByText("Server is offline.")).toBeInTheDocument();
		expect(saved).toEqual([]);
		expect(api.calls).toHaveLength(1);
	});

	test("an error body that is not a problem document falls back to the status", async () => {
		stubApi({
			"GET /services/:id/logs/download": () =>
				new Response("<html>bad gateway</html>", { status: 502 }),
		});
		await renderWithProviders(
			<DownloadLogsButton target={{ kind: "service", id: "s1" }} />,
		);
		const user = await openMenu();
		await user.click(
			await screen.findByRole("menuitem", { name: "Plain text (.txt)" }),
		);
		expect(await screen.findByText("HTTP 502")).toBeInTheDocument();
		expect(saved).toEqual([]);
	});

	test("the button is disabled while the file is being fetched", async () => {
		let release: (res: Response) => void = () => {};
		let asked = false;
		stubApi({
			"GET /services/:id/logs/download": () =>
				new Promise<Response>((resolve) => {
					asked = true;
					release = resolve;
				}),
		});
		await renderWithProviders(
			<DownloadLogsButton
				target={{ kind: "service", id: "s1" }}
				label="Export"
			/>,
		);
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: /Export/ }));
		await user.click(
			await screen.findByRole("menuitem", { name: "Plain text (.txt)" }),
		);
		// Plain checks: a failing jest-dom matcher prints the whole DOM, which is slow.
		const exportButton = () =>
			screen.getByRole("button", { name: /Export/ }) as HTMLButtonElement;
		await waitFor(() => expect(exportButton().disabled).toBe(true));
		await waitFor(() => expect(asked).toBe(true));
		await act(async () => {
			release(new Response("x"));
		});
		await waitFor(() => expect(exportButton().disabled).toBe(false));
		expect(saved).toHaveLength(1);
	});
});
