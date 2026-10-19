package keyshop

import (
	"fmt"
	"strings"
	"time"
)

type CreatedKey struct {
	Key    Key
	Secret string
}

func (s *Store) CreateBatch(in CreateInput, count int) ([]CreatedKey, error) {
	if count < 1 || count > 500 {
		return nil, fmt.Errorf("count must be between 1 and 500")
	}
	if in.TokenLimit < 0 || in.TTLSeconds < 0 || in.TTLSeconds > int64((1<<63-1)/time.Second) {
		return nil, fmt.Errorf("invalid token limit or lifetime")
	}
	requestLimit, err := s.requestLimit(in.UnlimitedRequests, in.RequestLimitSet, in.RequestLimit)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.requireGroupOn(s.db, in.GroupID); err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	created := make([]CreatedKey, 0, count)
	for range count {
		secret, hash, prefix, last4, err := NewSecret()
		if err != nil {
			return nil, err
		}
		id, err := newID()
		if err != nil {
			return nil, err
		}
		label := batchLabel(in.Label, id)
		now := s.cfg.Now().UTC()
		expires := s.expiry(in.NeverExpires, in.TTLSeconds, true, now)
		limit := s.limit(in.UnlimitedTokens, in.TokenLimitSet, in.TokenLimit)
		_, err = tx.Exec(`INSERT INTO keys (id, secret_hash, display_prefix, display_last4, label, customer, note, status, created_at, expires_at, token_limit, request_limit, group_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, hash, prefix, last4, label, in.Customer, in.Note, "active", now.Format(time.RFC3339Nano), formatTime(expires), limit, requestLimit, in.GroupID)
		if err != nil {
			return nil, err
		}
		created = append(created, CreatedKey{
			Secret: secret,
			Key: Key{
				ID: id, Display: Display(prefix, last4), Label: label,
				Customer: in.Customer, Note: in.Note, Status: "active",
				CreatedAt: now, ExpiresAt: expires, TokenLimit: limit, RequestLimit: requestLimit, GroupID: in.GroupID,
			},
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return created, nil
}

// MaxBulkKeys caps one bulk update or delete.
const MaxBulkKeys = 500

// BulkUpdateInput changes the same fields on many keys. Only fields whose *Set
// flag is true change; everything else is left alone.
type BulkUpdateInput struct {
	IDs                            []string
	GroupID                        string
	GroupSet                       bool
	Status                         string
	StatusSet                      bool
	Customer, Note                 string
	CustomerSet, NoteSet           bool
	TTLSeconds                     int64
	TTLSet, NeverExpires           bool
	TokenLimit                     int64
	TokenLimitSet, UnlimitedTokens bool
	RequestLimit                   int64
	RequestLimitSet                bool
	UnlimitedRequests              bool
}

func uniqueIDs(ids []string) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 || len(out) > MaxBulkKeys {
		return nil, fmt.Errorf("choose between 1 and %d keys", MaxBulkKeys)
	}
	return out, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func idArgs(ids []string) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// BulkUpdate applies one change set to every listed key in a single transaction.
// An unknown or deleted id aborts the whole call without changing anything.
func (s *Store) BulkUpdate(in BulkUpdateInput) (int, error) {
	ids, err := uniqueIDs(in.IDs)
	if err != nil {
		return 0, err
	}
	var sets []string
	var args []any
	if in.StatusSet {
		if in.Status != "active" && in.Status != "disabled" {
			return 0, fmt.Errorf("invalid status")
		}
		sets, args = append(sets, "status=?"), append(args, in.Status)
	}
	if in.CustomerSet {
		sets, args = append(sets, "customer=?"), append(args, in.Customer)
	}
	if in.NoteSet {
		sets, args = append(sets, "note=?"), append(args, in.Note)
	}
	if in.NeverExpires {
		sets = append(sets, "expires_at=NULL")
	} else if in.TTLSet {
		if in.TTLSeconds < 0 || in.TTLSeconds > int64((1<<63-1)/time.Second) {
			return 0, fmt.Errorf("invalid lifetime")
		}
		sets, args = append(sets, "expires_at=?"), append(args, formatTime(s.expiry(false, in.TTLSeconds, true, s.cfg.Now().UTC())))
	}
	if in.UnlimitedTokens {
		sets, args = append(sets, "token_limit=?"), append(args, 0)
	} else if in.TokenLimitSet {
		if in.TokenLimit < 0 {
			return 0, fmt.Errorf("invalid token limit")
		}
		sets, args = append(sets, "token_limit=?"), append(args, in.TokenLimit)
	}
	if in.UnlimitedRequests {
		sets, args = append(sets, "request_limit=?"), append(args, 0)
	} else if in.RequestLimitSet {
		if in.RequestLimit < 0 {
			return 0, fmt.Errorf("invalid request limit")
		}
		sets, args = append(sets, "request_limit=?"), append(args, in.RequestLimit)
	}
	if in.GroupSet {
		if strings.TrimSpace(in.GroupID) == "" {
			return 0, ErrGroupRequired
		}
		sets, args = append(sets, "group_id=?"), append(args, in.GroupID)
	}
	if len(sets) == 0 {
		return 0, fmt.Errorf("no changes requested")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if in.GroupSet {
		if err = s.requireGroupOn(tx, in.GroupID); err != nil {
			return 0, err
		}
	}
	if err = requireLiveKeys(tx, ids); err != nil {
		return 0, err
	}
	args = append(args, idArgs(ids)...)
	res, err := tx.Exec(`UPDATE keys SET `+strings.Join(sets, ", ")+` WHERE deleted_at IS NULL AND id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

// BulkDelete soft-deletes every listed key in a single transaction.
func (s *Store) BulkDelete(idsIn []string) (int, error) {
	ids, err := uniqueIDs(idsIn)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = requireLiveKeys(tx, ids); err != nil {
		return 0, err
	}
	args := append([]any{s.cfg.Now().UTC().Format(time.RFC3339Nano)}, idArgs(ids)...)
	res, err := tx.Exec(`UPDATE keys SET deleted_at=? WHERE deleted_at IS NULL AND id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

// ErrKeysNotFound reports a bulk call that named an unknown or deleted key.
var ErrKeysNotFound = fmt.Errorf("one or more keys were not found")

func requireLiveKeys(q rowQuery, ids []string) error {
	var n int
	if err := q.QueryRow(`SELECT COUNT(1) FROM keys WHERE deleted_at IS NULL AND id IN (`+placeholders(len(ids))+`)`, idArgs(ids)...).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return ErrKeysNotFound
	}
	return nil
}
