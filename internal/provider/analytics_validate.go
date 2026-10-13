// Plan-time validation for the analytics-entity JSON blobs (cohort groups,
// behavior/metric/formula definitions, bookmark params).
//
// WHY: the Mixpanel app API accepts several malformed definition shapes with a
// 200 (`validate_cohorts_params` validates cohort `groups` as just {"groups":
// list} — webapp/app_api/projects/cohorts/utils.py + api/version_2_0/cohorts/
// validate.py `_validate_groups`; the behaviors/metrics POST schemas type-check
// but do not cross-check step counts or measurement/property coherence). A
// saved-but-corrupt entity then crashes the webapp query builder for the whole
// project, and Terraform makes the corrupt config durable (re-applied on every
// converge). These validators reject exactly the confirmed-crash shapes at plan
// time, before anything is sent to the API.
//
// The rules are deliberately CONSERVATIVE: only shapes confirmed to corrupt the
// webapp (or hard server limits) are flagged; ambiguous shapes are allowed so a
// legitimate config is never blocked. Every rule cites its source in the
// analytics monorepo (paths relative to analytics/).
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

// maxFunnelSteps is the hard server-side step limit: "ARB merger limits number
// of steps to 100" — api/version_2_0/validate.py (raises "Please reduce the
// number of steps to under 100" at query time, i.e. the saved report is
// unusable). There is no constant named MAX_FUNNEL_STEPS in the webapp source;
// the limit is the literal 100 in that validator.
const maxFunnelSteps = 100

// maxRetentionIntervalCount mirrors DEFAULT_MAX_RETENTION_INTERVAL_COUNT = 60
// (api/version_2_0/retention/util.py:750). The server silently CLAMPS the
// interval count, so buckets beyond this are unreachable — surfaced as a plan
// warning, not an error.
const maxRetentionIntervalCount = 60

// funnelOrderValues is the FunnelOrder enum: loose | any
// (iron/common/types/reports/bookmark.ts, enum FunnelOrder).
var funnelOrderValues = map[string]bool{"loose": true, "any": true}

// groupingOperatorValues: per-filter-group groupingOperator must be and|or
// (api/version_2_0/cohorts/parser.py `_validate_filter_group`).
var groupingOperatorValues = map[string]bool{"and": true, "or": true}

// propertyRequiringMath are measurement math values that aggregate a property
// and therefore crash the webapp query builder when saved with property:null.
// Derived from VALID_ACROSS_USER_AGGREGATIONS + property-menu operators
// (bookmark_parser/insights/validate.py; iron count-menu). "total" is excluded:
// without a property it means count-of-events (bookmark_parser/insights/
// validate.py: "sum of property value if 'property' is set, otherwise count").
var propertyRequiringMath = map[string]bool{
	"average": true, "median": true,
	"p25": true, "p75": true, "p90": true, "p99": true,
	"custom_percentile": true,
	"min":               true, "max": true,
	"histogram":     true,
	"unique_values": true, "most_frequent": true, "first_value": true,
	"numeric_summary": true,
}

// validateCohortGroups checks a decoded cohort `groups` value for the
// confirmed-crash shapes. Returns a list of problems (empty = OK).
//
// Sources:
//   - filter-group required keys: api/version_2_0/cohorts/parser.py
//     `_validate_filter_group` (filtersOperator, behavioralFiltersOperator,
//     groupingOperator on all but the last group, groupingOperator in {and,or}).
//     The create path only reaches that validator via the referenced-cohorts
//     resolution; a missing/null `filters` array passes it and saves with 200,
//     then crashes the webapp cohort builder.
//   - `groups` itself is validated server-side as only {"groups": list}
//     (api/version_2_0/cohorts/validate.py `_validate_groups`).
func validateCohortGroups(groups any) []string {
	if groups == nil {
		return nil
	}
	arr, ok := groups.([]any)
	if !ok {
		return []string{"groups: expected a JSON array of filter groups"}
	}
	var problems []string
	for i, g := range arr {
		p := fmt.Sprintf("groups[%d]", i)
		obj, ok := g.(map[string]any)
		if !ok {
			problems = append(problems, p+": expected a JSON object (filter group)")
			continue
		}
		// filters must be present and an array. A group saved without a filters
		// array renders the webapp cohort builder unusable (confirmed crash);
		// use [] for "no filters".
		if fv, present := obj["filters"]; !present || fv == nil {
			problems = append(problems, p+".filters: missing or null — must be a JSON array (use [] for no filters); Mixpanel saves this with 200 but the webapp cohort builder crashes rendering it")
		} else if fArr, ok := fv.([]any); !ok {
			problems = append(problems, p+".filters: must be a JSON array")
		} else {
			problems = append(problems, validateFilterEntries(p+".filters", fArr)...)
		}
		if s, _ := obj["filtersOperator"].(string); s == "" {
			problems = append(problems, p+".filtersOperator: missing or empty (required; see analytics api/version_2_0/cohorts/parser.py _validate_filter_group)")
		}
		if s, _ := obj["behavioralFiltersOperator"].(string); s == "" {
			problems = append(problems, p+".behavioralFiltersOperator: missing or empty (required; see analytics api/version_2_0/cohorts/parser.py _validate_filter_group)")
		}
		gop, gopPresent := obj["groupingOperator"]
		if i < len(arr)-1 {
			if s, _ := gop.(string); s == "" {
				problems = append(problems, p+".groupingOperator: required on every filter group except the last")
			}
		}
		if gopPresent && gop != nil {
			if s, ok := gop.(string); ok && s != "" && !groupingOperatorValues[s] {
				problems = append(problems, fmt.Sprintf("%s.groupingOperator: %q is not valid (must be \"and\" or \"or\")", p, s))
			}
		}
	}
	return problems
}

