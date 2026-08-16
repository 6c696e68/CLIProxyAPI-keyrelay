package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type mappingTabModel struct {
	client   *Client
	viewport viewport.Model
	rules    []map[string]any
	err      error
	width    int
	height   int
	ready    bool
	cursor   int
	confirm  int
	status   string

	editing     bool
	adding      bool
	editField   int // 0=match, 1=target, 2=mode
	draftMatch  string
	draftTarget string
	draftMode   string
	editInput   textinput.Model
}

type mappingDataMsg struct {
	rules []map[string]any
	err   error
}

type mappingActionMsg struct {
	action string
	err    error
}

func newMappingTabModel(client *Client) mappingTabModel {
	ti := textinput.New()
	ti.CharLimit = 256
	return mappingTabModel{
		client:    client,
		confirm:   -1,
		editInput: ti,
	}
}

func (m mappingTabModel) Init() tea.Cmd {
	return m.fetchRules
}

func (m mappingTabModel) fetchRules() tea.Msg {
	rules, err := m.client.GetModelMapping()
	return mappingDataMsg{rules: rules, err: err}
}

func (m mappingTabModel) Update(msg tea.Msg) (mappingTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case mappingDataMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.rules = msg.rules
			if m.cursor >= len(m.rules) {
				m.cursor = max(0, len(m.rules)-1)
			}
		}
		m.viewport.SetContent(m.renderContent())
		return m, nil
	case mappingActionMsg:
		if msg.err != nil {
			m.status = errorStyle.Render("✗ " + msg.err.Error())
		} else {
			m.status = successStyle.Render("✓ " + msg.action)
		}
		m.confirm = -1
		m.viewport.SetContent(m.renderContent())
		return m, m.fetchRules
	case tea.KeyMsg:
		if m.editing || m.adding {
			return m.updateEditor(msg)
		}
		if m.confirm >= 0 {
			switch msg.String() {
			case "y", "Y":
				idx := m.confirm
				m.confirm = -1
				return m, m.saveRules(func(rules []map[string]any) []map[string]any {
					if idx < 0 || idx >= len(rules) {
						return rules
					}
					return append(rules[:idx], rules[idx+1:]...)
				}, T("mapping_deleted"))
			case "n", "N", "esc":
				m.confirm = -1
				m.viewport.SetContent(m.renderContent())
				return m, nil
			}
			return m, nil
		}
		switch msg.String() {
		case "j", "down":
			if len(m.rules) > 0 {
				m.cursor = (m.cursor + 1) % len(m.rules)
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		case "k", "up":
			if len(m.rules) > 0 {
				m.cursor = (m.cursor - 1 + len(m.rules)) % len(m.rules)
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		case "a":
			m.adding = true
			m.editing = false
			m.editField = 0
			m.draftMatch = ""
			m.draftTarget = ""
			m.draftMode = "exact"
			m.editInput.SetValue("")
			m.editInput.Prompt = T("mapping_match_prompt")
			m.editInput.Focus()
			m.viewport.SetContent(m.renderContent())
			return m, textinput.Blink
		case "e":
			if m.cursor >= len(m.rules) {
				return m, nil
			}
			rule := m.rules[m.cursor]
			m.editing = true
			m.adding = false
			m.editField = 0
			m.draftMatch = mappingString(rule, "match")
			m.draftTarget = mappingString(rule, "target")
			m.draftMode = mappingMode(rule)
			m.editInput.SetValue(m.draftMatch)
			m.editInput.Prompt = T("mapping_match_prompt")
			m.editInput.Focus()
			m.viewport.SetContent(m.renderContent())
			return m, textinput.Blink
		case "d":
			if m.cursor < len(m.rules) {
				m.confirm = m.cursor
				m.viewport.SetContent(m.renderContent())
			}
			return m, nil
		case " ", "space":
			if m.cursor >= len(m.rules) {
				return m, nil
			}
			idx := m.cursor
			return m, m.saveRules(func(rules []map[string]any) []map[string]any {
				if idx < 0 || idx >= len(rules) {
					return rules
				}
				enabled := mappingBool(rules[idx], "enabled", true)
				rules[idx]["enabled"] = !enabled
				return rules
			}, T("mapping_toggled"))
		case "f":
			if m.cursor >= len(m.rules) {
				return m, nil
			}
			idx := m.cursor
			return m, m.saveRules(func(rules []map[string]any) []map[string]any {
				if idx < 0 || idx >= len(rules) {
					return rules
				}
				force := mappingBool(rules[idx], "force-mapping", true)
				rules[idx]["force-mapping"] = !force
				return rules
			}, T("mapping_force_toggled"))
		case "r":
			m.status = ""
			return m, m.fetchRules
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m mappingTabModel) updateEditor(msg tea.KeyMsg) (mappingTabModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		value := strings.TrimSpace(m.editInput.Value())
		switch m.editField {
		case 0:
			if value == "" {
				m.status = errorStyle.Render(T("mapping_match_required"))
				m.viewport.SetContent(m.renderContent())
				return m, nil
			}
			m.draftMatch = value
			m.editField = 1
			m.editInput.SetValue(m.draftTarget)
			m.editInput.Prompt = T("mapping_target_prompt")
			m.viewport.SetContent(m.renderContent())
			return m, textinput.Blink
		case 1:
			if value == "" {
				m.status = errorStyle.Render(T("mapping_target_required"))
				m.viewport.SetContent(m.renderContent())
				return m, nil
			}
			m.draftTarget = value
			m.editField = 2
			m.editInput.SetValue(m.draftMode)
			m.editInput.Prompt = T("mapping_mode_prompt")
			m.viewport.SetContent(m.renderContent())
			return m, textinput.Blink
		default:
			mode := strings.ToLower(strings.TrimSpace(value))
			if mode == "" {
				mode = inferMappingMode(m.draftMatch)
			}
			if mode != "exact" && mode != "wildcard" && mode != "regex" {
				m.status = errorStyle.Render(T("mapping_invalid_mode"))
				m.viewport.SetContent(m.renderContent())
				return m, nil
			}
			rule := map[string]any{
				"match":         m.draftMatch,
				"target":        m.draftTarget,
				"mode":          mode,
				"enabled":       true,
				"force-mapping": true,
			}
			isAdding := m.adding
			idx := m.cursor
			m.adding = false
			m.editing = false
			m.editInput.Blur()
			return m, m.saveRules(func(rules []map[string]any) []map[string]any {
				if isAdding {
					return append(rules, rule)
				}
				if idx >= 0 && idx < len(rules) {
					if enabled, ok := rules[idx]["enabled"]; ok {
						rule["enabled"] = enabled
					}
					if force, ok := rules[idx]["force-mapping"]; ok {
						rule["force-mapping"] = force
					}
					rules[idx] = rule
				}
				return rules
			}, map[bool]string{true: T("mapping_added"), false: T("mapping_updated")}[isAdding])
		}
	case "esc":
		m.adding = false
		m.editing = false
		m.editInput.Blur()
		m.viewport.SetContent(m.renderContent())
		return m, nil
	default:
		var cmd tea.Cmd
		m.editInput, cmd = m.editInput.Update(msg)
		m.viewport.SetContent(m.renderContent())
		return m, cmd
	}
}

func (m mappingTabModel) saveRules(mutate func([]map[string]any) []map[string]any, action string) tea.Cmd {
	current := cloneMappingRules(m.rules)
	next := mutate(current)
	return func() tea.Msg {
		if err := m.client.PutModelMapping(next); err != nil {
			return mappingActionMsg{err: err}
		}
		return mappingActionMsg{action: action}
	}
}

func (m *mappingTabModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	m.editInput.Width = max(8, w-16)
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.viewport.SetContent(m.renderContent())
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
}

func (m mappingTabModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.viewport.View()
}

func (m mappingTabModel) renderContent() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(T("mapping_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(T("mapping_help")))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", max(1, m.width)))
	sb.WriteString("\n")
	if m.status != "" {
		sb.WriteString(m.status)
		sb.WriteString("\n")
	}
	if m.err != nil {
		sb.WriteString(errorStyle.Render(T("error_prefix") + m.err.Error()))
		sb.WriteString("\n")
		return sb.String()
	}
	if len(m.rules) == 0 && !m.adding {
		sb.WriteString(subtitleStyle.Render(T("mapping_empty")))
		sb.WriteString("\n")
	}
	for i, rule := range m.rules {
		cursor := "  "
		rowStyle := lipgloss.NewStyle()
		if i == m.cursor {
			cursor = "▸ "
			rowStyle = lipgloss.NewStyle().Bold(true)
		}
		enabled := mappingBool(rule, "enabled", true)
		force := mappingBool(rule, "force-mapping", true)
		state := T("mapping_on")
		if !enabled {
			state = T("mapping_off")
		}
		forceLabel := T("mapping_force_on")
		if !force {
			forceLabel = T("mapping_force_off")
		}
		row := fmt.Sprintf("%s%d. %s → %s  [%s] [%s] [%s]",
			cursor,
			i+1,
			mappingString(rule, "match"),
			mappingString(rule, "target"),
			mappingMode(rule),
			state,
			forceLabel,
		)
		sb.WriteString(rowStyle.Render(row))
		sb.WriteString("\n")
		if m.confirm == i {
			sb.WriteString(warningStyle.Render("    " + T("mapping_confirm_delete")))
			sb.WriteString("\n")
		}
	}
	if m.adding || m.editing {
		sb.WriteString("\n")
		sb.WriteString(m.editInput.View())
		sb.WriteString("\n")
		sb.WriteString(helpStyle.Render(T("enter_save_esc")))
		sb.WriteString("\n")
	}
	return sb.String()
}

func mappingString(rule map[string]any, key string) string {
	if rule == nil {
		return ""
	}
	value, _ := rule[key].(string)
	return strings.TrimSpace(value)
}

func mappingMode(rule map[string]any) string {
	mode := strings.ToLower(mappingString(rule, "mode"))
	if mode == "" {
		return inferMappingMode(mappingString(rule, "match"))
	}
	return mode
}

func mappingBool(rule map[string]any, key string, fallback bool) bool {
	if rule == nil {
		return fallback
	}
	value, ok := rule[key]
	if !ok || value == nil {
		return fallback
	}
	b, ok := value.(bool)
	if !ok {
		return fallback
	}
	return b
}

func inferMappingMode(match string) string {
	if strings.Contains(match, "*") {
		return "wildcard"
	}
	return "exact"
}

func cloneMappingRules(rules []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		copied := make(map[string]any, len(rule))
		for k, v := range rule {
			copied[k] = v
		}
		out = append(out, copied)
	}
	return out
}
