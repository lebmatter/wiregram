package tui

import (
	"context"
	"slices"
	"strings"
	"time"

	"charm.land/bubbletea/v2"

	"wiregram/internal/plugin"
	"wiregram/internal/textfmt"
)

const (
	widgetMinCardW = 44
	widgetFrame    = 250 * time.Millisecond
)

type widgetTick struct{}

type widgetUpdated struct {
	p   *plugin.Plugin
	err error
}

type widgetClicked struct {
	p     *plugin.Plugin
	modal *plugin.Modal
	err   error
}

// widgetModal is the overlay a widget's click opened, with its last frame.
type widgetModal struct {
	p     *plugin.Plugin
	m     *plugin.Modal
	lines []string
	err   error
}

type widgetState struct {
	lines     []string
	renderErr error
	updateErr error
	clickErr  error
	updating  bool
	due       time.Time
	changes   uint64 // the plugin's setting changes the last update saw
}

// widgetsModel is the account chooser's plugin widgets: one card per enabled
// plugin that draws one, redrawn every frame and updated in the background.
type widgetsModel struct {
	st     *styles
	ctx    context.Context
	mgr    *plugin.Manager
	frame  int
	states map[*plugin.Plugin]*widgetState
	modal  *widgetModal
}

func newWidgets(ctx context.Context, st *styles, mgr *plugin.Manager) widgetsModel {
	return widgetsModel{st: st, ctx: ctx, mgr: mgr, states: make(map[*plugin.Plugin]*widgetState)}
}

func (m widgetsModel) order() []*plugin.Plugin {
	if m.mgr == nil {
		return nil
	}
	var out []*plugin.Plugin
	for _, p := range m.mgr.Plugins() {
		if p.HasWidget() && p.Enabled() {
			out = append(out, p)
		}
	}
	return out
}

func widgetTickCmd() tea.Cmd {
	return tea.Tick(widgetFrame, func(time.Time) tea.Msg { return widgetTick{} })
}

// start draws the first frame; with no widgets there is nothing to tick.
func (m widgetsModel) start(width, screenW, screenH int) (widgetsModel, tea.Cmd) {
	if len(m.order()) == 0 {
		return m, nil
	}
	return m.tick(width, screenW, screenH)
}

// tick draws the next frame of every widget at this width, and of the open
// modal on a screen this size, and starts the updates that are due: on first
// show, after the plugin's refresh, or when one of its settings changed.
func (m widgetsModel) tick(width, screenW, screenH int) (widgetsModel, tea.Cmd) {
	m.frame++
	cmds := []tea.Cmd{widgetTickCmd()}
	widgets := m.order()
	cols := m.columns(width)
	for i, p := range widgets {
		s, ok := m.states[p]
		if !ok {
			s = &widgetState{}
			m.states[p] = s
		}
		if !s.updating && (time.Now().After(s.due) || p.Changes() != s.changes) {
			s.updating, s.changes = true, p.Changes()
			cmds = append(cmds, func() tea.Msg { return widgetUpdated{p: p, err: p.Update(m.ctx)} })
		}
		if lines, ok, err := p.Render(cardWidth(width, cols, i%cols)-4, m.frame); ok {
			s.lines, s.renderErr = lines, err
		}
	}
	if md := m.modal; md != nil {
		w, h := md.size(screenW, screenH)
		if lines, ok, err := md.p.RenderModal(md.m, w, h, m.frame); ok {
			md.lines, md.err = lines, err
		}
	}
	return m, tea.Batch(cmds...)
}

// click runs the clicked widget's handler in the background, since it may
// wait for an update to finish.
func (m widgetsModel) click(p *plugin.Plugin) tea.Cmd {
	if !p.Clickable() {
		return nil
	}
	return func() tea.Msg {
		modal, err := p.Click(m.ctx)
		return widgetClicked{p: p, modal: modal, err: err}
	}
}

