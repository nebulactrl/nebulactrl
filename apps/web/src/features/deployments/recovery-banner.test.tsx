import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import { renderWithProviders } from "#/test/render";
import { RecoveryBanner, WaitingForServer } from "./recovery-banner";
import { deployment, service } from "./test-fixtures";

const waiting = {
	serverId: "srv-1",
	serverName: "edge-1",
	offlineSince: "2026-10-31T09:00:00Z",
};

describe("WaitingForServer", () => {
	test("names the offline server and explains the saved command", async () => {
		await renderWithProviders(
			<WaitingForServer
				deployment={deployment({ status: "queued", waitingFor: waiting })}
			/>,
		);
		expect(screen.getByText(/Waiting for edge-1/)).toBeInTheDocument();
		expect(screen.getByText(/offline since/)).toBeInTheDocument();
		expect(
			screen.getByText(/sent when the agent reconnects/),
		).toBeInTheDocument();
	});

	test("omits the offline-since part when unknown", async () => {
		await renderWithProviders(
			<WaitingForServer
				deployment={deployment({
					status: "queued",
					waitingFor: { ...waiting, offlineSince: null },
				})}
			/>,
		);
		expect(screen.getByText(/Waiting for edge-1/)).toBeInTheDocument();
		expect(screen.queryByText(/offline since/)).not.toBeInTheDocument();
	});

	test("renders nothing when the deployment is not waiting on a server", async () => {
		const { container } = await renderWithProviders(
			<WaitingForServer deployment={deployment({ status: "queued" })} />,
		);
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		expect(container.textContent).toBe("");
	});
});

describe("RecoveryBanner", () => {
	test("an in-flight deployment shows the waiting notice", async () => {
		await renderWithProviders(
			<RecoveryBanner
				deployment={deployment({ status: "queued", waitingFor: waiting })}
				service={service()}
			/>,
		);
		expect(screen.getByText(/Waiting for edge-1/)).toBeInTheDocument();
	});

	test("an in-flight deployment on a connected server shows nothing", async () => {
		const { container } = await renderWithProviders(
			<RecoveryBanner
				deployment={deployment({ status: "building" })}
				service={service()}
			/>,
		);
		expect(container.textContent).toBe("");
	});

	test("a failed rollback gives hand recovery steps with the server and service ids", async () => {
		await renderWithProviders(
			<RecoveryBanner
				deployment={deployment({
					status: "rollback_failed",
					statusMessage: "container exited 137",
				})}
				service={service({ id: "svc-42", name: "billing" })}
			/>,
		);
		expect(
			screen.getByText("Rollback failed: billing may not be serving"),
		).toBeInTheDocument();
		expect(screen.getByText(/container exited 137/)).toBeInTheDocument();
		expect(screen.getByText(/Check edge-1 is online/)).toBeInTheDocument();
		expect(
			screen.getByText(/label=nebulactrl\.service=svc-42/),
		).toBeInTheDocument();
		expect(screen.getByText(/docker rm -f/)).toBeInTheDocument();
	});

	test("a failed rollback falls back to 'the server' when the service has none", async () => {
		await renderWithProviders(
			<RecoveryBanner
				deployment={deployment({ status: "rollback_failed" })}
				service={service({ server: null })}
			/>,
		);
		expect(screen.getByText(/Check the server is online/)).toBeInTheDocument();
	});

	test("a failed deploy with a release still serving says nothing was taken down", async () => {
		await renderWithProviders(
			<RecoveryBanner
				deployment={deployment({
					status: "failed",
					statusMessage: "health check timed out",
				})}
				service={service({ currentDeploymentId: "dep-0" })}
			/>,
		);
		expect(
			screen.getByText(
				"Deployment failed: the previous release is still serving",
			),
		).toBeInTheDocument();
		expect(screen.getByText(/health check timed out/)).toBeInTheDocument();
		expect(screen.getByText(/Nothing was taken down/)).toBeInTheDocument();
	});

	test("a failed deploy without a message points at the log", async () => {
		await renderWithProviders(
			<RecoveryBanner
				deployment={deployment({ status: "failed" })}
				service={service({ currentDeploymentId: "dep-0" })}
			/>,
		);
		expect(
			screen.getByText(/Open the deployment below for the log\./),
		).toBeInTheDocument();
	});

	test("a failed first deploy (nothing serving) shows no banner", async () => {
		const { container } = await renderWithProviders(
			<RecoveryBanner
				deployment={deployment({ status: "failed", statusMessage: "boom" })}
				service={service({ currentDeploymentId: null })}
			/>,
		);
		expect(container.textContent).toBe("");
	});

	test("a rolled-back deployment explains the rollback and its reason", async () => {
		await renderWithProviders(
			<RecoveryBanner
				deployment={deployment({
					status: "rolled_back",
					statusMessage: "readiness probe failed",
				})}
				service={service()}
			/>,
		);
		expect(
			screen.getByText(
				"The new release failed verification and was rolled back",
			),
		).toBeInTheDocument();
		expect(screen.getByText("readiness probe failed")).toBeInTheDocument();
	});

	test("a rolled-back deployment without a message says the previous release was restored", async () => {
		await renderWithProviders(
			<RecoveryBanner
				deployment={deployment({ status: "rolled_back" })}
				service={service()}
			/>,
		);
		expect(
			screen.getByText("The previous release was restored."),
		).toBeInTheDocument();
	});

	test("a succeeded deployment shows nothing", async () => {
		const { container } = await renderWithProviders(
			<RecoveryBanner
				deployment={deployment({ status: "succeeded" })}
				service={service({ currentDeploymentId: "dep-1" })}
			/>,
		);
		expect(container.textContent).toBe("");
	});
});
