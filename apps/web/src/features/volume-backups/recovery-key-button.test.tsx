import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { RecoveryKeyButton } from "./recovery-key-button";

interface Download {
	blob: Blob;
	filename: string;
	href: string;
}

const realCreate = URL.createObjectURL;
const realRevoke = URL.revokeObjectURL;
const realAnchorClick = HTMLAnchorElement.prototype.click;
let downloads: Download[];
let revoked: string[];

beforeEach(() => {
	downloads = [];
	revoked = [];
	let n = 0;
	const blobs = new Map<string, Blob>();
	URL.createObjectURL = (obj: Blob | MediaSource) => {
		const url = `blob:test/${++n}`;
		blobs.set(url, obj as Blob);
		return url;
	};
	URL.revokeObjectURL = (url: string) => {
		revoked.push(url);
	};
	// happy-dom would try to navigate to the blob URL; record the download instead.
	HTMLAnchorElement.prototype.click = function click(this: HTMLAnchorElement) {
		const blob = blobs.get(this.href);
		if (!blob) throw new Error(`no blob behind ${this.href}`);
		downloads.push({ blob, filename: this.download, href: this.href });
	};
});

afterEach(() => {
	URL.createObjectURL = realCreate;
	URL.revokeObjectURL = realRevoke;
	HTMLAnchorElement.prototype.click = realAnchorClick;
});

const KEY = {
	repository: "s3:https://s3.example.com/bucket/svc-1",
	password: "correct-horse-battery-staple",
	note: "Pass this to restic as RESTIC_PASSWORD.",
};

describe("RecoveryKeyButton", () => {
	test("asks before revealing and sends nothing until confirmed", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /services/:serviceId/volume-backup/recovery-key": { body: KEY },
		});
		await renderWithProviders(
			<RecoveryKeyButton serviceId="svc-1" serviceSlug="shop" />,
		);
		await user.click(
			screen.getByRole("button", { name: "Download recovery key" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Download the recovery key?");
		expect(dialog).toHaveTextContent("recorded in the audit log");
		expect(api.calls).toEqual([]);

		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls).toEqual([]);
		expect(downloads).toEqual([]);
	});

	test("confirming POSTs the reveal and downloads the key as a named text file", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /services/:serviceId/volume-backup/recovery-key": { body: KEY },
		});
		await renderWithProviders(
			<RecoveryKeyButton serviceId="svc-1" serviceSlug="shop" />,
		);
		await user.click(
			screen.getByRole("button", { name: "Download recovery key" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Download key" }),
		);

		expect(await screen.findByText("Recovery key downloaded")).toBeVisible();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/services/svc-1/volume-backup/recovery-key",
			body: {},
		});

		expect(downloads).toHaveLength(1);
		const [file] = downloads;
		expect(file?.filename).toBe(
			"nebulactrl-shop-volume-backup-recovery-key.txt",
		);
		expect(file?.blob.type).toBe("text/plain");
		const text = await file?.blob.text();
		expect(text).toContain("recovery key for shop");
		expect(text).toContain(`Repository: ${KEY.repository}`);
		expect(text).toContain(`Password:   ${KEY.password}`);
		expect(text).toContain(KEY.note);
		// The object URL is released and no link is left in the page.
		expect(revoked).toEqual([file?.href ?? ""]);
		expect(document.querySelector("a[download]")).toBeNull();
		// The key is never kept on the page.
		expect(document.body).not.toHaveTextContent(KEY.password);
		expect(api.unmatched).toEqual([]);
	});

	test("a failed reveal downloads nothing and shows the server's reason", async () => {
		const user = userEvent.setup();
		stubApi({
			"POST /services/:serviceId/volume-backup/recovery-key": {
				status: 409,
				body: {
					status: 409,
					code: "conflict",
					title: "Conflict",
					detail: "No backup has run yet.",
				},
			},
		});
		await renderWithProviders(
			<RecoveryKeyButton serviceId="svc-1" serviceSlug="shop" />,
		);
		await user.click(
			screen.getByRole("button", { name: "Download recovery key" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Download key" }),
		);

		expect(
			await screen.findByText("Could not reveal the recovery key"),
		).toBeVisible();
		expect(
			screen.getByText("No backup has run yet.", {
				selector: "[data-toast] *",
			}),
		).toBeVisible();
		expect(screen.queryByText("Recovery key downloaded")).toBeNull();
		expect(downloads).toEqual([]);
		expect(revoked).toEqual([]);
	});
});
