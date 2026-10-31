import { Switch } from "#/components/ui/switch";
import { type DraftProps, SettingRow } from "./fields";

/**
 * Run to completion: a one-shot service (a migration, a seed job) runs once
 * per deploy, exit 0 is success, and it is never restarted. It has no port,
 * domain or health check, so turning it on clears the port and the health
 * check, and the sections that configure them are hidden.
 */
export function OneShotRow({ draft, set }: Pick<DraftProps, "draft" | "set">) {
	return (
		<SettingRow
			label="One-shot job"
			htmlFor="set-oneshot"
			description="Runs once per deploy and exits. Services that depend on it wait for it to finish."
		>
			<div className="flex items-center gap-3">
				<Switch
					id="set-oneshot"
					checked={draft.oneshot}
					onCheckedChange={(checked) => {
						set("oneshot", checked);
						if (checked) {
							set("port", "");
							set("healthType", "none");
							set("tailnet", null);
						}
					}}
				/>
				<span className="text-[13px] text-ink-2">
					{draft.oneshot
						? "On: exit code 0 is success; never restarted"
						: "Off: a long-running service"}
				</span>
			</div>
		</SettingRow>
	);
}
