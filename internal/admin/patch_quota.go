// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	"jul/internal/config"
)

func applyQuotaPolicySet(c *config.Config, patch *quotaPolicyPatch) (string, error) {
	if patch == nil {
		return "", fmt.Errorf("quota_policy_set: quota_policy payload is required")
	}
	name := strings.TrimSpace(patch.Name)
	if name == "" {
		return "", fmt.Errorf("quota_policy_set: policy name is required")
	}
	if patch.Mode == nil && patch.Rate == nil && patch.Burst == nil && patch.MaxCallers == nil &&
		patch.Classes == nil && len(patch.RemoveClasses) == 0 &&
		patch.Pools == nil && len(patch.RemovePools) == 0 &&
		patch.Operations == nil && len(patch.RemoveOperations) == 0 {
		return "", fmt.Errorf("quota_policy_set: at least one policy field must be provided")
	}
	if c.QuotaPolicies == nil {
		c.QuotaPolicies = make(map[string]config.QuotaPolicyConfig)
	}

	policy, exists := c.QuotaPolicies[name]
	if !exists {
		if patch.Mode == nil || patch.Rate == nil || patch.Burst == nil || patch.MaxCallers == nil {
			return "", fmt.Errorf("quota_policy_set: a new policy requires mode, rate, burst, and max_callers")
		}
		policy.Classes = make(map[string]config.QuotaClassConfig)
		policy.Pools = make(map[string]config.QuotaPoolConfig)
		policy.Operations = make(map[string]config.QuotaOperationConfig)
	}
	if err := rejectQuotaPatchRemovalOverlap(patch.Classes, patch.RemoveClasses, "class"); err != nil {
		return "", err
	}
	if err := rejectQuotaPatchRemovalOverlap(patch.Pools, patch.RemovePools, "pool"); err != nil {
		return "", err
	}
	if err := rejectQuotaPatchRemovalOverlap(patch.Operations, patch.RemoveOperations, "operation"); err != nil {
		return "", err
	}
	if patch.Mode != nil {
		mode := strings.TrimSpace(*patch.Mode)
		if mode != "enforce" && mode != "observe" {
			return "", fmt.Errorf("quota_policy_set: mode must be %q or %q", "enforce", "observe")
		}
		policy.Mode = mode
	}
	if patch.Rate != nil {
		if *patch.Rate <= 0 {
			return "", fmt.Errorf("quota_policy_set: rate must be greater than zero")
		}
		policy.Rate = *patch.Rate
	}
	if patch.Burst != nil {
		if *patch.Burst <= 0 {
			return "", fmt.Errorf("quota_policy_set: burst must be greater than zero")
		}
		policy.Burst = *patch.Burst
	}
	if patch.MaxCallers != nil {
		if *patch.MaxCallers <= 0 {
			return "", fmt.Errorf("quota_policy_set: max_callers must be greater than zero")
		}
		policy.MaxCallers = *patch.MaxCallers
	}

	if patch.Classes != nil || len(patch.RemoveClasses) > 0 {
		if policy.Classes == nil {
			policy.Classes = make(map[string]config.QuotaClassConfig)
		} else {
			policy.Classes = maps.Clone(policy.Classes)
		}
		for _, className := range sortedQuotaNames(patch.Classes) {
			if err := validateQuotaMapName(className, "class"); err != nil {
				return "", err
			}
			classPatch := patch.Classes[className]
			class, classExists := policy.Classes[className]
			if !classExists && (classPatch.Shared == nil || classPatch.PerCaller == nil) {
				return "", fmt.Errorf("quota_policy_set: a new class requires shared and per_caller limits")
			}
			if classPatch.Shared != nil {
				if err := applyQuotaLimitPatch(&class.Shared, classPatch.Shared, !classExists); err != nil {
					return "", fmt.Errorf("quota_policy_set: class %q shared: %w", className, err)
				}
			}
			if classPatch.PerCaller != nil {
				if err := applyQuotaLimitPatch(&class.PerCaller, classPatch.PerCaller, !classExists); err != nil {
					return "", fmt.Errorf("quota_policy_set: class %q per_caller: %w", className, err)
				}
			}
			policy.Classes[className] = class
		}
	}
	if err := removeQuotaEntries(policy.Classes, patch.RemoveClasses, "class", true); err != nil {
		return "", fmt.Errorf("quota_policy_set: %w", err)
	}

	if patch.Pools != nil || len(patch.RemovePools) > 0 {
		if policy.Pools == nil {
			policy.Pools = make(map[string]config.QuotaPoolConfig)
		} else {
			policy.Pools = maps.Clone(policy.Pools)
		}
		for _, poolName := range sortedQuotaNames(patch.Pools) {
			if err := validateQuotaMapName(poolName, "pool"); err != nil {
				return "", err
			}
			poolPatch := patch.Pools[poolName]
			pool, poolExists := policy.Pools[poolName]
			if !poolExists && (poolPatch.Class == nil || poolPatch.CIDRs == nil) {
				return "", fmt.Errorf("quota_policy_set: a new pool requires class and cidrs")
			}
			if poolPatch.Class != nil {
				pool.Class = strings.TrimSpace(*poolPatch.Class)
			}
			if poolPatch.CIDRs != nil {
				pool.CIDRs = append([]string(nil), (*poolPatch.CIDRs)...)
			}
			policy.Pools[poolName] = pool
		}
	}
	if err := removeQuotaEntries(policy.Pools, patch.RemovePools, "pool", false); err != nil {
		return "", fmt.Errorf("quota_policy_set: %w", err)
	}

	if patch.Operations != nil || len(patch.RemoveOperations) > 0 {
		if policy.Operations == nil {
			policy.Operations = make(map[string]config.QuotaOperationConfig)
		} else {
			policy.Operations = maps.Clone(policy.Operations)
		}
		for _, operationName := range sortedQuotaNames(patch.Operations) {
			if err := validateQuotaMapName(operationName, "operation"); err != nil {
				return "", err
			}
			operationPatch := patch.Operations[operationName]
			operation, operationExists := policy.Operations[operationName]
			if !operationExists && (operationPatch.Shared == nil || operationPatch.PerCaller == nil) {
				return "", fmt.Errorf("quota_policy_set: a new operation requires shared and per_caller limits")
			}
			if operationPatch.Shared != nil {
				if err := applyQuotaLimitPatch(&operation.Shared, operationPatch.Shared, !operationExists); err != nil {
					return "", fmt.Errorf("quota_policy_set: operation %q shared: %w", operationName, err)
				}
			}
			if operationPatch.PerCaller != nil {
				if err := applyQuotaLimitPatch(&operation.PerCaller, operationPatch.PerCaller, !operationExists); err != nil {
					return "", fmt.Errorf("quota_policy_set: operation %q per_caller: %w", operationName, err)
				}
			}
			policy.Operations[operationName] = operation
		}
	}
	if err := removeQuotaEntries(policy.Operations, patch.RemoveOperations, "operation", false); err != nil {
		return "", fmt.Errorf("quota_policy_set: %w", err)
	}

	if !exists {
		if _, ok := policy.Classes["public"]; !ok {
			return "", fmt.Errorf("quota_policy_set: a new policy requires the public class")
		}
	}
	c.QuotaPolicies[name] = policy
	return fmt.Sprintf("quota policy %s updated", name), nil
}

