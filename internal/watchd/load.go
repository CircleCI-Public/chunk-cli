package watchd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

const (
	// PollInterval is how often the daemon re-reads project state from disk.
	PollInterval = 5 * time.Second
	// RecentEvents is the maximum number of events kept per project.
	RecentEvents = 300
	// ActiveWindow is how recently a sidecar must have been active to appear.
	ActiveWindow = time.Hour
	// RunningTimeout is how long after the last non-terminal event a sidecar is
	// considered to still be running.
	RunningTimeout = 5 * time.Minute

	levelDone  = "done"
	levelError = "error"
)

// LoadSidecars reads all sidecar*.json files from dataDir and returns them as
// SidecarState entries. Entries with duplicate IDs are deduplicated, keeping
// the first occurrence.
func LoadSidecars(dataDir, projectRoot, snapshotName, head string) []SidecarState {
	matches, _ := filepath.Glob(filepath.Join(dataDir, "sidecar*.json"))
	projectName := filepath.Base(projectRoot)
	seen := map[string]bool{}
	var result []SidecarState
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var as sidecar.ActiveSidecar
		if json.Unmarshal(data, &as) != nil || as.SidecarID == "" {
			continue
		}
		if seen[as.SidecarID] {
			continue
		}
		seen[as.SidecarID] = true
		inSync := head != "" && as.LastSyncedRef != "" && head == as.LastSyncedRef
		var mtime time.Time
		if fi, err := os.Stat(path); err == nil {
			mtime = fi.ModTime()
		}
		result = append(result, SidecarState{
			ID:            as.SidecarID,
			Name:          as.Name,
			ProjectName:   projectName,
			SnapshotName:  snapshotName,
			FileMtime:     mtime,
			LastSyncedRef: as.LastSyncedRef,
			InSync:        inSync,
		})
	}
	return result
}

// LoadSnapshotName returns the Name field from any snapshot*.json in dataDir,
// or "" if none is found.
func LoadSnapshotName(dataDir string) string {
	matches, _ := filepath.Glob(filepath.Join(dataDir, "snapshot*.json"))
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var snap struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &snap) == nil && snap.Name != "" {
			return snap.Name
		}
	}
	return ""
}

// HeadRef returns the full HEAD SHA for the git repo at dir.
func HeadRef(dir string) string {
	if dir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sha, _ := gitutil.HeadRefCtx(ctx, dir)
	return sha
}

// AnnotateActivity fills LastActivity, LastOp, and Running on each sidecar
// from the most recent matching event.
func AnnotateActivity(sidecars []SidecarState, events []eventlog.Event) {
	for i := range sidecars {
		sc := &sidecars[i]
		for j := len(events) - 1; j >= 0; j-- {
			e := events[j]
			if e.SidecarID != sc.ID {
				continue
			}
			sc.LastActivity = e.Ts
			sc.LastOp = e.Op
			if e.Level != levelDone && e.Level != levelError && time.Since(e.Ts) < RunningTimeout {
				sc.Running = true
			}
			break
		}
	}
}

// SortByActivity sorts sidecars so the most recently active project comes
// first, with sidecars within each project also sorted by recency. Projects
// remain grouped together.
func SortByActivity(sidecars []SidecarState) {
	latest := map[string]time.Time{}
	for _, sc := range sidecars {
		if eff := EffectiveActivity(sc); eff.After(latest[sc.ProjectName]) {
			latest[sc.ProjectName] = eff
		}
	}
	sort.SliceStable(sidecars, func(i, j int) bool {
		a, b := sidecars[i], sidecars[j]
		if a.ProjectName != b.ProjectName {
			return latest[a.ProjectName].After(latest[b.ProjectName])
		}
		return EffectiveActivity(a).After(EffectiveActivity(b))
	})
}

// EffectiveActivity returns the sidecar's last event time, falling back to the
// mtime of its state file when no events are recorded.
func EffectiveActivity(sc SidecarState) time.Time {
	if !sc.LastActivity.IsZero() {
		return sc.LastActivity
	}
	return sc.FileMtime
}

// FilterSidecars keeps only sidecars active within ActiveWindow.
func FilterSidecars(sidecars []SidecarState) []SidecarState {
	now := time.Now()
	result := sidecars[:0]
	for _, sc := range sidecars {
		eff := EffectiveActivity(sc)
		if eff.IsZero() || now.Sub(eff) > ActiveWindow {
			continue
		}
		result = append(result, sc)
	}
	return result
}

// CapEvents appends fresh to prior, keeping at most maxEvents entries (newest
// survive). The cap is applied per-project so a busy project can't evict
// another project's recent events.
func CapEvents(prior, fresh []eventlog.Event, maxEvents int) []eventlog.Event {
	merged := make([]eventlog.Event, 0, len(prior)+len(fresh))
	merged = append(merged, prior...)
	merged = append(merged, fresh...)
	if len(merged) > maxEvents {
		merged = merged[len(merged)-maxEvents:]
	}
	return merged
}

// CurrentBranch returns the current git branch for the repo at root.
func CurrentBranch(root string) string {
	return sidecar.CurrentBranch(root)
}
