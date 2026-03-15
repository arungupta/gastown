package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

// ---------------------------------------------------------------------------
// RestartTracker: rate-limit backoff config
// ---------------------------------------------------------------------------

func TestDefaultRateLimitBackoffConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultRateLimitBackoffConfig()

	if cfg.InitialBackoff != 2*time.Minute {
		t.Errorf("InitialBackoff: got %v, want 2m", cfg.InitialBackoff)
	}
	if cfg.MaxBackoff != 20*time.Minute {
		t.Errorf("MaxBackoff: got %v, want 20m", cfg.MaxBackoff)
	}
	if cfg.BackoffMultiplier != 2.0 {
		t.Errorf("BackoffMultiplier: got %v, want 2.0", cfg.BackoffMultiplier)
	}
	if cfg.CrashLoopWindow != 15*time.Minute {
		t.Errorf("CrashLoopWindow: got %v, want 15m", cfg.CrashLoopWindow)
	}
	if cfg.CrashLoopCount != 3 {
		t.Errorf("CrashLoopCount: got %v, want 3", cfg.CrashLoopCount)
	}
	if cfg.StabilityPeriod != 30*time.Minute {
		t.Errorf("StabilityPeriod: got %v, want 30m", cfg.StabilityPeriod)
	}
}

func TestDefaultRateLimitBackoffConfig_DiffersFromNormal(t *testing.T) {
	t.Parallel()
	rl := DefaultRateLimitBackoffConfig()
	normal := DefaultRestartTrackerConfig()

	if rl.InitialBackoff <= normal.InitialBackoff {
		t.Errorf("rate-limit InitialBackoff (%v) should exceed normal (%v)",
			rl.InitialBackoff, normal.InitialBackoff)
	}
	if rl.MaxBackoff <= normal.MaxBackoff {
		t.Errorf("rate-limit MaxBackoff (%v) should exceed normal (%v)",
			rl.MaxBackoff, normal.MaxBackoff)
	}
	if rl.CrashLoopCount >= normal.CrashLoopCount {
		t.Errorf("rate-limit CrashLoopCount (%d) should be stricter (lower) than normal (%d)",
			rl.CrashLoopCount, normal.CrashLoopCount)
	}
}

// ---------------------------------------------------------------------------
// RestartTracker: RecordRateLimitRestart
// ---------------------------------------------------------------------------

func TestRecordRateLimitRestart_LongerInitialBackoff(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	rt.RecordRateLimitRestart("agent-1")

	remaining := rt.GetBackoffRemaining("agent-1")
	// Rate-limit initial backoff is 2 minutes; should be well above 1 minute.
	if remaining <= time.Minute {
		t.Errorf("backoff remaining after rate-limit restart should be > 1m, got %v", remaining)
	}

	// Verify failure reason is set.
	rt.mu.RLock()
	info := rt.state.Agents["agent-1"]
	rt.mu.RUnlock()
	if info == nil {
		t.Fatal("expected agent info to exist")
	}
	if info.LastFailureReason != "rate_limit" {
		t.Errorf("LastFailureReason: got %q, want %q", info.LastFailureReason, "rate_limit")
	}
}

func TestRecordRateLimitRestart_BackoffExceedsNormal(t *testing.T) {
	t.Parallel()
	rtRL := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})
	rtNormal := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	rtRL.RecordRateLimitRestart("agent-1")
	rtNormal.RecordRestart("agent-1")

	rlRemaining := rtRL.GetBackoffRemaining("agent-1")
	normalRemaining := rtNormal.GetBackoffRemaining("agent-1")

	if rlRemaining <= normalRemaining {
		t.Errorf("rate-limit backoff (%v) should exceed normal backoff (%v)",
			rlRemaining, normalRemaining)
	}
}

func TestRecordRateLimitRestart_CrashLoopAfter3(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	for i := 0; i < 3; i++ {
		rt.RecordRateLimitRestart("agent-1")
	}

	if !rt.IsInCrashLoop("agent-1") {
		t.Error("expected agent to be in crash loop after 3 rate-limit restarts")
	}
}

func TestRecordRateLimitRestart_NormalDoesNotCrashLoopAfter3(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	for i := 0; i < 3; i++ {
		rt.RecordRestart("agent-1")
	}

	if rt.IsInCrashLoop("agent-1") {
		t.Error("normal restarts should NOT trigger crash loop after only 3 restarts (threshold is 5)")
	}
}

