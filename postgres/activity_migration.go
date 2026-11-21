package postgres

import (
	"context"
	"database/sql"
)

// RollbackActivityCalendar rebuilds UTC day contributions from the current
// review and vote facts. Run it as the schema owner with the application and
// relay stopped, then rebuild the search projection from the restored sources.
func RollbackActivityCalendar(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `SELECT reviews.set_activity_timezone('UTC')`)
	return safe(err)
}
