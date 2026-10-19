package keyshop

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AllModelsGroupID is the built-in group that allows every model.
const AllModelsGroupID = "all-models"

var (
	ErrGroupRequired = errors.New("model group is required")
	ErrGroupNotFound = errors.New("model group not found")
	ErrGroupSystem   = errors.New("built-in model group cannot be changed")
	ErrGroupInvalid  = errors.New("invalid model group")
)

// GroupInUseError reports a delete blocked by keys that still use the group.
type GroupInUseError struct{ Count int }

func (e *GroupInUseError) Error() string {
	return fmt.Sprintf("model group is used by %d key(s)", e.Count)
}

// GroupModel is one upstream model and the optional name clients call it by.
type GroupModel struct {
	Name  string `json:"name"`
	Alias string `json:"alias"`
}

// Group is a named set of upstream models a customer key may call.
type Group struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	AllModels   bool         `json:"all_models"`
	Models      []GroupModel `json:"models"`
	IsSystem    bool         `json:"is_system"`
	KeyCount    int          `json:"key_count"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

type GroupInput struct {
	Name        string
	Description string
	AllModels   bool
	Models      []GroupModel
}

// ClientName is the name a customer sends. An empty alias means the upstream name.
func (m GroupModel) ClientName() string {
	if alias := strings.TrimSpace(m.Alias); alias != "" {
		return alias
	}
	return strings.TrimSpace(m.Name)
}

// Allows reports whether the group admits the requested model. A match may be
// the client alias or the upstream name. A thinking suffix such as "(high)" is
// ignored, matching the global model policy.
func (g Group) Allows(requested string) bool {
	if g.AllModels {
		return true
	}
	for _, model := range g.Models {
		if modelNamesEqual(requested, model.Name) || (strings.TrimSpace(model.Alias) != "" && modelNamesEqual(requested, model.Alias)) {
			return true
		}
	}
	return false
}

func (s *Store) seedSystemGroup() error {
	now := s.cfg.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`INSERT OR IGNORE INTO model_groups (id, name, description, all_models, models_json, is_system, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)`,
		AllModelsGroupID, "All models", "Every model available on this server.", 1, "[]", 1, now, now)
	return err
}

func normalizeGroupInput(in GroupInput) (GroupInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" || len(in.Name) > 80 {
		return in, fmt.Errorf("%w: name must be 1-80 characters", ErrGroupInvalid)
	}
	models, err := normalizeGroupModels(in.Models)
	if err != nil {
		return in, err
	}
	if in.AllModels {
		models = []GroupModel{}
	} else if len(models) == 0 {
		return in, fmt.Errorf("%w: choose at least one model", ErrGroupInvalid)
	}
	in.Models = models
	return in, nil
}

func normalizeGroupModels(in []GroupModel) ([]GroupModel, error) {
	seenName := make(map[string]string, len(in))
	models := make([]GroupModel, 0, len(in))
	for _, model := range in {
		name := strings.TrimSpace(model.Name)
		alias := strings.TrimSpace(model.Alias)
		if name == "" {
			continue
		}
		if strings.EqualFold(alias, name) {
			alias = ""
		}
		key := strings.ToLower(name)
		if prev, dup := seenName[key]; dup {
			if prev != strings.ToLower(alias) {
				return nil, fmt.Errorf("%w: names and aliases must be unique within the group", ErrGroupInvalid)
			}
			continue
		}
		seenName[key] = strings.ToLower(alias)
		models = append(models, GroupModel{Name: name, Alias: alias})
	}
	used := make(map[string]struct{}, len(models)*2)
	for _, model := range models {
		used[strings.ToLower(model.Name)] = struct{}{}
	}
	for _, model := range models {
		if model.Alias == "" {
			continue
		}
		key := strings.ToLower(model.Alias)
		if _, clash := used[key]; clash {
			return nil, fmt.Errorf("%w: names and aliases must be unique within the group", ErrGroupInvalid)
		}
		used[key] = struct{}{}
	}
	return models, nil
}

// StripPrefixAliases maps each upstream name to an alias with a leading prefix
// removed. A name that does not start with prefix is left unaliased. An alias
// that would collide with another name or alias in the result is dropped so the
// group stays valid. The function is pure: the same names always produce the
// same alias map.
func StripPrefixAliases(names []string, prefix string) []GroupModel {
	prefix = strings.TrimSpace(prefix)
	type item struct {
		name  string
		alias string
	}
	raw := make([]item, 0, len(names))
	seenName := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, dup := seenName[key]; dup {
			continue
		}
		seenName[key] = struct{}{}
		alias := ""
		if prefix != "" && len(name) >= len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
			alias = strings.TrimSpace(name[len(prefix):])
			if alias == "" || strings.EqualFold(alias, name) {
				alias = ""
			}
		}
		raw = append(raw, item{name: name, alias: alias})
	}
	used := make(map[string]struct{}, len(raw)*2)
	for _, entry := range raw {
		used[strings.ToLower(entry.name)] = struct{}{}
	}
	out := make([]GroupModel, 0, len(raw))
	for _, entry := range raw {
		alias := entry.alias
		if alias != "" {
			key := strings.ToLower(alias)
			if _, clash := used[key]; clash {
				alias = ""
			} else {
				used[key] = struct{}{}
			}
		}
		out = append(out, GroupModel{Name: entry.name, Alias: alias})
	}
	return out
}

const groupSelect = `SELECT g.id, g.name, g.description, g.all_models, g.models_json, g.aliases_json, g.is_system, g.created_at, g.updated_at,
  (SELECT COUNT(1) FROM keys WHERE keys.group_id = g.id AND keys.deleted_at IS NULL)