func (m widgetsModel) clicked(msg widgetClicked) widgetsModel {
	if s, ok := m.states[msg.p]; ok {
		s.clickErr = msg.err
	}
	if msg.modal != nil {
		m.modal = &widgetModal{p: msg.p, m: msg.modal}
	}
	return m
}

// size is the cells inside the modal's border: what the plugin asked for,
// cut to a screen this size.
func (md *widgetModal) size(screenW, screenH int) (w, h int) {
	return max(1, min(md.m.Width, screenW-6)), max(1, min(md.m.Height, screenH-2))
}

// box draws the modal with its last frame.
func (md *widgetModal) box(st *styles, screenW, screenH int) string {
	w, h := md.size(screenW, screenH)
	lines := md.lines
	if lines == nil && md.err == nil {
		lines = []string{st.subtle.Render("Loading…")}
	}
	if md.err != nil {
		lines = append(slices.Clone(lines[:min(len(lines), h-1)]),
			styleError.Render(textfmt.CleanError(md.err)))
	}
	return renderBox(md.m.Title, lines, w+4, h+2, st.accentColor)
}

// widgetAt maps a click at x, y inside the widgets to its plugin.
func (m widgetsModel) widgetAt(x, y, width int) (*plugin.Plugin, bool) {
	if x < 0 || y < 0 || width < 1 {
		return nil, false
	}
	rows, heights := m.rows(width)
	cols := m.columns(width)
	col := min(cols-1, x/(width/cols))
	for i, row := range rows {
		if y < heights[i] {
			if col < len(row) {
				return row[col], true
			}
			return nil, false
		}
		y -= heights[i]
	}
	return nil, false
}

func (m widgetsModel) updated(msg widgetUpdated) widgetsModel {
	if s, ok := m.states[msg.p]; ok {
		s.updating, s.updateErr = false, msg.err
		s.due = time.Now().Add(msg.p.Refresh())
	}
	return m
}

func (m widgetsModel) columns(width int) int { return max(1, width/widgetMinCardW) }

// rows splits the widgets into rows of cards, each row as tall as its tallest card.
func (m widgetsModel) rows(width int) (rows [][]*plugin.Plugin, heights []int) {
	widgets := m.order()
	cols := m.columns(width)
	for start := 0; start < len(widgets); start += cols {
		row := widgets[start:min(start+cols, len(widgets))]
		h := 0
		for _, p := range row {
			h = max(h, p.WidgetHeight()+2)
		}
		rows, heights = append(rows, row), append(heights, h)
	}
	return rows, heights
}

// height is the rows the widgets need to show every card at this width.
func (m widgetsModel) height(width int) int {
	_, heights := m.rows(width)
	total := 0
	for _, h := range heights {
		total += h
	}
	return total
}

// view tiles the cards in a grid, dropping the rows that don't fit.
func (m widgetsModel) view(width, height int) string {
	rows, heights := m.rows(width)
	cols := m.columns(width)
	var out []string
	used := 0
	for i, row := range rows {
		if used+heights[i] > height {
			break
		}
		used += heights[i]
		line := ""
		for col, p := range row {
			card := m.card(p, cardWidth(width, cols, col), heights[i])
			if line == "" {
				line = card
			} else {
				line = joinColumns(line, card)
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (m widgetsModel) card(p *plugin.Plugin, width, height int) string {
	s := m.states[p]
	var lines []string
	err := error(nil)
	if s != nil {
		lines, err = s.lines, s.renderErr
		if err == nil {
			err = s.updateErr
		}
		if err == nil {
			err = s.clickErr
		}
	}
	if lines == nil && err == nil {
		lines = []string{m.st.subtle.Render("Loading…")}
	}
	if err != nil {
		lines = append(slices.Clone(lines[:min(len(lines), height-3)]), styleError.Render(textfmt.CleanError(err)))
	}
	return renderBox(p.Title, lines, width, height, m.st.dimColor)
}
