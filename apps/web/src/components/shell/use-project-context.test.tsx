import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef, useState } from "react";
import { renderAt, shellApi } from "./shell-test-kit";
import { useProjectContext } from "./use-project-context";

function Probe() {
	const context = useProjectContext();
	if (!context) return <output aria-label="context">none</output>;
	return (
		<output aria-label="context">
			{JSON.stringify({
				projectId: context.projectId,
				project: context.project?.name ?? null,
				environments: context.environments.map((e) => e.slug),
				environment: context.environment?.slug ?? null,
				envSlug: context.envSlug ?? null,
			})}
		</output>
	);
}

/** Reports whether the environments array is the one it first saw, across re-renders. */
function IdentityProbe() {
	const environments = useProjectContext()?.environments;
	const [renders, setRenders] = useState(0);
	const first = useRef<unknown>(undefined);
	if (environments && environments.length > 0 && first.current === undefined) {
		first.current = environments;
	}
	return (
		<>
			<output aria-label="same array">
				{String(first.current !== undefined && first.current === environments)}
			</output>
			<button type="button" onClick={() => setRenders(renders + 1)}>
				Render again
			</button>
		</>
	);
}

const read = () =>
	JSON.parse(screen.getByLabelText("context").textContent ?? "null");

describe("useProjectContext", () => {
	test("is undefined outside a project and asks the API for nothing", async () => {
		const api = shellApi();
		await renderAt(<Probe />, { path: "/servers" });
		expect(screen.getByLabelText("context")).toHaveTextContent("none");
		expect(api.calls).toEqual([]);
	});

	test("reads the project, its environments (production first) and the one on screen", async () => {
		const api = shellApi();
		await renderAt(<Probe />, { path: "/p/p1/staging/deployments" });
		await waitFor(() =>
			expect(read()).toEqual({
				projectId: "p1",
				project: "Shop",
				environments: ["production", "staging"],
				environment: "staging",
				envSlug: "staging",
			}),
		);
		expect(api.unmatched).toEqual([]);
	});

	test("a project's own pages have a project but no environment", async () => {
		shellApi();
		await renderAt(<Probe />, { path: "/p/p1/settings" });
		await waitFor(() => expect(read().project).toBe("Shop"));
		expect(read()).toMatchObject({
			projectId: "p1",
			environment: null,
			envSlug: null,
		});
	});

	test("an environment slug that does not exist resolves to no environment", async () => {
		shellApi();
		await renderAt(<Probe />, { path: "/p/p1/nope" });
		await waitFor(() =>
			expect(read().environments).toEqual(["production", "staging"]),
		);
		expect(read()).toMatchObject({ environment: null, envSlug: "nope" });
	});

	test("while loading the project and environments are empty", async () => {
		shellApi({ "GET /projects": () => new Promise<Response>(() => {}) });
		await renderAt(<Probe />, { path: "/p/p1/staging" });
		expect(read()).toMatchObject({
			projectId: "p1",
			project: null,
			envSlug: "staging",
		});
	});

	test("keeps the same environments array while the environments are unchanged", async () => {
		shellApi();
		const user = userEvent.setup();
		await renderAt(<IdentityProbe />, { path: "/p/p1/staging" });
		await waitFor(() =>
			expect(screen.getByLabelText("same array")).toHaveTextContent("true"),
		);
		await user.click(screen.getByRole("button", { name: "Render again" }));
		await user.click(screen.getByRole("button", { name: "Render again" }));
		expect(screen.getByLabelText("same array")).toHaveTextContent("true");
	});
});
