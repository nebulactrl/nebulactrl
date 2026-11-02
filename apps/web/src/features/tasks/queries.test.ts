import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { ApiError } from "#/api/client-config";
import { invalidateTasks, startErrorMessage } from "./queries";

describe("startErrorMessage", () => {
	test("tells the user to deploy first on not_deployed", () => {
		expect(
			startErrorMessage(new ApiError(409, "not_deployed", "no deployment")),
		).toBe(
			"Deploy the service first: a command runs in the image of its current release.",
		);
	});

	test("explains an offline server on server_offline", () => {
		expect(
			startErrorMessage(new ApiError(409, "server_offline", "agent gone")),
		).toBe(
			"The service's server is offline, so nothing can run on it right now.",
		);
	});

	test("shows the server's own message for any other API error", () => {
		expect(
			startErrorMessage(new ApiError(400, "bad_request", "command is empty")),
		).toBe("command is empty");
	});

	test("shows the message of a plain error and a generic one for non-errors", () => {
		expect(startErrorMessage(new Error("network down"))).toBe("network down");
		expect(startErrorMessage("nope")).toBe("Something went wrong.");
	});
});

describe("invalidateTasks", () => {
	const seed = (client: QueryClient, id: string) => {
		client.setQueryData([{ _id: id }], { cached: true });
	};
	const isInvalidated = (client: QueryClient, id: string) =>
		client.getQueryState([{ _id: id }])?.isInvalidated;

	test("marks the task, run and log queries stale and leaves others alone", async () => {
		const client = new QueryClient();
		for (const id of [
			"listServiceTasks",
			"listServiceTaskRuns",
			"getTaskRun",
			"getTaskRunLogs",
			"listServices",
		]) {
			seed(client, id);
		}
		await invalidateTasks(client);
		expect(isInvalidated(client, "listServiceTasks")).toBe(true);
		expect(isInvalidated(client, "listServiceTaskRuns")).toBe(true);
		expect(isInvalidated(client, "getTaskRun")).toBe(true);
		expect(isInvalidated(client, "getTaskRunLogs")).toBe(true);
		expect(isInvalidated(client, "listServices")).toBe(false);
	});
});