// validateFilterEntries applies the filter-strictness rules to a list of
// property-filter objects (cohort group filters, behavior filters, bookmark
// filter clauses). Only entries that carry a filterOperator are inspected;
// anything else (behavioral filters, nested trees) is left alone.
//
// Sources:
//   - "is set"/"is not set" carry no filter value; filterValue may be null but
//     not a concrete value (bookmark_parser/common/transforms/util.py:1406
//     "ok to be None when filterOperator is: is set/is not set"; operator
//     strings from iron/common/report/util/insights-permalink.ts).
//   - boolean property filters compare against the STRINGS "true"/"false",
//     never JSON booleans (webapp cohort test fixtures; a JSON boolean
//     filterValue on a boolean property corrupts the saved definition).
//   - a property filter may name its property via EITHER propertyName or value.
//     bookmark_parser/funnels/validation.py has Required("propertyName"), but
//     the app API does not: every filter it returns for project 2519332 — 318 of
//     them, across reports this provider imports without complaint — names its
//     property in `value` and none carries `propertyName`. A report built that
//     way opens in the webapp query builder and renders its filters, tested
//     2026-10-13, so the shape is not the crash the rule assumed. Rejecting it
//     made 39 of 153 saved reports unimportable while the rest went through,
//     which is the real tell: the rule walked funnel filter paths alone.
func validateFilterEntries(basePath string, filters []any) []string {
	var problems []string
	for j, f := range filters {
		obj, ok := f.(map[string]any)
		if !ok {
			continue
		}
		p := fmt.Sprintf("%s[%d]", basePath, j)
		opRaw, hasOp := obj["filterOperator"]
		op, _ := opRaw.(string)
		if !hasOp || op == "" {
			continue
		}
		if op == "is set" || op == "is not set" {
			if fv, present := obj["filterValue"]; present && fv != nil {
				problems = append(problems, fmt.Sprintf("%s: filterOperator %q must not carry a filterValue (omit it or set it to null)", p, op))
			}
		}
		if pt, _ := obj["propertyType"].(string); pt == "boolean" {
			if hasJSONBool(obj["filterValue"]) {
				problems = append(problems, p+`: boolean property filters take the strings "true"/"false", not JSON booleans`)
			}
		}
	}
	return problems
}

// hasJSONBool reports whether v is a JSON boolean or an array containing one.
func hasJSONBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return true
	case []any:
		for _, e := range x {
			if _, ok := e.(bool); ok {
				return true
			}
		}
	}
	return false
}

// validateBehaviorDefinition checks a decoded behavior `definition` (the
// {"behavior": {...}} wrapper — webapp/app_api/projects/behaviors/types.d.ts
// BehaviorEntity.definition) or a bare behavior object.
func validateBehaviorDefinition(def any) []string {
	obj, ok := def.(map[string]any)
	if !ok {
		return nil
	}
	if b, ok := obj["behavior"].(map[string]any); ok {
		return validateBehaviorObject("definition.behavior", b)
	}
	// Bare behavior object (has the behavior discriminators).
	if _, hasType := obj["type"]; hasType {
		return validateBehaviorObject("definition", obj)
	}
	return nil
}

