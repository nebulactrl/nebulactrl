import type {
	InstanceServicesView,
	InstanceServiceView,
	SliceView,
} from "#/api/gen/types.gen";
import { make, type Overrides } from "#/test/factories";
import { GIB } from "./model";

// Test-only builders for the instance-services views.

export function slice(over: Overrides<SliceView> = {}): SliceView {
	return make("SliceView", {
		id: "slice-1",
		kind: "s3",
		teamId: "team-1",
		teamName: "Acme",
		state: "ready",
		message: "",
		usedBytes: 10 * GIB,
		effectiveQuotaBytes: 50 * GIB,
		quotaBytes: null,
		deletedAt: null,
		purgeAt: null,
		...over,
	});
}

export function service(
	over: Overrides<InstanceServiceView> = {},
): InstanceServiceView {
	return make("InstanceServiceView", {
		kind: "s3",
		status: "ready",
		message: "",
		teams: 2,
		usedBytes: 0,
		...over,
	});
}

export function view(
	over: Overrides<InstanceServicesView> = {},
): InstanceServicesView {
	return make("InstanceServicesView", {
		defaultQuotaBytes: 50 * GIB,
		services: [
			service({
				kind: "s3",
				usedBytes: 3 * GIB,
				updatedAt: new Date().toISOString(),
			}),
			service({ kind: "registry", teams: 1 }),
			service({ kind: "loki", status: "provisioning" }),
		],
		slices: [slice()],
		...over,
	});
}
