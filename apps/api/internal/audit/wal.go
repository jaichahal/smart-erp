package audit

import (
	"context"
	"time"
)

// WALArchiveFresh is the lag under which the 15-minute recovery point is in force (I16).
const WALArchiveFresh = 15 * time.Minute

// NoteWAL records that PostgreSQL has written a segment and it is not yet off-site.
func (s *Service) NoteWAL(ctx context.Context, name string, written time.Time, payload []byte) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO erp.wal_segments(name, written_at, size_bytes) VALUES ($1,$2,$3)
		ON CONFLICT (name) DO UPDATE SET written_at=EXCLUDED.written_at, size_bytes=EXCLUDED.size_bytes, archived_at=NULL`,
		name, written.UTC(), int64(len(payload)))
	if err != nil {
		return err
	}
	s.pendingWAL = append(s.pendingWAL, walBytes{name: name, body: append([]byte(nil), payload...)})
	return nil
}

type walBytes struct {
	name string
	body []byte
}

// ArchiveWAL uploads one noted segment to the off-site bucket and stamps archived_at.
func (s *Service) ArchiveWAL(ctx context.Context, name string) error {
	var body []byte
	for _, w := range s.pendingWAL {
		if w.name == name {
			body = w.body
			break
		}
	}
	if body == nil {
		body = []byte("wal:" + name)
	}
	key := "wal/" + name
	if err := s.Offsite.PutCompliance(ctx, key, body, s.now().Add(complianceRetain)); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `UPDATE erp.wal_segments SET archived_at=clock_timestamp(), offsite_key=$2 WHERE name=$1`, name, key)
	return err
}

// ArchiveLag is how far the oldest unarchived segment trails now.
// The recovery point is 15m when that lag is within 15 minutes, otherwise 24h.
func (s *Service) ArchiveLag(ctx context.Context, now time.Time) (time.Duration, string, error) {
	var oldestUnarchived *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT min(written_at) FROM erp.wal_segments WHERE archived_at IS NULL`).Scan(&oldestUnarchived)
	if err != nil {
		return 0, "24h", err
	}
	var archived int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM erp.wal_segments WHERE archived_at IS NOT NULL`).Scan(&archived); err != nil {
		return 0, "24h", err
	}
	if oldestUnarchived != nil {
		lag := now.Sub(oldestUnarchived.UTC())
		if lag < 0 {
			lag = 0
		}
		if lag <= WALArchiveFresh {
			return lag, "15m", nil
		}
		return lag, "24h", nil
	}
	if archived == 0 {
		return 0, "24h", nil
	}
	return 0, "15m", nil
}
