import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { InstanceBackupView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { InstanceDestinationDialog } from "./destination-dialog";
import { backupView, problem, s3Destination } from "./instance-backup.fixture";

const unconfiguredS3 = () =>
	backupView({
		enabled: false,
		destination: s3Destination({
			endpoint: undefined,
			region: undefined,
			bucket: undefined,
			prefix: undefined,
			pathStyle: false,
			compatMode: true,
			accessKeySet: false,
			secretKeySet: false,
			configured: false,
			where: "",
		}),
	});

async function renderDialog(settings: InstanceBackupView) {
	const onOpenChange = mock((_open: boolean) => {});
	const view = await renderWithProviders(
		<InstanceDestinationDialog
			open
			onOpenChange={onOpenChange}
			settings={settings}
		/>,
	);
	await screen.findByRole("dialog", { name: "Backup destination" });
	return { ...view, onOpenChange };
}

function ReopenHarness() {
	const [open, setOpen] = useState(true);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				Reopen
			</button>
			<InstanceDestinationDialog
				open={open}
				onOpenChange={setOpen}
				settings={backupView()}
			/>
		</>
	);
}

/** Replaces a field's text in one paste: per-key typing is slow on a loaded machine. */
async function fill(
	user: ReturnType<typeof userEvent.setup>,
	field: HTMLElement,
	text: string,
) {
	await user.clear(field);
	await user.click(field);
	await user.paste(text);
}

const save = () => screen.getByRole("button", { name: "Save destination" });
const patches = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((c) => c.method === "PATCH");

describe("InstanceDestinationDialog: S3", () => {
	test("an edit starts from the stored values with the keys empty and marked keep-able", async () => {
		await renderDialog(backupView());
		expect(screen.getByLabelText("Endpoint")).toHaveValue(
			"https://s3.example.com",
		);
		expect(screen.getByLabelText("Bucket")).toHaveValue("nebula-backups");
		expect(screen.getByLabelText("Region")).toHaveValue("eu-west-1");
		expect(screen.getByLabelText("Key prefix")).toHaveValue("prod");
		expect(screen.getByLabelText(/^Access key/)).toHaveValue("");
		expect(screen.getByLabelText(/^Access key/)).toHaveAccessibleName(
			"Access key (leave empty to keep)",
		);
		expect(screen.getByLabelText(/^Secret key/)).toHaveAccessibleName(
			"Secret key (leave empty to keep)",
		);
		expect(screen.getByRole("checkbox", { name: /Path-style/ })).toBeChecked();
		expect(
			screen.getByRole("checkbox", { name: /Compatibility mode/ }),
		).toBeChecked();
	});

	test("saving without touching the keys keeps the stored ones: no key fields in the PATCH", async () => {
		const api = stubApi({
			"GET /instance/backup": { body: backupView() },
			"PATCH /instance/backup": { body: backupView() },
		});
		const user = userEvent.setup();
		const { onOpenChange } = await renderDialog(backupView());
		await fill(user, screen.getByLabelText("Bucket"), "  other-bucket  ");
		await user.click(save());

		expect(await screen.findByText("Destination saved")).toBeInTheDocument();
		expect(
			screen.getByText("Test it to confirm backups can be written there."),
		).toBeInTheDocument();
		expect(patches(api)).toHaveLength(1);
		expect(patches(api)[0]?.path).toBe("/instance/backup");
		expect(patches(api)[0]?.body).toEqual({
			kind: "s3",
			endpoint: "https://s3.example.com",
			region: "eu-west-1",
			bucket: "other-bucket",
			prefix: "prod",
			pathStyle: true,
			compatMode: true,
		});
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.unmatched).toEqual([]);
	});

	test("typed keys and toggled options go in the body, text fields trimmed", async () => {
		const api = stubApi({
			"GET /instance/backup": { body: unconfiguredS3() },
			"PATCH /instance/backup": { body: backupView() },
		});
		const user = userEvent.setup();
		await renderDialog(unconfiguredS3());
		await fill(
			user,
			screen.getByLabelText("Endpoint"),
			" https://minio.lan:9000 ",
		);
		await fill(user, screen.getByLabelText("Bucket"), " nebula ");
		await fill(user, screen.getByLabelText("Region"), " us-east-1 ");
		await fill(user, screen.getByLabelText("Key prefix"), " cp ");
		await fill(user, screen.getByLabelText("Access key"), "AKIA123");
		await fill(user, screen.getByLabelText("Secret key"), "s3cr3t");
		await user.click(screen.getByRole("checkbox", { name: /Path-style/ }));
		await user.click(
			screen.getByRole("checkbox", { name: /Compatibility mode/ }),
		);
		await user.click(save());

		await screen.findByText("Destination saved");
		expect(patches(api)[0]?.body).toEqual({
			kind: "s3",
			endpoint: "https://minio.lan:9000",
			region: "us-east-1",
			bucket: "nebula",
			prefix: "cp",
			pathStyle: true,
			compatMode: false,
			accessKey: "AKIA123",
			secretKey: "s3cr3t",
		});
	});

	test("the secret key field is masked", async () => {
		await renderDialog(unconfiguredS3());
		expect(screen.getByLabelText("Secret key")).toHaveAttribute(
			"type",
			"password",
		);
	});

	test("without stored keys, saving needs a bucket and both keys", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await renderDialog(unconfiguredS3());
		expect(save()).toBeDisabled();

		await fill(user, screen.getByLabelText("Bucket"), "nebula");
		expect(save()).toBeDisabled();
		await fill(user, screen.getByLabelText("Access key"), "AKIA123");
		expect(save()).toBeDisabled();
		await fill(user, screen.getByLabelText("Secret key"), "s3cr3t");
		expect(save()).toBeEnabled();

		await user.clear(screen.getByLabelText("Bucket"));
		await fill(user, screen.getByLabelText("Bucket"), "   ");
		expect(save()).toBeDisabled();
		expect(api.calls).toEqual([]);
	});

	test("with stored keys, retyping only one of the two keys blocks the save", async () => {
		const user = userEvent.setup();
		await renderDialog(backupView());
		expect(save()).toBeEnabled();
		await user.type(screen.getByLabelText(/^Access key/), "AKIA999");
		expect(save()).toBeDisabled();
		await user.type(screen.getByLabelText(/^Secret key/), "new-secret");
		expect(save()).toBeEnabled();
	});

	test("a rejected save shows the reason in the dialog, keeps it open and the typed values", async () => {
		const api = stubApi({
			"PATCH /instance/backup": problem(400, "bucket is not reachable"),
		});
		const user = userEvent.setup();
		const { onOpenChange } = await renderDialog(backupView());
		await fill(user, screen.getByLabelText("Key prefix"), "-x");
		await user.click(save());

		expect(
			await within(screen.getByRole("dialog")).findByText(
				"bucket is not reachable",
			),
		).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(screen.getByLabelText("Key prefix")).toHaveValue("-x");
		expect(screen.queryByText("Destination saved")).not.toBeInTheDocument();
		expect(save()).toBeEnabled();
		expect(patches(api)).toHaveLength(1);
	});

	test("Cancel closes without sending anything", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		const { onOpenChange } = await renderDialog(backupView());
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls).toEqual([]);
	});
});

