// Unit tests for the plan-time analytics-blob validators (analytics_validate.go).
// One table per rule, each with valid + invalid cases, so every confirmed-crash
// shape and every deliberately-allowed ambiguous shape is pinned.
package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// mustJSON decodes a JSON literal for validator input.
func mustJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad test JSON: %v", err)
	}
	return v
}

// checkProblems asserts the number of problems and that each expected
// substring appears in some problem.
func checkProblems(t *testing.T, got []string, wantCount int, wantSubstrings ...string) {
	t.Helper()
	if len(got) != wantCount {
		t.Fatalf("expected %d problems, got %d: %v", wantCount, len(got), got)
	}
	for _, want := range wantSubstrings {
		found := false
		for _, p := range got {
			if strings.Contains(p, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected a problem containing %q, got: %v", want, got)
		}
	}
}

const validCohortGroup = `{
  "event": {"value": "$all_users", "resourceType": "cohort", "label": "All Users"},
  "filters": [],
  "filtersOperator": "and",
  "behavioralFiltersOperator": "and",
  "groupingOperator": null
}`

func TestValidateCohortGroups(t *testing.T) {
	cases := []struct {
		name     string
		groups   string
		problems int
		contains []string
	}{
		{"valid single group (live-verified accepted shape)", `[` + validCohortGroup + `]`, 0, nil},
		{"null groups allowed", `null`, 0, nil},
		{"not an array", `{"filters": []}`, 1, []string{"expected a JSON array"}},
		{"group not an object", `[42]`, 1, []string{"expected a JSON object"}},
		{"missing filters (confirmed crash)", `[{"filtersOperator":"and","behavioralFiltersOperator":"and"}]`, 1, []string{".filters: missing or null"}},
		{"null filters (confirmed crash)", `[{"filters":null,"filtersOperator":"and","behavioralFiltersOperator":"and"}]`, 1, []string{".filters: missing or null"}},
		{"filters wrong type", `[{"filters":{},"filtersOperator":"and","behavioralFiltersOperator":"and"}]`, 1, []string{".filters: must be a JSON array"}},
		{"missing filtersOperator", `[{"filters":[],"behavioralFiltersOperator":"and"}]`, 1, []string{"filtersOperator: missing"}},
		{"missing behavioralFiltersOperator", `[{"filters":[],"filtersOperator":"and"}]`, 1, []string{"behavioralFiltersOperator: missing"}},
		{"missing groupingOperator on non-last group", `[{"filters":[],"filtersOperator":"and","behavioralFiltersOperator":"and"},` + validCohortGroup + `]`, 1, []string{"groupingOperator: required on every filter group except the last"}},
		{"null groupingOperator on last group OK", `[` + validCohortGroup + `]`, 0, nil},
		{"invalid groupingOperator value", `[{"filters":[],"filtersOperator":"and","behavioralFiltersOperator":"and","groupingOperator":"then"},` + validCohortGroup + `]`, 1, []string{`groupingOperator: "then" is not valid`}},
		{"multiple problems reported together", `[{"filtersOperator":"","behavioralFiltersOperator":"and"}]`, 2, []string{".filters: missing or null", "filtersOperator: missing"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkProblems(t, validateCohortGroups(mustJSON(t, tc.groups)), tc.problems, tc.contains...)
		})
	}
}

func TestValidateFilterEntries(t *testing.T) {
	cases := []struct {
		name     string
		filters  string
		problems int
		contains []string
	}{
		{"plain equals filter OK", `[{"resourceType":"user","propertyType":"string","propertyName":"foo","filterOperator":"equals","filterValue":"bar"}]`, 0, nil},
		{"is set with null filterValue OK (transforms/util.py: ok to be None)", `[{"propertyName":"foo","filterOperator":"is set","filterValue":null}]`, 0, nil},
		{"is set without filterValue key OK", `[{"propertyName":"foo","filterOperator":"is set"}]`, 0, nil},
		{"is set WITH filterValue (strictness)", `[{"propertyName":"foo","filterOperator":"is set","filterValue":"x"}]`, 1, []string{`"is set" must not carry a filterValue`}},
		{"is not set WITH filterValue (strictness)", `[{"propertyName":"foo","filterOperator":"is not set","filterValue":["x"]}]`, 1, []string{`"is not set" must not carry a filterValue`}},
		{"boolean op with string true OK", `[{"propertyName":"paid","propertyType":"boolean","filterOperator":"equals","filterValue":"true"}]`, 0, nil},
		{"boolean op with JSON bool (strictness)", `[{"propertyName":"paid","propertyType":"boolean","filterOperator":"equals","filterValue":true}]`, 1, []string{`strings "true"/"false"`}},
		{"boolean op with JSON bool in list (strictness)", `[{"propertyName":"paid","propertyType":"boolean","filterOperator":"equals","filterValue":[false]}]`, 1, []string{`strings "true"/"false"`}},
		{"value instead of propertyName OK (the app API returns this shape)", `[{"filterOperator":"equals","value":"foo","filterValue":"bar"}]`, 0, nil},
		{"value alongside propertyName allowed (GroupClause carries both)", `[{"filterOperator":"equals","propertyName":"foo","value":"foo","filterValue":"bar"}]`, 0, nil},
		{"value naming a property inside a funnel step filter OK", `[{"resourceType":"people","filterType":"string","value":"type","filterOperator":"equals","filterValue":["teacher"]}]`, 0, nil},
		{"behavioral filter without filterOperator skipped", `[{"customProperty":{"behavior":{}}}]`, 0, nil},
		{"non-object entries skipped", `[42, "x"]`, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arr := mustJSON(t, tc.filters).([]any)
			checkProblems(t, validateFilterEntries("filters", arr), tc.problems, tc.contains...)
		})
	}
}

