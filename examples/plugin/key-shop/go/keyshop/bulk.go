package keyshop

import (
	"fmt"
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
		_, err = tx.Exec(`INSERT INTO keys (id, secret_hash, display_prefix, display_last4, label, customer, note, status, created_at, expires_at, token_limit, request_limit) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, hash, prefix, last4, label, in.Customer, in.Note, "active", now.Format(time.RFC3339Nano), formatTime(expires), limit, requestLimit)
		if err != nil {
			return nil, err
		}
		created = append(created, CreatedKey{
			Secret: secret,
			Key: Key{
				ID: id, Display: Display(prefix, last4), Label: label,
				Customer: in.Customer, Note: in.Note, Status: "active",
				CreatedAt: now, ExpiresAt: expires, TokenLimit: limit, RequestLimit: requestLimit,
			},
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return created, nil
}
