// Thresholds of the service health alerts (service.crashed, service.unhealthy,
// service.restart_loop, service.recovered) and of the missed-backup alert.
// Which channel receives which event stays the channel's own event selection;
// these decide when a condition becomes an alert. Pure, so the form's parsing
// and limits (the server enforces the same ones) are tested.

export interface AlertSettings {
	crashGraceSeconds: number;
	unhealthyGraceSeconds: number;
	loopRestarts: number;
	loopWindowMinutes: number;
	recoverAfterSeconds: number;
	flapIncidents: number;
	flapWindowMinutes: number;
	backupGraceMinutes: number;
	logLines: number;
}

export type AlertKey = keyof AlertSettings;

export interface AlertField {
	key: AlertKey;
	label: string;
	unit: string;
	min: number;
	max: number;
	hint: string;
}

/** In display order: crashes, health, loops, recovery, flapping, backups, logs. */
export const ALERT_FIELDS: readonly AlertField[] = [
	{
		key: "crashGraceSeconds",
		label: "Down for",
		unit: "seconds",
		min: 0,
		max: 3600,
		hint: "How long a container stays down before it is announced as crashed.",
	},
	{
		key: "unhealthyGraceSeconds",
		label: "Unhealthy for",
		unit: "seconds",
		min: 0,
		max: 3600,
		hint: "How long the health check keeps failing before the service is unhealthy.",
	},
	{
		key: "loopRestarts",
		label: "Restart loop at",
		unit: "restarts",
		min: 2,
		max: 50,
		hint: "Restarts inside the window below that count as a restart loop.",
	},
	{
		key: "loopWindowMinutes",
		label: "Restart window",
		unit: "minutes",
		min: 1,
		max: 1440,
		hint: "How far back restarts are counted.",
	},
	{
		key: "recoverAfterSeconds",
		label: "Healthy for",
		unit: "seconds",
		min: 10,
		max: 3600,
		hint: "How long the service looks healthy before the incident closes.",
	},
	{
		key: "flapIncidents",
		label: "Flapping at",
		unit: "incidents",
		min: 2,
		max: 20,
		hint: "Incidents inside the flap window that make a service flapping.",
	},
	{
		key: "flapWindowMinutes",
		label: "Flap window",
		unit: "minutes",
		min: 5,
		max: 1440,
		hint: "How far back incidents are counted, and the stable time before recovery.",
	},
	{
		key: "backupGraceMinutes",
		label: "Backup grace",
		unit: "minutes",
		min: 0,
		max: 10080,
		hint: "Extra time a backup gets after its due time. 0 is automatic.",
	},
	{
		key: "logLines",
		label: "Log lines",
		unit: "lines",
		min: 0,
		max: 20,
		hint: "Log lines attached to a crash or restart-loop message. 0 attaches none.",
	},
];

export type AlertForm = Record<AlertKey, string>;
export type AlertErrors = Partial<Record<AlertKey, string>>;

export function toForm(s: AlertSettings): AlertForm {
	const form = {} as AlertForm;
	for (const f of ALERT_FIELDS) form[f.key] = String(s[f.key]);
	return form;
}

/** The settings of a form, or one message per field that is not a whole number in range. */
export function parseForm(form: AlertForm): {
	value: AlertSettings | null;
	errors: AlertErrors;
} {
	const errors: AlertErrors = {};
	const value = {} as AlertSettings;
	for (const f of ALERT_FIELDS) {
		const raw = form[f.key].trim();
		const n = Number(raw);
		if (raw === "" || !Number.isInteger(n)) {
			errors[f.key] = "Enter a whole number.";
		} else if (n < f.min || n > f.max) {
			errors[f.key] = `Between ${f.min} and ${f.max}.`;
		} else {
			value[f.key] = n;
		}
	}
	return { value: Object.keys(errors).length > 0 ? null : value, errors };
}

export function isDirty(base: AlertSettings, form: AlertForm): boolean {
	return ALERT_FIELDS.some((f) => form[f.key].trim() !== String(base[f.key]));
}