FROM model_groups g`

func scanGroup(sc rowScanner) (Group, error) {
	var g Group
	var all, system int
	var raw, aliases, created, updated string
	if err := sc.Scan(&g.ID, &g.Name, &g.Description, &all, &raw, &aliases, &system, &created, &updated, &g.KeyCount); err != nil {
		return Group{}, err
	}
	g.AllModels = all == 1
	g.IsSystem = system == 1
	g.Models = attachAliases(decodeModelNames(raw), aliases)
	g.CreatedAt = parseTime(created)
	g.UpdatedAt = parseTime(updated)
	return g, nil
}

func decodeModelNames(raw string) []GroupModel {
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil || names == nil {
		return []GroupModel{}
	}
	models := make([]GroupModel, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		models = append(models, GroupModel{Name: name})
	}
	return models
}

func attachAliases(models []GroupModel, raw string) []GroupModel {
	if models == nil {
		models = []GroupModel{}
	}
	aliases := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
		return models
	}
	folded := make(map[string]string, len(aliases))
	for name, alias := range aliases {
		folded[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(alias)
	}
	for i := range models {
		if alias := folded[strings.ToLower(models[i].Name)]; alias != "" && !strings.EqualFold(alias, models[i].Name) {
			models[i].Alias = alias
		}
	}
	return models
}

func groupModelColumns(models []GroupModel) (string, string) {
	names := make([]string, 0, len(models))
	aliases := map[string]string{}
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
		if alias := strings.TrimSpace(model.Alias); alias != "" {
			aliases[name] = alias
		}
	}
	rawNames, _ := json.Marshal(names)
	rawAliases, _ := json.Marshal(aliases)
	if rawNames == nil {
		rawNames = []byte("[]")
	}
	if rawAliases == nil {
		rawAliases = []byte("{}")
	}
	return string(rawNames), string(rawAliases)
}

func (s *Store) ListGroups() ([]Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(groupSelect + ` ORDER BY g.is_system DESC, g.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) Group(id string) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.groupOn(s.db, id)
}

func (s *Store) groupOn(q rowQuery, id string) (Group, error) {
	g, err := scanGroup(q.QueryRow(groupSelect+` WHERE g.id=?`, id))
	if err == sql.ErrNoRows {
		return Group{}, ErrGroupNotFound
	}
	return g, err
}

// requireGroupOn returns nil for an empty id (legacy keys) and ErrGroupNotFound for an unknown one.
func (s *Store) requireGroupOn(q rowQuery, id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	var n int
	if err := q.QueryRow(`SELECT COUNT(1) FROM model_groups WHERE id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrGroupNotFound
	}
	return nil
}

func (s *Store) CreateGroup(in GroupInput) (Group, error) {
	in, err := normalizeGroupInput(in)
	if err != nil {
		return Group{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := newID()
	if err != nil {
		return Group{}, err
	}
	rawNames, rawAliases := groupModelColumns(in.Models)
	now := s.cfg.Now().UTC().Format(time.RFC3339Nano)
	all := 0
	if in.AllModels {
		all = 1
	}
	if _, err = s.db.Exec(`INSERT INTO model_groups (id, name, description, all_models, models_json, aliases_json, is_system, created_at, updated_at) VALUES (?,?,?,?,?,?,0,?,?)`,
		id, in.Name, in.Description, all, rawNames, rawAliases, now, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Group{}, fmt.Errorf("%w: name already exists", ErrGroupInvalid)
		}
		return Group{}, err
	}
	return s.groupOn(s.db, id)
}

func (s *Store) UpdateGroup(id string, in GroupInput) (Group, error) {
	in, err := normalizeGroupInput(in)
	if err != nil {
		return Group{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.groupOn(s.db, id)
	if err != nil {
		return Group{}, err
	}
	if current.IsSystem {
		return Group{}, ErrGroupSystem
	}
	rawNames, rawAliases := groupModelColumns(in.Models)
	all := 0
	if in.AllModels {
		all = 1
	}
	if _, err = s.db.Exec(`UPDATE model_groups SET name=?, description=?, all_models=?, models_json=?, aliases_json=?, updated_at=? WHERE id=?`,
		in.Name, in.Description, all, rawNames, rawAliases, s.cfg.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Group{}, fmt.Errorf("%w: name already exists", ErrGroupInvalid)
		}
		return Group{}, err
	}
	return s.groupOn(s.db, id)
}

func (s *Store) DeleteGroup(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.groupOn(s.db, id)
	if err != nil {
		return err
	}
	if current.IsSystem {
		return ErrGroupSystem
	}
	if current.KeyCount > 0 {
		return &GroupInUseError{Count: current.KeyCount}
	}
	_, err = s.db.Exec(`DELETE FROM model_groups WHERE id=?`, id)
	return err
}

// GroupAllows reports whether a key's group admits the model. Keys without a
// group keep the legacy behavior and are not limited here. An unknown group
// fails closed.
func (s *Store) GroupAllows(groupID, model string) bool {
	if strings.TrimSpace(groupID) == "" {
		return true
	}
	g, err := s.Group(groupID)
	if err != nil {
		return false
	}
	return g.Allows(model)
}
