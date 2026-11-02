import { describe, expect, test } from "bun:test";
import type { DeploymentStatus } from "#/api/models";
import {
	deploymentMessage,
	deploymentStatusKind,
	failedAtLabel,
	isCancelable,
	isInFlight,
	releaseDetail,
	releaseLabel,
	STATUS_KIND,
	stepLabel,
} from "./labels";

describe("STATUS_KIND", () => {
	test("maps every status to a status icon", () => {
		expect(STATUS_KIND.queued).toBe("pending");
		expect(STATUS_KIND.building).toBe("building");
		expect(STATUS_KIND.releasing).toBe("deploying");
		expect(STATUS_KIND.verifying).toBe("verifying");
		expect(STATUS_KIND.succeeded).toBe("live");
		expect(STATUS_KIND.failed).toBe("failed");
		expect(STATUS_KIND.rollback_failed).toBe("failed");
		expect(STATUS_KIND.rolled_back).toBe("degraded");
		expect(STATUS_KIND.canceled).toBe("stopped");
	});
});

describe("deploymentStatusKind", () => {
	test("an in-flight deployment shows its working icon", () => {
		expect(deploymentStatusKind("building", false)).toBe("building");
		expect(deploymentStatusKind("verifying", true)).toBe("verifying");
		expect(deploymentStatusKind("failed", false)).toBe("failed");
	});

	test("a succeeded deployment has an icon only while it is the live one", () => {
		expect(deploymentStatusKind("succeeded", true)).toBe("live");
		expect(deploymentStatusKind("succeeded", false)).toBeNull();
	});
});

describe("isCancelable", () => {
	test("is true at every step until the release is live", () => {
		const cancelable: DeploymentStatus[] = [
			"created",
			"queued",
			"preparing",
			"building",
			"releasing",
			"verifying",
		];
		for (const status of cancelable) expect(isCancelable(status)).toBe(true);
	});

	test("is false once settled", () => {
		const not: DeploymentStatus[] = [
			"succeeded",
			"failed",
			"canceled",
			"rolled_back",
			"rollback_failed",
		];
		for (const status of not) expect(isCancelable(status)).toBe(false);
	});

	test("is a subset of the in-flight statuses", () => {
		for (const status of [
			"created",
			"queued",
			"preparing",
			"building",
		] as const)
			expect(isInFlight(status)).toBe(true);
	});
});

describe("failedAtLabel", () => {
	test("names the step", () => {
		expect(failedAtLabel("verify")).toBe("Failed at Verify");
		expect(failedAtLabel("predeploy")).toBe("Failed at Pre-deploy");
	});
	test("is null without a step", () => {
		expect(failedAtLabel(null)).toBeNull();
		expect(failedAtLabel("  ")).toBeNull();
	});
});

describe("stepLabel", () => {
	test("names predeploy and leaves other steps as sent", () => {
		expect(stepLabel("predeploy")).toBe("Pre-deploy");
		expect(stepLabel("postdeploy")).toBe("Post-deploy");
		expect(stepLabel("health")).toBe("health");
	});

	test("names the push to a registry", () => {
		expect(stepLabel("push")).toBe("Push to registry");
		expect(failedAtLabel("push")).toBe("Failed at Push to registry");
	});
});

type MessageInput = Parameters<typeof deploymentMessage>[0];

const AGENT_TEXT =
	"Deployed 2 replicas (nc-whoami-8bcef913-0, nc-whoami-8bcef913-1)";

function input(overrides: Partial<MessageInput> = {}): MessageInput {
	return {
		message: null,
		statusMessage: AGENT_TEXT,
		trigger: "manual",
		status: "succeeded",
		release: {
			id: "rel-1",
			imageRef: "traefik/whoami:latest",
			createdAt: "2026-01-01T00:00:00Z",
			source: {
				type: "image",
				image: "traefik/whoami:latest",
				digest: "traefik/whoami@sha256:0a1b2c3d4e5f60718293a4b5c6d7e8f9",
			},
		},
		...overrides,
	};
}

describe("deploymentMessage", () => {
	test("prefers the note the deployment was requested with", () => {
		const m = deploymentMessage(input({ message: "  Fix login " }));
		expect(m).toEqual({
			text: "Fix login",
			kind: "note",
			detail: "sha256:0a1b2c3d",
		});
	});

	test("an image release reads as its ref, digest secondary", () => {
		const m = deploymentMessage(input());
		expect(m.text).toBe("traefik/whoami:latest");
		expect(m.kind).toBe("release");
		expect(m.detail).toBe("sha256:0a1b2c3d");
	});

	test("never shows the agent's result text for a success", () => {
		expect(deploymentMessage(input()).text).not.toContain("nc-whoami");
	});

	test("a git release reads as sha7 and branch", () => {
		const m = deploymentMessage(
			input({
				release: {
					id: "rel-2",
					imageRef: "nc-build/api:1234",
					createdAt: "2026-01-01T00:00:00Z",
					source: {
						type: "git",
						gitUrl: "https://github.com/acme/api.git",
						branch: "main",
						commitSha: "a91c3f2d8e7b6a5948372615f4e3d2c1b0a99887",
					},
				},
			}),
		);
		expect(m.text).toBe("a91c3f2 · main");
		expect(m.detail).toBeNull();
	});

	test("a failure explains itself, with the step when known", () => {
		const failed = input({
			status: "failed",
			statusMessage: "health check timed out",
		});
		expect(deploymentMessage(failed)).toEqual({
			text: "health check timed out",
			kind: "failure",
			detail: "sha256:0a1b2c3d",
		});
		expect(deploymentMessage(failed, "verify").text).toBe(
			"Failed at Verify: health check timed out",
		);
	});

	test("a canceled deployment keeps its status message", () => {
		const m = deploymentMessage(
			input({ status: "canceled", statusMessage: "Canceled by Alice" }),
		);
		expect(m.text).toBe("Canceled by Alice");
	});

	test("falls back to the trigger without a release", () => {
		const m = deploymentMessage(
			input({ release: null, statusMessage: null, trigger: "redeploy" }),
		);
		expect(m).toEqual({ text: "Redeploy", kind: "trigger", detail: null });
	});
});

