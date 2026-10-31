// The registries calls over the generated SDK, and the mappers that turn the
// wire shapes into the models in `model.ts` (the boundary `api/models.ts`
// is for other resources).

import {
	checkRegistry as checkRegistryOp,
	createRegistry as createRegistryOp,
	deleteRegistry as deleteRegistryOp,
	listRegistries,
	listRegistryImages,
	updateRegistry as updateRegistryOp,
} from "#/api/gen/sdk.gen";
import type {
	RegistryImageView,
	RegistryView,
	RegistryCheck as WireCheck,
} from "#/api/gen/types.gen";
import type {
	CreateRegistryBody,
	ImageState,
	Registry,
	RegistryCheck,
	RegistryImage,
	UpdateRegistryBody,
} from "./model";

export function toCheck(wire: WireCheck): RegistryCheck {
	return {
		ok: wire.ok,
		push: wire.push,
		pull: wire.pull,
		error: wire.error ?? "",
		at: wire.at,
	};
}

export function toRegistry(wire: RegistryView): Registry {
	return {
		id: wire.id,
		name: wire.name,
		kind: wire.kind,
		host: wire.host,
		namespace: wire.namespace,
		hasPushAuth: wire.hasPushAuth,
		pushUsername: wire.pushUsername ?? "",
		hasPullAuth: wire.hasPullAuth,
		pullUsername: wire.pullUsername ?? "",
		manageRetention: wire.manageRetention,
		lastCheck: Object.fromEntries(
			Object.entries(wire.lastCheck ?? {}).map(([serverId, check]) => [
				serverId,
				toCheck(check),
			]),
		),
		imageCount: wire.imageCount,
		sizeBytes: wire.sizeBytes,
		builtin: wire.builtin
			? {
					status: wire.builtin.status,
					statusMessage: wire.builtin.statusMessage,
					gcState: wire.builtin.gcState,
					lastGcAt: wire.builtin.lastGcAt ?? null,
				}
			: null,
		inUse: wire.inUse,
		createdAt: wire.createdAt,
	};
}

export function toImage(wire: RegistryImageView): RegistryImage {
	return {
		id: wire.id,
		repository: wire.repository,
		digest: wire.digest,
		platform: wire.platform,
		sizeBytes: wire.sizeBytes ?? null,
		state: wire.state,
		pushedAt: wire.pushedAt,
		serviceId: wire.serviceId ?? null,
		releaseId: wire.releaseId ?? null,
	};
}

export async function fetchRegistries(
	signal: AbortSignal,
): Promise<Registry[]> {
	const { data } = await listRegistries({ signal, throwOnError: true });
	return (data.items ?? []).map(toRegistry);
}

export async function fetchImages(
	registryId: string,
	state: ImageState,
	signal: AbortSignal,
): Promise<RegistryImage[]> {
	const { data } = await listRegistryImages({
		path: { registryId },
		query: { state, limit: 100 },
		signal,
		throwOnError: true,
	});
	return (data.items ?? []).map(toImage);
}

export async function createRegistry(
	body: CreateRegistryBody,
): Promise<Registry> {
	const { data } = await createRegistryOp({ body, throwOnError: true });
	return toRegistry(data);
}

export async function updateRegistry(
	registryId: string,
	body: UpdateRegistryBody,
): Promise<Registry> {
	const { data } = await updateRegistryOp({
		path: { registryId },
		body,
		throwOnError: true,
	});
	return toRegistry(data);
}

export async function deleteRegistry(registryId: string): Promise<void> {
	await deleteRegistryOp({ path: { registryId }, throwOnError: true });
}

export async function checkRegistry(
	registryId: string,
	serverId: string,
): Promise<RegistryCheck> {
	const { data } = await checkRegistryOp({
		path: { registryId },
		query: { serverId },
		throwOnError: true,
	});
	return toCheck(data);
}
