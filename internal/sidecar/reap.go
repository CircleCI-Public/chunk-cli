package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
)

// StaleAfter is how long a sidecar's state file may go untouched before the
// sidecar is treated as abandoned and deleted.
//
// Every save rewrites the state file, so its mtime is the only record of when
// the sidecar was last used, and a cold file means nobody has used it since.
// Five days keeps a sidecar alive across a long weekend while still reclaiming
// spend on the ones whose session was closed and forgotten.
const StaleAfter = 5 * 24 * time.Hour

// ReapResult records what Reap did, so the caller can report it.
type ReapResult struct {
	// Deleted holds sidecars that were still running but abandoned, and so were
	// deleted through the API.
	Deleted []string
	// Vanished holds sidecars already absent server-side. Only their local state
	// files were removed; there was nothing left to delete.
	Vanished []string
	// Failed holds sidecars the API refused to delete. Their state is still
	// dropped, so the leak is reported rather than silent.
	Failed []string
}

// record files the outcome of deleting id.
//
// A 404 means the sidecar went away before the delete landed, which is the same
// end state as never having been listed. Anything else is a sidecar that may
// still be running, and is reported so it can be chased.
//
// Either way the state file goes, which is why this returns nothing to act on.
// The sidecar has already been judged abandoned, and a file kept back becomes
// the sidecar the next run picks up: a failed delete would hand every future run
// a box the API will not let go of, which is the resurrection this is meant to
// stop. Whatever survives server-side expires on its own.
func (r *ReapResult) record(id string, err error) {
	switch {
	case err == nil:
		r.Deleted = append(r.Deleted, id)
	case circleci.SidecarGone(err):
		r.Vanished = append(r.Vanished, id)
	default:
		r.Failed = append(r.Failed, id)
	}
}

// Empty reports whether the reap changed nothing.
func (r ReapResult) Empty() bool {
	return len(r.Deleted) == 0 && len(r.Vanished) == 0 && len(r.Failed) == 0
}

// Summary describes the result in one line, or "" when nothing changed.
func (r ReapResult) Summary() string {
	var parts []string
	if n := len(r.Deleted); n > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", n))
	}
	if n := len(r.Vanished); n > 0 {
		parts = append(parts, fmt.Sprintf("%d already gone", n))
	}
	if n := len(r.Failed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d could not be deleted and may still be running", n))
	}
	if len(parts) == 0 {
		return ""
	}
	total := len(r.Deleted) + len(r.Vanished) + len(r.Failed)
	return fmt.Sprintf("reaped %d abandoned %s (%s)", total, plural("sidecar", total), strings.Join(parts, ", "))
}

