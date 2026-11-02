import type * as Gen from "#/api/gen/types.gen";
import { type Server, type Service, toService } from "#/api/models";
import {
	make,
	type Overrides,
	problem as problemReply,
} from "#/test/factories";
import { ENGINE_CATALOG } from "./engines.fixture";

// Builds a literal `${NAME}` placeholder (the escaped `$` keeps it from interpolating).
const ref = (name: string) => `\${${name}}`;

/** A model object with only the fields a test names (component props, never sent over the wire). */
function wire<T>(fields: Record<string, unknown>): T {
	return fields as unknown as T;
}

/** An RFC 9457 problem reply, as the API sends errors. */
export const problem = (status: number, detail: string, code = "bad_request") =>
	problemReply(status, { title: "Error", detail, code });

/** `GET /database-engines`. */
export const catalogBody = { engines: ENGINE_CATALOG };

/** A valkey database that was just created: staged, never deployed, on `srv-1`. */
export function serviceView(
	extra: Overrides<Gen.ServiceView> = {},
): Gen.ServiceView {
	return make("ServiceView", {
		id: "svc-db",
		environmentId: "env-1",
		projectId: "proj-1",
		name: "valkey",
		slug: "valkey",
		kind: "database",
		sourceType: "image",
		config: {
			image: "valkey/valkey:9",
			port: 6379,
			healthcheck: { type: "none" },
			database: { engine: "valkey", version: "9" },
		},
		serverId: "srv-1",
		server: null,
		appliedGeneration: 0,
		hasStagedChanges: true,
		stagedKind: "new",
		currentDeploymentId: null,
		latestDeployment: null,
		currentRelease: null,
		observed: null,
		observedAt: null,
		privateHost: "valkey.internal",
		...extra,
	});
}

/** A managed-database service as the app models it. */
export function service(extra: Overrides<Gen.ServiceView> = {}): Service {
	return toService(serviceView(extra));
}

/** A server that can take services. */
export function server(extra: Record<string, unknown> = {}): Server {
	return wire<Server>({
		id: "srv-1",
		name: "alpha",
		status: "online",
		latest: null,
		drainState: "none",
		drainMessage: "",
		acceptWorkloads: true,
		buildEnabled: false,
		...extra,
	});
}

const POSTGRES = ENGINE_CATALOG.find((engine) => engine.id === "postgres");
if (!POSTGRES) throw new Error("fixture catalog has no postgres");
export const postgres: Gen.EngineView = POSTGRES;

/** `GET /services/:id/database` for a PostgreSQL 16 database. */
export function databaseView(
	extra: Overrides<Gen.DatabaseView> = {},
): Gen.DatabaseView {
	return make("DatabaseView", {
		serviceId: "svc-db",
		engine: postgres,
		spec: { engine: "postgres", version: "16" },
		image: "postgres:16",
		deployed: true,
		credentialKeys: ["DATABASE_URL"],
		exports: [
			{
				key: "DATABASE_URL",
				description: "Connection string",
				primary: true,
				secret: true,
				reference: ref("{svc-db.DATABASE_URL}"),
			},
		],
		...extra,
	});
}

export interface ClipboardStub {
	/** What was written, in order. */
	writes: string[];
	restore(): void;
}

/**
 * Replaces `navigator.clipboard`: with a working one that records writes, or
 * (`"absent"`) removes it, as over plain http where the API does not exist.
 */
export function stubClipboard(mode: "ok" | "absent"): ClipboardStub {
	const original = Object.getOwnPropertyDescriptor(navigator, "clipboard");
	const writes: string[] = [];
	Object.defineProperty(navigator, "clipboard", {
		configurable: true,
		value:
			mode === "ok"
				? {
						writeText: async (text: string) => {
							writes.push(text);
						},
					}
				: undefined,
	});
	return {
		writes,
		restore() {
			if (original) Object.defineProperty(navigator, "clipboard", original);
			else Reflect.deleteProperty(navigator, "clipboard");
		},
	};
}
