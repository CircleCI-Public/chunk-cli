package server

import (
	"slices"

	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/smeltd"
)

// maxFeedLines is how many lines a Feed keeps.
const maxFeedLines = 100

// feedLevel spells out an iostream level.
func feedLevel(l iostream.Level) smeltd.FeedLevel {
	switch l {
	case iostream.LevelStep:
		return smeltd.FeedStep
	case iostream.LevelWarn:
		return smeltd.FeedWarn
	case iostream.LevelDone:
		return smeltd.FeedDone
	case iostream.LevelError:
		return smeltd.FeedError
	case iostream.LevelInfo:
	}
	return smeltd.FeedInfo
}

// addFeedLine appends a line to f, dropping the oldest past maxFeedLines.
func addFeedLine(f *smeltd.Feed, level iostream.Level, text string) {
	f.Lines = append(f.Lines, smeltd.FeedLine{Level: feedLevel(level), Text: text})
	if over := len(f.Lines) - maxFeedLines; over > 0 {
		f.Lines = slices.Delete(f.Lines, 0, over)
	}
	f.Total++
}

// cloneFeed copies f so a snapshot does not share its lines with the record.
func cloneFeed(f smeltd.Feed) smeltd.Feed {
	return smeltd.Feed{Lines: slices.Clone(f.Lines), Total: f.Total}
}