// validateBehaviorObject checks one Behavior object (iron/common/types/reports/
// bookmark.ts `interface Behavior`) for the confirmed-crash shapes:
//
//   - funnel with fewer than 2 steps (iron requires behaviors.length >= 2 for
//     funnels, e.g. iron/common/report/insights/util/index.ts; a saved 0/1-step
//     funnel crashes the query builder),
//   - undefined steps: behaviors[] entries that are null or name no event
//     (no "name" and no saved "id"),
//   - funnelOrder outside the FunnelOrder enum loose|any
//     (iron/common/types/reports/bookmark.ts enum FunnelOrder),
//   - more than maxFunnelSteps steps (hard ARB limit, api/version_2_0/validate.py).
func validateBehaviorObject(basePath string, b map[string]any) []string {
	var problems []string
	btype, _ := b["type"].(string)
	behaviorsRaw, hasBehaviors := b["behaviors"]

	if fo, present := b["funnelOrder"]; present && fo != nil {
		if s, ok := fo.(string); !ok || !funnelOrderValues[s] {
			problems = append(problems, fmt.Sprintf("%s.funnelOrder: %v is not valid (must be \"loose\" or \"any\")", basePath, fo))
		}
	}

	if !hasBehaviors || behaviorsRaw == nil {
		// Saved-behavior references ({id, type, name}) legitimately carry no
		// behaviors array; only a definition that claims to be a funnel AND
		// carries no steps at all is checked below via the array path.
		return problems
	}
	steps, ok := behaviorsRaw.([]any)
	if !ok {
		problems = append(problems, basePath+".behaviors: must be a JSON array of steps")
		return problems
	}

	for i, s := range steps {
		p := fmt.Sprintf("%s.behaviors[%d]", basePath, i)
		stepObj, ok := s.(map[string]any)
		if !ok || stepObj == nil {
			problems = append(problems, p+": step is null or not an object — every step must name an event")
			continue
		}
		name, _ := stepObj["name"].(string)
		_, hasID := stepObj["id"]
		if name == "" && !hasID {
			problems = append(problems, p+": step defines no event (missing \"name\") — Mixpanel saves this with 200 but the webapp query builder crashes on undefined steps")
		}
		if fo, present := stepObj["funnelOrder"]; present && fo != nil {
			if fs, ok := fo.(string); !ok || !funnelOrderValues[fs] {
				problems = append(problems, fmt.Sprintf("%s.funnelOrder: %v is not valid (must be \"loose\" or \"any\")", p, fo))
			}
		}
		if fArr, ok := stepObj["filters"].([]any); ok {
			problems = append(problems, validateFilterEntries(p+".filters", fArr)...)
		}
	}

	switch btype {
	case "funnel":
		if len(steps) < 2 {
			problems = append(problems, fmt.Sprintf("%s.behaviors: a funnel needs at least 2 steps, got %d — Mixpanel saves this with 200 but the webapp query builder crashes on it", basePath, len(steps)))
		}
		if len(steps) > maxFunnelSteps {
			problems = append(problems, fmt.Sprintf("%s.behaviors: %d steps exceeds the server limit of %d (ARB merger limit, see analytics api/version_2_0/validate.py)", basePath, len(steps), maxFunnelSteps))
		}
	case "retention":
		if len(steps) < 2 {
			problems = append(problems, fmt.Sprintf("%s.behaviors: retention needs a birth event and a return event (2 steps), got %d", basePath, len(steps)))
		}
	}

	if fArr, ok := b["filters"].([]any); ok {
		problems = append(problems, validateFilterEntries(basePath+".filters", fArr)...)
	}
	return problems
}

// validateBehaviorBulk checks the behavior entity's bulk `behaviors` attribute
// (BehaviorsRequest bulk variant — webapp/app_api/projects/behaviors/types.d.ts:
// {behaviors: [{definition?: {behavior}, name, type}]}).
func validateBehaviorBulk(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var problems []string
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if def, ok := obj["definition"].(map[string]any); ok {
			if b, ok := def["behavior"].(map[string]any); ok {
				for _, p := range validateBehaviorObject("behavior", b) {
					problems = append(problems, fmt.Sprintf("behaviors[%d].definition.%s", i, p))
				}
			}
		}
	}
	return problems
}

// validateMetricMeasurement checks one BehaviorMeasurement object
// (iron/common/types/reports/bookmark.ts `interface BehaviorMeasurement`):
//
//   - measurement.property must be an object or null; any other JSON type is
//     the confirmed-crash "wrong type" shape (the server schema is
//     "property": Any(None, dict) — bookmark_parser/insights/validate.py:196 —
//     but only at QUERY time; the save returns 200),
//   - property-requiring math (average, median, percentiles, min/max, ...)
//     with property null/missing crashes the query builder.
func validateMetricMeasurement(meas any) []string {
	obj, ok := meas.(map[string]any)
	if !ok {
		return nil
	}
	return validateMeasurementObject("definition.measurement", obj)
}

