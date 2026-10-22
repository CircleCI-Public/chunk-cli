package watch

// foldHeader is a collapsible block's heading: ▾ and its label when open,
// ▸, its label and what it holds in a few words when not.
func foldHeader(open bool, label, summary string) string {
	if open {
		return "▾ " + label
	}
	if summary == "" {
		return "▸ " + label
	}
	return "▸ " + label + " · " + summary
}

// foldHint words the toggle key for a fold that is open or not.
func foldHint(open bool) string {
	if open {
		return "collapse"
	}
	return "expand"
}
