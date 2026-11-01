import { ApiError, reportFailure } from "#/api/client-config";

/** Same-origin URL the dump file of a restore is PUT to. */
function dumpUploadUrl(restoreId: string, fileName: string): string {
	const q = new URLSearchParams({ fileName });
	return `/api/v1/dump-restores/${encodeURIComponent(restoreId)}/upload?${q.toString()}`;
}

export interface DumpUpload {
	/** Resolves when the control plane has handed the whole file to the agent. */
	done: Promise<void>;
	/** Stops the transfer; the restore fails and the live database is untouched. */
	abort: () => void;
}

function problemOf(status: number, text: string): ApiError {
	let code = "unknown";
	let message = `Request failed with status ${status}.`;
	try {
		const body = JSON.parse(text) as { code?: string; detail?: string };
		code = body.code ?? code;
		message = body.detail ?? message;
	} catch {
		/* not a problem document */
	}
	const error = new ApiError(status, code, message);
	reportFailure(error);
	return error;
}

/**
 * Streams a file to a restore. fetch cannot report upload progress, so this
 * uses XMLHttpRequest; the browser sets Content-Length from the File and reads
 * it from disk as it sends (the file is never held in memory).
 */
export function uploadDump(
	restoreId: string,
	file: File,
	onProgress: (sent: number, total: number) => void,
): DumpUpload {
	const xhr = new XMLHttpRequest();
	const { promise: done, resolve, reject } = Promise.withResolvers<void>();
	xhr.open("PUT", dumpUploadUrl(restoreId, file.name));
	xhr.withCredentials = true;
	xhr.setRequestHeader("Content-Type", "application/octet-stream");
	xhr.upload.onprogress = (e) => {
		if (e.lengthComputable) onProgress(e.loaded, e.total);
	};
	xhr.onload = () => {
		if (xhr.status === 202) {
			onProgress(file.size, file.size);
			resolve();
		} else {
			reject(problemOf(xhr.status, xhr.responseText));
		}
	};
	xhr.onerror = () =>
		reject(
			new ApiError(
				0,
				"network_error",
				"The connection to the control plane broke during the upload. Nothing was switched over.",
			),
		);
	xhr.onabort = () =>
		reject(new ApiError(0, "aborted", "The upload was canceled."));
	xhr.send(file);
	return { done, abort: () => xhr.abort() };
}