func TestRecordRateLimitRestart_NormalCrashLoopAfter5(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	for i := 0; i < 5; i++ {
		rt.RecordRestart("agent-1")
	}

	if !rt.IsInCrashLoop("agent-1") {
		t.Error("normal restarts SHOULD trigger crash loop after 5 restarts")
	}
}

func TestRecordRateLimitRestart_NotCrashLoopAfter2(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	for i := 0; i < 2; i++ {
		rt.RecordRateLimitRestart("agent-1")
	}

	if rt.IsInCrashLoop("agent-1") {
		t.Error("2 rate-limit restarts should NOT yet trigger crash loop (threshold is 3)")
	}
}

func TestRecordRateLimitRestart_SetsLastFailureReason(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	// Normal restart first — sets "crash" as failure reason.
	rt.RecordRestart("agent-1")
	rt.mu.RLock()
	info := rt.state.Agents["agent-1"]
	rt.mu.RUnlock()
	if info.LastFailureReason != "crash" {
		t.Errorf("normal restart should set failure reason to 'crash', got %q", info.LastFailureReason)
	}

	// Now rate-limit restart — should set failure reason.
	rt.RecordRateLimitRestart("agent-1")
	rt.mu.RLock()
	info = rt.state.Agents["agent-1"]
	rt.mu.RUnlock()
	if info.LastFailureReason != "rate_limit" {
		t.Errorf("rate-limit restart should set failure reason, got %q", info.LastFailureReason)
	}
}

func TestRecordRateLimitRestart_StabilityResets(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	// Record a rate-limit restart.
	rt.RecordRateLimitRestart("agent-1")

	rt.mu.RLock()
	info := rt.state.Agents["agent-1"]
	rt.mu.RUnlock()
	if info.RestartCount != 1 {
		t.Fatalf("expected RestartCount=1 after first restart, got %d", info.RestartCount)
	}

	// Simulate stability: set LastRestart to well beyond the StabilityPeriod.
	rt.mu.Lock()
	info.LastRestart = time.Now().Add(-45 * time.Minute) // 45min > 30min stability period
	rt.mu.Unlock()

	// Record another rate-limit restart. The count should reset due to stability.
	rt.RecordRateLimitRestart("agent-1")

	rt.mu.RLock()
	info = rt.state.Agents["agent-1"]
	rt.mu.RUnlock()
	// After stability reset the count goes to 0, then incremented to 1.
	if info.RestartCount != 1 {
		t.Errorf("expected RestartCount=1 after stability reset + new restart, got %d", info.RestartCount)
	}
	if !info.CrashLoopSince.IsZero() {
		t.Error("crash loop should have been cleared by stability reset")
	}
}

func TestRecordRateLimitRestart_ExponentialBackoff(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	// Record two rapid rate-limit restarts.
	rt.RecordRateLimitRestart("agent-1")
	first := rt.GetBackoffRemaining("agent-1")

	rt.RecordRateLimitRestart("agent-1")
	second := rt.GetBackoffRemaining("agent-1")

	// Second backoff should be longer due to exponential increase.
	if second <= first {
		t.Errorf("second backoff (%v) should exceed first (%v) due to exponential increase",
			second, first)
	}
}

func TestRecordRateLimitRestart_MaxBackoffCap(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	// Record many restarts to hit the max backoff cap.
	// With 2min initial * 2^N, after enough restarts we should hit 20min cap.
	for i := 0; i < 10; i++ {
		// Prevent crash loop from blocking: clear it each time.
		rt.ClearCrashLoop("agent-1")
		rt.RecordRateLimitRestart("agent-1")
	}

	remaining := rt.GetBackoffRemaining("agent-1")
	maxBackoff := DefaultRateLimitBackoffConfig().MaxBackoff

	// Should be at or near max backoff (20min), not exceeding it.
	if remaining > maxBackoff+time.Second {
		t.Errorf("backoff (%v) should not exceed max (%v)", remaining, maxBackoff)
	}
}

func TestRecordRateLimitRestart_CanRestartFalseInCrashLoop(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	for i := 0; i < 3; i++ {
		rt.RecordRateLimitRestart("agent-1")
	}

	if rt.CanRestart("agent-1") {
		t.Error("CanRestart should return false when agent is in crash loop")
	}
}

func TestRecordRateLimitRestart_CanRestartFalseDuringBackoff(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	rt.RecordRateLimitRestart("agent-1")

	if rt.CanRestart("agent-1") {
		t.Error("CanRestart should return false during active backoff period")
	}
}

