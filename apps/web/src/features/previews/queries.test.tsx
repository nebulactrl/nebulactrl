import { describe, expect, test } from "bun:test";
import { useQuery } from "@tanstack/react-query";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { environmentsQuery } from "#/api/hooks";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { draftFrom } from "./model";
import {
	previewSettingsQuery,
	previewsQuery,
	useDeletePreview,
	useSavePreviewSettings,
} from "./queries";
import { wireEnv, wirePreview, wireSettings } from "./test-data";

/** Keeps the three queries a preview change must refresh on screen, and exposes both mutations. */
function Harness() {
	const previews = useQuery(previewsQuery("proj-1"));
	useQuery(previewSettingsQuery("proj-1"));
	useQuery(environmentsQuery("proj-1"));
	const save = useSavePreviewSettings("proj-1");
	const remove = useDeletePreview();
	return (
		<>
			<p>{previews.data?.length ?? "…"} previews</p>
			<button
				type="button"
				onClick={() => save.mutate(draftFrom(wireSettings({ enabled: false })))}
			>
				save
			</button>
			<button
				type="button"
				onClick={() => previews.data?.[0] && remove.mutate(previews.data[0])}
			>
				remove
			</button>
		</>
	);
}

function counted() {
	const reads = { previews: 0, settings: 0, environments: 0 };
	const api = stubApi({
		"GET /projects/:id/previews": () => {
			reads.previews += 1;
			return Response.json({ items: [wirePreview()] });
		},
		"GET /projects/:id/previews/settings": () => {
			reads.settings += 1;
			return Response.json(wireSettings());
		},
		"GET /projects/:id/environments": () => {
			reads.environments += 1;
			return Response.json({ items: [wireEnv()] });
		},
		"PUT /projects/:id/previews/settings": {
			body: wireSettings({ enabled: false }),
		},
		"DELETE /previews/:id": { status: 202, body: wirePreview() },
	});
	return { api, reads };
}

function Probe() {
	const q = useQuery(previewsQuery("proj-1"));
	return <p>{q.data ? `got ${q.data.length}` : "none"}</p>;
}

describe("previews queries", () => {
	test("previewsQuery selects the item list, an omitted list being empty", async () => {
		stubApi({ "GET /projects/:id/previews": { body: { items: null } } });

		await renderWithProviders(<Probe />);

		expect(await screen.findByText("got 0")).toBeInTheDocument();
	});

	test("saving settings sends the draft as the body and refreshes previews, settings and environments", async () => {
		const { api, reads } = counted();
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await screen.findByText("1 previews");
		await waitFor(() =>
			expect(reads).toEqual({ previews: 1, settings: 1, environments: 1 }),
		);

		await user.click(screen.getByRole("button", { name: "save" }));

		expect(await screen.findByText("Previews are off")).toBeInTheDocument();
		expect(reads).toEqual({ previews: 2, settings: 2, environments: 2 });
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/projects/proj-1/previews/settings");
		expect(put?.body).toEqual({
			enabled: false,
			baseEnvironmentId: "env-prod",
			forkPrs: false,
			maxConcurrent: 5,
			idleDays: 7,
			excludedServices: [],
			seedCommands: [],
		});
	});

	test("enabling previews says every pull request gets an environment", async () => {
		stubApi({
			"GET /projects/:id/previews": { body: { items: [] } },
			"GET /projects/:id/previews/settings": { body: wireSettings() },
			"GET /projects/:id/environments": { body: { items: [wireEnv()] } },
			"PUT /projects/:id/previews/settings": {
				body: wireSettings({ enabled: true }),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await screen.findByText("0 previews");

		await user.click(screen.getByRole("button", { name: "save" }));

		expect(await screen.findByText("Previews are on")).toBeInTheDocument();
		expect(
			screen.getByText(
				"Every pull request of a connected repository gets its own environment.",
			),
		).toBeInTheDocument();
	});

	test("deleting a preview calls DELETE for it, refreshes the same queries and says what is going", async () => {
		const { api, reads } = counted();
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await screen.findByText("1 previews");
		await waitFor(() =>
			expect(reads).toEqual({ previews: 1, settings: 1, environments: 1 }),
		);

		await user.click(screen.getByRole("button", { name: "remove" }));

		expect(await screen.findByText("Deleting PR #12")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/previews/pv-1",
		);
		expect(reads).toEqual({ previews: 2, settings: 2, environments: 2 });
	});

	test("a failed delete raises no success toast and refreshes nothing", async () => {
		const reads = { previews: 0 };
		const api = stubApi({
			"GET /projects/:id/previews": () => {
				reads.previews += 1;
				return Response.json({ items: [wirePreview()] });
			},
			"GET /projects/:id/previews/settings": { body: wireSettings() },
			"GET /projects/:id/environments": { body: { items: [wireEnv()] } },
			"DELETE /previews/:id": problem(409, {
				code: "busy",
				detail: "being deployed",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await screen.findByText("1 previews");

		await user.click(screen.getByRole("button", { name: "remove" }));

		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "DELETE")).toBe(true),
		);
		await act(async () => {});
		expect(screen.queryByText("Deleting PR #12")).not.toBeInTheDocument();
		expect(reads.previews).toBe(1);
	});
});
