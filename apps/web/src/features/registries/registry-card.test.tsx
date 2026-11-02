import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import type { Registry } from "./model";
import { RegistryCard } from "./registry-card";
import { registry, wireImage } from "./test-data";

function stubQuiet() {
	return stubApi({
		"GET /servers": { body: { items: [] } },
		"GET /registries/:id/images": { body: { items: [wireImage()] } },
	});
}

async function renderCard(
	patch: Partial<Registry> = {},
	handlers: { onEdit?: () => void; onDelete?: () => void } = {},
) {
	const onEdit = handlers.onEdit ?? (() => undefined);
	const onDelete = handlers.onDelete ?? (() => undefined);
	return renderWithProviders(
		<RegistryCard
			registry={registry(patch)}
			onEdit={onEdit}
			onDelete={onDelete}
		/>,
	);
}

describe("RegistryCard: external registry", () => {
	test("shows where images go and what credentials are stored, never a secret", async () => {
		stubQuiet();

		await renderCard({
			hasPullAuth: true,
			pullUsername: "reader",
			imageCount: 1,
		});

		const card = screen.getByRole("article", { name: "GitHub registry" });
		expect(within(card).getByText("External")).toBeInTheDocument();
		expect(within(card).getByText("ghcr.io/acme")).toBeInTheDocument();
		expect(
			within(card).getByText("push as deploy-bot; pull as reader"),
		).toBeInTheDocument();
		expect(within(card).getByText("1 image")).toBeInTheDocument();
		// Facts that belong to the instance's built-in registry are absent.
		expect(within(card).queryByText("Status")).not.toBeInTheDocument();
		expect(within(card).queryByText("Cleanup")).not.toBeInTheDocument();
	});

	test("says so when the pull falls back to the push credential or none is stored", async () => {
		stubQuiet();

		await renderCard({ imageCount: 4 });

		expect(
			screen.getByText("push as deploy-bot; pulls with the push credential"),
		).toBeInTheDocument();
		expect(screen.getByText("4 images")).toBeInTheDocument();
	});

	test("a registry with no push credential says so", async () => {
		stubQuiet();

		await renderCard({ hasPushAuth: false, namespace: "" });

		expect(
			screen.getByText("no push credential; pulls with the push credential"),
		).toBeInTheDocument();
		// No namespace: the bare host is the root.
		expect(screen.getByText("ghcr.io")).toBeInTheDocument();
	});

	test("Edit and Delete call back", async () => {
		stubQuiet();
		const onEdit = mock(() => undefined);
		const onDelete = mock(() => undefined);
		const user = userEvent.setup();

		await renderCard({}, { onEdit, onDelete });
		await user.click(screen.getByRole("button", { name: "Edit" }));
		expect([onEdit.mock.calls.length, onDelete.mock.calls.length]).toEqual([
			1, 0,
		]);
		await user.click(screen.getByRole("button", { name: "Delete" }));

		expect([onEdit.mock.calls.length, onDelete.mock.calls.length]).toEqual([
			1, 1,
		]);
	});

	test("a registry a setting still uses cannot be deleted and says why", async () => {
		stubQuiet();
		const onDelete = mock(() => undefined);
		const user = userEvent.setup();

		await renderCard({ inUse: true }, { onDelete });

		const del = screen.getByRole("button", { name: "Delete" });
		expect(del).toBeDisabled();
		expect(del).toHaveAttribute(
			"title",
			"A team or project build setting uses this registry. Change that first.",
		);
		await user.click(del);
		expect(onDelete).not.toHaveBeenCalled();
		expect(screen.getByRole("button", { name: "Edit" })).toBeEnabled();
	});

	test("images stay hidden and unrequested until Show images is pressed", async () => {
		const api = stubQuiet();
		const user = userEvent.setup();

		await renderCard();
		const toggle = screen.getByRole("button", { name: "Show images" });
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		expect(api.calls.some((c) => c.path.endsWith("/images"))).toBe(false);

		await user.click(toggle);

		expect(
			await screen.findByText("ghcr.io/acme/shop-api"),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Hide images" })).toHaveAttribute(
			"aria-expanded",
			"true",
		);
		expect(api.calls.find((c) => c.path.endsWith("/images"))?.path).toBe(
			"/registries/reg-1/images",
		);
	});

	test("offers the per-server registry test", async () => {
		stubQuiet();

		await renderCard();

		expect(
			screen.getByRole("button", { name: "Test registry" }),
		).toBeInTheDocument();
	});
});

