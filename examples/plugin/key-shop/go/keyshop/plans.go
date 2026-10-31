package keyshop

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Plan errors. Partner-facing mint errors that mention plans live in partners.go.
var (
	ErrPlanNotFound = errors.New("plan not found")
	ErrPlanInvalid  = errors.New("invalid plan")
)

// PlanInUseError reports a delete blocked by partner orders that still use the plan.
type PlanInUseError struct{ OrderCount int }

func (e *PlanInUseError) Error() string {
	return fmt.Sprintf("plan is used by %d partner order(s)", e.OrderCount)
}

// PlanGroup is the group summary a plan exposes. Models holds client-visible
// names, matching the plan JSON the partner API returns.
type PlanGroup struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	AllModels bool     `json:"all_models"`
	Models    []string `json:"models"`
}

// Plan is a sale plan: the group, lifetime and limits a minted key gets.
type Plan struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	Revision          int64     `json:"revision"`
	Active            bool      `json:"active"`
	Group             PlanGroup `json:"group"`
	TTLSeconds        int64     `json:"ttl_seconds"`
	NeverExpires      bool      `json:"never_expires"`
	TokenLimit        int64     `json:"token_limit"`
	UnlimitedTokens   bool      `json:"unlimited_tokens"`
	RequestLimit      int64     `json:"request_limit"`
	UnlimitedRequests bool      `json:"unlimited_requests"`
	GroupID           string    `json:"-"`
	CreatedAt         time.Time `json:"-"`
	UpdatedAt         time.Time `json:"-"`
}

// PlanInput replaces every plan field. UpdatePlan bumps the revision only when
// at least one field differs from the current row.
type PlanInput struct {
	Name              string
	Description       string
	GroupID           string
	TTLSeconds        int64
	NeverExpires      bool
	TokenLimit        int64
	UnlimitedTokens   bool
	RequestLimit      int64
	UnlimitedRequests bool
	Active            bool
}

// planSelect reads the plan and its group in one query so listing plans never
// nests queries on the single connection.
const planSelect = `SELECT p.id, p.name, p.description, p.group_id, p.ttl_seconds, p.never_expires, p.token_limit, p.unlimited_tokens, p.request_limit, p.unlimited_requests, p.active, p.revision, p.created_at, p.updated_at,
  g.id, g.name, g.all_models, g.models_json, g.aliases_json
FROM plans p LEFT JOIN model_groups g ON g.id = p.group_id`

func scanPlan(sc rowScanner) (Plan, error) {
	var (
		plan                                              Plan
		never, unlimitedTokens, unlimitedRequests, active int
		created, updated                                  string
		groupID, groupName, modelsJSON, aliasesJSON       sql.NullString
		allModels                                         sql.NullInt64
	)
	err := sc.Scan(&plan.ID, &plan.Name, &plan.Description, &plan.GroupID, &plan.TTLSeconds, &never, &plan.TokenLimit,
		&unlimitedTokens, &plan.RequestLimit, &unlimitedRequests, &active, &plan.Revision, &created, &updated,
		&groupID, &groupName, &allModels, &modelsJSON, &aliasesJSON)
	if err != nil {
		return Plan{}, err
	}
	plan.NeverExpires = never == 1
	plan.UnlimitedTokens = unlimitedTokens == 1
	plan.UnlimitedRequests = unlimitedRequests == 1
	plan.Active = active == 1
	plan.CreatedAt = parseTime(created)
	plan.UpdatedAt = parseTime(updated)
	// A group can be deleted while the plan is inactive, so an empty group
	// summary is valid and keeps the plan readable.
	plan.Group = PlanGroup{ID: plan.GroupID, Models: []string{}}
	if groupID.Valid {
		plan.Group.ID = groupID.String
		plan.Group.Name = groupName.String
		plan.Group.AllModels = allModels.Int64 == 1
		models := attachAliases(decodeModelNames(modelsJSON.String), aliasesJSON.String)
		names := make([]string, 0, len(models))
		for _, model := range models {
			if name := model.ClientName(); name != "" {
				names = append(names, name)
			}
		}
		plan.Group.Models = names
	}
	return plan, nil
}

func (s *Store) planOn(q rowQuery, id string) (Plan, error) {
	plan, err := scanPlan(q.QueryRow(planSelect+` WHERE p.id=?`, id))
	if err == sql.ErrNoRows {
		return Plan{}, ErrPlanNotFound
	}
	return plan, err
}

