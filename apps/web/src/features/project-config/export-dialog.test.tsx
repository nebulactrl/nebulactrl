import {
	afterEach,
	beforeEach,
	describe,
	expect,
	jest,
	spyOn,
	test,
} from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ExportDialog } from "./export-dialog";
import { environment } from "./test-support";

const FILE = '[project]\nname = "shop"\n\n[services.web]\nport = 8080\n';

let writeText: ReturnType<typeof spyOn>;

beforeEach(() => {
	// user-event swaps in its own clipboard when a session starts; spying on the
	// one that was there before would be bypassed by the first click.
	userEvent.setup();
	writeText = spyOn(navigator.clipboard, "writeText").mockResolvedValue(
		undefined,
	);
});

afterEach(() => {
	jest.restoreAllMocks();
});

function open(onOpenChange: (next: boolean) => void = () => {}, open = true) {
	return renderWithProviders(
		<ExportDialog
			environment={environment()}
			open={open}
			onOpenChange={onOpenChange}
		/>,
	);
}

describe("ExportDialog", () => {
	test("exports the environment's file as JSON and shows it read-only", async () => {
		const api = stubApi({
			"GET /projects/:id/config/export": {
				body: make("ConfigExportResult", { file: FILE }),
			},
		});
		await open();
		const file = await screen.findByRole("textbox", { name: "nebula.toml" });
		expect(file).toHaveValue(FILE);
		expect(file).toHaveAttribute("readonly");
		expect(api.calls[0]?.path).toBe("/projects/p1/config/export");
		expect(api.calls[0]?.query.get("environment")).toBe("staging");
		expect(screen.queryByRole("alert", { name: "Export warnings" })).toBeNull();
	});

	test("asks nothing while closed", async () => {
		const api = stubApi({});
		await open(() => {}, false);
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("shows a skeleton while the file is being produced", async () => {
		let release: (response: Response) => void = () => {};
		stubApi({
			"GET /projects/:id/config/export": () =>
				new Promise<Response>((resolve) => {
					release = resolve;
				}),
		});
		await open();
		expect(
			await screen.findByRole("status", { name: "Exporting" }),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /Download/ })).toBeDisabled();
		release(Response.json({ file: FILE, warnings: [] }));
		expect(
			await screen.findByRole("textbox", { name: "nebula.toml" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("status", { name: "Exporting" })).toBeNull();
	});

	test("lists what the format cannot say, with each code", async () => {
		stubApi({
			"GET /projects/:id/config/export": {
				body: {
					file: FILE,
					warnings: [
						{
							severity: "warning",
							code: "secret_flag_differs",
							message: "API_KEY is a secret in staging only.",
						},
						{
							severity: "info",
							code: "volumes_not_exported",
							message: "Volumes are not exported.",
						},
						{ severity: "info", code: "empty", message: "" },
					],
				},
			},
		});
		await open();
		const warnings = await screen.findByRole("alert", {
			name: "Export warnings",
		});
		expect(within(warnings).getByText("2 things to know")).toBeInTheDocument();
		expect(
			within(warnings).getByText("API_KEY is a secret in staging only."),
		).toBeInTheDocument();
		expect(
			within(warnings).getByText("secret_flag_differs"),
		).toBeInTheDocument();
		expect(
			within(warnings).getByText("Volumes are not exported."),
		).toBeInTheDocument();
	});

	test("a single warning reads in the singular", async () => {
		stubApi({
			"GET /projects/:id/config/export": {
				body: {
					file: FILE,
					warnings: [
						{
							severity: "info",
							code: "volumes_not_exported",
							message: "Volumes are not exported.",
						},
					],
				},
			},
		});
		await open();
		expect(await screen.findByText("One thing to know")).toBeInTheDocument();
	});

	test("a bare string warning still reads", async () => {
		stubApi({
			// A function reply: the contract sends warning objects, the dialog
			// still shows a plain string.
			"GET /projects/:id/config/export": () =>
				Response.json({ file: FILE, warnings: ["Volumes are not exported."] }),
		});
		await open();
		expect(
			await screen.findByText("Volumes are not exported."),
		).toBeInTheDocument();
	});

	test("switching to all environments exports the factored file", async () => {
		const api = stubApi({
			"GET /projects/:id/config/export": (request) => {
				const env = new URL(request.url).searchParams.get("environment");
				return Response.json({
					file: env === "all" ? "# base + overrides\n" : FILE,
					warnings: [],
				});
			},
		});
		await open();
		await screen.findByRole("textbox", { name: "nebula.toml" });
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "All environments" }));
		await waitFor(() =>
			expect(screen.getByRole("textbox", { name: "nebula.toml" })).toHaveValue(
				"# base + overrides\n",
			),
		);
		expect(api.calls.map((c) => c.query.get("environment"))).toEqual([
			"staging",
			"all",
		]);
	});

	test("shows the control plane's reason when the export fails", async () => {
		stubApi({
			"GET /projects/:id/config/export": problem(500, {
				code: "internal",
				detail: "The exporter crashed.",
			}),
		});
		await open();
		expect(await screen.findByText("Could not export")).toBeInTheDocument();
		expect(screen.getByText("The exporter crashed.")).toBeInTheDocument();
		expect(screen.queryByRole("textbox", { name: "nebula.toml" })).toBeNull();
		expect(screen.getByRole("button", { name: /Download/ })).toBeDisabled();
	});

	test("Copy puts the file on the clipboard", async () => {
		stubApi({
			"GET /projects/:id/config/export": {
				body: make("ConfigExportResult", { file: FILE }),
			},
		});
		await open();
		await screen.findByRole("textbox", { name: "nebula.toml" });
		await userEvent.setup().click(screen.getByRole("button", { name: "Copy" }));
		await waitFor(() => expect(writeText).toHaveBeenCalledWith(FILE));
	});

	test("Download saves the file as nebula.toml", async () => {
		stubApi({
			"GET /projects/:id/config/export": {
				body: make("ConfigExportResult", { file: FILE }),
			},
		});
		const blobs: Blob[] = [];
		const create = spyOn(URL, "createObjectURL").mockImplementation((blob) => {
			blobs.push(blob as Blob);
			return "blob:nebula";
		});
		const revoke = spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
		const saved: { name: string; href: string }[] = [];
		spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
			this: HTMLAnchorElement,
		) {
			saved.push({ name: this.download, href: this.href });
		});
		await open();
		await screen.findByRole("textbox", { name: "nebula.toml" });
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /Download/ }));
		expect(create).toHaveBeenCalledTimes(1);
		expect(saved).toEqual([{ name: "nebula.toml", href: "blob:nebula" }]);
		expect(await blobs[0]?.text()).toBe(FILE);
		expect(revoke).toHaveBeenCalledWith("blob:nebula");
	});

	test("an export with an empty file cannot be downloaded", async () => {
		stubApi({
			"GET /projects/:id/config/export": {
				body: make("ConfigExportResult", { file: "" }),
			},
		});
		await open();
		await screen.findByRole("textbox", { name: "nebula.toml" });
		expect(screen.getByRole("button", { name: /Download/ })).toBeDisabled();
	});

	test("Close asks the parent to close", async () => {
		stubApi({
			"GET /projects/:id/config/export": {
				body: make("ConfigExportResult", { file: FILE }),
			},
		});
		const calls: boolean[] = [];
		await open((next) => {
			calls.push(next);
		});
		await screen.findByRole("textbox", { name: "nebula.toml" });
		const dialog = screen.getByRole("dialog");
		await userEvent.setup().click(
			within(dialog).getAllByRole("button", {
				name: "Close",
			})[0] as HTMLElement,
		);
		expect(calls).toContain(false);
	});
});