func plural(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// stateEntry is a parsed sidecar state file on disk.
type stateEntry struct {
	path    string
	active  ActiveSidecar
	modTime time.Time
}

// ids returns the sidecar IDs recorded in a state file. It is the only place
// that knows the state holds a single ID, so widening the state to a group of
// sidecars changes this function and nothing else in this file.
func (e stateEntry) ids() []string {
	if e.active.SidecarID == "" {
		return nil
	}
	return []string{e.active.SidecarID}
}

// Reap deletes abandoned sidecars for the current project and removes their
// local state files.
//
// State accumulates one file per session and branch, and nothing else removes
// them. Forgotten sidecars keep costing money, and a state file naming a sidecar
// that no longer exists causes every command against it to fail with a bare 404.
//
// Three cases, deliberately treated differently:
//
//   - Absent from the org's sidecar list: the state is worthless, so the file
//     goes. This includes the current session's own file, because there is no
//     live sidecar left to protect and leaving it is exactly what causes the
//     resurrection.
//   - Present but untouched for longer than StaleAfter: deleted through the API,
//     then the file goes. The current session's file is spared, since deleting
//     the sidecar this run is about to use only forces an immediate recreate.
//   - Present and recently used: left alone. A concurrent session on another
//     branch legitimately owns its own sidecar, and its warm mtime says so.
//
// Reap fails open. If the sidecar list cannot be fetched, nothing is deleted and
// nothing is pruned: an empty or partial list is not proof of absence, and
// destroying a live sidecar is far worse than leaving a stale file behind.
func Reap(ctx context.Context, client *circleci.Client, orgID string) (ReapResult, error) {
	var res ReapResult
	if client == nil || orgID == "" {
		return res, nil
	}
	dir, err := saveDir()
	if err != nil {
		return res, err
	}
	entries, err := loadStateEntries(dir)
	if err != nil || len(entries) == 0 {
		return res, err
	}

	live, err := client.ListSidecars(ctx, orgID, false)
	if err != nil {
		return res, fmt.Errorf("list sidecars: %w", err)
	}
	liveIDs := make(map[string]bool, len(live))
	for _, sc := range live {
		liveIDs[sc.ID] = true
	}

	root, _ := projectRoot()
	current := filepath.Join(dir, sidecarFileName(session.IDFromCtx(ctx), CurrentBranch(root)))
	cutoff := time.Now().Add(-StaleAfter)

	var errs []error
	for _, e := range entries {
		// A state file recording a different org cannot be judged against this
		// org's list; its sidecar would look absent when it is merely elsewhere.
		// Files written before the org was recorded are assumed to belong to this
		// project's org, which is the only org they could have come from unless
		// the project config changed underneath them.
		if e.active.OrgID != "" && e.active.OrgID != orgID {
			continue
		}
		keep := false
		for _, id := range e.ids() {
			switch {
			case !liveIDs[id]:
				res.Vanished = append(res.Vanished, id)
			case e.modTime.After(cutoff) || e.path == current:
				keep = true
			default:
				res.record(id, client.DeleteSidecar(ctx, id))
			}
		}
		if keep {
			continue
		}
		if err := removeState(e.path); err != nil {
			errs = append(errs, err)
		}
	}
	return res, errors.Join(errs...)
}

// PruneID removes every state file for the current project that names sidecarID,
// deleting the sidecar through the API first when it still exists.
//
// Removing one file is not enough. Adoption re-keys an ID under the adopting
// session and branch, and older chunk versions copied rather than moved the file,
// so the same ID can be recorded two or three times over; dropping a single file
// leaves a duplicate behind that resurrects the dead ID on the next run.
//
// deleteRemote separates the two ways a sidecar stops being usable. Already
// deleted (404) leaves nothing to delete. Out of date (410) means it is still
// running, still costing money, and can never be used again, so it is deleted
// before its state is dropped.
func PruneID(ctx context.Context, client *circleci.Client, sidecarID string, deleteRemote bool) error {
	if sidecarID == "" {
		return nil
	}
	dir, err := saveDir()
	if err != nil {
		return err
	}
	entries, err := loadStateEntries(dir)
	if err != nil {
		return err
	}

	var errs []error
	if deleteRemote && client != nil {
		if err := client.DeleteSidecar(ctx, sidecarID); err != nil {
			// Keep going: the sidecar may already be gone, and either way the local
			// state must be dropped or the next run reuses the same dead ID.
			errs = append(errs, fmt.Errorf("delete sidecar %s: %w", sidecarID, err))
		}
	}
	for _, e := range entries {
		for _, id := range e.ids() {
			if id != sidecarID {
				continue
			}
			if err := removeState(e.path); err != nil {
				errs = append(errs, err)
			}
			break
		}
	}
	return errors.Join(errs...)
}

// loadStateEntries reads every sidecar state file in dir. Files that cannot be
// read or parsed are skipped rather than failing the sweep, so one corrupt file
// does not block cleaning up the rest.
func loadStateEntries(dir string) ([]stateEntry, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "sidecar*.json"))
	if err != nil {
		return nil, err
	}
	entries := make([]stateEntry, 0, len(matches))
	for _, path := range matches {
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var a ActiveSidecar
		if jsonErr := json.Unmarshal(data, &a); jsonErr != nil {
			continue
		}
		entries = append(entries, stateEntry{path: path, active: a, modTime: info.ModTime()})
	}
	return entries, nil
}

// removeState deletes a state file, treating an already-absent file as success.
func removeState(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove sidecar state %s: %w", filepath.Base(path), err)
	}
	return nil
}
