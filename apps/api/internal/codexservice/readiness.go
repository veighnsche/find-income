package codexservice

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
)

// CheckRound is the rounds.Start/Resume readiness gate for commissioned
// discovery, preparation and process input. It checks the live connection, ChatGPT account, selected
// model/effort, quota and required MCP tools, but starts no work. The caller
// must still explicitly commission ExecuteRoundTurn through the work loop.
func (s *Service) CheckRound(ctx context.Context, outcome string) error {
	if s == nil || ctx == nil || outcome != "discover" && outcome != "prepare" && outcome != "process_input" && outcome != "compare_offers" {
		return ErrUnavailable
	}
	if outcome == "prepare" {
		s.mu.Lock()
		configured := s.packConfig.ProjectRoot != "" && s.packConfig.TypstPath != "" && s.packConfig.PrivateTempDir != ""
		s.mu.Unlock()
		if !configured {
			return ErrUnavailable
		}
	}
	status := s.Status(ctx)
	if status.State != "ready" || !status.Connected || ctx.Err() != nil {
		return ErrUnavailable
	}
	return nil
}

// UsageWindow contains only non-secret quota metadata. Missing numbers remain nil.
type UsageWindow struct {
	LimitID            string   `json:"limitId"`
	Window             string   `json:"window"`
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins *int64   `json:"windowDurationMins"`
	ResetsAt           *int64   `json:"resetsAt"`
}

func (s *Service) statusLocked(ctx context.Context) Status {
	out := Status{State: "unavailable", Busy: s.busy, Model: s.cfg.Model, Effort: s.cfg.Effort, Usage: []UsageWindow{}}
	if s.disconnecting || s.logoutRequired {
		out.Code = "disconnect_pending"
		return out
	}
	client, reason := s.ensureLocked(ctx)
	if reason != "" {
		out.Code = reason
		return out
	}
	account, err := client.ReadAccount(ctx)
	if err != nil {
		out.Code = "account_unavailable"
		return out
	}
	// An active turn controller owns events during a run. Login is prohibited
	// then, so status must never steal a terminal turn notification.
	if !s.busy {
		s.drainAccountEventsLocked(client)
	}
	if s.login != nil {
		if time.Since(s.loginAt) > 10*time.Minute {
			_ = s.cancelLoginLocked(ctx, "login_attempt_timed_out")
		} else if s.loginSucceeded && s.loginGeneration == client.Generation() && account.Account != nil && account.Account.Type == "chatgpt" {
			s.login, s.loginCode, s.loginSucceeded, s.loginGeneration = nil, "", false, 0
		} else {
			out.State, out.Code = "connecting", "owner_sign_in_pending"
			return out
		}
	}
	if s.logoutRequired {
		out.Code = "disconnect_pending"
		return out
	}
	if s.loginCode != "" && account.Account != nil {
		// A late completion of a cancelled/failed attempt cannot silently
		// authenticate the service, even if account/read now sees ChatGPT.
		if err := s.logoutLocked(ctx); err != nil {
			out.Code = "disconnect_pending"
			return out
		}
		account.Account = nil
	}
	if s.loginCode != "" || account.Account == nil {
		out.State, out.Code = "needs_sign_in", "needs_sign_in"
		if s.loginCode != "" {
			out.Code = s.loginCode
		}
		return out
	}
	if account.Account.Type != "chatgpt" {
		out.Code = "chatgpt_account_required"
		return out
	}
	out.Connected = true
	if strings.TrimSpace(s.cfg.Model) == "" || strings.TrimSpace(s.cfg.Effort) == "" {
		out.Code = "model_not_configured"
		return out
	}
	if code := s.checkModel(ctx, client); code != "" {
		out.Code = code
		return out
	}
	if limits, err := client.ReadRateLimits(ctx); err == nil {
		var exhausted bool
		out.Usage, exhausted = projectUsage(limits, time.Now())
		for _, window := range out.Usage {
			if window.LimitID == "codex" {
				out.UsageAvailable = true
				break
			}
		}
		if exhausted {
			out.State, out.Code = "blocked", "usage_exhausted"
			return out
		}
	} else {
		out.Code = "usage_unavailable"
		return out
	}
	if err := s.checkTools(ctx, client); err != nil {
		out.Code = "required_tools_unavailable"
		return out
	}
	// The old standalone intake dispatcher was retired. A commissioned round
	// executor must be bound before execution can be advertised as available.
	out.State, out.Code = "ready", "ready"
	return out
}

