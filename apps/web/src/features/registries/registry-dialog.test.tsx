import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import type { Registry } from "./model";
import { RegistryDialog } from "./registry-dialog";
import { registry, wireRegistry } from "./test-data";

async function open(reg: Registry | null = null) {
	const onOpenChange = mock((_open: boolean) => undefined);
	await renderWithProviders(
		<RegistryDialog open onOpenChange={onOpenChange} registry={reg} />,
	);
	return onOpenChange;
}

const field = (label: string | RegExp) => screen.getByLabelText(label);
const save = () =>
	screen.getByRole("button", { name: /^(Add registry|Save)$/ });

describe("RegistryDialog: add", () => {
	test("posts the trimmed form and closes with a confirmation", async () => {
		const api = stubApi({
			"POST /registries": { status: 201, body: wireRegistry() },
		});
		const user = userEvent.setup();
		const onOpenChange = await open();

		await user.type(field("Name"), "  GitHub registry ");
		await user.type(field("Host"), "ghcr.io");
		await user.type(field("Namespace"), " acme/apps ");
		await user.type(field("User name"), "deploy-bot");
		await user.type(field("Token or password"), "ghp_secret");
		await user.click(save());

		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/registries",
			body: {
				name: "GitHub registry",
				host: "ghcr.io",
				namespace: "acme/apps",
				pushUsername: "deploy-bot",
				pushPassword: "ghp_secret",
			},
		});
		// No pull credential unless the checkbox is on.
		const body = api.calls[0]?.body as Record<string, unknown>;
		expect(body).not.toHaveProperty("pullUsername");
		expect(await screen.findByText("Registry added")).toBeInTheDocument();
	});

	test("a separate pull credential is sent when asked for", async () => {
		const api = stubApi({
			"POST /registries": { status: 201, body: wireRegistry() },
		});
		const user = userEvent.setup();
		const onOpenChange = await open();

		await user.type(field("Name"), "Hub");
		await user.type(field("Host"), "registry.example.com:5000");
		await user.type(field("User name"), "pusher");
		await user.type(field("Token or password"), "push-pw");
		await user.click(
			screen.getByRole("checkbox", {
				name: /Use a separate read-only credential/,
			}),
		);
		await user.type(field("Pull user name"), "puller");
		await user.type(field("Pull token or password"), "pull-pw");
		await user.click(save());

		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls[0]?.body).toEqual({
			name: "Hub",
			host: "registry.example.com:5000",
			namespace: "",
			pushUsername: "pusher",
			pushPassword: "push-pw",
			pullUsername: "puller",
			pullPassword: "pull-pw",
		});
	});

	test("submitting an empty form shows every missing field and sends nothing", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		const onOpenChange = await open();

		await user.click(save());

		expect(await screen.findByText("Give the registry a name.")).toBeVisible();
		expect(
			screen.getByText("Enter the registry host, such as ghcr.io."),
		).toBeVisible();
		expect(
			screen.getByText("Enter the user name the builds push with."),
		).toBeVisible();
		expect(
			screen.getByText("Paste the token or password the builds push with."),
		).toBeVisible();
		expect(field("Name")).toHaveAttribute("aria-invalid", "true");
		expect(api.calls).toEqual([]);
		expect(onOpenChange).not.toHaveBeenCalled();
	});

	test("a pasted https:// host is flagged as it is typed, before any save", async () => {
		const user = userEvent.setup();
		await open();

		await user.type(field("Host"), "https://ghcr.io");

		expect(
			await screen.findByText(
				"Enter the host only, without https:// or http://.",
			),
		).toBeVisible();
		// Other fields stay quiet until the person leaves them or saves.
		expect(
			screen.queryByText("Give the registry a name."),
		).not.toBeInTheDocument();
	});

	test("an invalid namespace is reported on save", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		await open();

		await user.type(field("Name"), "X");
		await user.type(field("Host"), "ghcr.io");
		await user.type(field("Namespace"), "Acme");
		await user.type(field("User name"), "u");
		await user.type(field("Token or password"), "p");
		await user.click(save());

		expect(
			await screen.findByText(
				"Use lowercase letters; registries refuse capitals.",
			),
		).toBeVisible();
		expect(api.calls).toEqual([]);
	});

	test("the preview line follows the host and namespace", async () => {
		const user = userEvent.setup();
		await open();
		expect(
			screen.getByText("registry.example.com/<project>-<service>"),
		).toBeInTheDocument();

		await user.type(field("Host"), "ghcr.io");
		await user.type(field("Namespace"), "acme");

		expect(
			screen.getByText("ghcr.io/acme/<project>-<service>"),
		).toBeInTheDocument();
	});

	test("a server refusal stays in the dialog with its message", async () => {
		stubApi({
			"POST /registries": problem(422, {
				code: "invalid",
				detail: "That registry refused the login.",
			}),
		});
		const user = userEvent.setup();
		const onOpenChange = await open();

		await user.type(field("Name"), "X");
		await user.type(field("Host"), "ghcr.io");
		await user.type(field("User name"), "u");
		await user.type(field("Token or password"), "p");
		await user.click(save());

		expect(
			await screen.findByText("That registry refused the login."),
		).toBeVisible();
		expect(onOpenChange).not.toHaveBeenCalled();
		// The button is usable again for another try.
		expect(save()).toBeEnabled();
	});

	test("Cancel closes without a request", async () => {
		const api = stubApi({});
		const user = userEvent.setup();
		const onOpenChange = await open();

		await user.click(screen.getByRole("button", { name: "Cancel" }));

		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls).toEqual([]);
	});
});

