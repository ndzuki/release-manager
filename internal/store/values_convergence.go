package store

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// LockedPathsForTasks validates one exact convergence task selection and returns
// its stable, deduplicated promotion paths.
func LockedPathsForTasks(taskIDs []string, tasks []*ConvergenceTask) ([]string, error) {
	selected := make(map[string]*ConvergenceTask, len(taskIDs))
	for _, task := range tasks {
		if task != nil {
			selected[task.ID] = task
		}
	}

	seenIDs := make(map[string]struct{}, len(taskIDs))
	seenPaths := make(map[string]struct{})
	paths := make([]string, 0)
	for _, taskID := range taskIDs {
		if taskID == "" {
			return nil, ErrConvergenceTaskConflict
		}
		if _, duplicate := seenIDs[taskID]; duplicate {
			return nil, ErrConvergenceTaskConflict
		}
		seenIDs[taskID] = struct{}{}

		task, ok := selected[taskID]
		if !ok || task.Status != "pending_promotion" {
			return nil, ErrConvergenceTaskConflict
		}
		if task.ActiveRevisionID != nil && *task.ActiveRevisionID != "" {
			return nil, ErrConvergenceRevisionExists
		}
		var taskPaths []string
		if err := json.Unmarshal(task.PromotionPaths, &taskPaths); err != nil {
			return nil, fmt.Errorf("decode convergence promotion paths: %w", err)
		}
		for _, path := range taskPaths {
			if path == "" {
				return nil, ErrConvergenceTaskConflict
			}
			if _, duplicate := seenPaths[path]; duplicate {
				// The same promotion path may be covered by several selected
				// tasks (REQ-018: locked_paths are deduplicated, stable-sorted).
				continue
			}
			seenPaths[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// LockedPathHash returns the stable SHA-256 hash of a sorted locked-path set.
func LockedPathHash(paths []string) string {
	stable := append([]string(nil), paths...)
	sort.Strings(stable)
	hash := sha256.New()
	for _, path := range stable {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(path)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(path))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// HasActiveConvergencePathConflict reports whether another active convergence
// revision owns any selected path.
func HasActiveConvergencePathConflict(taskIDs, paths []string, tasks []*ConvergenceTask) (bool, error) {
	selectedIDs := make(map[string]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		selectedIDs[taskID] = struct{}{}
	}
	selectedPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		selectedPaths[path] = struct{}{}
	}
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if _, selected := selectedIDs[task.ID]; selected {
			continue
		}
		if task.Status != "pending_promotion" || task.ActiveRevisionID == nil || *task.ActiveRevisionID == "" {
			continue
		}
		var taskPaths []string
		if err := json.Unmarshal(task.PromotionPaths, &taskPaths); err != nil {
			return false, fmt.Errorf("decode active convergence promotion paths: %w", err)
		}
		for _, path := range taskPaths {
			if _, conflict := selectedPaths[path]; conflict {
				return true, nil
			}
		}
	}
	return false, nil
}
