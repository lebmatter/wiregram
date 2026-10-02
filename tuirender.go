package main

import (
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"
)

func termSize() (width, height int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 4 || h < 4 {
		return 80, 24
	}
	return w, h
}

var (
	styleTitle   = lipgloss.NewStyle().Bold(true)
	styleSubtle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleUnread  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleHelpBar = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

func renderHelpBar(keys []helpKey, width int) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, styleSubtle.Render(k.key)+" "+k.desc)
	}
	line := strings.Join(parts, "  ")
	return styleHelpBar.Width(width).Render(line)
}

type helpKey struct {
	key  string
	desc string
}

func chatListLine(entry chatEntry, i int, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = "> "
	}
	unread := ""
	if entry.Unread > 0 {
		unread = styleUnread.Render(fmt.Sprintf(" - %d*", entry.Unread))
	}
	title := plain(entry.Title)
	line := fmt.Sprintf("%s%2d  %s%s", marker, i+1, title, unread)
	return clipLine(line, width)
}