func normalizePlanInput(in PlanInput) (PlanInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.GroupID = strings.TrimSpace(in.GroupID)
	if in.Name == "" {
		return in, fmt.Errorf("%w: name is required", ErrPlanInvalid)
	}
	if in.GroupID == "" {
		return in, ErrGroupRequired
	}
	if !in.NeverExpires && in.TTLSeconds <= 0 {
		return in, fmt.Errorf("%w: ttl_seconds must be positive unless never_expires", ErrPlanInvalid)
	}
	if in.UnlimitedTokens {
		in.TokenLimit = 0
	} else if in.TokenLimit < 0 {
		return in, fmt.Errorf("%w: token_limit must not be negative", ErrPlanInvalid)
	}
	if in.UnlimitedRequests {
		in.RequestLimit = 0
	} else if in.RequestLimit < 0 {
		return in, fmt.Errorf("%w: request_limit must not be negative", ErrPlanInvalid)
	}
	return in, nil
}

func planWriteError(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Errorf("%w: name already exists", ErrPlanInvalid)
	}
	return err
}

func planSame(current Plan, in PlanInput) bool {
	return current.Name == in.Name &&
		current.Description == in.Description &&
		current.GroupID == in.GroupID &&
		current.TTLSeconds == in.TTLSeconds &&
		current.NeverExpires == in.NeverExpires &&
		current.TokenLimit == in.TokenLimit &&
		current.UnlimitedTokens == in.UnlimitedTokens &&
		current.RequestLimit == in.RequestLimit &&
		current.UnlimitedRequests == in.UnlimitedRequests &&
		current.Active == in.Active
}

// ListPlans returns every plan ordered by name.
func (s *Store) ListPlans() ([]Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(planSelect + ` ORDER BY p.name COLLATE NOCASE, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		plan, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, plan)
	}
	return out, rows.Err()
}

// GetPlan returns one plan, or ErrPlanNotFound.
func (s *Store) GetPlan(id string) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.planOn(s.db, id)
}

// CreatePlan inserts a plan at revision 1.
func (s *Store) CreatePlan(in PlanInput) (Plan, error) {
	in, err := normalizePlanInput(in)
	if err != nil {
		return Plan{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.requireGroupOn(s.db, in.GroupID); err != nil {
		return Plan{}, err
	}
	id, err := newID()
	if err != nil {
		return Plan{}, err
	}
	now := partnerTime(s.cfg.Now())
	_, err = s.db.Exec(`INSERT INTO plans (id, name, description, group_id, ttl_seconds, never_expires, token_limit, unlimited_tokens, request_limit, unlimited_requests, active, revision, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
		id, in.Name, in.Description, in.GroupID, in.TTLSeconds, planFlag(in.NeverExpires), in.TokenLimit, planFlag(in.UnlimitedTokens),
		in.RequestLimit, planFlag(in.UnlimitedRequests), planFlag(in.Active), now, now)
	if err != nil {
		return Plan{}, planWriteError(err)
	}
	return s.planOn(s.db, id)
}

// UpdatePlan replaces every field and bumps the revision only on a real change.
func (s *Store) UpdatePlan(id string, in PlanInput) (Plan, error) {
	in, err := normalizePlanInput(in)
	if err != nil {
		return Plan{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Plan{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.planOn(tx, id)
	if err != nil {
		return Plan{}, err
	}
	if err = s.requireGroupOn(tx, in.GroupID); err != nil {
		return Plan{}, err
	}
	if planSame(current, in) {
		return current, nil
	}
	_, err = tx.Exec(`UPDATE plans SET name=?, description=?, group_id=?, ttl_seconds=?, never_expires=?, token_limit=?, unlimited_tokens=?, request_limit=?, unlimited_requests=?, active=?, revision=revision+1, updated_at=? WHERE id=?`,
		in.Name, in.Description, in.GroupID, in.TTLSeconds, planFlag(in.NeverExpires), in.TokenLimit, planFlag(in.UnlimitedTokens),
		in.RequestLimit, planFlag(in.UnlimitedRequests), planFlag(in.Active), partnerTime(s.cfg.Now()), id)
	if err != nil {
		return Plan{}, planWriteError(err)
	}
	updated, err := s.planOn(tx, id)
	if err != nil {
		return Plan{}, err
	}
	if err = tx.Commit(); err != nil {
		return Plan{}, err
	}
	return updated, nil
}

// DeletePlan removes an unused plan. Orders keep their plan reference forever,
// so any row that uses the plan blocks the delete.
func (s *Store) DeletePlan(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var orders int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM partner_orders WHERE plan_id=?`, id).Scan(&orders); err != nil {
		return err
	}
	if orders > 0 {
		return &PlanInUseError{OrderCount: orders}
	}
	res, err := s.db.Exec(`DELETE FROM plans WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPlanNotFound
	}
	return nil
}

func planFlag(v bool) int {
	if v {
		return 1
	}
	return 0
}