describe("deploymentMessage for commits", () => {
	test("a push is known by its commit subject and author", () => {
		const m = deploymentMessage(
			input({
				release: null,
				statusMessage: null,
				commit: {
					sha: "a91c3f2deadbeef",
					branch: "main",
					author: "Ada",
					message: "Fix the login redirect\n\nLong body that is not shown",
				},
			}),
		);
		expect(m.text).toBe("Fix the login redirect · Ada");
		expect(m.kind).toBe("release");
	});

	test("a commit without an author shows the subject alone", () => {
		const m = deploymentMessage(
			input({
				release: null,
				statusMessage: null,
				commit: {
					sha: "a91c3f2",
					branch: "main",
					author: "",
					message: "Bump deps",
				},
			}),
		);
		expect(m.text).toBe("Bump deps");
	});

	test("a note still beats the commit subject", () => {
		const m = deploymentMessage(
			input({
				message: "hotfix",
				commit: {
					sha: "a91c3f2",
					branch: "main",
					author: "Ada",
					message: "Bump deps",
				},
			}),
		);
		expect(m.text).toBe("hotfix");
		expect(m.kind).toBe("note");
	});

	test("a git release without a commit message reads sha7 · branch, with the release as detail only when not shown", () => {
		const m = deploymentMessage(
			input({
				statusMessage: null,
				release: {
					id: "rel-1",
					imageRef: "registry/api:a91c3f2",
					createdAt: "2026-01-01T00:00:00Z",
					source: { type: "git", commitSha: "a91c3f2deadbeef", branch: "main" },
				},
			}),
		);
		expect(m).toEqual({
			text: "a91c3f2 · main",
			kind: "release",
			detail: null,
		});
	});

	test("a git release with neither sha nor branch text falls back to the image reference", () => {
		const m = deploymentMessage(
			input({
				statusMessage: null,
				release: {
					id: "rel-1",
					imageRef: "registry/api:latest",
					createdAt: "2026-01-01T00:00:00Z",
					source: { type: "git", commitSha: "" },
				},
			}),
		);
		expect(m.text).toBe("registry/api:latest");
		expect(m.kind).toBe("release");
	});
});

describe("releaseLabel", () => {
	test("is the short commit for a git release", () => {
		expect(releaseLabel({ type: "git", commitSha: "a91c3f2deadbeef" })).toBe(
			"a91c3f2",
		);
	});

	test("is the short digest for an image release", () => {
		expect(
			releaseLabel({ type: "image", digest: "repo@sha256:0a1b2c3d4e5f6071" }),
		).toBe("sha256:0a1b2c3d");
	});

	test("is the image when there is no usable digest", () => {
		expect(
			releaseLabel({
				type: "image",
				image: "traefik/whoami:1",
				digest: "nope",
			}),
		).toBe("traefik/whoami:1");
	});

	test("is the image reference without a source, and 'unknown release' without either", () => {
		expect(releaseLabel(undefined, "registry/api:1")).toBe("registry/api:1");
		expect(releaseLabel(undefined)).toBe("unknown release");
	});
});

describe("releaseDetail", () => {
	test("a git release is 'repo @ sha7' without scheme or .git", () => {
		expect(
			releaseDetail({
				type: "git",
				gitUrl: "https://git.example/acme/api.git",
				commitSha: "a91c3f2deadbeef",
			}),
		).toBe("git.example/acme/api @ a91c3f2");
	});

	test("a git release without a URL still names the commit", () => {
		expect(releaseDetail({ type: "git", commitSha: "a91c3f2deadbeef" })).toBe(
			" @ a91c3f2",
		);
	});

	test("an image release is its digest, else its image, else the reference", () => {
		expect(
			releaseDetail({
				type: "image",
				digest: "repo@sha256:abc",
				image: "repo",
			}),
		).toBe("repo@sha256:abc");
		expect(releaseDetail({ type: "image", image: "repo:1" })).toBe("repo:1");
		expect(releaseDetail({ type: "image" }, "registry/api:1")).toBe(
			"registry/api:1",
		);
	});

	test("without a source it is the image reference", () => {
		expect(releaseDetail(undefined, "registry/api:1")).toBe("registry/api:1");
		expect(releaseDetail(undefined)).toBe("");
	});
});