describe("InstanceDestinationDialog: other destination kinds", () => {
	test("built-in storage warns it is not off-site and sends only the prefix", async () => {
		const api = stubApi({
			"PATCH /instance/backup": { body: backupView() },
			"GET /instance/backup": { body: backupView() },
		});
		const user = userEvent.setup();
		await renderDialog(backupView());
		await user.click(screen.getByRole("button", { name: "Built-in storage" }));

		expect(screen.getByText("Not off-site")).toBeInTheDocument();
		expect(screen.getByText(/runs on the same host/)).toBeInTheDocument();
		expect(screen.queryByLabelText("Bucket")).not.toBeInTheDocument();
		expect(save()).toBeEnabled();
		await user.click(save());

		await screen.findByText("Destination saved");
		expect(patches(api)[0]?.body).toEqual({ kind: "builtin", prefix: "prod" });
	});

	test("a directory on this host sends the trimmed directory name and prefix", async () => {
		const api = stubApi({
			"PATCH /instance/backup": { body: backupView() },
			"GET /instance/backup": { body: backupView() },
		});
		const user = userEvent.setup();
		await renderDialog(backupView());
		await user.click(
			screen.getByRole("button", { name: "Directory on this host" }),
		);

		expect(screen.getByText("Not off-site")).toBeInTheDocument();
		expect(
			screen.getByText(/These backups stay on the host/),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Access key")).not.toBeInTheDocument();
		await fill(user, screen.getByLabelText("Directory name"), " nightly ");
		await user.click(save());

		await screen.findByText("Destination saved");
		expect(patches(api)[0]?.body).toEqual({
			kind: "local",
			localDir: "nightly",
			prefix: "prod",
		});
	});

	test("when NEBULACTRL_BACKUP_DIR is unset the directory option is disabled and explained", async () => {
		await renderDialog(backupView({ localDirEnabled: false }));
		expect(
			screen.getByRole("button", { name: "Directory on this host" }),
		).toBeDisabled();
		expect(screen.getByText("NEBULACTRL_BACKUP_DIR")).toBeInTheDocument();
		expect(screen.getByText(/with a volume mounted there/)).toBeInTheDocument();
	});

	test("an existing directory destination cannot be saved while the directory is unavailable", async () => {
		await renderDialog(
			backupView({
				localDirEnabled: false,
				destination: s3Destination({
					kind: "local",
					bucket: undefined,
					localDir: "nightly",
					offSite: false,
					where: "directory /var/backups/nightly",
				}),
			}),
		);
		expect(screen.getByLabelText("Directory name")).toHaveValue("nightly");
		expect(save()).toBeDisabled();
	});

	test("switching an S3 destination to a directory and back needs the keys again only when none are stored", async () => {
		const user = userEvent.setup();
		await renderDialog(unconfiguredS3());
		await user.click(
			screen.getByRole("button", { name: "Directory on this host" }),
		);
		expect(save()).toBeEnabled();
		await user.click(
			screen.getByRole("button", { name: "S3-compatible bucket" }),
		);
		expect(screen.getByLabelText("Bucket")).toBeInTheDocument();
		expect(save()).toBeDisabled();
	});
});

describe("InstanceDestinationDialog: reopening", () => {
	test("reopening discards unsaved edits and the previous error", async () => {
		stubApi({ "PATCH /instance/backup": problem(400, "nope") });
		const user = userEvent.setup();
		await renderWithProviders(<ReopenHarness />);
		await screen.findByRole("dialog");
		await fill(user, screen.getByLabelText("Bucket"), "-edited");
		await user.click(save());
		await screen.findByText("nope");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);

		await user.click(screen.getByRole("button", { name: "Reopen" }));
		expect(await screen.findByLabelText("Bucket")).toHaveValue(
			"nebula-backups",
		);
		expect(screen.queryByText("nope")).not.toBeInTheDocument();
	});
});
