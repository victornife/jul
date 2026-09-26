// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"jul/internal/admin"
)

// newManagedApplyInstanceID returns a 12-hex-character boot-scoped identifier
// generated once per process. It is correlation metadata, not a cryptographic
// secret; the fallback only runs if the OS CSPRNG is unavailable.
func newManagedApplyInstanceID() string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}

	// Extremely defensive fallback. This identifier is correlation metadata,
	// not a cryptographic secret.
	fallback := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UTC().UnixNano())
	sum := sha256.Sum256([]byte(fallback))
	return hex.EncodeToString(sum[:6])
}

func (c *ConfigApplyCoordinator) managedApplyInstanceID() string {
	c.applyIDOnce.Do(func() {
		c.applyInstanceID = newManagedApplyInstanceID()
	})
	return c.applyInstanceID
}

// BootID returns the boot-scoped instance identity embedded in every apply id.
//
// It is published as `boot_id` by GET /api/v1/status and /api/v1/capabilities:
// the terminal ledger is process-local, so a changed boot_id tells a client its
// replay window and every idempotency binding were discarded (ADR 0019 §27.2).
// Reading it from here rather than minting a second identity is what keeps
// `boot_id` and the `rl_<instance>_<seq>` ids a client correlates it with from
// disagreeing.
func (c *ConfigApplyCoordinator) BootID() string { return c.managedApplyInstanceID() }

// nextID allocates the next boot-scoped managed apply ID in the form
// rl_<boot-id>_<sequence>. The boot-id prevents apply-ID reuse across process
// restarts; the sequence is monotonically increasing within the process.
func (c *ConfigApplyCoordinator) nextID() string {
	return fmt.Sprintf(
		"rl_%s_%d",
		c.managedApplyInstanceID(),
		c.seq.Add(1),
	)
}

// reserveManagedApplyIdempotency records the client binding at the write
// linearization point. Callers invoke it only after all side-effect-free
// validation/CAS gates have succeeded and while holding the same coordinator
// mutation lock that guards the first write. A request without a key is a no-op.
func (c *ConfigApplyCoordinator) reserveManagedApplyIdempotency(reqCtx admin.ApplyRequestContext, applyID string, mode ApplyMode) error {
	if reqCtx.Idempotency == nil {
		return nil
	}
	if c.OnManagedApplyAdmitted == nil {
		return errors.New("managed apply idempotency admission is unavailable")
	}
	return c.OnManagedApplyAdmitted(admin.ManagedApplyAdmission{
		Context: reqCtx,
		ApplyID: applyID,
		Mode:    string(mode),
	})
}

func (c *ConfigApplyCoordinator) abortManagedApplyIdempotency(reqCtx admin.ApplyRequestContext, applyID string) {
	if reqCtx.Idempotency == nil || applyID == "" || c.OnManagedApplyAdmissionAborted == nil {
		return
	}
	c.OnManagedApplyAdmissionAborted(applyID)
}

// notifyManagedApplyStarted registers the provisional pending record for a
// managed apply the moment the candidate is persisted and the reload is
// enqueued (AC-02). It projects the provisional saved_not_live result into the
// admin shape so the composition root can insert an exact-ID pending ledger
// record before the synchronous HTTP path can return a 202. A nil hook is a
// no-op so context-free and unit-test callers are unaffected. A non-nil error
// is a transaction-tracking failure after persistence; the caller carries it
// into terminal finalization rather than rolling back an accepted reload.
func (c *ConfigApplyCoordinator) notifyManagedApplyStarted(reqCtx admin.ApplyRequestContext, result ApplyResult) error {
	if c.OnManagedApplyStarted == nil {
		return nil
	}
	return c.OnManagedApplyStarted(admin.ManagedApplyStart{
		Context: reqCtx,
		Result:  toAdminConfigApplyResult(result),
	})
}

// notifyManagedApplyComplete invokes the single composition-root completion
// callback with the ManagedApplyCompletion object and returns the resulting
// ManagedApplyFinalization. A nil callback yields a zero finalization so
// context-free and unit-test callers are unaffected.
//
// WS02 §3.6: a callback panic is made EXPLICIT rather than silently discarded.
// The recovered panic is reconstructed into a FinalizationError on the returned
// finalization (so it is threaded onto the terminal result and surfaced through
// the ledger/overview) and, when wired, reported to OnManagedApplyFinalizationError
// so the composition root can emit a structured error log, increment the
// finalization-error metric, set an advisory health state, and best-effort
// write a terminal ledger record carrying the FinalizationError. A finalization
// panic never fails an already-committed apply: the raw configuration stays
// roll-back-able and the coordinator finalizer is not wedged.
func (c *ConfigApplyCoordinator) notifyManagedApplyComplete(comp admin.ManagedApplyCompletion) (fin admin.ManagedApplyFinalization) {
	if c.OnManagedApplyComplete == nil {
		return fin
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			fin.FinalizationError = fmt.Sprintf(
				"managed apply finalization panic: %v",
				recovered,
			)
			if c.OnManagedApplyFinalizationError != nil {
				c.OnManagedApplyFinalizationError(
					comp,
					errors.New(fin.FinalizationError),
				)
			}
		}
	}()

	return c.OnManagedApplyComplete(comp)
}

// completeManagedApply drives the single terminal completion for a managed
// apply: it hands the trusted composition root a ManagedApplyCompletion
// (request context, serialized terminal result, and the exact prior on-disk
// configuration) and threads the returned ManagedApplyFinalization provenance
// (history snapshot id, history degradation, and any post-persistence
// finalization error) back onto the terminal result. previousRaw is sensitive
// and is forwarded only to the callback, never logged or retained here. A nil
// callback yields a zero finalization so context-free and unit-test callers are
// unaffected.
func (c *ConfigApplyCoordinator) completeManagedApply(reqCtx admin.ApplyRequestContext, result ApplyResult, previousRaw []byte) ApplyResult {
	// Managed finalization writes history and publishes audit/metric/ledger
	// truth. Keep those side effects ordered even though #226 deliberately
	// releases the config mutation gate before terminal publication.
	c.finalizeMu.Lock()
	defer c.finalizeMu.Unlock()

	// ADR 0019 §16: config_state is computed once, here — the single point
	// every managed apply/adopt path funnels through at terminalization —
	// rather than re-derived independently by each surface.
	result.ConfigState, _ = c.currentConfigState()

	fin := c.notifyManagedApplyComplete(admin.ManagedApplyCompletion{
		Context:     reqCtx,
		Result:      toAdminConfigApplyResult(result),
		PreviousRaw: append([]byte(nil), previousRaw...),
	})
	result.HistorySnapshotID = fin.HistorySnapshotID
	result.HistoryError = fin.HistoryError
	result.FinalizationError = fin.FinalizationError
	return result
}

// logPendingRegistrationFailure makes a post-persistence pending-registration
// write failure explicit (WS06 §7.6). The error is still carried onto the
// terminal result for the ledger/overview; this routes the same failure to the
// composition root's structured log and finalization-error metric at the point
// it happens.
func (c *ConfigApplyCoordinator) logPendingRegistrationFailure(id string, err error) {
	if c.ReportManagedApplyError != nil {
		c.ReportManagedApplyError(id, "pending", err)
	}
}
