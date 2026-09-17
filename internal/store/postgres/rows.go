package postgres

import "database/sql"

// rowScanner is the subset of *sql.Rows and *sql.Row that a single-row scan
// needs, so one scan function serves both the list and the single-row queries.
type rowScanner interface {
	Scan(dest ...any) error
}

// collectRows drains a result set with scan. Keeping the loop in one place
// means every list query inherits the terminal rows.Err() check instead of
// re-implementing (and potentially forgetting) it.
func collectRows[T any](rows *sql.Rows, scan func(rowScanner) (T, error)) ([]T, error) {
	var items []T
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