func TestRecordRateLimitRestart_NewAgentCreatesInfo(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	// Unknown agent should have no info.
	if rt.IsInCrashLoop("unknown") {
		t.Error("unknown agent should not be in crash loop")
	}
	if rt.GetBackoffRemaining("unknown") != 0 {
		t.Error("unknown agent should have zero backoff remaining")
	}
	if !rt.CanRestart("unknown") {
		t.Error("unknown agent should be restartable")
	}

	// After recording, info should exist.
	rt.RecordRateLimitRestart("unknown")
	if rt.GetBackoffRemaining("unknown") == 0 {
		t.Error("agent should have positive backoff after rate-limit restart")
	}
}

func TestRecordRateLimitRestart_MultipleAgentsIndependent(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	// Put agent-1 in crash loop.
	for i := 0; i < 3; i++ {
		rt.RecordRateLimitRestart("agent-1")
	}

	// agent-2 should be unaffected.
	if rt.IsInCrashLoop("agent-2") {
		t.Error("agent-2 should not be in crash loop")
	}
	if !rt.CanRestart("agent-2") {
		t.Error("agent-2 should be restartable")
	}
}

// ---------------------------------------------------------------------------
// RestartTracker: RateLimitBackoff field initialization
// ---------------------------------------------------------------------------

func TestNewRestartTracker_RateLimitBackoffInitialized(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	expected := DefaultRateLimitBackoffConfig()
	if rt.rateLimitBackoff.InitialBackoff != expected.InitialBackoff {
		t.Errorf("RateLimitBackoff.InitialBackoff: got %v, want %v",
			rt.rateLimitBackoff.InitialBackoff, expected.InitialBackoff)
	}
	if rt.rateLimitBackoff.CrashLoopCount != expected.CrashLoopCount {
		t.Errorf("RateLimitBackoff.CrashLoopCount: got %v, want %v",
			rt.rateLimitBackoff.CrashLoopCount, expected.CrashLoopCount)
	}
}

// ---------------------------------------------------------------------------
// Pattern matching: matchesRateLimitPattern
// ---------------------------------------------------------------------------

func TestMatchesRateLimitPattern_PositiveMatches(t *testing.T) {
	t.Parallel()
	positives := []struct {
		name string
		line string
	}{
		{"primary_rate_limit", "You've hit your usage limit"},
		{"daily_limit", "You've hit your daily limit"},
		{"resets_time_pm", "limit · resets 7pm"},
		{"resets_time_colon_am", "limit · resets 7:30am"},
		{"stop_and_wait", "Stop and wait for limit to reset"},
		{"api_error_429", "API Error: Rate limit reached"},
		{"oauth_revoked", "OAuth token revoked"},
		{"oauth_expired", "OAuth token has expired"},
		// Case insensitivity
		{"case_insensitive_hit", "you've hit your usage LIMIT"},
		{"case_insensitive_api", "api error: rate limit reached"},
		{"case_insensitive_oauth", "oauth TOKEN revoked"},
		// With surrounding text
		{"wrapped_text", "  Error: You've hit your usage limit. Please wait.  "},
		{"log_prefix", "[2025-01-01 12:00:00] API Error: Rate limit reached"},
	}

	for _, tt := range positives {
		t.Run(tt.name, func(t *testing.T) {
			if !matchesRateLimitPattern(tt.line) {
				t.Errorf("expected %q to match rate-limit pattern", tt.line)
			}
		})
	}
}

func TestMatchesRateLimitPattern_NegativeMatches(t *testing.T) {
	t.Parallel()
	negatives := []struct {
		name string
		line string
	}{
		{"empty_string", ""},
		{"normal_output", "normal output from agent"},
		{"code_comment", "// Check rate limits in the API response"},
		{"discussion", "We should handle rate limits better"},
		{"partial_match_resets", "resets 7pm"}, // missing "limit" prefix
		{"partial_match_limit", "your limit"},  // missing "You've hit"
		{"just_whitespace", "   "},
		{"numbers_only", "42"},
		{"generic_error", "Error: connection refused"},
		{"similar_but_not_matching", "You have reached a milestone"},
		{"oauth_unrelated", "OAuth token created successfully"},
		{"add_funds_without_context", "funds to continue"},
	}

	for _, tt := range negatives {
		t.Run(tt.name, func(t *testing.T) {
			if matchesRateLimitPattern(tt.line) {
				t.Errorf("expected %q to NOT match rate-limit pattern", tt.line)
			}
		})
	}
}

