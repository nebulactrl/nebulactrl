import { useRef } from "react";
import { PlatformSetupDialog } from "./setup-dialog";
import {
	platformSetup,
	type SetupRequest,
	usePlatformSetupRequest,
} from "./store";

/**
 * Mounted once by the settings shell: shows the set-up dialog for whatever a
 * "Set up for me" button asked for. The dialog lives here, not in the button,
 * because the sections around a button re-mount as the service is wired in.
 */
export function PlatformSetupHost() {
	const request = usePlatformSetupRequest();
	// Keep the last request while the dialog animates closed.
	const last = useRef<SetupRequest>({});
	if (request !== null) last.current = request;
	return (
		<PlatformSetupDialog
			defaults={last.current.defaults}
			open={request !== null}
			onOpenChange={(open) => {
				if (!open) platformSetup.close();
			}}
		/>
	);
}
