import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import type { DeploymentView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { makeDeployment, makeService } from "../../lib/fixtures";
import { LatestDeployments } from "./latest-deployments";

function wire(overrides: Partial<DeploymentView>): DeploymentView {
	return {
		id: "dep-1",
		serviceId: "svc-1",
		releaseId: "",
		serverId: "server-1",
		buildServerId: "",
		trigger: "manual",
		actorType: "user",
		actorLabel: "Ada",
		status: "succeeded",
		strategy: "rolling",
		statusMessage: "",
		message: null,
		rolledBackFrom: "",
		generation: 1,
		createdAt: "2026-10-31T10:00:00Z",
		startedAt: null,
		finishedAt: null,
		cancelRequestedAt: null,
		cancelRequestedBy: "",
		canceledAtStep: "",
		build: null,
		commit: null,
		release: {
			id: "",
			imageRef: "",
			createdAt: "2026-10-31T10:00:00Z",
			source: { type: "image" },
		},
		waitingFor: null,
		...overrides,
	};
}

const gitRelease = (sha: string) => ({
	id: `rel-${sha}`,
	imageRef: "",
	createdAt: "2026-10-31T10:00:00Z",
	source: {
		type: "git",
		commitSha: `${sha}0000000000`,
		gitUrl: "https://github.com/acme/api.git",
		branch: "main",
	},
});

describe("LatestDeployments", () => {
	test("lists the three newest deployments: message, release and where each stands, and who", async () => {
		const service = makeService({
			id: "svc-1",
			currentDeploymentId: "dep-live",
			latestDeployment: makeDeployment("failed", {
				id: "dep-failed",
				failedStep: "verify",
			}),
		});
		const api = stubApi({
			"GET /services/:serviceId/deployments": {
				body: make("PageDeploymentView", {
					items: [
						wire({
							id: "dep-failed",
							status: "failed",
							statusMessage: "health check timed out",
							actorLabel: "Grace",
							release: gitRelease("3fd29a8"),
						}),
						wire({
							id: "dep-live",
							status: "succeeded",
							message: "Ship the new checkout",
							actorLabel: "Ada",
							release: gitRelease("a91c3f2"),
						}),
						wire({
							id: "dep-old",
							status: "succeeded",
							actorLabel: "System",
							release: gitRelease("0b1c2d3"),
						}),
						wire({ id: "dep-ancient", status: "succeeded" }),
					],
				}),
			},
		});
		await renderWithProviders(<LatestDeployments service={service} />);
		const rows = await screen.findAllByRole("listitem");
		expect(rows).toHaveLength(3);
		const [failed, live, old] = rows as [HTMLElement, HTMLElement, HTMLElement];
		expect(failed).toHaveTextContent(
			"Failed at Verify: health check timed out",
		);
		expect(failed).toHaveTextContent("3fd29a8 · Failed at Verify");
		expect(failed).toHaveTextContent("Grace");
		expect(live).toHaveTextContent("Ship the new checkout");
		expect(live).toHaveTextContent("a91c3f2 · Live");
		expect(live).toHaveTextContent("Ada");
		expect(old).toHaveTextContent("0b1c2d3 · Superseded");
		// The row's release line carries the repository as its hover title.
		expect(
			within(live).getByTitle("github.com/acme/api @ a91c3f2"),
		).toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/services/svc-1/deployments");
		expect(api.unmatched).toEqual([]);
	});

	test("a deployment still in flight says its own status", async () => {
		stubApi({
			"GET /services/:serviceId/deployments": {
				body: make("PageDeploymentView", {
					items: [
						wire({
							id: "dep-b",
							status: "building",
							release: gitRelease("abc1234"),
						}),
					],
				}),
			},
		});
		await renderWithProviders(
			<LatestDeployments service={makeService({ id: "svc-1" })} />,
		);
		expect(await screen.findByText("abc1234 · Building")).toBeInTheDocument();
	});

	test("an empty history says nothing has been deployed", async () => {
		stubApi({
			"GET /services/:serviceId/deployments": {
				body: make("PageDeploymentView", { items: [] }),
			},
		});
		await renderWithProviders(<LatestDeployments service={makeService()} />);
		expect(
			await screen.findByText("Nothing has been deployed yet."),
		).toBeInTheDocument();
	});

	test("says so when the deployments do not load", async () => {
		stubApi({
			"GET /services/:serviceId/deployments": problem(500, { title: "boom" }),
		});
		await renderWithProviders(<LatestDeployments service={makeService()} />);
		expect(
			await screen.findByText("Deployments did not load."),
		).toBeInTheDocument();
	});
});
