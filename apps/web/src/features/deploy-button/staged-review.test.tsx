import { describe, expect, mock, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { StagedReview, type StagedReviewProps } from "./staged-review";
import { fakeDeploy, group, service } from "./test-fixtures";
import type { EnvironmentDeploy } from "./use-environment-deploy";

const API_LINES = [
	{
		id: "svc-api:config.port",
		field: "config.port",
		sign: "~",
		text: "config.port",
		value: "80 → 8080",
	},
	{
		id: "svc-api:variables.TOKEN",
		field: "variables.TOKEN",
		sign: "+",
		text: "variables.TOKEN",
		value: "••••••",
		note: "secret",
	},
] as const;
const WORKER_LINES = [
	{
		id: "svc-worker:deployment",
		field: "deployment",
		sign: "+",
		text: "First deployment",
	},
] as const;

function stubConfigSource(managed = false) {
	return stubApi({
		"GET /services/:id/config-source": {
			body: managed
				? {
						managed: true,
						state: "in_sync",
						fields: [{ field: "config.port", state: "in_sync" }],
						pending: null,
					}
				: { managed: false, fields: null, pending: null },
		},
	});
}

function CountHarness({
	onOpenChange,
	exposeSetCount,
}: {
	onOpenChange: (open: boolean) => void;
	exposeSetCount: (set: (n: number) => void) => void;
}) {
	const [count, setCount] = useState(1);
	exposeSetCount(setCount);
	return (
		<StagedReview
			open
			onOpenChange={onOpenChange}
			environmentName="Staging"
			isProduction={false}
			deploy={fakeDeploy({
				count,
				stagedCountOf: () => count,
				groups: count ? [group("svc-api", "api", [API_LINES[0]])] : [],
			})}
		/>
	);
}

function twoServices(overrides: Partial<EnvironmentDeploy> = {}) {
	return fakeDeploy({
		services: [
			service({ id: "svc-api", name: "api", staged: 2 }),
			service({ id: "svc-worker", name: "worker", staged: 1 }),
		],
		stagedCountOf: (id) => (id === "svc-api" ? 2 : id === "svc-worker" ? 1 : 0),
		groups: [
			group("svc-api", "api", [...API_LINES]),
			group("svc-worker", "worker", [...WORKER_LINES]),
		],
		count: 3,
		...overrides,
	});
}

async function renderReview(
	deploy: EnvironmentDeploy,
	props: Partial<StagedReviewProps> = {},
) {
	const onOpenChange = mock((_open: boolean) => {});
	await renderWithProviders(
		<StagedReview
			open
			onOpenChange={onOpenChange}
			environmentName="Staging"
			isProduction={false}
			deploy={deploy}
			{...props}
		/>,
	);
	const sheet = await screen.findByRole("dialog");
	return { sheet, onOpenChange, user: userEvent.setup() };
}

describe("StagedReview content", () => {
	test("lists every service's staged lines with what each does", async () => {
		stubConfigSource();
		const { sheet } = await renderReview(twoServices());
		expect(within(sheet).getByText("Review changes")).toBeInTheDocument();
		expect(
			within(sheet).getByText(
				"3 changes across 2 services · Staging. Saved, not deployed.",
			),
		).toBeInTheDocument();
		const api = within(sheet).getByRole("region", { name: "api" });
		const lines = within(api).getAllByRole("listitem");
		expect(lines.map((line) => line.getAttribute("data-sign"))).toEqual([
			"changed",
			"added",
		]);
		expect(
			within(lines[0] as HTMLElement).getByText("80 → 8080"),
		).toBeInTheDocument();
		expect(
			within(lines[1] as HTMLElement).getByText("secret"),
		).toBeInTheDocument();
		const worker = within(sheet).getByRole("region", { name: "worker" });
		expect(within(worker).getByText("First deployment")).toBeInTheDocument();
		// A first deployment has no earlier release to go back to: no discard.
		expect(
			within(worker).queryByRole("button", { name: /Discard change/ }),
		).toBeNull();
		expect(
			within(api).getByRole("button", {
				name: "Discard change to config.port on api",
			}),
		).toBeInTheDocument();
	});

	test("one change in one service uses the singular", async () => {
		stubConfigSource();
		const { sheet } = await renderReview(
			fakeDeploy({
				services: [service({ id: "svc-worker", name: "worker", staged: 1 })],
				groups: [group("svc-worker", "worker", [...WORKER_LINES])],
				count: 1,
			}),
		);
		expect(
			within(sheet).getByText(
				"1 change across 1 service · Staging. Saved, not deployed.",
			),
		).toBeInTheDocument();
	});

	test("lines the nebula.toml file owns say so", async () => {
		const api = stubConfigSource(true);
		const { sheet } = await renderReview(twoServices());
		const apiGroup = within(sheet).getByRole("region", { name: "api" });
		expect(
			await within(apiGroup).findByText("set by nebula.toml"),
		).toBeInTheDocument();
		expect(
			api.calls.some((c) => c.path === "/services/svc-api/config-source"),
		).toBe(true);
	});

	test("nothing staged: says so and cannot deploy", async () => {
		const { sheet } = await renderReview(fakeDeploy());
		expect(
			within(sheet).getByText("Nothing staged in Staging."),
		).toBeInTheDocument();
		expect(
			within(sheet).getByRole("button", { name: "Deploy · 0" }),
		).toBeDisabled();
		expect(
			within(sheet).getByRole("button", { name: "Discard all" }),
		).toBeDisabled();
	});

	test("while the lines load a placeholder stands in", async () => {
		const { sheet } = await renderReview(
			fakeDeploy({ count: 2, groups: [], stagedCountOf: () => 2 }),
		);
		expect(
			within(sheet).getByRole("status", { name: "Loading changes" }),
		).toBeInTheDocument();
	});

	test("services flagged staged with no change left are listed as nothing to deploy", async () => {
		stubConfigSource();
		const { sheet } = await renderReview(
			twoServices({
				emptyStaged: [
					service({
						id: "a",
						name: "alpha",
						staged: 0,
						hasStagedChanges: true,
					}),
					service({ id: "b", name: "beta", staged: 0, hasStagedChanges: true }),
				],
			}),
		);
		const note = within(sheet).getByRole("region", {
			name: "Nothing to review",
		});
		expect(note).toHaveTextContent(
			"alpha, beta are marked staged but no change is left. Nothing deploys for them.",
		);
	});

	test("a single empty-staged service reads in the singular", async () => {
		const { sheet } = await renderReview(
			fakeDeploy({
				emptyStaged: [
					service({ id: "a", name: "alpha", hasStagedChanges: true }),
				],
			}),
		);
		expect(
			within(sheet).getByRole("region", { name: "Nothing to review" }),
		).toHaveTextContent(
			"alpha is marked staged but no change is left. Nothing deploys for it.",
		);
	});

	test("a line being discarded is disabled along with the others while busy", async () => {
		stubConfigSource();
		const { sheet } = await renderReview(
			twoServices({ busy: true, discarding: "svc-api:config.port" }),
		);
		for (const button of within(sheet).getAllByRole("button", {
			name: /Discard change/,
		})) {
			expect(button).toBeDisabled();
		}
		expect(
			within(sheet).getByRole("button", { name: "Discard all" }),
		).toBeDisabled();
		expect(
			within(sheet).getByRole("button", { name: /Deploy · 3/ }),
		).toBeDisabled();
	});
});

describe("StagedReview actions", () => {
	test("Deploy sends the staged changes with the typed message and closes", async () => {
		stubConfigSource();
		const deploy = twoServices();
		const { sheet, onOpenChange, user } = await renderReview(deploy);
		await user.type(
			within(sheet).getByLabelText(/Deploy message/),
			"Bump port",
		);
		expect(within(sheet).getByText("9/200")).toBeInTheDocument();
		await user.click(within(sheet).getByRole("button", { name: "Deploy · 3" }));
		expect(deploy.deployStaged).toHaveBeenCalledTimes(1);
		expect(deploy.deployStaged).toHaveBeenCalledWith("Bump port");
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
	});

	test("a refused deploy keeps the sheet open and the message", async () => {
		stubConfigSource();
		const deploy = twoServices({ deployStaged: mock(async () => false) });
		const { sheet, onOpenChange, user } = await renderReview(deploy);
		await user.type(within(sheet).getByLabelText(/Deploy message/), "keep me");
		await user.click(within(sheet).getByRole("button", { name: "Deploy · 3" }));
		await waitFor(() => expect(deploy.deployStaged).toHaveBeenCalled());
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(within(sheet).getByLabelText(/Deploy message/)).toHaveValue(
			"keep me",
		);
	});

	test("line breaks in the message become spaces", async () => {
		const { sheet, user } = await renderReview(fakeDeploy());
		const box = within(sheet).getByLabelText(/Deploy message/);
		await user.type(box, "one{Enter}two");
		expect(box).toHaveValue("one two");
	});

	test("while sending the button reads Deploying… and cannot be pressed", async () => {
		stubConfigSource();
		const { sheet } = await renderReview(twoServices({ sent: true }));
		expect(
			within(sheet).getByRole("button", { name: /Deploying…/ }),
		).toBeDisabled();
	});

	test("discarding a line asks for that field of that service", async () => {
		stubConfigSource();
		const deploy = twoServices();
		const { sheet, user } = await renderReview(deploy);
		await user.click(
			within(sheet).getByRole("button", {
				name: "Discard change to variables.TOKEN on api",
			}),
		);
		expect(deploy.discardChange).toHaveBeenCalledWith(
			"svc-api",
			"variables.TOKEN",
		);
	});

	test("Discard all reverts everything", async () => {
		stubConfigSource();
		const deploy = twoServices();
		const { sheet, user } = await renderReview(deploy);
		await user.click(
			within(sheet).getByRole("button", { name: "Discard all" }),
		);
		expect(deploy.discardAll).toHaveBeenCalledTimes(1);
	});

	test("Shift+Enter deploys outside production", async () => {
		stubConfigSource();
		const deploy = twoServices();
		const { sheet, user } = await renderReview(deploy);
		within(sheet)
			.getByLabelText(/Deploy message/)
			.focus();
		await user.keyboard("{Shift>}{Enter}{/Shift}");
		await waitFor(() => expect(deploy.deployStaged).toHaveBeenCalledWith(""));
	});

	test("plain Enter in the message does not deploy", async () => {
		const deploy = twoServices();
		stubConfigSource();
		const { sheet, user } = await renderReview(deploy);
		within(sheet)
			.getByLabelText(/Deploy message/)
			.focus();
		await user.keyboard("{Enter}");
		expect(deploy.deployStaged).not.toHaveBeenCalled();
	});

	test("Shift+Enter with nothing staged does nothing", async () => {
		const deploy = fakeDeploy();
		const { sheet, user } = await renderReview(deploy);
		within(sheet)
			.getByLabelText(/Deploy message/)
			.focus();
		await user.keyboard("{Shift>}{Enter}{/Shift}");
		expect(deploy.deployStaged).not.toHaveBeenCalled();
	});

	test("emptying the sheet by discarding closes it", async () => {
		stubConfigSource();
		let setCount: (n: number) => void = () => {};
		const onOpenChange = mock((_open: boolean) => {});
		await renderWithProviders(
			<CountHarness
				onOpenChange={onOpenChange}
				exposeSetCount={(set) => {
					setCount = set;
				}}
			/>,
		);
		await screen.findByRole("dialog");
		expect(onOpenChange).not.toHaveBeenCalled();
		act(() => setCount(0));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
	});

	test("a sheet opened with nothing staged stays open", async () => {
		const { onOpenChange } = await renderReview(fakeDeploy());
		expect(onOpenChange).not.toHaveBeenCalled();
	});
});

describe("StagedReview scoped to one service", () => {
	test("deploys that service alone", async () => {
		stubConfigSource();
		const deploy = twoServices();
		const { sheet, user } = await renderReview(deploy, {
			scopeServiceId: "svc-api",
		});
		expect(within(sheet).getByText("Deploy api")).toBeInTheDocument();
		expect(within(sheet).queryByRole("region", { name: "worker" })).toBeNull();
		expect(
			within(sheet).queryByRole("button", { name: "Discard all" }),
		).toBeNull();
		expect(
			within(sheet).getByText(
				"2 changes across 1 service · Staging. Saved, not deployed.",
			),
		).toBeInTheDocument();
		await user.type(within(sheet).getByLabelText(/Deploy message/), "just api");
		await user.click(within(sheet).getByRole("button", { name: "Deploy · 2" }));
		expect(deploy.deployStaged).toHaveBeenCalledWith("just api", ["svc-api"]);
	});

	test("a scoped service that is not loaded has nothing to deploy or redeploy", async () => {
		stubConfigSource();
		const deploy = twoServices();
		const { sheet, onOpenChange, user } = await renderReview(deploy, {
			scopeServiceId: "svc-idle",
		});
		// The service is not in the list yet: no redeploy to offer.
		expect(
			within(sheet).getByRole("button", { name: "Deploy · 0" }),
		).toBeDisabled();
		expect(onOpenChange).not.toHaveBeenCalled();
		await user.keyboard("{Escape}");
	});

	test("a known idle service redeploys with the typed message and closes", async () => {
		const deploy = fakeDeploy({
			services: [service({ id: "svc-idle", name: "idle" })],
		});
		const { sheet, onOpenChange, user } = await renderReview(deploy, {
			scopeServiceId: "svc-idle",
		});
		expect(within(sheet).getByText("Deploy idle")).toBeInTheDocument();
		expect(
			within(sheet).getByText("Nothing staged for idle · Staging."),
		).toBeInTheDocument();
		expect(
			within(sheet).getByRole("region", { name: "Redeploy current release" }),
		).toHaveTextContent(
			"Deploys idle again with its current release and configuration.",
		);
		await user.type(within(sheet).getByLabelText(/Deploy message/), "again");
		await user.click(within(sheet).getByRole("button", { name: "Redeploy" }));
		expect(deploy.redeployService).toHaveBeenCalledWith("svc-idle", "again");
		expect(deploy.deployStaged).not.toHaveBeenCalled();
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
	});
});

describe("StagedReview in production", () => {
	test("shows the production cue and a guarded key instead of the plain button", async () => {
		stubConfigSource();
		const { sheet } = await renderReview(twoServices(), { isProduction: true });
		expect(
			within(sheet).getByText("Production", { selector: ".env-caption" }),
		).toBeInTheDocument();
		expect(
			within(sheet).getByRole("group", { name: "Production deploy" }),
		).toBeInTheDocument();
		expect(
			within(sheet).queryByRole("button", { name: /Deploying…/ }),
		).toBeNull();
		expect(within(sheet).queryByText("⇧↵")).toBeNull();
	});

	test("the guarded key deploys only after the cover is lifted", async () => {
		stubConfigSource();
		const deploy = twoServices();
		const { sheet, user } = await renderReview(deploy, { isProduction: true });
		const key = within(sheet).getByRole("button", {
			name: "Deploy · 3",
			hidden: true,
		});
		await user.click(key);
		expect(deploy.deployStaged).not.toHaveBeenCalled();
		await user.click(
			within(sheet).getByRole("button", { name: "Lift production guard" }),
		);
		await user.click(key);
		await waitFor(() => expect(deploy.deployStaged).toHaveBeenCalledTimes(1));
	});

	test("Shift+Enter lifts the guard first, then deploys", async () => {
		stubConfigSource();
		const deploy = twoServices();
		const { sheet, user } = await renderReview(deploy, { isProduction: true });
		within(sheet)
			.getByLabelText(/Deploy message/)
			.focus();
		await user.keyboard("{Shift>}{Enter}{/Shift}");
		expect(
			await within(sheet).findByRole("button", {
				name: "Lower production guard",
			}),
		).toBeInTheDocument();
		expect(deploy.deployStaged).not.toHaveBeenCalled();
		await user.keyboard("{Shift>}{Enter}{/Shift}");
		await waitFor(() => expect(deploy.deployStaged).toHaveBeenCalledTimes(1));
	});

	test("the production key is disabled with nothing staged", async () => {
		const { sheet } = await renderReview(fakeDeploy(), { isProduction: true });
		expect(
			within(sheet).getByRole("button", { name: "Lift production guard" }),
		).toHaveAttribute("title", "Nothing staged to deploy");
	});
});
