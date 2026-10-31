import { useQuery } from "@tanstack/react-query";
import { ApiError, reportFailure } from "#/api/client-config";
import {
	getServerHostShellOptions,
	getServiceTerminalOptions,
	listServiceFilesOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { fileUploadUrl } from "./protocol";

/** Availability, replicas and limits of a service's terminal and files. */
export function useTerminalInfo(serviceId: string | null) {
	return useQuery({
		...getServiceTerminalOptions({ path: { serviceId: serviceId ?? "" } }),
		enabled: serviceId !== null,
		// Replica states change while the panel is open.
		refetchInterval: 10_000,
	});
}

/**
 * What the caller can do with a server's host shell. A 403 means the caller is
 * not an Owner or Admin: callers show nothing then, so it is not retried.
 */
export function useHostShellInfo(serverId: string | null) {
	return useQuery({
		...getServerHostShellOptions({ path: { serverId: serverId ?? "" } }),
		enabled: serverId !== null,
		retry: false,
		// Whether the server is online changes while a panel is open.
		refetchInterval: 15_000,
	});
}

/** One directory listing. */
export function useDirectory(
	serviceId: string | null,
	replica: number,
	path: string,
	process = "",
) {
	return useQuery({
		...listServiceFilesOptions({
			path: { serviceId: serviceId ?? "" },
			query: { ...(process === "" ? {} : { process }), replica, path },
		}),
		enabled: serviceId !== null,
		retry: false,
	});
}

/** Reads a failed raw response as the app's typed error. */
async function toApiError(res: Response): Promise<ApiError> {
	let code = "unknown";
	let message = res.statusText || `Request failed with status ${res.status}.`;
	try {
		const body = (await res.json()) as { code?: string; detail?: string };
		code = body.code ?? code;
		message = body.detail ?? message;
	} catch {
		/* not a problem document */
	}
	const error = new ApiError(res.status, code, message);
	reportFailure(error);
	return error;
}

/**
 * Uploads one file into a directory of a replica container. The raw endpoint
 * takes the bytes as the request body (the browser sets Content-Length from
 * the File), so there is no generated client call for it.
 */
export async function uploadFile(input: {
	serviceId: string;
	/** The process whose replica receives the file; "" for a service without processes. */
	process?: string;
	replica: number;
	dir: string;
	file: File;
}): Promise<{ path: string; size: number }> {
	const res = await fetch(
		fileUploadUrl(
			input.serviceId,
			input.replica,
			input.dir,
			input.file.name,
			input.process ?? "",
		),
		{
			method: "POST",
			credentials: "include",
			headers: { "Content-Type": "application/octet-stream" },
			body: input.file,
		},
	).catch(() => {
		throw new ApiError(
			0,
			"network_error",
			"The control plane did not answer. The file was not uploaded.",
		);
	});
	if (!res.ok) throw await toApiError(res);
	return (await res.json()) as { path: string; size: number };
}

/** Reads the inline preview of a text file (at most the preview limit). */
export async function fetchPreview(url: string): Promise<Uint8Array> {
	const res = await fetch(url, { credentials: "include" }).catch(() => {
		throw new ApiError(0, "network_error", "The control plane did not answer.");
	});
	if (!res.ok) throw await toApiError(res);
	return new Uint8Array(await res.arrayBuffer());
}