describe("RegistryCard: built-in registry", () => {
	const builtinBase: Partial<Registry> = {
		id: "reg-builtin",
		name: "Built-in registry",
		kind: "builtin",
		host: "",
		namespace: "",
		hasPushAuth: false,
		pushUsername: "",
	};

	test("shows status, size and cleanup, with no address, credentials, edit or delete", async () => {
		stubQuiet();

		await renderCard({
			...builtinBase,
			imageCount: 2,
			sizeBytes: 3 * 1024 * 1024,
			builtin: {
				status: "ready",
				statusMessage: "",
				gcState: "collecting",
				lastGcAt: null,
			},
		});

		const card = screen.getByRole("article", { name: "Built-in registry" });
		expect(within(card).getByText("Built-in")).toBeInTheDocument();
		expect(
			within(card).getByText(/The instance admin runs it/),
		).toBeInTheDocument();
		expect(within(card).getByText("Ready")).toBeInTheDocument();
		expect(within(card).getByText("3 MiB")).toBeInTheDocument();
		expect(within(card).getByText("2 images")).toBeInTheDocument();
		const cleanup = within(card).getByText("Cleaning up now");
		expect(cleanup).toHaveAttribute("title", "Cleaning up");
		expect(within(card).queryByText("Credentials")).not.toBeInTheDocument();
		expect(
			within(card).queryByRole("button", { name: "Edit" }),
		).not.toBeInTheDocument();
		expect(
			within(card).queryByRole("button", { name: "Delete" }),
		).not.toBeInTheDocument();
		expect(
			within(card).getByRole("button", { name: "Test registry" }),
		).toBeInTheDocument();
	});

	test("an empty built-in registry reads Empty, not 0 B", async () => {
		stubQuiet();

		await renderCard({
			...builtinBase,
			sizeBytes: 0,
			builtin: {
				status: "ready",
				statusMessage: "",
				gcState: "idle",
				lastGcAt: null,
			},
		});

		expect(screen.getByText("Empty")).toBeInTheDocument();
		expect(screen.getByText("No cleanup yet")).toBeInTheDocument();
	});

	test("when the registry is down it explains why and hides testing and images", async () => {
		const api = stubQuiet();

		await renderCard({
			...builtinBase,
			builtin: {
				status: "unavailable",
				statusMessage: "dial tcp 10.0.0.2:5000: connection refused",
				gcState: "idle",
				lastGcAt: null,
			},
		});

		expect(screen.getByText("Unavailable")).toBeInTheDocument();
		expect(
			screen.getByText("dial tcp 10.0.0.2:5000: connection refused"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/The built-in registry is not answering/),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Test registry" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Show images" }),
		).not.toBeInTheDocument();
		await waitFor(() => expect(api.unmatched).toEqual([]));
		expect(api.calls).toEqual([]);
	});

	test("an unavailable registry with no message shows only the generic note", async () => {
		stubQuiet();

		await renderCard({
			...builtinBase,
			builtin: {
				status: "unavailable",
				statusMessage: "",
				gcState: "idle",
				lastGcAt: null,
			},
		});

		expect(
			screen.getByText(/The built-in registry is not answering/),
		).toBeInTheDocument();
		expect(screen.getAllByText(/./, { selector: "p" })).toHaveLength(1);
	});
});
