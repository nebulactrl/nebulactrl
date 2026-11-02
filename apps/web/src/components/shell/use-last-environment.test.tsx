import { beforeEach, describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import { renderAt, shellApi } from "./shell-test-kit";
import { useSidebarEnvironment } from "./use-last-environment";
import { useProjectContext } from "./use-project-context";

const KEY = "nebulactrl:last-environment:t1";

function Probe() {
	const context = useProjectContext();
	const target = useSidebarEnvironment(context);
	return (
		<output aria-label="target">
			{target ? `${target.projectId}/${target.envSlug}` : "none"}
		</output>
	);
}

const target = () => screen.getByLabelText("target");

beforeEach(() => {
	window.localStorage.clear();
});

describe("useSidebarEnvironment", () => {
	test("inside an environment it is that environment, and the team remembers it", async () => {
		shellApi();
		await renderAt(<Probe />, { path: "/p/p1/staging" });
		expect(target()).toHaveTextContent("p1/staging");
		await waitFor(() =>
			expect(JSON.parse(window.localStorage.getItem(KEY) ?? "null")).toEqual({
				projectId: "p1",
				envSlug: "staging",
			}),
		);
	});

	test("outside a project it points at the last environment the team had open", async () => {
		window.localStorage.setItem(
			KEY,
			JSON.stringify({ projectId: "p2", envSlug: "production" }),
		);
		shellApi();
		await renderAt(<Probe />, { path: "/servers" });
		await waitFor(() => expect(target()).toHaveTextContent("p2/production"));
	});

	test("offers nothing until it has something to offer", async () => {
		shellApi();
		await renderAt(<Probe />, { path: "/servers" });
		await waitFor(() => expect(target()).toHaveTextContent("none"));
	});

	test("a remembered project that is gone (or another team's) is not offered", async () => {
		window.localStorage.setItem(
			KEY,
			JSON.stringify({ projectId: "gone", envSlug: "production" }),
		);
		const api = shellApi();
		await renderAt(<Probe />, { path: "/servers" });
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/projects")).toBe(true),
		);
		await waitFor(() => expect(target()).toHaveTextContent("none"));
	});

	test("each team has its own memory", async () => {
		window.localStorage.setItem(
			"nebulactrl:last-environment:other",
			JSON.stringify({ projectId: "p2", envSlug: "production" }),
		);
		shellApi();
		await renderAt(<Probe />, { path: "/servers" });
		await waitFor(() => expect(target()).toHaveTextContent("none"));
	});

	test("the global key older builds wrote is dropped", async () => {
		window.localStorage.setItem(
			"nebulactrl:last-environment",
			JSON.stringify({ projectId: "p2", envSlug: "production" }),
		);
		shellApi();
		await renderAt(<Probe />, { path: "/servers" });
		await waitFor(() =>
			expect(
				window.localStorage.getItem("nebulactrl:last-environment"),
			).toBeNull(),
		);
		expect(target()).toHaveTextContent("none");
	});

	test("an unreadable or malformed memory is the same as none", async () => {
		shellApi();
		for (const raw of [
			"{not json",
			JSON.stringify({ projectId: 1, envSlug: "x" }),
			JSON.stringify(null),
		]) {
			window.localStorage.setItem(KEY, raw);
			const view = await renderAt(<Probe />, { path: "/servers" });
			await waitFor(() => expect(target()).toHaveTextContent("none"));
			view.unmount();
		}
	});

	test("on a project's settings page it stays on that project: the remembered environment, else its first", async () => {
		window.localStorage.setItem(
			KEY,
			JSON.stringify({ projectId: "p1", envSlug: "staging" }),
		);
		shellApi();
		const first = await renderAt(<Probe />, { path: "/p/p1/settings" });
		await waitFor(() => expect(target()).toHaveTextContent("p1/staging"));
		first.unmount();

		window.localStorage.clear();
		await renderAt(<Probe />, { path: "/p/p1/settings" });
		// Environments are listed production first.
		await waitFor(() => expect(target()).toHaveTextContent("p1/production"));
	});

	test("a storage that refuses writes still follows the page", async () => {
		const set = Storage.prototype.setItem;
		Storage.prototype.setItem = () => {
			throw new Error("quota");
		};
		try {
			shellApi();
			await renderAt(<Probe />, { path: "/p/p1/staging" });
			expect(target()).toHaveTextContent("p1/staging");
		} finally {
			Storage.prototype.setItem = set;
		}
	});
});