func applyQuotaLimitPatch(dst *config.QuotaLimitConfig, patch *quotaLimitPatch, complete bool) error {
	if patch == nil {
		return fmt.Errorf("limit payload is required")
	}
	if complete && (patch.Rate == nil || patch.Burst == nil) {
		return fmt.Errorf("a new limit requires rate and burst")
	}
	if patch.Rate == nil && patch.Burst == nil {
		return fmt.Errorf("rate or burst must be provided")
	}
	if patch.Rate != nil {
		if *patch.Rate <= 0 {
			return fmt.Errorf("rate must be greater than zero")
		}
		dst.Rate = *patch.Rate
	}
	if patch.Burst != nil {
		if *patch.Burst <= 0 {
			return fmt.Errorf("burst must be greater than zero")
		}
		dst.Burst = *patch.Burst
	}
	return nil
}

func removeQuotaEntries[T any](entries map[string]T, names []string, kind string, protectPublic bool) error {
	seen := make(map[string]struct{}, len(names))
	for _, rawName := range names {
		name := strings.TrimSpace(rawName)
		if name == "" || name != rawName {
			return fmt.Errorf("remove_%s requires a name", kind)
		}
		if protectPublic && name == "public" {
			return fmt.Errorf("the public class cannot be removed")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate %s removal", kind)
		}
		seen[name] = struct{}{}
		if _, ok := entries[name]; !ok {
			return fmt.Errorf("%s %q does not exist", kind, name)
		}
		delete(entries, name)
	}
	return nil
}

