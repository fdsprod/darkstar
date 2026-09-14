package trackerrules

// MatchConditions evaluates normalized predicates for read-only mapping previews.
// Callers must validate the rule set and discovery before using this result.
func MatchConditions(conditions Conditions, observation Observation) (bool, error) {
	if err := validateConditions(conditions); err != nil {
		return false, err
	}
	return matches(conditions, observation)
}
