package ui

import (
	"fmt"
	"strings"
)

// Success formats a green checkmark success message.
func Success(msg string) string {
	return Green("✓ " + msg)
}

// Warning formats a yellow warning message.
func Warning(msg string) string {
	return Yellow("⚠ " + msg)
}

// FormatError formats a red error with optional detail and suggestion.
func FormatError(brief string, detail string, suggestion string) string {
	var b strings.Builder
	fmt.Fprintln(&b, Red("✗ Error: "+brief))
	if detail != "" {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, detail)
	}
	if suggestion != "" {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "Suggestion: "+suggestion)
	}
	return b.String()
}

// Step formats a numbered pipeline step header: "Step 1/3  Title".
func Step(current, total int, title string) string {
	return fmt.Sprintf("%s  %s", Dim(fmt.Sprintf("Step %d/%d", current, total)), Bold(title))
}

// Label pads and bolds a label for aligned key-value output.
func Label(text string, width int) string {
	return Bold(fmt.Sprintf("%-*s", width, text))
}

// CommandEntry describes a command for list formatting.
type CommandEntry struct {
	Name        string
	Run         string
	Description string
}

// CommandList formats commands in aligned columns.
func CommandList(cmds []CommandEntry) string {
	if len(cmds) == 0 {
		return ""
	}
	maxName := 0
	for _, c := range cmds {
		if len(c.Name) > maxName {
			maxName = len(c.Name)
		}
	}
	var lines []string
	for _, c := range cmds {
		padded := fmt.Sprintf("%-*s", maxName, c.Name)
		desc := ""
		if c.Description != "" {
			desc = "  " + Dim(c.Description)
		}
		lines = append(lines, fmt.Sprintf("  %s  %s%s", Bold(padded), Gray(c.Run), desc))
	}
	return strings.Join(lines, "\n")
}
