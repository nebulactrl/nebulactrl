import { describe, expect, test } from "bun:test";
import type { RegisteredRouter } from "@tanstack/react-router";
import { act, screen } from "@testing-library/react";
import { useState } from "react";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { resetTeamScope, TeamScopeBoundary } from "./team-scope";

function Counter() {
	const [n, setN] = useState(0);
	return (
		<button type="button" onClick={() => setN(n + 1)}>
			clicks {n}
		</button>
	);
}

describe("TeamScopeBoundary", () => {
	test("replaces the page with a loader during a team change, then mounts it fresh", async () => {
		const client = testQueryClient();
		await renderWithProviders(
			<TeamScopeBoundary>
				<Counter />
			</TeamScopeBoundary>,
			{ queryClient: client },
		);
		await act(async () => {
			screen.getByRole("button", { name: "clicks 0" }).click();
		});
		expect(
			screen.getByRole("button", { name: "clicks 1" }),
		).toBeInTheDocument();

		let release: () => void = () => undefined;
		const warm = new Promise<void>((resolve) => {
			release = resolve;
		});
		const router = {
			invalidate: async () => undefined,
			navigate: async () => undefined,
		} as unknown as RegisteredRouter;
		let done: Promise<void> = Promise.resolve();
		await act(async () => {
			done = resetTeamScope(client, router, () => warm);
			await Promise.resolve();
		});

		// The old team's page is gone while the new team's lists load.
		expect(screen.getByText("Switching team")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /clicks/ }),
		).not.toBeInTheDocument();

		await act(async () => {
			release();
			await done;
		});
		// Mounted afresh: the page's own state did not survive.
		expect(
			screen.getByRole("button", { name: "clicks 0" }),
		).toBeInTheDocument();
		expect(screen.queryByText("Switching team")).not.toBeInTheDocument();
	});
});
