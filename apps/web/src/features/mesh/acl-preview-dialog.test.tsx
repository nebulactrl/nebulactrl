import { describe, expect, mock, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { AclPreviewDialog } from "./acl-preview-dialog";
import { link, meshView, rule } from "./test-data";

const POLICY = '{\n  // generated\n  "acls": []\n}';

describe("AclPreviewDialog", () => {
	test("makes no request while closed", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<AclPreviewDialog
				open={false}
				onOpenChange={() => {}}
				mesh={meshView()}
			/>,
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("shows the rules by link name, the policy text and what would change in the tailnet", async () => {
		const api = stubApi({
			"POST /mesh/acl/preview": {
				body: {
					policy: POLICY,
					rules: [rule()],
					diff: {
						add: [
							{
								action: "accept",
								src: ["100.64.0.9"],
								dst: ["100.64.0.7:15432"],
							},
						],
						remove: [
							{ action: "accept", src: ["100.64.0.1"], dst: ["100.64.0.2:80"] },
						],
					},
				},
			},
		});
		await renderWithProviders(
			<AclPreviewDialog
				open
				onOpenChange={() => {}}
				mesh={meshView({ links: [link()] })}
			/>,
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			await within(dialog).findByText("production · web-02 → db-01"),
		).toBeInTheDocument();
		expect(within(dialog).getByText("100.64.0.7:15432")).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				"1 rule generated from your servers, links and grants. This is a preview: nothing is written.",
			),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				(_, el) => el?.tagName === "PRE" && el.textContent === POLICY,
			),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("What would change in the tailnet"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				(_, el) =>
					el?.tagName === "PRE" &&
					el.textContent ===
						"+ accept 100.64.0.9 -> 100.64.0.7:15432\n- accept 100.64.0.1 -> 100.64.0.2:80",
			),
		).toBeInTheDocument();
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"POST /mesh/acl/preview",
		]);
		expect(api.unmatched).toEqual([]);
	});

	test("with no rules it explains the policy denies everything and shows no diff section", async () => {
		stubApi({
			"POST /mesh/acl/preview": { body: { policy: "{}", rules: null } },
		});
		await renderWithProviders(
			<AclPreviewDialog open onOpenChange={() => {}} mesh={meshView()} />,
		);
		expect(
			await screen.findByText(/No rules yet: no link needs one/),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
		expect(
			screen.queryByText("What would change in the tailnet"),
		).not.toBeInTheDocument();
	});

	test("an unreadable tailnet policy (no diff) is not presented as 'no change'", async () => {
		stubApi({
			"POST /mesh/acl/preview": {
				body: { policy: "{}", rules: [rule()] },
			},
		});
		await renderWithProviders(
			<AclPreviewDialog
				open
				onOpenChange={() => {}}
				mesh={meshView({ links: [link()] })}
			/>,
		);
		await screen.findByText("production · web-02 → db-01");
		expect(
			screen.queryByText("What would change in the tailnet"),
		).not.toBeInTheDocument();
	});

	test("shows the mesh error's sentence when the preview fails", async () => {
		stubApi({
			"POST /mesh/acl/preview": problem(409, {
				code: "mesh_paused",
				detail: "paused upstream",
			}),
		});
		await renderWithProviders(
			<AclPreviewDialog open onOpenChange={() => {}} mesh={meshView()} />,
		);
		expect(
			await screen.findByText(
				"The mesh is paused. Changes wait until it resumes.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("Policy")).not.toBeInTheDocument();
	});

	test("an unknown error code shows the server's own message", async () => {
		stubApi({
			"POST /mesh/acl/preview": problem(500, {
				code: "boom",
				detail: "Coordinator exploded",
			}),
		});
		await renderWithProviders(
			<AclPreviewDialog open onOpenChange={() => {}} mesh={meshView()} />,
		);
		expect(await screen.findByText("Coordinator exploded")).toBeInTheDocument();
	});

	test("Close asks the parent to close", async () => {
		stubApi({
			"POST /mesh/acl/preview": { body: { policy: "{}", rules: [] } },
		});
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderWithProviders(
			<AclPreviewDialog open onOpenChange={onOpenChange} mesh={meshView()} />,
		);
		await user.click(await screen.findByRole("button", { name: "Close" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});
});
