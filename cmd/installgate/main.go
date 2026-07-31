// Command installgate applies the time-bounded infrastructure quarantine policy
// for one Helm Install SDK gate failure.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/ndzuki/release-manager/internal/quality/installgate"
)

type diagnostic struct {
	Gate      string `json:"gate"`
	Scenario  string `json:"scenario"`
	RuleID    string `json:"rule_id"`
	Status    string `json:"status"`
	Owner     string `json:"owner,omitempty"`
	Issue     string `json:"issue,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Message   string `json:"message"`
}

func main() {
	quarantinePath := flag.String("quarantine", "install-sdk.quarantine.yaml", "path to quarantine YAML file")
	scenario := flag.String("scenario", "", "failed integration scenario")
	ruleID := flag.String("rule-id", "", "stable failure rule ID")
	message := flag.String("message", "", "failure summary")
	flag.Parse()

	if *scenario == "" || *ruleID == "" || *message == "" {
		fmt.Fprintln(os.Stderr, "installgate: --scenario, --rule-id, and --message are required")
		os.Exit(2)
	}

	quarantines, err := installgate.LoadQuarantines(*quarantinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "installgate: %v\n", err)
		os.Exit(1)
	}

	entry := diagnostic{
		Gate:     "install-sdk",
		Scenario: *scenario,
		RuleID:   *ruleID,
		Status:   "failed",
		Message:  *message,
	}
	if exception, found := quarantines.Match(*scenario, *ruleID); found {
		entry.Status = "quarantined"
		entry.Owner = exception.Owner
		entry.Issue = exception.Issue
		entry.ExpiresAt = exception.ExpiresAt
	}

	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "installgate: marshal diagnostic: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, string(data))
	if entry.Status == "quarantined" {
		return
	}
	os.Exit(1)
}
