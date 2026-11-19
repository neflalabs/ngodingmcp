package mcpserver_test

import (
	"testing"
)

// Size of the compact tools/list result before the token-efficiency work
// (06 Fase 0, measured on commit 9a11010 with the configuration of
// listAllTools and listCoreTools).
const (
	baselineAllBytes  = 44869 // 43 tools, ~11.2k tokens
	baselineCoreBytes = 33719 // 35 tools, ~8.4k tokens
)

// Budgets for the tools/list result. They sit a little above the size after
// 06 Fase 1 so that small, deliberate additions fit, while a new tool with a
// long description or schema, or a typed outputSchema coming back, fails
// here. Raise a budget only on purpose, together with tools.golden.json.
const (
	budgetAllBytes  = baselineAllBytes * 60 / 100  // 26,921: at least -40%
	budgetCoreBytes = baselineCoreBytes * 62 / 100 // 20,905
)

func TestToolsListBudget(t *testing.T) {
	cases := []struct {
		name     string
		bytes    int
		budget   int
		baseline int
	}{
		{"all tools", toolsListBytes(t, listAllTools(t)), budgetAllBytes, baselineAllBytes},
		{"core tools", toolsListBytes(t, listCoreTools(t)), budgetCoreBytes, baselineCoreBytes},
	}
	for _, c := range cases {
		saved := 100 - c.bytes*100/c.baseline
		t.Logf("%-10s %6d bytes (~%d tokens), budget %d, baseline %d (-%d%%)", c.name, c.bytes, estTokens(c.bytes), c.budget, c.baseline, saved)
		if c.bytes > c.budget {
			t.Errorf("%s: tools/list is %d bytes, over the budget of %d. Shorten the new descriptions or schemas, "+
				"or raise the budget in tools_budget_test.go on purpose.", c.name, c.bytes, c.budget)
		}
	}
}
