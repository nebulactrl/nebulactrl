import { afterEach, beforeEach, describe, expect, mock, test } from "bun:test";
import { ApiError, setApiHandlers } from "#/api/client-config";
import { uploadDump } from "./dump-upload";

/** Just enough of XMLHttpRequest for uploadDump; tests drive its events by hand. */
class FakeXhr {
	static last: FakeXhr;
	method = "";
	url = "";
	withCredentials = false;
	headers: Record<string, string> = {};
	sent: unknown = undefined;
	aborted = false;
	status = 0;
	responseText = "";
	upload: { onprogress: ((e: unknown) => void) | null } = { onprogress: null };
	onload: (() => void) | null = null;
	onerror: (() => void) | null = null;
	onabort: (() => void) | null = null;

	constructor() {
		FakeXhr.last = this;
	}
	open(method: string, url: string) {
		this.method = method;
		this.url = url;
	}
	setRequestHeader(name: string, value: string) {
		this.headers[name] = value;
	}
	send(body: unknown) {
		this.sent = body;
	}
	abort() {
		this.aborted = true;
		this.onabort?.();
	}
	respond(status: number, text = "") {
		this.status = status;
		this.responseText = text;
		this.onload?.();
	}
}

const realXhr = globalThis.XMLHttpRequest;

beforeEach(() => {
	globalThis.XMLHttpRequest = FakeXhr as unknown as typeof XMLHttpRequest;
});

afterEach(() => {
	globalThis.XMLHttpRequest = realXhr;
	setApiHandlers({});
});

const file = (name = "orders.dump", size = 10) =>
	new File([new Uint8Array(size)], name, { type: "application/octet-stream" });

/** The rejection of `p`, so a test can inspect the ApiError. */
async function rejection(p: Promise<unknown>): Promise<ApiError> {
	try {
		await p;
	} catch (e) {
		if (e instanceof ApiError) return e;
		throw e;
	}
	throw new Error("expected a rejection");
}

describe("uploadDump request", () => {
	test("PUTs the file itself to the restore's upload URL with credentials", () => {
		const f = file();
		uploadDump("r1", f, () => {});
		const xhr = FakeXhr.last;
		expect(xhr.method).toBe("PUT");
		expect(xhr.url).toBe(
			"/api/v1/dump-restores/r1/upload?fileName=orders.dump",
		);
		expect(xhr.withCredentials).toBe(true);
		expect(xhr.headers).toEqual({ "Content-Type": "application/octet-stream" });
		// The File is handed over as is, so the browser streams it from disk.
		expect(xhr.sent).toBe(f);
	});

	test("escapes the restore id and the file name", () => {
		uploadDump("a/b c", file("my dump & more?.sql.gz"), () => {});
		expect(FakeXhr.last.url).toBe(
			"/api/v1/dump-restores/a%2Fb%20c/upload?fileName=my+dump+%26+more%3F.sql.gz",
		);
	});
});

describe("uploadDump progress and success", () => {
	test("reports measurable progress and ignores events without a total", () => {
		const onProgress = mock((_sent: number, _total: number) => {});
		uploadDump("r1", file(), onProgress);
		const progress = FakeXhr.last.upload.onprogress;
		progress?.({ lengthComputable: true, loaded: 3, total: 10 });
		progress?.({ lengthComputable: false, loaded: 5, total: 0 });
		progress?.({ lengthComputable: true, loaded: 7, total: 10 });
		expect(onProgress.mock.calls).toEqual([
			[3, 10],
			[7, 10],
		]);
	});

	test("202 completes the upload with a final 100% report", async () => {
		const onProgress = mock((_sent: number, _total: number) => {});
		const { done } = uploadDump("r1", file("a.dump", 42), onProgress);
		FakeXhr.last.respond(202);
		await expect(done).resolves.toBeUndefined();
		expect(onProgress.mock.calls).toEqual([[42, 42]]);
	});

	test("other success codes are not an accepted upload", async () => {
		const onProgress = mock((_sent: number, _total: number) => {});
		const { done } = uploadDump("r1", file(), onProgress);
		FakeXhr.last.respond(200);
		const error = await rejection(done);
		expect(error.status).toBe(200);
		expect(error.message).toBe("Request failed with status 200.");
		expect(onProgress).not.toHaveBeenCalled();
	});
});

describe("uploadDump failures", () => {
	test("a problem document becomes an ApiError with its code and detail", async () => {
		const { done } = uploadDump("r1", file(), () => {});
		FakeXhr.last.respond(
			413,
			JSON.stringify({
				code: "too_large",
				detail: "The file is larger than 2 GiB.",
			}),
		);
		const error = await rejection(done);
		expect(error.status).toBe(413);
		expect(error.code).toBe("too_large");
		expect(error.message).toBe("The file is larger than 2 GiB.");
	});

	test("a problem document without fields falls back to the status", async () => {
		const { done } = uploadDump("r1", file(), () => {});
		FakeXhr.last.respond(500, "{}");
		const error = await rejection(done);
		expect(error.code).toBe("unknown");
		expect(error.message).toBe("Request failed with status 500.");
	});

	test("a body that is not JSON falls back to the status", async () => {
		const { done } = uploadDump("r1", file(), () => {});
		FakeXhr.last.respond(502, "<html>Bad gateway</html>");
		const error = await rejection(done);
		expect(error.status).toBe(502);
		expect(error.code).toBe("unknown");
		expect(error.message).toBe("Request failed with status 502.");
	});

	test("a 401 and a 403 trigger the app's session and permission handlers", async () => {
		const onUnauthorized = mock(() => {});
		const onForbidden = mock((_e: ApiError) => {});
		setApiHandlers({ onUnauthorized, onForbidden });

		const first = uploadDump("r1", file(), () => {});
		FakeXhr.last.respond(401, "{}");
		await rejection(first.done);
		expect(onUnauthorized).toHaveBeenCalledTimes(1);
		expect(onForbidden).not.toHaveBeenCalled();

		const second = uploadDump("r1", file(), () => {});
		FakeXhr.last.respond(
			403,
			JSON.stringify({ code: "forbidden", detail: "no scope" }),
		);
		await rejection(second.done);
		expect(onForbidden).toHaveBeenCalledTimes(1);
		expect(onForbidden.mock.calls[0]?.[0].message).toBe("no scope");
	});

	test("other failures do not touch the global handlers", async () => {
		const onUnauthorized = mock(() => {});
		const onForbidden = mock((_e: ApiError) => {});
		setApiHandlers({ onUnauthorized, onForbidden });
		const { done } = uploadDump("r1", file(), () => {});
		FakeXhr.last.respond(500);
		await rejection(done);
		expect(onUnauthorized).not.toHaveBeenCalled();
		expect(onForbidden).not.toHaveBeenCalled();
	});

	test("a broken connection says nothing was switched over", async () => {
		const { done } = uploadDump("r1", file(), () => {});
		FakeXhr.last.onerror?.();
		const error = await rejection(done);
		expect(error.status).toBe(0);
		expect(error.code).toBe("network_error");
		expect(error.message).toContain("Nothing was switched over");
	});

	test("abort stops the transfer and rejects as canceled", async () => {
		const upload = uploadDump("r1", file(), () => {});
		expect(FakeXhr.last.aborted).toBe(false);
		upload.abort();
		expect(FakeXhr.last.aborted).toBe(true);
		const error = await rejection(upload.done);
		expect(error.status).toBe(0);
		expect(error.code).toBe("aborted");
		expect(error.message).toBe("The upload was canceled.");
	});
});
