package policy

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyDestructiveActionNameWarning(t *testing.T) {
	cases := []struct {
		expression string
		literal    string
	}{
		{`action.name == "drop_table"`, "drop_table"},
		{`"removeUser" == action.name && resource.environment == "production"`, "removeUser"},
		{`action.name in ["delete", "purge"]`, "purge"},
		{`action.name != "rm"`, "rm"},
		{`action["name"] == "destroyAll"`, "destroyAll"},
		{`action["operation"] == "destroyAll"`, ""},
		// Already migrated: the operation is matched next to the old form.
		{`action.operation == "drop_table" || action.name == "drop_table"`, ""},
		{`action.operation == "drop_table" || action.name == "purge"`, "purge"},
		{`action.name == "delete"`, ""},
		{`action.name == "read"`, ""},
		{`action.operation == "drop_table"`, ""},
		{`args.name == "drop_table"`, ""},
	}
	for _, tc := range cases {
		engine, err := NewEngine(Rule{Name: "r", Expression: tc.expression})
		if err != nil {
			t.Fatalf("%s: %v", tc.expression, err)
		}
		warnings := engine.Warnings()
		if tc.literal == "" {
			if len(warnings) != 0 {
				t.Errorf("%s: unexpected warnings %v", tc.expression, warnings)
			}
			continue
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], `"`+tc.literal+`"`) || !strings.Contains(warnings[0], "action.operation") {
			t.Errorf("%s: warnings = %v, want one naming %q", tc.expression, warnings, tc.literal)
		}
	}
}

func TestLegacyActionWarningOncePerRule(t *testing.T) {
	engine, err := NewEngine(
		Rule{Name: "a", Expression: `action.name == "drop" || action.name == "truncate_table"`},
		Rule{Name: "b", Expression: `action.name == "delete"`},
	)
	if err != nil {
		t.Fatal(err)
	}
	if warnings := engine.Warnings(); len(warnings) != 1 || !strings.Contains(warnings[0], `policy "a"`) {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestShippedPoliciesHaveNoLegacyActionWarnings(t *testing.T) {
	paths, err := filepath.Glob("../../policies/*")
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob policies: %v %v", paths, err)
	}
	engine, err := NewEngineFromFiles(paths...)
	if err != nil {
		t.Fatal(err)
	}
	if warnings := engine.Warnings(); len(warnings) != 0 {
		t.Fatalf("shipped policies warn: %v", warnings)
	}
}
