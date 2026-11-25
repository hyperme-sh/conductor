package businesstool

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS outlook_cursors (
 scope TEXT PRIMARY KEY, generation TEXT NOT NULL, sequence INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS outlook_seen (
 scope TEXT NOT NULL, message_id TEXT NOT NULL, received_at INTEGER NOT NULL,
 PRIMARY KEY(scope, message_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS outlook_sync_results (
 scope TEXT NOT NULL, operation_id TEXT NOT NULL, page_limit INTEGER NOT NULL,
 created_at INTEGER NOT NULL, metadata TEXT NOT NULL,
 PRIMARY KEY(scope, operation_id)
) WITHOUT ROWID;`

func openState(path string) (*sql.DB, error) {
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0022 != 0 {
		return nil, Unavailable
	}
	info, err := os.Lstat(path)
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return nil, Unavailable
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, Unavailable
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil || file.Close() != nil {
		return nil, Unavailable
	}
	location := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", location.String()+"?_pragma=busy_timeout(3000)&_txlock=immediate")
	if err != nil {
		return nil, Unavailable
	}
	return db, nil
}

func (o Outlook) sync(ctx context.Context, fixture outlookFixture, scope, folder, operationID string, limit int) (syncPage, error) {
	db, err := openState(o.state)
	if err != nil {
		return syncPage{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return syncPage{}, Unavailable
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return syncPage{}, Unavailable
	}
	var priorLimit int
	var metadata []byte
	now := time.Now().Unix()
	keepAfter := now - int64((7 * 24 * time.Hour).Seconds())
	err = tx.QueryRowContext(ctx, `SELECT page_limit, metadata FROM outlook_sync_results
 WHERE scope=? AND operation_id=? AND created_at>=?`, scope, operationID, keepAfter).Scan(&priorLimit, &metadata)
	switch {
	case err == nil:
		var prior syncPage
		if priorLimit != limit {
			return syncPage{}, OperationChanged
		}
		if json.Unmarshal(metadata, &prior) != nil {
			return syncPage{}, Unavailable
		}
		return prior, nil
	case !errors.Is(err, sql.ErrNoRows):
		return syncPage{}, Unavailable
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM outlook_sync_results WHERE scope=? AND created_at<?", scope, keepAfter); err != nil {
		return syncPage{}, Unavailable
	}
	page, err := advanceCursor(ctx, tx, fixture, scope, folder, limit)
	if err != nil {
		return syncPage{}, err
	}
	metadata, err = json.Marshal(page)
	if err != nil {
		return syncPage{}, Unavailable
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO outlook_sync_results VALUES(?,?,?,?,?)",
		scope, operationID, limit, now, string(metadata)); err != nil || tx.Commit() != nil {
		return syncPage{}, Unavailable
	}
	return page, nil
}

func advanceCursor(ctx context.Context, tx *sql.Tx, fixture outlookFixture, scope, folder string, limit int) (syncPage, error) {
	var generation string
	var sequence int64
	err := tx.QueryRowContext(ctx, "SELECT generation, sequence FROM outlook_cursors WHERE scope=?", scope).Scan(&generation, &sequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return syncPage{}, Unavailable
	}
	page := syncPage{Items: []mailRef{}, CursorReset: generation != "" && generation != fixture.Generation}
	if page.CursorReset {
		sequence = 0
	}
	if sequence > 0 && (len(fixture.Messages) == 0 || fixture.Messages[len(fixture.Messages)-1].Sequence < sequence) {
		return syncPage{}, StaleSync
	}
	cutoff := fixture.AsOf.AddDate(0, 0, -maxHistory)
	if _, err = tx.ExecContext(ctx, "DELETE FROM outlook_seen WHERE scope=? AND received_at<?", scope, cutoff.Unix()); err != nil {
		return syncPage{}, Unavailable
	}
	for _, item := range fixture.Messages {
		if item.Sequence <= sequence {
			continue
		}
		if item.Folder != folder || item.ReceivedAt.Before(cutoff) {
			sequence = item.Sequence
			continue
		}
		var seen int
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM outlook_seen WHERE scope=? AND message_id=?)",
			scope, item.ID).Scan(&seen); err != nil {
			return syncPage{}, Unavailable
		}
		if seen == 0 {
			if len(page.Items) == limit {
				page.More = true
				break
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO outlook_seen VALUES(?,?,?)",
				scope, item.ID, item.ReceivedAt.Unix()); err != nil {
				return syncPage{}, Unavailable
			}
			page.Items = append(page.Items, item.ref())
		}
		sequence = item.Sequence
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO outlook_cursors VALUES(?,?,?)
 ON CONFLICT(scope) DO UPDATE SET generation=excluded.generation, sequence=excluded.sequence`, scope, fixture.Generation, sequence); err != nil {
		return syncPage{}, Unavailable
	}
	return page, nil
}
