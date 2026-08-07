// Package installgate defines infrastructure-failure quarantine policy for the
// Helm Install SDK integration gate.
package installgate

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Stable infrastructure and exception rule identifiers.
const (
	RuleClusterUnavailable      = "cluster_unavailable"
	RuleImagePullFailed         = "image_pull_failed"
	RuleRunnerEnvironmentFailed = "runner_environment_failed"
	RuleExpiredException        = "expired_exception"
)

const maxQuarantineDuration = 7 * 24 * time.Hour

// Exception is one exact, time-bounded infrastructure failure quarantine.
type Exception struct {
	Scenario  string `yaml:"scenario"`
	RuleID    string `yaml:"rule_id"`
	Owner     string `yaml:"owner"`
	Reason    string `yaml:"reason"`
	Issue     string `yaml:"issue"`
	ExpiresAt string `yaml:"expires_at"`
}

type quarantineFile struct {
	Version    string      `yaml:"version"`
	Exceptions []Exception `yaml:"exceptions"`
}

// Quarantines contains validated exceptions keyed by scenario and rule ID.
type Quarantines struct {
	exceptions map[string]Exception
}

// LoadQuarantines reads and validates the gate quarantine file.
func LoadQuarantines(path string) (Quarantines, error) {
	return loadQuarantinesAt(path, time.Now())
}

func loadQuarantinesAt(path string, now time.Time) (Quarantines, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Quarantines{}, fmt.Errorf("read quarantine file: %w", err)
	}

	var file quarantineFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return Quarantines{}, fmt.Errorf("parse quarantine file: %w", err)
	}
	if file.Version != "1" {
		return Quarantines{}, fmt.Errorf("unsupported quarantine version %q", file.Version)
	}

	day := startOfDay(now)
	exceptions := make(map[string]Exception, len(file.Exceptions))
	for index, exception := range file.Exceptions {
		if err := validateException(exception, day); err != nil {
			return Quarantines{}, fmt.Errorf("validate quarantine exception %d: %w", index+1, err)
		}
		key := quarantineKey(exception.Scenario, exception.RuleID)
		if _, exists := exceptions[key]; exists {
			return Quarantines{}, fmt.Errorf(
				"validate quarantine exception %d: duplicate scenario %q and rule_id %q",
				index+1,
				exception.Scenario,
				exception.RuleID,
			)
		}
		exceptions[key] = exception
	}
	return Quarantines{exceptions: exceptions}, nil
}

// Match returns an exact scenario and rule ID quarantine.
func (q Quarantines) Match(scenario, ruleID string) (Exception, bool) {
	exception, found := q.exceptions[quarantineKey(scenario, ruleID)]
	return exception, found
}

func validateException(exception Exception, now time.Time) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "scenario", value: exception.Scenario},
		{name: "rule_id", value: exception.RuleID},
		{name: "owner", value: exception.Owner},
		{name: "reason", value: exception.Reason},
		{name: "issue", value: exception.Issue},
		{name: "expires_at", value: exception.ExpiresAt},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("missing %s field", field.name)
		}
	}
	if !isInfrastructureRule(exception.RuleID) {
		return fmt.Errorf("rule_id %q cannot be quarantined", exception.RuleID)
	}

	expiresAt, err := time.Parse(time.DateOnly, exception.ExpiresAt)
	if err != nil {
		return fmt.Errorf("parse expires_at %q: %w", exception.ExpiresAt, err)
	}
	expiresAt = startOfDay(expiresAt)
	if expiresAt.Before(now) {
		return fmt.Errorf("[%s] exception expired at %s", RuleExpiredException, exception.ExpiresAt)
	}
	if expiresAt.After(now.Add(maxQuarantineDuration)) {
		return fmt.Errorf("expires_at must be within seven days")
	}
	return nil
}

func isInfrastructureRule(ruleID string) bool {
	switch ruleID {
	case RuleClusterUnavailable, RuleImagePullFailed, RuleRunnerEnvironmentFailed:
		return true
	default:
		return false
	}
}

func quarantineKey(scenario, ruleID string) string {
	return scenario + "\x00" + ruleID
}

func startOfDay(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}