func TestMatchesRateLimitPattern_SpecialCharacters(t *testing.T) {
	t.Parallel()

	// The interpunct (·) in "limit · resets" is a special Unicode character.
	// The pattern uses \s* which matches the spaces around it.
	if !matchesRateLimitPattern("limit · resets 3pm") {
		t.Error("expected interpunct variant to match")
	}

	// Newlines should not affect single-line matching.
	if matchesRateLimitPattern("limit\nresets 3pm") {
		t.Error("newline in middle should not match (separate lines)")
	}
}

func TestMatchesRateLimitPattern_AddFundsOption(t *testing.T) {
	t.Parallel()
	// Exact Claude TUI option text.
	if !matchesRateLimitPattern("Add funds to continue with extra usage") {
		t.Error("expected 'Add funds to continue with extra usage' to match")
	}
}

// ---------------------------------------------------------------------------
// Daemon: rate-limit cooldown (unit-level)
// ---------------------------------------------------------------------------

// newTestDaemon creates a minimal Daemon suitable for unit testing cooldown
// methods. It does not start the heartbeat loop or require tmux.
func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(io.Discard, "", 0),
	}
}

func TestRateLimitCooldown_InitiallyInactive(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t)

	if d.isRateLimitCooldownActive() {
		t.Error("expected cooldown to be inactive on fresh daemon")
	}
}

func TestRateLimitCooldown_SetAndCheck(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t)

	d.setRateLimitCooldown(5 * time.Second)

	if !d.isRateLimitCooldownActive() {
		t.Error("expected cooldown to be active after setRateLimitCooldown")
	}
}

func TestRateLimitCooldown_ExpiresAfterDuration(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t)

	// Set a very short cooldown and verify it expires.
	d.setRateLimitCooldown(1 * time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	if d.isRateLimitCooldownActive() {
		t.Error("expected cooldown to be inactive after duration elapsed")
	}
}

func TestRateLimitCooldown_ResetToZeroIsInactive(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t)

	// Activate, then set to zero.
	d.setRateLimitCooldown(5 * time.Minute)
	d.rateLimitCooldownUntil = time.Time{} // manually reset

	if d.isRateLimitCooldownActive() {
		t.Error("expected cooldown to be inactive after zero-value reset")
	}
}

func TestRateLimitCooldown_ExtendsDuration(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t)

	d.setRateLimitCooldown(1 * time.Minute)
	first := d.rateLimitCooldownUntil

	// Setting a longer cooldown should extend the deadline.
	d.setRateLimitCooldown(10 * time.Minute)
	second := d.rateLimitCooldownUntil

	if !second.After(first) {
		t.Errorf("second cooldown deadline (%v) should be after first (%v)", second, first)
	}
}

func TestRateLimitCooldown_PastDeadlineIsInactive(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t)

	// Set deadline in the past.
	d.rateLimitCooldownUntil = time.Now().Add(-1 * time.Minute)

	if d.isRateLimitCooldownActive() {
		t.Error("cooldown with past deadline should be inactive")
	}
}

// ---------------------------------------------------------------------------
// Config accessor: RateLimitCooldownDurationD
// ---------------------------------------------------------------------------

func TestRateLimitCooldownDurationD_Default(t *testing.T) {
	t.Parallel()

	// Nil DaemonThresholds should return the default 5 minutes.
	var dt *config.DaemonThresholds
	got := dt.RateLimitCooldownDurationD()
	want := 5 * time.Minute

	if got != want {
		t.Errorf("nil DaemonThresholds.RateLimitCooldownDurationD(): got %v, want %v", got, want)
	}
}

func TestRateLimitCooldownDurationD_EmptyStruct(t *testing.T) {
	t.Parallel()

	// Empty struct (no field set) should return the default.
	dt := &config.DaemonThresholds{}
	got := dt.RateLimitCooldownDurationD()
	want := 5 * time.Minute

	if got != want {
		t.Errorf("empty DaemonThresholds.RateLimitCooldownDurationD(): got %v, want %v", got, want)
	}
}

func TestRateLimitCooldownDurationD_Custom(t *testing.T) {
	t.Parallel()

	dt := &config.DaemonThresholds{
		RateLimitCooldownDuration: "10m",
	}
	got := dt.RateLimitCooldownDurationD()
	want := 10 * time.Minute

	if got != want {
		t.Errorf("custom DaemonThresholds.RateLimitCooldownDurationD(): got %v, want %v", got, want)
	}
}

