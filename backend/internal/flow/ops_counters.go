package flow

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Process-wide event counters for §14 alerts (Prometheus counters, reset on
// restart; alerts use increase()). Labels are fixed enums, never tenant data.
var opsCounters = struct {
	sync.Mutex
	m map[string]map[string]int64
}{m: map[string]map[string]int64{}}

func countOp(name, label string) {
	opsCounters.Lock()
	defer opsCounters.Unlock()
	if opsCounters.m[name] == nil {
		opsCounters.m[name] = map[string]int64{}
	}
	opsCounters.m[name][label]++
}

// CountBudgetDenied records a reservation refused for budget reasons.
func CountBudgetDenied(err error) {
	switch {
	case errors.Is(err, ErrBudgetQuota):
		countOp("flow_budget_denied_total", "quota")
	case errors.Is(err, ErrBudgetInsufficient):
		countOp("flow_budget_denied_total", "insufficient")
	case errors.Is(err, ErrBudgetParent):
		countOp("flow_budget_denied_total", "parent")
	case errors.Is(err, ErrBudgetProvider):
		countOp("flow_budget_denied_total", "provider_account")
	}
}

// CountCitationRejected records a claim rejected by citation validation or the Reviewer.
func CountCitationRejected(stage string) { countOp("flow_citation_rejected_total", stage) }

// CountDuplicateDelivery records an engine event delivered more than once (inbox dedupe hit).
func CountDuplicateDelivery() { countOp("flow_event_duplicate_deliveries_total", "inbox") }

// CountMCPError records a failed MCP tools/call (transport, protocol or tool error).
func CountMCPError(kind string) { countOp("flow_mcp_errors_total", kind) }

var counterLabel = map[string]string{
	"flow_budget_denied_total":              "reason",
	"flow_citation_rejected_total":          "stage",
	"flow_event_duplicate_deliveries_total": "source",
	"flow_mcp_errors_total":                 "kind",
}

func writeCounters(b *strings.Builder) {
	opsCounters.Lock()
	defer opsCounters.Unlock()
	names := make([]string, 0, len(counterLabel))
	for n := range counterLabel {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(b, "# HELP %s §14 alert counter.\n# TYPE %s counter\n", n, n)
		labels := opsCounters.m[n]
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			fmt.Fprintf(b, "%s 0\n", n)
		}
		for _, k := range keys {
			fmt.Fprintf(b, "%s{%s=%q} %d\n", n, counterLabel[n], k, labels[k])
		}
	}
}