func (s *Service) checkModel(ctx context.Context, client *codex.Client) string {
	var cursor *string
	for page := 0; page < 10; page++ {
		models, err := client.ListModels(ctx, cursor)
		if err != nil {
			return "models_unavailable"
		}
		for _, model := range models.Data {
			if model.Model != s.cfg.Model {
				continue
			}
			for _, effort := range model.SupportedReasoningEfforts {
				if effort.ReasoningEffort == s.cfg.Effort {
					return ""
				}
			}
			return "effort_unavailable"
		}
		if models.NextCursor == nil {
			return "model_unavailable"
		}
		cursor = models.NextCursor
	}
	return "models_unavailable"
}

func projectUsage(limits codex.RateLimits, now time.Time) ([]UsageWindow, bool) {
	out := []UsageWindow{}
	exhausted := false
	add := func(id string, snapshot codex.LimitSnapshot, controlsDispatch bool) {
		for _, pair := range []struct {
			name  string
			value *codex.LimitWindow
		}{{"primary", snapshot.Primary}, {"secondary", snapshot.Secondary}} {
			w := pair.value
			if w == nil {
				continue
			}
			if w.UsedPercent != nil && (math.IsNaN(*w.UsedPercent) || math.IsInf(*w.UsedPercent, 0) || *w.UsedPercent < 0 || *w.UsedPercent > 100) {
				continue
			}
			if w.WindowDurationMins != nil && *w.WindowDurationMins <= 0 || w.ResetsAt != nil && *w.ResetsAt <= 0 {
				continue
			}
			if w.UsedPercent == nil && w.WindowDurationMins == nil && w.ResetsAt == nil {
				continue
			}
			out = append(out, UsageWindow{id, pair.name, w.UsedPercent, w.WindowDurationMins, w.ResetsAt})
			if controlsDispatch && w.UsedPercent != nil && *w.UsedPercent >= 100 && (w.ResetsAt == nil || *w.ResetsAt > now.Unix()) {
				exhausted = true
			}
		}
	}
	if len(limits.ByLimitID) > 0 {
		keys := make([]string, 0, len(limits.ByLimitID))
		for key := range limits.ByLimitID {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			add(key, limits.ByLimitID[key], key == "codex")
		}
	} else if limits.RateLimits != nil {
		id := "codex"
		if limits.RateLimits.LimitID != nil {
			id = *limits.RateLimits.LimitID
		}
		add(id, *limits.RateLimits, true)
	}
	return out, exhausted
}

// Consume bounded idle notifications without keeping a conversation archive.
// Completion is accepted only for the current attempt on this client generation.
func (s *Service) drainAccountEventsLocked(client *codex.Client) {
	for {
		select {
		case event, ok := <-client.Events():
			if !ok {
				return
			}
			if event.Request != nil {
				_ = client.Close()
				return
			}
			if event.Method != "account/login/completed" {
				continue
			}
			var result struct {
				LoginID *string `json:"loginId"`
				Success *bool   `json:"success"`
			}
			if json.Unmarshal(event.Params, &result) != nil || result.Success == nil {
				_ = client.Close()
				return
			}
			if s.login == nil || s.loginGeneration != client.Generation() || result.LoginID == nil || *result.LoginID != s.login.LoginID {
				continue
			}
			if *result.Success {
				s.loginSucceeded = true
			} else {
				s.login, s.loginGeneration, s.loginCode = nil, 0, "login_attempt_failed"
			}
		default:
			return
		}
	}
}
