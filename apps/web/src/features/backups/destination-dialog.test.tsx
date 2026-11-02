import { describe, expect, it } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { useState } from "react";
import type { DestinationView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { DestinationDialog } from "./destination-dialog";
import { destination, LOCAL, R2 } from "./destinations.fixture";

async function open(
	dest: DestinationView | null,
	routes: Parameters<typeof stubApi>[0] = {},
) {
	const stub = stubApi({
		"POST /backup-destinations": { status: 201, body: R2 },
		"PATCH /backup-destinations/:id": { body: R2 },
		...routes,
	});
	const closed: boolean[] = [];
	await renderWithProviders(
		<DestinationDialog
			open
			onOpenChange={(o) => closed.push(o)}
			destination={dest}
		/>,
	);
	const dialog = await screen.findByRole("dialog");
	return { stub, closed, dialog, user: userEvent.setup() };
}

const submitButton = (dialog: HTMLElement, name: string) =>
	within(dialog).getByRole("button", { name });

/** Pastes rather than types: the credential help re-renders on every keystroke of the endpoint. */
async function paste(
	user: UserEvent,
	dialog: HTMLElement,
	label: string | RegExp,
	text: string,
) {
	await user.click(within(dialog).getByLabelText(label));
	await user.paste(text);
}

async function fillS3(user: UserEvent, dialog: HTMLElement) {
	await paste(user, dialog, "Name", "  Wasabi  ");
	await paste(user, dialog, "Endpoint", " https://s3.wasabisys.com ");
	await paste(user, dialog, "Bucket", " bk ");
	await paste(user, dialog, /^Access key/, "AKIA");
	await paste(user, dialog, /^Secret key/, "s3cret");
}

describe("DestinationDialog: add an S3-compatible bucket", () => {
	it("keeps Add disabled until name, endpoint, bucket and both keys are set", async () => {
		const { dialog, user } = await open(null);
		const add = submitButton(dialog, "Add destination");
		expect(add).toBeDisabled();

		await user.type(within(dialog).getByLabelText("Name"), "Wasabi");
		await paste(user, dialog, "Endpoint", "https://s3.wasabisys.com");
		await user.type(within(dialog).getByLabelText("Bucket"), "bk");
		await user.type(within(dialog).getByLabelText(/^Access key/), "AKIA");
		// Everything but the secret key.
		expect(add).toBeDisabled();
		await user.type(within(dialog).getByLabelText(/^Secret key/), "x");
		expect(add).toBeEnabled();
	});

	it("does not treat a blank (whitespace) name, bucket or endpoint as filled", async () => {
		const { dialog, user } = await open(null);
		await fillS3(user, dialog);
		const add = submitButton(dialog, "Add destination");
		expect(add).toBeEnabled();
		for (const label of ["Name", "Endpoint", "Bucket"]) {
			const input = within(dialog).getByLabelText(label);
			const before = (input as HTMLInputElement).value;
			await user.clear(input);
			await user.paste("   ");
			expect(add).toBeDisabled();
			await user.clear(input);
			await user.paste(before.trim());
			expect(add).toBeEnabled();
		}
	});

	it("posts the trimmed fields, the checkbox choices and the keys as typed", async () => {
		const { stub, closed, dialog, user } = await open(null);
		await fillS3(user, dialog);
		await user.type(
			within(dialog).getByLabelText("Region (optional)"),
			" eu-1 ",
		);
		await user.type(
			within(dialog).getByLabelText("Key prefix"),
			" nebula/prod ",
		);
		await user.click(
			within(dialog).getByRole("checkbox", { name: /Path-style addressing/ }),
		);
		await user.click(
			within(dialog).getByRole("checkbox", { name: /Compatibility mode/ }),
		);
		await user.click(submitButton(dialog, "Add destination"));

		await waitFor(() => expect(closed).toEqual([false]));
		const post = stub.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/backup-destinations");
		expect(post?.body).toEqual({
			name: "Wasabi",
			kind: "s3",
			endpoint: "https://s3.wasabisys.com",
			region: "eu-1",
			bucket: "bk",
			prefix: "nebula/prod",
			pathStyle: true,
			compatMode: false,
			accessKey: "AKIA",
			secretKey: "s3cret",
		});
		expect(await screen.findByText("Destination added")).toBeVisible();
		expect(stub.unmatched).toEqual([]);
	});

	it("defaults to compatibility mode on and path-style off", async () => {
		const { stub, dialog, user } = await open(null);
		await fillS3(user, dialog);
		expect(
			within(dialog).getByRole("checkbox", { name: /Compatibility mode/ }),
		).toBeChecked();
		expect(
			within(dialog).getByRole("checkbox", { name: /Path-style addressing/ }),
		).not.toBeChecked();
		await user.click(submitButton(dialog, "Add destination"));
		await waitFor(() => expect(stub.calls).toHaveLength(1));
		expect(stub.calls[0]?.body).toMatchObject({
			pathStyle: false,
			compatMode: true,
		});
	});

	it("Amazon S3 needs no endpoint: the field goes away and an empty endpoint is sent", async () => {
		const { stub, dialog, user } = await open(null);
		await user.click(within(dialog).getByRole("button", { name: "Amazon S3" }));
		expect(within(dialog).queryByLabelText("Endpoint")).toBeNull();
		// Region is no longer optional for AWS.
		expect(within(dialog).getByLabelText("Region")).toBeVisible();
		expect(within(dialog).queryByLabelText("Region (optional)")).toBeNull();
		expect(
			await screen.findByRole("region", { name: "AWS S3 credential setup" }),
		).toBeVisible();

		await user.type(within(dialog).getByLabelText("Name"), "AWS");
		await user.type(within(dialog).getByLabelText("Bucket"), "my-bucket");
		await user.type(within(dialog).getByLabelText(/^Access key/), "AKIA");
		await user.type(within(dialog).getByLabelText(/^Secret key/), "sec");
		await user.click(submitButton(dialog, "Add destination"));
		await waitFor(() => expect(stub.calls).toHaveLength(1));
		expect(stub.calls[0]?.body).toMatchObject({
			kind: "s3",
			endpoint: "",
			bucket: "my-bucket",
		});
	});

	it("an endpoint typed before switching to Amazon S3 is not sent", async () => {
		const { stub, dialog, user } = await open(null);
		await paste(user, dialog, "Endpoint", "https://old.example.com");
		await user.click(within(dialog).getByRole("button", { name: "Amazon S3" }));
		await user.type(within(dialog).getByLabelText("Name"), "AWS");
		await user.type(within(dialog).getByLabelText("Bucket"), "b");
		await user.type(within(dialog).getByLabelText(/^Access key/), "a");
		await user.type(within(dialog).getByLabelText(/^Secret key/), "s");
		await user.click(submitButton(dialog, "Add destination"));
		await waitFor(() => expect(stub.calls).toHaveLength(1));
		expect(stub.calls[0]?.body).toMatchObject({ endpoint: "" });
	});

	it("shows the provider's credential help once an endpoint names a known store", async () => {
		const { dialog, user } = await open(null);
		expect(
			screen.queryByRole("region", { name: /credential setup/ }),
		).toBeNull();
		await paste(
			user,
			dialog,
			"Endpoint",
			"https://acct.r2.cloudflarestorage.com",
		);
		expect(
			await screen.findByRole("region", {
				name: "Cloudflare R2 credential setup",
			}),
		).toBeVisible();
	});

	it("shows the server's reason and stays open when saving fails", async () => {
		const { stub, closed, dialog, user } = await open(null, {
			"POST /backup-destinations": () =>
				Response.json(
					{
						code: "conflict",
						detail: "A destination named Wasabi already exists",
					},
					{ status: 409 },
				),
		});
		await fillS3(user, dialog);
		await user.click(submitButton(dialog, "Add destination"));
		expect(
			await within(dialog).findByText(
				"A destination named Wasabi already exists",
			),
		).toBeVisible();
		expect(closed).toEqual([]);
		expect(screen.queryByText("Destination added")).toBeNull();
		expect(stub.calls).toHaveLength(1);
		// Retrying clears the old error while the new attempt runs, and can succeed.
		expect(submitButton(dialog, "Add destination")).toBeEnabled();
	});

	it("Enter in a field submits only when the form is valid", async () => {
		const { stub, closed, dialog, user } = await open(null);
		await user.type(
			within(dialog).getByLabelText("Name"),
			"Only a name{Enter}",
		);
		expect(stub.calls).toEqual([]);
		await user.clear(within(dialog).getByLabelText("Name"));
		await fillS3(user, dialog);
		await user.type(within(dialog).getByLabelText("Bucket"), "{Enter}");
		await waitFor(() => expect(closed).toEqual([false]));
		expect(stub.calls).toHaveLength(1);
	});

	it("Cancel closes without a request", async () => {
		const { stub, closed, dialog, user } = await open(null);
		await user.click(submitButton(dialog, "Cancel"));
		expect(closed).toEqual([false]);
		expect(stub.calls).toEqual([]);
	});
});

describe("DestinationDialog: add a directory on the server", () => {
	it("needs only a name, warns it is not off-site and posts the directory", async () => {
		const { stub, closed, dialog, user } = await open(null);
		await user.click(
			within(dialog).getByRole("button", { name: "Directory on the server" }),
		);
		// No bucket, endpoint or key fields for a directory.
		expect(within(dialog).queryByLabelText("Bucket")).toBeNull();
		expect(within(dialog).queryByLabelText("Endpoint")).toBeNull();
		expect(within(dialog).queryByLabelText(/^Secret key/)).toBeNull();
		expect(within(dialog).getByText("Not off-site")).toBeVisible();

		const add = submitButton(dialog, "Add destination");
		expect(add).toBeDisabled();
		await user.type(within(dialog).getByLabelText("Name"), " Local copy ");
		expect(add).toBeEnabled();
		await user.type(
			within(dialog).getByLabelText("Directory name"),
			" nightly ",
		);
		await user.click(add);

		await waitFor(() => expect(closed).toEqual([false]));
		expect(stub.calls[0]?.method).toBe("POST");
		expect(stub.calls[0]?.body).toEqual({
			name: "Local copy",
			kind: "local",
			localDir: "nightly",
		});
	});

	it("the directory name is optional", async () => {
		const { stub, dialog, user } = await open(null);
		await user.click(
			within(dialog).getByRole("button", { name: "Directory on the server" }),
		);
		await user.type(within(dialog).getByLabelText("Name"), "Default dir");
		await user.click(submitButton(dialog, "Add destination"));
		await waitFor(() => expect(stub.calls).toHaveLength(1));
		expect(stub.calls[0]?.body).toMatchObject({ kind: "local", localDir: "" });
	});

	it("a key prefix typed on the bucket form is not sent for a directory", async () => {
		const { stub, dialog, user } = await open(null);
		await user.type(
			within(dialog).getByLabelText("Key prefix"),
			"nebulactrl/prod",
		);
		await user.click(
			within(dialog).getByRole("button", { name: "Directory on the server" }),
		);
		await user.type(within(dialog).getByLabelText("Name"), "Local copy");
		await user.click(submitButton(dialog, "Add destination"));
		await waitFor(() => expect(stub.calls).toHaveLength(1));
		expect(stub.calls[0]?.body).not.toHaveProperty("prefix");
	});
});

describe("DestinationDialog: edit", () => {
	it("prefills the destination, hides the type pickers and never shows stored keys", async () => {
		const { dialog } = await open(R2);
		expect(
			within(dialog).getByRole("heading", { name: "Edit R2 nightly" }),
		).toBeVisible();
		expect(within(dialog).getByLabelText("Name")).toHaveValue("R2 nightly");
		expect(within(dialog).getByLabelText("Endpoint")).toHaveValue(
			"https://acct.r2.cloudflarestorage.com",
		);
		expect(within(dialog).getByLabelText("Bucket")).toHaveValue("backups");
		expect(within(dialog).getByLabelText("Region (optional)")).toHaveValue(
			"auto",
		);
		expect(within(dialog).getByLabelText("Key prefix")).toHaveValue("prod");
		expect(
			within(dialog).getByRole("checkbox", { name: /Path-style addressing/ }),
		).toBeChecked();
		expect(
			within(dialog).queryByRole("button", { name: "Amazon S3" }),
		).toBeNull();
		expect(
			within(dialog).queryByRole("button", { name: "Directory on the server" }),
		).toBeNull();
		const access = within(dialog).getByLabelText(/Access key/);
		const secret = within(dialog).getByLabelText(/Secret key/);
		expect(access).toHaveValue("");
		expect(secret).toHaveValue("");
		expect(access).toHaveAttribute("placeholder", "••••••••");
		expect(secret).toHaveAttribute("placeholder", "••••••••");
		expect(
			within(dialog).getByText("Access key (leave empty to keep)"),
		).toBeVisible();
		expect(submitButton(dialog, "Save")).toBeEnabled();
	});

	it("shows no key placeholder when the stored destination has no keys", async () => {
		const { dialog } = await open({
			...R2,
			accessKeySet: false,
			secretKeySet: false,
		});
		expect(within(dialog).getByLabelText(/Access key/)).toHaveAttribute(
			"placeholder",
			"",
		);
		expect(within(dialog).getByLabelText(/Secret key/)).toHaveAttribute(
			"placeholder",
			"",
		);
	});

	it("patches the changed name and sends empty keys to keep the stored ones", async () => {
		const { stub, closed, dialog, user } = await open(R2);
		const name = within(dialog).getByLabelText("Name");
		await user.clear(name);
		await user.type(name, "R2 renamed");
		await user.click(submitButton(dialog, "Save"));

		await waitFor(() => expect(closed).toEqual([false]));
		const patch = stub.calls.find((c) => c.method === "PATCH");
		expect(patch?.path).toBe("/backup-destinations/dest-r2");
		expect(patch?.body).toEqual({
			name: "R2 renamed",
			endpoint: "https://acct.r2.cloudflarestorage.com",
			region: "auto",
			bucket: "backups",
			prefix: "prod",
			pathStyle: true,
			compatMode: true,
			accessKey: "",
			secretKey: "",
		});
		expect(stub.calls.some((c) => c.method === "POST")).toBe(false);
		expect(await screen.findByText("Destination saved")).toBeVisible();
	});

	it("sends new keys when the reader rotates them", async () => {
		const { stub, dialog, user } = await open(R2);
		await user.type(within(dialog).getByLabelText(/Access key/), "NEWKEY");
		await user.type(within(dialog).getByLabelText(/Secret key/), "NEWSECRET");
		await user.click(submitButton(dialog, "Save"));
		await waitFor(() => expect(stub.calls).toHaveLength(1));
		expect(stub.calls[0]?.body).toMatchObject({
			accessKey: "NEWKEY",
			secretKey: "NEWSECRET",
		});
	});

	it("locks endpoint, bucket and prefix once backups are stored, but not name or region", async () => {
		const { stub, dialog, user } = await open({ ...R2, storedBackups: 4 });
		expect(within(dialog).getByLabelText("Endpoint")).toBeDisabled();
		expect(within(dialog).getByLabelText("Bucket")).toBeDisabled();
		expect(within(dialog).getByLabelText("Key prefix")).toBeDisabled();
		expect(within(dialog).getByLabelText("Name")).toBeEnabled();
		expect(within(dialog).getByLabelText("Region (optional)")).toBeEnabled();
		await user.click(submitButton(dialog, "Save"));
		await waitFor(() => expect(stub.calls).toHaveLength(1));
	});

	it("leaves endpoint, bucket and prefix editable while nothing is stored", async () => {
		const { dialog } = await open({ ...R2, storedBackups: 0 });
		expect(within(dialog).getByLabelText("Endpoint")).toBeEnabled();
		expect(within(dialog).getByLabelText("Bucket")).toBeEnabled();
		expect(within(dialog).getByLabelText("Key prefix")).toBeEnabled();
	});

	it("an Amazon S3 destination (no endpoint) edits without an endpoint field", async () => {
		const aws = destination({
			id: "dest-aws",
			name: "AWS",
			bucket: "cold",
			region: "us-east-1",
		});
		const { stub, dialog, user } = await open(aws);
		expect(within(dialog).queryByLabelText("Endpoint")).toBeNull();
		expect(within(dialog).getByLabelText("Region")).toHaveValue("us-east-1");
		await user.click(submitButton(dialog, "Save"));
		await waitFor(() => expect(stub.calls).toHaveLength(1));
		expect(stub.calls[0]?.body).toMatchObject({ endpoint: "", bucket: "cold" });
	});

	it("cannot be saved once the bucket is cleared", async () => {
		const { dialog, user } = await open(R2);
		await user.clear(within(dialog).getByLabelText("Bucket"));
		expect(submitButton(dialog, "Save")).toBeDisabled();
	});

	it("cannot be saved once the name is cleared", async () => {
		const { dialog, user } = await open(R2);
		await user.clear(within(dialog).getByLabelText("Name"));
		expect(submitButton(dialog, "Save")).toBeDisabled();
	});

	it("edits a directory destination: name, directory and prefix only", async () => {
		const { stub, closed, dialog, user } = await open({
			...LOCAL,
			prefix: "p",
		});
		expect(within(dialog).getByLabelText("Directory name")).toHaveValue(
			"nightly",
		);
		expect(within(dialog).queryByLabelText("Bucket")).toBeNull();
		const dir = within(dialog).getByLabelText("Directory name");
		await user.clear(dir);
		await user.type(dir, "weekly");
		await user.click(submitButton(dialog, "Save"));
		await waitFor(() => expect(closed).toEqual([false]));
		expect(stub.calls[0]?.method).toBe("PATCH");
		expect(stub.calls[0]?.body).toEqual({
			name: "Server disk",
			localDir: "weekly",
			prefix: "p",
		});
	});

	it("locks the directory once backups are stored in it", async () => {
		const { dialog } = await open({ ...LOCAL, storedBackups: 1 });
		expect(within(dialog).getByLabelText("Directory name")).toBeDisabled();
	});

	it("shows the error of a refused save and keeps the dialog open", async () => {
		const { closed, dialog, user } = await open(R2, {
			"PATCH /backup-destinations/:id": () =>
				Response.json(
					{ code: "bad", detail: "Bucket not reachable" },
					{ status: 422 },
				),
		});
		await user.click(submitButton(dialog, "Save"));
		expect(
			await within(dialog).findByText("Bucket not reachable"),
		).toBeVisible();
		expect(closed).toEqual([]);
	});

	it("tells the reader the control plane cannot be reached when the request never gets an answer", async () => {
		const { closed, dialog, user } = await open(R2, {
			"PATCH /backup-destinations/:id": () => {
				throw new TypeError("fetch failed");
			},
		});
		await user.click(submitButton(dialog, "Save"));
		expect(
			await within(dialog).findByText(
				"The control plane did not answer. Check that the server is running.",
			),
		).toBeVisible();
		expect(closed).toEqual([]);
	});
});

let setOpen: (o: boolean) => void = () => {};

/** An open dialog whose open state the test can flip from outside. */
function Harness() {
	const [o, set] = useState(true);
	setOpen = set;
	return <DestinationDialog open={o} onOpenChange={set} destination={null} />;
}

describe("DestinationDialog: reopening", () => {
	it("starts from a clean form each time it is opened", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await user.type(await screen.findByLabelText("Name"), "draft");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await act(async () => setOpen(true));
		expect(await screen.findByLabelText("Name")).toHaveValue("");
	});
});
