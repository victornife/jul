// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package waf

import (
	"fmt"
	"strings"
	"time"

	"jul/internal/config"
)

// BuildExceptionDirectives compiles structured narrow WAF exceptions (#631,
// ADR 0031 §6.2) into deterministic phase:1 SecAction/SecRule directives with
// generated rule IDs 890001..890064. These directives are emitted before
// Include @owasp_crs/*.conf, external directive files, and inline_rules so
// Coraza v3.8.1 applies ctl:ruleRemoveTargetById / ctl:ruleRemoveById before
// any phase-1, phase-2, or chained target rule executes.
func BuildExceptionDirectives(exceptions []config.WAFException) ([]string, []int, error) {
	if len(exceptions) == 0 {
		return nil, nil, nil
	}
	if len(exceptions) > config.MaxWAFExceptions {
		return nil, nil, fmt.Errorf("waf: exceptions exceed maximum of %d (got %d)", config.MaxWAFExceptions, len(exceptions))
	}
	lines := make([]string, 0, len(exceptions))
	targets := make([]int, 0, len(exceptions))
	seenIDs := make(map[string]int, len(exceptions))
	seenTuples := make(map[string]int, len(exceptions))

	for i, exc := range exceptions {
		if id := strings.TrimSpace(exc.ID); id != "" {
			if prev, dup := seenIDs[id]; dup {
				return nil, nil, fmt.Errorf("waf: exception[%d] id %q duplicates exception[%d]", i, id, prev)
			}
			seenIDs[id] = i
		}
		if exc.RuleID <= 0 || exc.RuleID > 99999999 {
			return nil, nil, fmt.Errorf("waf: exception[%d] has invalid rule_id %d", i, exc.RuleID)
		}
		if exc.RuleID >= config.WAFGeneratedExceptionIDBase-1 && exc.RuleID <= config.WAFGeneratedRuleIDMax {
			return nil, nil, fmt.Errorf("waf: exception[%d] rule_id %d falls in Jul reserved generated range (%d–%d)",
				i, exc.RuleID, config.WAFGeneratedExceptionIDBase-1, config.WAFGeneratedRuleIDMax)
		}
		scope := config.CanonicalizeWAFExceptionScope(exc)
		var ctlAction string
		var canonTarget string
		switch scope {
		case "narrow_target":
			t, err := config.ValidateWAFExceptionTarget(exc.Target)
			if err != nil {
				return nil, nil, fmt.Errorf("waf: exception[%d] target: %w", i, err)
			}
			canonTarget = t
			ctlAction = fmt.Sprintf("ctl:ruleRemoveTargetById=%d;%s", exc.RuleID, canonTarget)
		case "rule_remove":
			if strings.TrimSpace(exc.Target) != "" {
				return nil, nil, fmt.Errorf("waf: exception[%d] target must be empty when scope is rule_remove", i)
			}
			ctlAction = fmt.Sprintf("ctl:ruleRemoveById=%d", exc.RuleID)
		default:
			return nil, nil, fmt.Errorf("waf: exception[%d] invalid scope %q", i, exc.Scope)
		}
		methods, err := config.CanonicalizeWAFExceptionMethods(exc.Methods)
		if err != nil {
			return nil, nil, fmt.Errorf("waf: exception[%d] methods: %w", i, err)
		}
		tupleKey := fmt.Sprintf("%d|%s|%s|%s", exc.RuleID, scope, canonTarget, strings.Join(methods, ","))
		if prev, dup := seenTuples[tupleKey]; dup {
			return nil, nil, fmt.Errorf("waf: exception[%d] duplicates exception[%d]", i, prev)
		}
		seenTuples[tupleKey] = i

		genID := config.WAFGeneratedExceptionIDBase + i
		switch len(methods) {
		case 0:
			lines = append(lines, fmt.Sprintf("SecAction \"id:%d,phase:1,nolog,pass,t:none,%s\"", genID, ctlAction))
		case 1:
			lines = append(lines, fmt.Sprintf("SecRule REQUEST_METHOD \"@streq %s\" \"id:%d,phase:1,nolog,pass,t:none,%s\"", methods[0], genID, ctlAction))
		default:
			lines = append(lines, fmt.Sprintf("SecRule REQUEST_METHOD \"@rx ^(?:%s)$\" \"id:%d,phase:1,nolog,pass,t:none,%s\"", strings.Join(methods, "|"), genID, ctlAction))
		}
		targets = append(targets, exc.RuleID)
	}
	return lines, targets, nil
}

// SummarizeExceptions returns the total exception count and the number of
// exceptions whose review_at date is strictly before today UTC (#631).
func SummarizeExceptions(exceptions []config.WAFException, now time.Time) (total int, reviewDue int) {
	total = len(exceptions)
	if total == 0 {
		return 0, 0
	}
	today := now.UTC().Truncate(24 * time.Hour)
	for _, exc := range exceptions {
		raw := strings.TrimSpace(exc.ReviewAt)
		if raw == "" {
			continue
		}
		if t, err := time.Parse("2006-01-02", raw); err == nil && t.Before(today) {
			reviewDue++
		}
	}
	return total, reviewDue
}

// DetectCustomTuningDirectives reports whether an inline or external SecLang
// snippet contains manual rule-removal or target-mutation directives outside
// structured [[waf.exceptions]].
func DetectCustomTuningDirectives(snippets ...string) bool {
	keywords := []string{
		"secruleremovebyid",
		"secruleremovebytag",
		"secruleremovebymsg",
		"secruleupdatetargetbyid",
		"secruleupdatetargetbytag",
		"secruleupdatetargetbymsg",
		"ctl:ruleremovebyid",
		"ctl:ruleremovebytag",
		"ctl:ruleremovebymsg",
		"ctl:ruleremovetargetbyid",
		"ctl:ruleremovetargetbytag",
		"ctl:ruleremovetargetbymsg",
		"ctl:ruleengine",
	}
	for _, snippet := range snippets {
		for _, line := range strings.Split(snippet, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			lower := strings.ToLower(trimmed)
			for _, kw := range keywords {
				if strings.Contains(lower, kw) {
					return true
				}
			}
		}
	}
	return false
}
