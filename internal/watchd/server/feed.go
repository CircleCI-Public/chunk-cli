package server

import (
	"slices"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// maxFeedLines is how many lines a Feed keeps.
const maxFeedLines = 100

// feedLevel spells out an iostream level.
func feedLevel(l iostream.Level) watchd.FeedLevel {
	switch l {
	case iostream.LevelStep:
		return watchd.FeedStep
	case iostream.LevelWarn:
		return watchd.FeedWarn
	case iostream.LevelDone:
		return watchd.FeedDone
	case iostream.LevelError:
		return watchd.FeedError
	case iostream.LevelInfo:
	}
	return watchd.FeedInfo
}

// addFeedLine appends a line to f, dropping the oldest past maxFeedLines.
func addFeedLine(f *watchd.Feed, level iostream.Level, text string) {
	f.Lines = append(f.Lines, watchd.FeedLine{Level: feedLevel(level), Text: text})
	if over := len(f.Lines) - maxFeedLines; over > 0 {
		f.Lines = slices.Delete(f.Lines, 0, over)
	}
	f.Total++
}

// cloneFeed copies f so a snapshot does not share its lines with the record.
func cloneFeed(f watchd.Feed) watchd.Feed {
	return watchd.Feed{Lines: slices.Clone(f.Lines), Total: f.Total}
}
