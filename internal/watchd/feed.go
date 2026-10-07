package watchd

import (
	"slices"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// maxFeedLines is how many lines a Feed keeps.
const maxFeedLines = 100

// feedLevel spells out an iostream level.
func feedLevel(l iostream.Level) FeedLevel {
	switch l {
	case iostream.LevelStep:
		return FeedStep
	case iostream.LevelWarn:
		return FeedWarn
	case iostream.LevelDone:
		return FeedDone
	case iostream.LevelError:
		return FeedError
	case iostream.LevelInfo:
	}
	return FeedInfo
}

// addFeedLine appends a line to f, dropping the oldest past maxFeedLines.
func addFeedLine(f *Feed, level iostream.Level, text string) {
	f.Lines = append(f.Lines, FeedLine{Level: feedLevel(level), Text: text})
	if over := len(f.Lines) - maxFeedLines; over > 0 {
		f.Lines = slices.Delete(f.Lines, 0, over)
	}
	f.Total++
}

// cloneFeed copies f so a snapshot does not share its lines with the record.
func cloneFeed(f Feed) Feed {
	return Feed{Lines: slices.Clone(f.Lines), Total: f.Total}
}