func TestRateLimitCooldownDurationD_InvalidFallsBackToDefault(t *testing.T) {
	t.Parallel()

	dt := &config.DaemonThresholds{
		RateLimitCooldownDuration: "not-a-duration",
	}
	got := dt.RateLimitCooldownDurationD()
	want := config.DefaultRateLimitCooldownDuration

	if got != want {
		t.Errorf("invalid duration should fallback to default: got %v, want %v", got, want)
	}
}

func TestRateLimitCooldownDurationD_ViaOperationalConfig(t *testing.T) {
	t.Parallel()

	// Access through the full OperationalConfig chain, as the daemon does.
	op := &config.OperationalConfig{
		Daemon: &config.DaemonThresholds{
			RateLimitCooldownDuration: "3m",
		},
	}
	got := op.GetDaemonConfig().RateLimitCooldownDurationD()
	want := 3 * time.Minute

	if got != want {
		t.Errorf("via OperationalConfig: got %v, want %v", got, want)
	}
}

func TestRateLimitCooldownDurationD_NilOperationalConfig(t *testing.T) {
	t.Parallel()

	var op *config.OperationalConfig
	got := op.GetDaemonConfig().RateLimitCooldownDurationD()
	want := 5 * time.Minute

	if got != want {
		t.Errorf("nil OperationalConfig: got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Config constant: DefaultRateLimitCooldownDuration
// ---------------------------------------------------------------------------

func TestDefaultRateLimitCooldownDuration_Value(t *testing.T) {
	t.Parallel()

	if config.DefaultRateLimitCooldownDuration != 5*time.Minute {
		t.Errorf("DefaultRateLimitCooldownDuration: got %v, want 5m",
			config.DefaultRateLimitCooldownDuration)
	}
}

// ---------------------------------------------------------------------------
// RestartTracker: persistence round-trip with rate-limit fields
// ---------------------------------------------------------------------------

func TestRecordRateLimitRestart_PersistsAndLoads(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	_ = os.MkdirAll(filepath.Join(townRoot, "daemon"), 0755)

	rt := NewRestartTracker(townRoot, RestartTrackerConfig{})
	rt.RecordRateLimitRestart("agent-1")

	if err := rt.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rt2 := NewRestartTracker(townRoot, RestartTrackerConfig{})
	if err := rt2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	rt2.mu.RLock()
	info := rt2.state.Agents["agent-1"]
	rt2.mu.RUnlock()

	if info == nil {
		t.Fatal("expected agent-1 info to survive persist/load")
	}
	if info.LastFailureReason != "rate_limit" {
		t.Errorf("LastFailureReason after load: got %q, want %q", info.LastFailureReason, "rate_limit")
	}
	if info.RestartCount != 1 {
		t.Errorf("RestartCount after load: got %d, want 1", info.RestartCount)
	}
}

// ---------------------------------------------------------------------------
// RestartTracker: ClearCrashLoop interaction with rate-limit state
// ---------------------------------------------------------------------------

func TestRateLimitCrashLoop_ClearedByClearCrashLoop(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	for i := 0; i < 3; i++ {
		rt.RecordRateLimitRestart("agent-1")
	}
	if !rt.IsInCrashLoop("agent-1") {
		t.Fatal("precondition: agent should be in crash loop")
	}

	rt.ClearCrashLoop("agent-1")

	if rt.IsInCrashLoop("agent-1") {
		t.Error("crash loop should be cleared after ClearCrashLoop")
	}
	if !rt.CanRestart("agent-1") {
		t.Error("agent should be restartable after clearing crash loop")
	}
}

// ---------------------------------------------------------------------------
// RestartTracker: RecordSuccess with rate-limit history
// ---------------------------------------------------------------------------

func TestRateLimitRestart_RecordSuccessResetsAfterStability(t *testing.T) {
	t.Parallel()
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})

	rt.RecordRateLimitRestart("agent-1")

	// Simulate the agent running stably for longer than the stability period.
	rt.mu.Lock()
	info := rt.state.Agents["agent-1"]
	info.LastRestart = time.Now().Add(-45 * time.Minute) // 45m > 30m stability
	rt.mu.Unlock()

	rt.RecordSuccess("agent-1")

	rt.mu.RLock()
	info = rt.state.Agents["agent-1"]
	rt.mu.RUnlock()

	if info.RestartCount != 0 {
		t.Errorf("RestartCount should be 0 after stable period + RecordSuccess, got %d", info.RestartCount)
	}
	if !info.CrashLoopSince.IsZero() {
		t.Error("CrashLoopSince should be zero after stable period + RecordSuccess")
	}
	if !info.BackoffUntil.IsZero() {
		t.Error("BackoffUntil should be zero after stable period + RecordSuccess")
	}
}
