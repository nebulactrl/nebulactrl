import type { Service } from "#/api/models";

/** What every inspector tab body receives. */
export interface ServiceTabProps {
	service: Service;
	/** Settings only: `?section=`, the category (or a section in it) to show. */
	section: string | undefined;
	/** Settings only: show another category, keeping the tab. */
	onSectionChange(section: string): void;
}