func sortedQuotaNames[T any](entries map[string]T) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateQuotaMapName(name, kind string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("quota_policy_set: %s name must be non-empty and have no surrounding whitespace", kind)
	}
	return nil
}

func rejectQuotaPatchRemovalOverlap[T any](updates map[string]T, removals []string, kind string) error {
	for _, name := range removals {
		if _, ok := updates[name]; ok {
			return fmt.Errorf("quota_policy_set: %s %q cannot be both updated and removed", kind, name)
		}
	}
	return nil
}

func applyQuotaPolicyRemove(c *config.Config, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("quota_policy_remove: policy name is required")
	}
	if _, ok := c.QuotaPolicies[name]; !ok {
		return "", fmt.Errorf("quota_policy_remove: policy %q does not exist", name)
	}
	for i := range c.Servers {
		for j := range c.Servers[i].Locations {
			loc := &c.Servers[i].Locations[j]
			if loc.QuotaPolicy == name {
				return "", fmt.Errorf("quota_policy_remove: policy %q is still attached to a route", name)
			}
		}
	}
	delete(c.QuotaPolicies, name)
	return fmt.Sprintf("quota policy %s removed", name), nil
}

func applyRouteQuotaPolicy(c *config.Config, req patchRequest) (string, error) {
	loc, err := findLocation(c, req.locationTarget())
	if err != nil {
		return "", err
	}
	if req.RouteQuota == nil {
		return "", fmt.Errorf("route_set_quota_policy: route_quota payload is required")
	}
	policyName := strings.TrimSpace(req.RouteQuota.Policy)
	operationName := strings.TrimSpace(req.RouteQuota.Operation)
	if policyName == "" {
		if operationName != "" {
			return "", fmt.Errorf("route_set_quota_policy: operation requires a policy")
		}
		loc.QuotaPolicy = ""
		loc.QuotaOperation = ""
		return fmt.Sprintf("route %s%s application quota detached", req.Listen, req.Path), nil
	}
	policy, ok := c.QuotaPolicies[policyName]
	if !ok {
		return "", fmt.Errorf("route_set_quota_policy: policy %q does not exist", policyName)
	}
	if operationName != "" {
		if _, ok := policy.Operations[operationName]; !ok {
			return "", fmt.Errorf("route_set_quota_policy: operation %q is not defined by the policy", operationName)
		}
	}
	loc.QuotaPolicy = policyName
	loc.QuotaOperation = operationName
	if operationName != "" {
		return fmt.Sprintf("route %s%s application quota %s with operation %s", req.Listen, req.Path, policyName, operationName), nil
	}
	return fmt.Sprintf("route %s%s application quota %s", req.Listen, req.Path, policyName), nil
}
