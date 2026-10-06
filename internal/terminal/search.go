package terminal

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"wiregram/internal/textfmt"
)

func RenderUserSearch(out io.Writer, labels []string, selected int, query, heading, notice string, searching bool) {
	_, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || rows < 8 {
		rows = 24
	}
	limit := max(1, rows-6)
	start := 0
	if selected >= limit {
		start = selected - limit + 1
	}
	end := min(len(labels), start+limit)
	fmt.Fprint(out, "\033[2J\033[HSearch users\n\n")
	fmt.Fprintln(out, heading)
	for index := start; index < end; index++ {
		marker := " "
		if index == selected {
			marker = ">"
		}
		fmt.Fprintf(out, "%s %s\n", marker, textfmt.Plain(labels[index]))
	}
	if len(labels) == 0 {
		fmt.Fprintln(out, "  No matching users")
	}
	if searching {
		fmt.Fprintln(out, "\nSearching…")
	} else if notice != "" {
		fmt.Fprintf(out, "\n%s\n", textfmt.Plain(notice))
	} else {
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "search> %s", textfmt.Plain(query))
}
