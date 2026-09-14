package sqlite

import "context"

// ActiveInvestigationSlots includes unresolved provider ownership. Expiry alone
// cannot make an uncertain attempt disappear from the global capacity budget.
func (d *Database) ActiveInvestigationSlots(ctx context.Context) (int, error) {
	var count int
	err := d.sql.QueryRowContext(ctx, `SELECT count(*) FROM investigation_attempts
		WHERE json_extract(record_json,'$.state') IN ('running','uncertain')`).Scan(&count)
	return count, err
}
