package keyshop

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const maxTunnels = 20

var (
	ErrTunnelNotFound  = errors.New("tunnel not found")
	ErrTunnelNameTaken = errors.New("duplicate tunnel name")
	ErrTunnelLimit     = errors.New("tunnel limit reached")
	ErrTunnelInvalid   = errors.New("invalid tunnel")
)

// TunnelRow is one persisted tunnel. Token is stored and never returned by the API.
type TunnelRow struct {
	ID        string
	Name      string
	Kind      string
	TargetURL string
	Token     string
	Autostart bool
	Desired   string
	PID       int
	PublicURL string
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Store) DataDir() string {
	if s == nil {
		return ""
	}
	return filepath.Dir(s.cfg.DataFile)
}

func (s *Store) ListTunnels() ([]TunnelRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, name, kind, target_url, token, autostart, desired, pid, public_url, last_error, created_at, updated_at FROM tunnels ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TunnelRow
	for rows.Next() {
		row, errScan := scanTunnel(rows)
		if errScan != nil {
			return nil, errScan
		}
		out = append(out, row)
	}
	if out == nil {
		out = []TunnelRow{}
	}
	return out, rows.Err()
}

func (s *Store) GetTunnel(id string) (TunnelRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getTunnelLocked(id)
}

func (s *Store) getTunnelLocked(id string) (TunnelRow, error) {
	row := s.db.QueryRow(`SELECT id, name, kind, target_url, token, autostart, desired, pid, public_url, last_error, created_at, updated_at FROM tunnels WHERE id = ?`, id)
	got, err := scanTunnel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TunnelRow{}, ErrTunnelNotFound
	}
	return got, err
}

func (s *Store) CountTunnels() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM tunnels`).Scan(&n)
	return n, err
}

func (s *Store) InsertTunnel(row TunnelRow) (TunnelRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM tunnels`).Scan(&n); err != nil {
		return TunnelRow{}, err
	}
	if n >= maxTunnels {
		return TunnelRow{}, ErrTunnelLimit
	}
	var existing string
	err := s.db.QueryRow(`SELECT id FROM tunnels WHERE name = ? COLLATE NOCASE`, row.Name).Scan(&existing)
	if err == nil {
		return TunnelRow{}, ErrTunnelNameTaken
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TunnelRow{}, err
	}
	if row.ID == "" {
		id, errID := newTunnelID()
		if errID != nil {
			return TunnelRow{}, errID
		}
		row.ID = id
	}
	now := s.cfg.Now().UTC()
	row.CreatedAt = now
	row.UpdatedAt = now
	if row.Desired == "" {
		row.Desired = "stopped"
	}
	_, err = s.db.Exec(`INSERT INTO tunnels (id, name, kind, target_url, token, autostart, desired, pid, public_url, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.Name, row.Kind, row.TargetURL, row.Token, boolInt(row.Autostart), row.Desired, row.PID, row.PublicURL, truncateErr(row.LastError),
		row.CreatedAt.Format(time.RFC3339Nano), row.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return TunnelRow{}, ErrTunnelNameTaken
		}
		return TunnelRow{}, err
	}
	return row, nil
}

// UpdateTunnel replaces editable columns. Kind is not changed.
func (s *Store) UpdateTunnel(row TunnelRow) (TunnelRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.getTunnelLocked(row.ID)
	if err != nil {
		return TunnelRow{}, err
	}
	var other string
	err = s.db.QueryRow(`SELECT id FROM tunnels WHERE name = ? COLLATE NOCASE AND id <> ?`, row.Name, row.ID).Scan(&other)
	if err == nil {
		return TunnelRow{}, ErrTunnelNameTaken
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TunnelRow{}, err
	}
	row.Kind = cur.Kind
	row.CreatedAt = cur.CreatedAt
	row.UpdatedAt = s.cfg.Now().UTC()
	_, err = s.db.Exec(`UPDATE tunnels SET name=?, target_url=?, token=?, autostart=?, desired=?, pid=?, public_url=?, last_error=?, updated_at=? WHERE id=?`,
		row.Name, row.TargetURL, row.Token, boolInt(row.Autostart), row.Desired, row.PID, row.PublicURL, truncateErr(row.LastError),
		row.UpdatedAt.Format(time.RFC3339Nano), row.ID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return TunnelRow{}, ErrTunnelNameTaken
		}
		return TunnelRow{}, err
	}
	return row, nil
}

func (s *Store) DeleteTunnel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM tunnels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTunnelNotFound
	}
	return nil
}

// SetTunnelRuntime writes pid, desired, public_url and last_error without touching other columns.
func (s *Store) SetTunnelRuntime(id, desired string, pid int, publicURL, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE tunnels SET desired=?, pid=?, public_url=?, last_error=?, updated_at=? WHERE id=?`,
		desired, pid, publicURL, truncateErr(lastError), s.cfg.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTunnelNotFound
	}
	return nil
}

func newTunnelID() (string, error) {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "tn_" + hex.EncodeToString(buf[:]), nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func truncateErr(s string) string {
	if len(s) <= 300 {
		return s
	}
	return s[:300]
}

type tunnelScanner interface {
	Scan(dest ...any) error
}

func scanTunnel(sc tunnelScanner) (TunnelRow, error) {
	var row TunnelRow
	var autostart int
	var created, updated string
	err := sc.Scan(&row.ID, &row.Name, &row.Kind, &row.TargetURL, &row.Token, &autostart, &row.Desired, &row.PID, &row.PublicURL, &row.LastError, &created, &updated)
	if err != nil {
		return TunnelRow{}, err
	}
	row.Autostart = autostart != 0
	row.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		row.CreatedAt, _ = time.Parse(time.RFC3339, created)
	}
	row.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		row.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	}
	return row, nil
}

func (s *Store) tunnelsTableExists() error {
	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='tunnels'`).Scan(&name)
	if err != nil {
		return fmt.Errorf("tunnels table: %w", err)
	}
	return nil
}