func TestValidateBehaviorDefinition(t *testing.T) {
	cases := []struct {
		name     string
		def      string
		problems int
		contains []string
	}{
		{
			"valid 2-step funnel (UBER_DEFAULT_BEHAVIOR shape, behaviors/constants.py)",
			`{"behavior":{"type":"funnel","resourceType":"events","behaviors":[
				{"type":"event","name":"Sign Up","filters":[],"filtersDeterminer":"all","funnelOrder":"loose"},
				{"type":"event","name":"Purchase","filters":[],"filtersDeterminer":"all","funnelOrder":"loose"}
			],"conversionWindowDuration":7,"conversionWindowUnit":"day","funnelOrder":"loose"}}`,
			0, nil,
		},
		{"non-object definition allowed", `"x"`, 0, nil},
		{"wrapper without behavior key allowed", `{"something":"else"}`, 0, nil},
		{
			"funnel with 1 step (confirmed crash)",
			`{"behavior":{"type":"funnel","behaviors":[{"type":"event","name":"Sign Up"}]}}`,
			1, []string{"at least 2 steps, got 1"},
		},
		{
			"funnel with 0 steps (confirmed crash)",
			`{"behavior":{"type":"funnel","behaviors":[]}}`,
			1, []string{"at least 2 steps, got 0"},
		},
		{
			"undefined step: null entry (confirmed crash)",
			`{"behavior":{"type":"funnel","behaviors":[{"name":"A"},null]}}`,
			1, []string{"behaviors[1]: step is null"},
		},
		{
			"undefined step: no name, no id (confirmed crash)",
			`{"behavior":{"type":"funnel","behaviors":[{"name":"A"},{"type":"event","filters":[]}]}}`,
			1, []string{"behaviors[1]: step defines no event"},
		},
		{
			"step with saved id but no name allowed",
			`{"behavior":{"type":"funnel","behaviors":[{"name":"A"},{"id":123,"type":"funnel"}]}}`,
			0, nil,
		},
		{
			"funnelOrder outside loose|any (bookmark.ts FunnelOrder)",
			`{"behavior":{"type":"funnel","funnelOrder":"strict","behaviors":[{"name":"A"},{"name":"B"}]}}`,
			1, []string{"funnelOrder: strict is not valid"},
		},
		{
			"sub-behavior funnelOrder outside loose|any",
			`{"behavior":{"type":"funnel","behaviors":[{"name":"A","funnelOrder":"exact"},{"name":"B"}]}}`,
			1, []string{`behaviors[0].funnelOrder: exact is not valid`},
		},
		{
			"saved-behavior reference without behaviors array allowed",
			`{"behavior":{"id":42,"type":"funnel","name":"My Funnel"}}`,
			0, nil,
		},
		{
			"retention with 1 step",
			`{"behavior":{"type":"retention","behaviors":[{"name":"A"}]}}`,
			1, []string{"retention needs a birth event and a return event"},
		},
		{
			"filter strictness applies inside step filters",
			`{"behavior":{"type":"funnel","behaviors":[
				{"name":"A","filters":[{"propertyName":"p","filterOperator":"is set","filterValue":"x"}]},
				{"name":"B"}
			]}}`,
			1, []string{"must not carry a filterValue"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkProblems(t, validateBehaviorDefinition(mustJSON(t, tc.def)), tc.problems, tc.contains...)
		})
	}
}

func TestValidateBehaviorObjectStepLimit(t *testing.T) {
	// maxFunnelSteps = 100 (ARB merger limit, api/version_2_0/validate.py).
	steps := make([]any, maxFunnelSteps+1)
	for i := range steps {
		steps[i] = map[string]any{"name": "E"}
	}
	b := map[string]any{"type": "funnel", "behaviors": steps}
	checkProblems(t, validateBehaviorObject("behavior", b), 1, "exceeds the server limit of 100")

	// Exactly at the limit is allowed.
	b["behaviors"] = steps[:maxFunnelSteps]
	checkProblems(t, validateBehaviorObject("behavior", b), 0)
}

