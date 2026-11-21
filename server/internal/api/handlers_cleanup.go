package api

import "postwerk/internal/store"

// validateCleanup checks the housekeeping settings for DKIM keys and DAV
// tombstones.
func validateCleanup(r *store.RetentionSettings) error {
	if r.DAVTombstoneDays != 0 && (r.DAVTombstoneDays < 7 || r.DAVTombstoneDays > 3650) {
		return invalid("Gelöschte Kalender- und Kontakteinträge: 0 (nie vergessen) oder 7 bis 3650 Tage merken")
	}
	return nil
}