describe("RegistryDialog: edit", () => {
	const stored = () => registry({ hasPullAuth: true, pullUsername: "reader" });

	test("opens filled with the stored values and no secret", async () => {
		await open(stored());

		expect(screen.getByText("Edit GitHub registry")).toBeInTheDocument();
		expect(field("Name")).toHaveValue("GitHub registry");
		expect(field("Host")).toHaveValue("ghcr.io");
		expect(field("Namespace")).toHaveValue("acme");
		expect(field("User name")).toHaveValue("deploy-bot");
		const push = field(/^Token or password/);
		expect(push).toHaveValue("");
		expect(push).toHaveAttribute("placeholder", "stored");
		// Both stored passwords say an empty field keeps them.
		expect(
			screen.getAllByText(/\(empty keeps it\)/, { selector: "label" }),
		).toHaveLength(2);
		expect(field("Pull user name")).toHaveValue("reader");
	});

	test("patches only what changed and keeps stored passwords", async () => {
		const api = stubApi({
			"PATCH /registries/:id": {
				body: wireRegistry({ name: "Renamed" }),
			},
		});
		const user = userEvent.setup();
		const onOpenChange = await open(stored());

		await user.clear(field("Name"));
		await user.type(field("Name"), "Renamed");
		await user.click(save());

		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "PATCH",
			path: "/registries/reg-1",
			body: { name: "Renamed" },
		});
		expect(await screen.findByText("Registry saved")).toBeInTheDocument();
	});

	test("turning the separate pull credential off clears it", async () => {
		const api = stubApi({
			"PATCH /registries/:id": { body: wireRegistry() },
		});
		const user = userEvent.setup();
		await open(stored());

		await user.click(
			screen.getByRole("checkbox", {
				name: /Use a separate read-only credential/,
			}),
		);
		expect(screen.queryByLabelText("Pull user name")).not.toBeInTheDocument();
		await user.click(save());

		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.body).toEqual({ clearPull: true });
	});

	test("changing the host demands both passwords again, then sends them", async () => {
		const api = stubApi({
			"PATCH /registries/:id": { body: wireRegistry() },
		});
		const user = userEvent.setup();
		const onOpenChange = await open(stored());

		await user.clear(field("Host"));
		await user.type(field("Host"), "registry.example.com");

		expect(
			screen.getAllByText(
				"Changing the host needs the credentials entered again.",
			),
		).toHaveLength(3);
		await user.click(save());
		expect(
			await screen.findByText(
				"Enter the token or password again; the host changed.",
			),
		).toBeVisible();
		expect(
			screen.getByText(
				"Enter the pull token or password again; the host changed.",
			),
		).toBeVisible();
		expect(api.calls).toEqual([]);

		await user.type(field("Token or password"), "new-push");
		await user.type(field("Pull token or password"), "new-pull");
		await user.click(save());

		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls[0]?.body).toEqual({
			host: "registry.example.com",
			pushPassword: "new-push",
			pullPassword: "new-pull",
		});
	});

	test("a secret_required answer marks the password fields and keeps the dialog open", async () => {
		stubApi({
			"PATCH /registries/:id": problem(422, {
				code: "secret_required",
				detail: "send the password again",
			}),
		});
		const user = userEvent.setup();
		const onOpenChange = await open(registry());

		// Same host in the form, so the form's own check lets the save through.
		await user.clear(field("Name"));
		await user.type(field("Name"), "Other");
		await user.click(save());

		expect(
			await screen.findByText("Enter the password again; the host changed."),
		).toBeVisible();
		expect(onOpenChange).not.toHaveBeenCalled();
		// The server's message is not also shown as a generic error.
		expect(
			screen.queryByText("send the password again"),
		).not.toBeInTheDocument();

		// Typing in the field clears its server-side error.
		await user.type(field(/^Token or password/), "x");
		expect(
			screen.queryByText("Enter the password again; the host changed."),
		).not.toBeInTheDocument();
	});
});