func validateMeasurementObject(basePath string, m map[string]any) []string {
	var problems []string
	prop, propPresent := m["property"]
	if propPresent && prop != nil {
		if _, ok := prop.(map[string]any); !ok {
			problems = append(problems, fmt.Sprintf("%s.property: must be a JSON object (or null), got %T — Mixpanel saves the wrong type with 200 but the webapp query builder crashes on it", basePath, prop))
			// Wrong type dominates the requires-property rule below.
			return problems
		}
	}
	if math, _ := m["math"].(string); math != "" && propertyRequiringMath[math] {
		if !propPresent || prop == nil {
			problems = append(problems, fmt.Sprintf("%s: math %q aggregates a property but property is null/missing — Mixpanel saves this with 200 but the webapp query builder crashes on it", basePath, math))
		}
	}
	return problems
}

// validateMetricDefinition checks a metric/formula `definition`
// ({behavior, measurement, ...} — webapp/app_api/projects/metrics/types.d.ts
// BehaviorMetricDefinition), including the retention cross-check:
// measurement.retentionSegmentationEvent must name one of the retention's step
// events (the webapp derives it from behavior.behaviors[1].name, falling back
// to behaviors[0].name under attribution — iron/common/query-builder/
// query-entry-section/row-types/metric-entry/metrics/metric-entry-util.ts and
// iron/common/report/insights/query-builder/qb-update.ts). An event name not
// present in the steps at all is the confirmed-crash shape.
func validateMetricDefinition(def any) []string {
	obj, ok := def.(map[string]any)
	if !ok {
		return nil
	}
	var problems []string
	behavior, _ := obj["behavior"].(map[string]any)
	if behavior != nil {
		problems = append(problems, validateBehaviorObject("definition.behavior", behavior)...)
	}
	meas, measPresent := obj["measurement"].(map[string]any)
	if measPresent {
		problems = append(problems, validateMeasurementObject("definition.measurement", meas)...)
	}
	if behavior != nil && measPresent {
		if btype, _ := behavior["type"].(string); btype == "retention" {
			if rse, ok := meas["retentionSegmentationEvent"].(string); ok && rse != "" {
				if steps, ok := behavior["behaviors"].([]any); ok && len(steps) > 0 {
					names := map[string]bool{}
					allKnown := true
					for _, s := range steps {
						so, _ := s.(map[string]any)
						if so == nil {
							allKnown = false
							continue
						}
						if n, _ := so["name"].(string); n != "" {
							names[n] = true
						} else {
							allKnown = false
						}
					}
					// Only flag when every step name is known and none matches:
					// saved-reference steps without inline names make the check
					// ambiguous, and ambiguity must not block a legit config.
					if allKnown && !names[rse] {
						problems = append(problems, fmt.Sprintf("definition.measurement.retentionSegmentationEvent: %q does not match any retention step event (expected behaviors[1].name %v)", rse, stepName(steps, 1)))
					}
				}
			}
		}
	}
	return problems
}

func stepName(steps []any, i int) string {
	if i < len(steps) {
		if so, _ := steps[i].(map[string]any); so != nil {
			if n, _ := so["name"].(string); n != "" {
				return fmt.Sprintf("%q", n)
			}
		}
	}
	return "(unknown)"
}

// validateBookmarkParams checks a decoded bookmark `params` blob. bookmarkType
// is the bookmark's `type` attribute ("funnels", "insights", "retention", ...)
// when known, "" otherwise.
//
//   - Legacy funnel params carry a top-level `steps` array; the funnels
//     bookmark schema requires minItems 1 (bookmark_parser/funnels/schema/
//     bookmark.json) and the ARB merger caps steps at maxFunnelSteps
//     (api/version_2_0/validate.py).
//   - Multi-metric params carry sections.show[] clauses with behavior /
//     measurement objects: the behavior and measurement rules above apply.
func validateBookmarkParams(params any, bookmarkType string) []string {
	obj, ok := params.(map[string]any)
	if !ok {
		return nil
	}
	var problems []string
	if stepsRaw, present := obj["steps"]; present && bookmarkType == "funnels" {
		if steps, ok := stepsRaw.([]any); ok {
			if len(steps) < 1 {
				problems = append(problems, "params.steps: a funnels bookmark needs at least 1 step (bookmark schema minItems; the webapp funnel builder needs 2 to convert)")
			}
			if len(steps) > maxFunnelSteps {
				problems = append(problems, fmt.Sprintf("params.steps: %d steps exceeds the server limit of %d (ARB merger limit, see analytics api/version_2_0/validate.py)", len(steps), maxFunnelSteps))
			}
		}
	}
	// Multi-metric format: sections.show[] clauses.
	if sections, ok := obj["sections"].(map[string]any); ok {
		if show, ok := sections["show"].([]any); ok {
			for i, c := range show {
				clause, ok := c.(map[string]any)
				if !ok {
					continue
				}
				cp := fmt.Sprintf("params.sections.show[%d]", i)
				if b, ok := clause["behavior"].(map[string]any); ok {
					problems = append(problems, validateBehaviorObject(cp+".behavior", b)...)
				}
				if m, ok := clause["measurement"].(map[string]any); ok {
					problems = append(problems, validateMeasurementObject(cp+".measurement", m)...)
				}
			}
		}
	}
	return problems
}

