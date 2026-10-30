package sqlite

import (
	"database/sql"
	"time"
)

func parseOptionalTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid {
		return nil, nil
	}
	t, e := parseTime(v.String)
	return &t, e
}

func nullableCommitTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return formatTime(*v)
}
