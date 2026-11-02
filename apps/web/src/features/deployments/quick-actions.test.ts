import { describe, expect, test } from "bun:test";
import { ApiError } from "#/api/client-config";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import {
	cancelDeploymentRequest,
	startDeploy,
	startRestart,
} from "./quick-actions";
import { deploymentView } from "./test-fixtures";

function recordingReply(body: unknown, status = 202) {
	const headers: Headers[] = [];
	return {
		headers,
		reply: (request: Request) => {
			headers.push(request.headers);
			return Response.json(body, { status });
		},
	};
}

describe("startDeploy", () => {
	test("posts no body for a plain deploy and returns the accepted deployment", async () => {
		const recorded = recordingReply(
			deploymentView({ id: "dep-9", status: "queued" }),
		);
		const api = stubApi({ "POST /services/:id/deployments": recorded.reply });

		const created = await startDeploy("svc-1");

		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/services/svc-1/deployments",
			body: undefined,
		});
		expect(created.id).toBe("dep-9");
		expect(created.status).toBe("queued");
	});

	test("sends a fresh Idempotency-Key with every request", async () => {
		const recorded = recordingReply(deploymentView());
		stubApi({ "POST /services/:id/deployments": recorded.reply });

		await startDeploy("svc-1");
		await startDeploy("svc-1");

		const keys = recorded.headers.map((h) => h.get("Idempotency-Key"));
		expect(keys[0]).toBeTruthy();
		expect(keys[1]).toBeTruthy();
		expect(keys[0]).not.toBe(keys[1]);
	});

	test("rebuilds an exact commit when given a sha", async () => {
		const api = stubApi({
			"POST /services/:id/deployments": {
				status: 202,
				body: deploymentView(),
			},
		});
		await startDeploy("svc-1", "abc1234");
		expect(api.calls[0]?.body).toEqual({ commitSha: "abc1234" });
	});

	test("trims the note and drops a blank one", async () => {
		const api = stubApi({
			"POST /services/:id/deployments": {
				status: 202,
				body: deploymentView(),
			},
		});
		await startDeploy("svc-1", undefined, "  hotfix  ");
		await startDeploy("svc-1", undefined, "   ");
		expect(api.calls[0]?.body).toEqual({ message: "hotfix" });
		expect(api.calls[1]?.body).toBeUndefined();
	});

	test("asks for a build without cache only when noCache is set", async () => {
		const api = stubApi({
			"POST /services/:id/deployments": {
				status: 202,
				body: deploymentView(),
			},
		});
		await startDeploy("svc-1", undefined, undefined, true);
		await startDeploy("svc-1", undefined, undefined, false);
		expect(api.calls[0]?.body).toEqual({ noCache: true });
		expect(api.calls[1]?.body).toBeUndefined();
	});

	test("combines commit, note and noCache in one body", async () => {
		const api = stubApi({
			"POST /services/:id/deployments": {
				status: 202,
				body: deploymentView(),
			},
		});
		await startDeploy("svc-1", "abc1234", "again", true);
		expect(api.calls[0]?.body).toEqual({
			commitSha: "abc1234",
			message: "again",
			noCache: true,
		});
	});

	test("rejects when the server refuses", async () => {
		stubApi({
			"POST /services/:id/deployments": problem(409, {
				code: "deploy_in_progress",
				detail: "busy",
			}),
		});
		const failure = await startDeploy("svc-1").catch((error) => error);
		expect(failure).toBeInstanceOf(ApiError);
		expect(failure).toMatchObject({
			status: 409,
			code: "deploy_in_progress",
			message: "busy",
		});
	});
});

describe("startRestart", () => {
	test("posts the restart with an Idempotency-Key", async () => {
		const recorded = recordingReply({}, 202);
		const api = stubApi({ "POST /services/:id/restart": recorded.reply });

		await startRestart("svc-7");

		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/services/svc-7/restart",
		});
		expect(recorded.headers[0]?.get("Idempotency-Key")).toBeTruthy();
	});
});

describe("cancelDeploymentRequest", () => {
	test("posts the cancel for that deployment", async () => {
		const recorded = recordingReply({}, 202);
		const api = stubApi({ "POST /deployments/:id/cancel": recorded.reply });

		await cancelDeploymentRequest("dep-4");

		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/deployments/dep-4/cancel",
		});
		expect(recorded.headers[0]?.get("Idempotency-Key")).toBeTruthy();
	});

	test("a 409 cannot_cancel surfaces as a rejection", async () => {
		stubApi({
			"POST /deployments/:id/cancel": problem(409, {
				code: "cannot_cancel",
				detail: "already finished",
			}),
		});
		const failure = await cancelDeploymentRequest("dep-4").catch((e) => e);
		expect(failure).toBeInstanceOf(ApiError);
		expect(failure).toMatchObject({ status: 409, code: "cannot_cancel" });
	});
});