// retentionBucketWarnings returns limit-proximity warnings (never errors): the
// server CLAMPS retention intervals to DEFAULT_MAX_RETENTION_INTERVAL_COUNT
// (api/version_2_0/retention/util.py:750), so custom bucket sizes beyond it are
// silently unreachable.
func retentionBucketWarnings(def any) []string {
	obj, ok := def.(map[string]any)
	if !ok {
		return nil
	}
	b, _ := obj["behavior"].(map[string]any)
	if b == nil {
		b = obj
	}
	if sizes, ok := b["retentionCustomBucketSizes"].([]any); ok && len(sizes) > maxRetentionIntervalCount {
		return []string{fmt.Sprintf("definition.behavior.retentionCustomBucketSizes: %d buckets exceeds the server's retention interval cap of %d (DEFAULT_MAX_RETENTION_INTERVAL_COUNT); buckets beyond the cap are silently clamped", len(sizes), maxRetentionIntervalCount)}
	}
	return nil
}

// serviceAccountExpiresPattern is the exact accepted expiration format
// (%Y-%m-%dT%H:%M:%SZ): webapp/app_api/organizations/service_accounts/views.py
// `create_service_account` (rejects sub-seconds, offsets, lowercase t/z; empty
// string / null means "never expires").
var serviceAccountExpiresPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

// validateServiceAccountExpires checks the service_account `expires` format.
func validateServiceAccountExpires(s string) []string {
	if s == "" {
		return nil // never expires
	}
	if !serviceAccountExpiresPattern.MatchString(s) {
		return []string{fmt.Sprintf("expires: %q does not match the required format 2006-01-02T15:04:05Z (UTC, trailing Z, no sub-seconds, no timezone offset); leave unset or empty for a non-expiring account", s)}
	}
	return nil
}

// validatedJSONAttr reads a top-level jsontypes.Normalized attribute from the
// plan-time config and runs check over its decoded value, appending one error
// diagnostic per problem. Null and unknown values are skipped (an unknown blob
// — e.g. from another resource's output — cannot be validated until apply);
// JSON syntax errors are also skipped here because jsontypes.Normalized already
// reports them.
func validatedJSONAttr(ctx context.Context, config tfsdk.Config, attr string, diags *diag.Diagnostics, check func(v any) []string) {
	var v jsontypes.Normalized
	d := config.GetAttribute(ctx, path.Root(attr), &v)
	diags.Append(d...)
	if d.HasError() || v.IsNull() || v.IsUnknown() {
		return
	}
	var decoded any
	if err := json.Unmarshal([]byte(v.ValueString()), &decoded); err != nil {
		return // jsontypes.Normalized reports invalid JSON itself
	}
	for _, problem := range check(decoded) {
		diags.AddAttributeError(path.Root(attr),
			"Invalid Mixpanel definition (would corrupt the project's saved reports)",
			problem)
	}
}

// warnJSONAttr is validatedJSONAttr's warning-severity counterpart for
// limit-proximity notices.
func warnJSONAttr(ctx context.Context, config tfsdk.Config, attr string, diags *diag.Diagnostics, check func(v any) []string) {
	var v jsontypes.Normalized
	d := config.GetAttribute(ctx, path.Root(attr), &v)
	if d.HasError() || v.IsNull() || v.IsUnknown() {
		return
	}
	var decoded any
	if err := json.Unmarshal([]byte(v.ValueString()), &decoded); err != nil {
		return
	}
	for _, w := range check(decoded) {
		diags.AddAttributeWarning(path.Root(attr), "Mixpanel server limit", w)
	}
}