func TestValidateBehaviorBulk(t *testing.T) {
	bulk := `[
	  {"name":"ok","type":"funnel","definition":{"behavior":{"type":"funnel","behaviors":[{"name":"A"},{"name":"B"}]}}},
	  {"name":"bad","type":"funnel","definition":{"behavior":{"type":"funnel","behaviors":[{"name":"A"}]}}}
	]`
	got := validateBehaviorBulk(mustJSON(t, bulk))
	checkProblems(t, got, 1, "behaviors[1].definition.behavior.behaviors: a funnel needs at least 2 steps")

	// Non-array and non-object entries pass through.
	checkProblems(t, validateBehaviorBulk(mustJSON(t, `{"behaviors":[]}`)), 0)
	checkProblems(t, validateBehaviorBulk(mustJSON(t, `[1,2]`)), 0)
}

func TestValidateMetricMeasurement(t *testing.T) {
	cases := []struct {
		name     string
		meas     string
		problems int
		contains []string
	}{
		{"total without property OK (count of events, insights/validate.py)", `{"math":"total","property":null}`, 0, nil},
		{"unique without property OK", `{"math":"unique"}`, 0, nil},
		{"average with property object OK", `{"math":"average","property":{"resourceType":"event","propertyName":"Price","propertyType":"number"}}`, 0, nil},
		{"average with property null (confirmed crash)", `{"math":"average","property":null}`, 1, []string{`math "average" aggregates a property`}},
		{"median with property missing (confirmed crash)", `{"math":"median"}`, 1, []string{`math "median" aggregates a property`}},
		{"p90 with property null (confirmed crash)", `{"math":"p90","property":null}`, 1, []string{`math "p90"`}},
		{"property wrong type: string (confirmed crash)", `{"math":"total","property":"Price"}`, 1, []string{"property: must be a JSON object"}},
		{"property wrong type: array (confirmed crash)", `{"math":"average","property":["Price"]}`, 1, []string{"property: must be a JSON object"}},
		{"wrong type dominates requires-property", `{"math":"average","property":42}`, 1, []string{"property: must be a JSON object"}},
		{"non-object measurement allowed", `[1]`, 0, nil},
		{"unknown math allowed (no enum policing)", `{"math":"something_new"}`, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkProblems(t, validateMetricMeasurement(mustJSON(t, tc.meas)), tc.problems, tc.contains...)
		})
	}
}

func TestValidateMetricDefinition(t *testing.T) {
	cases := []struct {
		name     string
		def      string
		problems int
		contains []string
	}{
		{
			"valid retention metric with matching segmentation event (metric-entry-util.ts: defaults to behaviors[1].name)",
			`{"behavior":{"type":"retention","behaviors":[{"name":"Sign Up"},{"name":"Purchase"}]},
			  "measurement":{"math":"unique","retentionSegmentationEvent":"Purchase"}}`,
			0, nil,
		},
		{
			"retentionSegmentationEvent matching behaviors[0] allowed (qb-update.ts attribution path)",
			`{"behavior":{"type":"retention","behaviors":[{"name":"Sign Up"},{"name":"Purchase"}]},
			  "measurement":{"math":"unique","retentionSegmentationEvent":"Sign Up"}}`,
			0, nil,
		},
		{
			"retentionSegmentationEvent matching no step (confirmed crash)",
			`{"behavior":{"type":"retention","behaviors":[{"name":"Sign Up"},{"name":"Purchase"}]},
			  "measurement":{"math":"unique","retentionSegmentationEvent":"Checkout"}}`,
			1, []string{`retentionSegmentationEvent: "Checkout" does not match any retention step event`, `"Purchase"`},
		},
		{
			"ambiguous step names skip the cross-check",
			`{"behavior":{"type":"retention","behaviors":[{"name":"Sign Up"},{"id":7}]},
			  "measurement":{"math":"unique","retentionSegmentationEvent":"Checkout"}}`,
			0, nil,
		},
		{
			"behavior and measurement problems both reported",
			`{"behavior":{"type":"funnel","behaviors":[{"name":"A"}]},
			  "measurement":{"math":"average","property":null}}`,
			2, []string{"at least 2 steps", `math "average"`},
		},
		{"formula-style definition without behavior/measurement allowed", `{"formula":{"definition":"A/B","referencedMetrics":[]}}`, 0, nil},
		{"non-object allowed", `"x"`, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkProblems(t, validateMetricDefinition(mustJSON(t, tc.def)), tc.problems, tc.contains...)
		})
	}
}

