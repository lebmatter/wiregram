package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"wiregram/internal/plugin"
	"wiregram/internal/textfmt"
)

// pluginRow is one row of Settings' Plugins pane: a plugin's on/off switch
// (setting -1), or one of its settings, listed only while it is on.
type pluginRow struct {
	p       *plugin.Plugin
	setting int
}

func (m settingsModel) pluginRows() []pluginRow {
	if m.mgr.Plugins == nil {
		return nil
	}
	var rows []pluginRow
	for _, p := range m.mgr.Plugins.Plugins() {
		rows = append(rows, pluginRow{p: p, setting: -1})
		if p.Enabled() {
			for i := range p.Settings {
				rows = append(rows, pluginRow{p: p, setting: i})
			}
		}
	}
	return rows
}

// showPlugin opens the Plugins pane focused on the named plugin's row.
func (m settingsModel) showPlugin(name string) settingsModel {
	m.left, m.focus = len(m.accounts)+actionPlugins, paneItems
	for i, r := range m.pluginRows() {
		if r.p.Name == name && r.setting < 0 {
			m.sel = i
		}
	}
	return m
}

func (m settingsModel) pluginsKey(k string) (tea.Model, tea.Cmd) {
	rows := m.pluginRows()
	switch k {
	case "esc", "ctrl+c", "tab", "left", "h":
		m.focus = paneAccounts
	case "up", "k":
		m.sel = max(0, m.sel-1)
	case "down", "j":
		m.sel = min(len(rows)-1, m.sel+1)
	case "enter", "space", " ", "right", "l":
		if m.sel < len(rows) {
			return m.applyPlugin(rows[m.sel])
		}
	}
	return m, nil
}

// applyPlugin switches a plugin on or off, moves a choice to its next option,
// or starts typing a text setting.
func (m settingsModel) applyPlugin(r pluginRow) (tea.Model, tea.Cmd) {
	p := r.p
	if r.setting < 0 {
		on := !p.Enabled()
		if err := p.SetEnabled(on); err != nil {
			m.notice = textfmt.CleanError(err)
			return m, nil
		}
		m.notice = p.Title + " off"
		if on {
			m.notice = p.Title + " on"
		}
		return m, nil
	}
	s := p.Settings[r.setting]
	if s.Kind == "choice" {
		next := s.Options[0]
		for i, o := range s.Options {
			if o == p.Value(s.Key) {
				next = s.Options[(i+1)%len(s.Options)]
			}
		}
		m.notice = p.Title + ": " + s.Label + " " + next
		if err := p.SetValue(s.Key, next); err != nil {
			m.notice = textfmt.CleanError(err)
		}
		return m, nil
	}
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 1000
	if s.Secret {
		in.EchoMode = textinput.EchoPassword
	}
	in.SetValue(p.Value(s.Key))
	m.edit, m.editing = in, true
	return m, m.edit.Focus()
}

// editKey types into a plugin's or provider's text setting; Enter saves it and
// Esc leaves it as it was.
func (m settingsModel) editKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = false
	case "enter":
		m.editing = false
		if m.onProviders() {
			return m.saveProviderField(strings.TrimSpace(textfmt.Plain(m.edit.Value())))
		}
		rows := m.pluginRows()
		if m.sel >= len(rows) || rows[m.sel].setting < 0 {
			return m, nil
		}
		r := rows[m.sel]
		s := r.p.Settings[r.setting]
		v := strings.TrimSpace(textfmt.Plain(m.edit.Value()))
		m.notice = r.p.Title + ": " + s.Label + " " + v
		if s.Secret {
			m.notice = r.p.Title + ": " + s.Label + " saved"
		}
		if err := r.p.SetValue(s.Key, v); err != nil {
			m.notice = textfmt.CleanError(err)
		}
	default:
		var cmd tea.Cmd
		m.edit, cmd = m.edit.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m settingsModel) pluginLines(width, height int) []string {
	rows := m.pluginRows()
	if len(rows) == 0 {
		return []string{m.st.dim.Render("No plugins. Put .lua files in ~/.config/wiregram/plugins/.")}
	}
	focused := m.focus == paneItems
	labelW := 0
	for _, r := range rows {
		label := r.p.Title
		if r.setting >= 0 {
			label = "  " + r.p.Settings[r.setting].Label
		}
		labelW = max(labelW, textfmt.DisplayWidth(label))
	}
	var lines []string
	for i := window(m.sel, height); i < len(rows) && len(lines) < height; i++ {
		r := rows[i]
		selected := i == m.sel
		if r.setting < 0 {
			value := "off"
			if r.p.Enabled() {
				value = "on"
			}
			value = m.st.subtle.Render(value)
			if !r.p.HasWidget() {
				value += m.st.dim.Render(" (no widget)")
			}
			if r.p.ReadsMessages() {
				value += m.st.dim.Render(" · reads messages")
			}
			if r.p.UsesAITasks() {
				value += m.st.dim.Render(" · uses AI tasks")
			}
			lines = append(lines, m.st.formRow(r.p.Title, value, labelW, selected,
				focused, width))
			continue
		}
		s := r.p.Settings[r.setting]
		shown := r.p.Value(s.Key)
		if s.Secret && shown != "" {
			shown = "••••••"
		}
		label, value := "  "+s.Label, m.st.subtle.Render(shown)
		if selected && m.editing {
			in := m.edit
			in.SetWidth(max(8, width-labelW-5))
			value = in.View()
		}
		lines = append(lines, m.st.formRow(label, value, labelW, selected, focused, width))
	}
	return lines
}