func TestValidateBookmarkParams(t *testing.T) {
	longSteps := `[` + strings.Repeat(`{"event":"E","bool_op":"and","step_label":"E","property_filter_params_list":[]},`, maxFunnelSteps) + `{"event":"E","bool_op":"and","step_label":"E","property_filter_params_list":[]}]`
	cases := []struct {
		name     string
		params   string
		bmType   string
		problems int
		contains []string
	}{
		{"legacy funnel with steps OK", `{"steps":[{"event":"A"},{"event":"B"}],"date_range":{}}`, "funnels", 0, nil},
		{"legacy funnel with empty steps", `{"steps":[],"date_range":{}}`, "funnels", 1, []string{"at least 1 step"}},
		{"legacy funnel over step limit", `{"steps":` + longSteps + `}`, "funnels", 1, []string{"exceeds the server limit of 100"}},
		{"empty steps on non-funnel type allowed (type scopes the rule)", `{"steps":[]}`, "insights", 0, nil},
		{"empty steps with unknown type allowed", `{"steps":[]}`, "", 0, nil},
		{
			"multi-metric funnel clause with 1 step (confirmed crash)",
			`{"sections":{"show":[{"behavior":{"type":"funnel","behaviors":[{"name":"A"}]},"measurement":{"math":"general"}}]}}`,
			"insights", 1, []string{"params.sections.show[0].behavior.behaviors: a funnel needs at least 2 steps"},
		},
		{
			"multi-metric measurement property crash shape",
			`{"sections":{"show":[{"measurement":{"math":"average","property":null}}]}}`,
			"insights", 1, []string{`params.sections.show[0].measurement: math "average"`},
		},
		{"non-object params allowed", `[1,2]`, "funnels", 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkProblems(t, validateBookmarkParams(mustJSON(t, tc.params), tc.bmType), tc.problems, tc.contains...)
		})
	}
}

func TestRetentionBucketWarnings(t *testing.T) {
	over := make([]string, maxRetentionIntervalCount+1)
	for i := range over {
		over[i] = "1"
	}
	def := `{"behavior":{"type":"retention","retentionCustomBucketSizes":[` + strings.Join(over, ",") + `]}}`
	checkProblems(t, retentionBucketWarnings(mustJSON(t, def)), 1, "retention interval cap of 60")

	// At the cap: no warning.
	def = `{"behavior":{"type":"retention","retentionCustomBucketSizes":[` + strings.Join(over[:maxRetentionIntervalCount], ",") + `]}}`
	checkProblems(t, retentionBucketWarnings(mustJSON(t, def)), 0)
}

func TestValidateServiceAccountExpires(t *testing.T) {
	// Format cases mirror webapp/app_api/organizations/service_accounts/test.py
	// test_create_service_account_with_expiration.
	valid := []string{"", "2021-07-21T17:32:28Z", "2030-01-01T00:00:00Z"}
	for _, s := range valid {
		if got := validateServiceAccountExpires(s); len(got) != 0 {
			t.Errorf("expected %q to be valid, got %v", s, got)
		}
	}
	invalid := []string{
		"2021-07-21 17:32:28",         // missing T and Z
		"2021-07-21 17:32:28Z",        // missing T
		"2021-07-21T17:32:28",         // missing Z
		"2021-07-21t17:32:28z",        // lowercase t/z
		"2021-07-21T17:32:28-05:00",   // non-UTC offset
		"2021-07-21T17:32:59.999999",  // sub-seconds
		"2021-07-21T17:32:59.999999Z", // sub-seconds with Z
		"not-a-date",
	}
	for _, s := range invalid {
		got := validateServiceAccountExpires(s)
		if len(got) != 1 {
			t.Errorf("expected %q to be rejected, got %v", s, got)
			continue
		}
		if !strings.Contains(got[0], "2006-01-02T15:04:05Z") {
			t.Errorf("expected the error for %q to name the required format, got %q", s, got[0])
		}
	}
}

func TestCoerceNumericIDs(t *testing.T) {
	in := mustJSON(t, `{
	  "projects": [
	    {"id": "123", "users": [{"email": "a@b.c", "role": "analyst", "id": "456"}]},
	    {"id": 789, "users": []}
	  ],
	  "id": "007",
	  "name": "42"
	}`) // "007" has a leading zero: not a canonical integer, must stay a string
	out := coerceNumericIDs(in)
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"id":123`, `"id":456`, `"id":789`, `"id":"007"`, `"name":"42"`} {
		if !strings.Contains(s, want) {
			t.Errorf("expected marshaled payload to contain %s, got %s", want, s)
		}
	}
	// Non-numeric ids stay strings.
	in2 := mustJSON(t, `{"id":"abc-123"}`)
	b2, _ := json.Marshal(coerceNumericIDs(in2))
	if string(b2) != `{"id":"abc-123"}` {
		t.Errorf("non-numeric id must stay a string, got %s", b2)
	}
}
