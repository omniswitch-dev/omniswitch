package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

const (
	// defaultEstimatedOutputTokens is the output estimate for a request that
	// sets neither max_tokens nor max_completion_tokens.
	defaultEstimatedOutputTokens = 1024
	// maxEstimatedOutputTokens bounds a client-supplied max_tokens.
	maxEstimatedOutputTokens = 1 << 20
	// budgetFailOpenLogEvery rate-limits the fail-open log line.
	budgetFailOpenLogEvery = 30 * time.Second

	budgetKeyContextKey         contextKey = "budget_key_limits"
	budgetReservationContextKey contextKey = "budget_reservation"

	budgetUnavailableMessage = "budget state is temporarily unavailable; request rejected"
)

// keyBudgetLimits are the budgets of an API key as loaded by the auth
// middleware, so admission needs no extra key lookup.
type keyBudgetLimits struct {
	keyID  string
	limits budgetLimits
}

// withAPIKeyBudget records the budgets of the authenticated API key.
func withAPIKeyBudget(ctx context.Context, key store.APIKey) context.Context {
	return context.WithValue(ctx, budgetKeyContextKey, keyBudgetLimits{keyID: key.ID, limits: apiKeyBudgetLimits(key)})
}

func apiKeyBudgetLimits(key store.APIKey) budgetLimits {
	return budgetLimits{
		LifetimeCost:  key.BudgetUSD,
		MonthlyCost:   key.MonthlyCostBudget,
		MonthlyTokens: int64(key.MonthlyTokenBudget),
	}
}

// budgetHint returns the auth-loaded limits of keyID, or nil.
func budgetHint(ctx context.Context, keyID string) *budgetLimits {
	if value, ok := ctx.Value(budgetKeyContextKey).(keyBudgetLimits); ok && value.keyID == keyID {
		limits := value.limits
		return &limits
	}
	return nil
}

func withBudgetReservation(ctx context.Context, res *budgetReservation) context.Context {
	if res == nil {
		return ctx
	}
	return context.WithValue(ctx, budgetReservationContextKey, res)
}

func budgetReservationFromContext(ctx context.Context) *budgetReservation {
	if ctx == nil {
		return nil
	}
	res, _ := ctx.Value(budgetReservationContextKey).(*budgetReservation)
	return res
}

// storeBudgetLoader reads an API key's lifetime spend and, when it has a
// monthly budget, its usage in the month that starts at month.
func storeBudgetLoader(st *store.Store) budgetLoader {
	return func(ctx context.Context, scope budgetScope, month time.Time, withMonthly bool) (budgetSnapshot, error) {
		if scope.Kind != budgetScopeAPIKey {
			return budgetSnapshot{}, fmt.Errorf("unsupported budget scope %q", scope.Kind)
		}
		key, err := st.GetAPIKeyByID(ctx, scope.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return budgetSnapshot{}, nil
		}
		if err != nil {
			return budgetSnapshot{}, err
		}
		snap := budgetSnapshot{Found: true, Limits: apiKeyBudgetLimits(key), LifetimeCost: key.SpendUSD}
		if withMonthly || snap.Limits.monthly() {
			usage, err := st.GetBudgetUsage(ctx, scope.ID, month)
			if err != nil {
				return budgetSnapshot{}, err
			}
			snap.Month = month
			snap.Monthly = budgetAmount{Cost: usage.Cost, Tokens: int64(usage.Tokens)}
		}
		return snap, nil
	}
}

// budgetState holds the handler's budget ledger and its settings.
type budgetState struct {
	ledger      *budgetLedger
	failOpen    atomic.Bool
	lastOpenLog atomic.Int64
}

// SetBudgetFailOpen chooses what happens when the budget state of a key
// that has a budget cannot be read: false (the default) rejects the request
// with HTTP 503, true lets it through.
func (h *Handler) SetBudgetFailOpen(failOpen bool) {
	h.budget.failOpen.Store(failOpen)
}

// SetBudgetRefreshInterval sets how often a key's committed usage is re-read
// from the store; spend from other replicas becomes visible within it.
// Values <= 0 restore the default (5s).
func (h *Handler) SetBudgetRefreshInterval(interval time.Duration) {
	h.budget.ledger.setRefreshInterval(interval)
}

// reserveBudget admits a request of apiKeyID with the given estimate. The
// returned reservation (possibly nil) must be released when the request
// ends; logRequest settles it with the actual cost.
func (h *Handler) reserveBudget(ctx context.Context, apiKeyID string, estimate budgetAmount) (*budgetReservation, budgetVerdict) {
	if h.store == nil || apiKeyID == "" || h.budget == nil {
		return nil, budgetVerdict{}
	}
	res, verdict, err := h.budget.ledger.admit(ctx, budgetScope{Kind: budgetScopeAPIKey, ID: apiKeyID}, budgetHint(ctx, apiKeyID), estimate)
	if !verdict.Unavailable {
		return res, verdict
	}
	if h.budget.failOpen.Load() {
		h.logBudgetFailOpen(apiKeyID, err)
		return nil, budgetVerdict{}
	}
	log.Printf("budget check failed for API key %q, rejecting request: %v", apiKeyID, err)
	verdict.Reason = budgetUnavailableMessage
	return nil, verdict
}

func (h *Handler) logBudgetFailOpen(apiKeyID string, err error) {
	now := time.Now().UnixNano()
	last := h.budget.lastOpenLog.Load()
	if now-last < int64(budgetFailOpenLogEvery) || !h.budget.lastOpenLog.CompareAndSwap(last, now) {
		return
	}
	log.Printf("budget check failed for API key %q, allowing request because budget fail-open is enabled (further messages suppressed for %s): %v",
		apiKeyID, budgetFailOpenLogEvery, err)
}

// writeBudgetVerdict writes the error response of a rejecting verdict.
func writeBudgetVerdict(w http.ResponseWriter, verdict budgetVerdict) {
	if verdict.Unavailable {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]string{"message": verdict.Reason, "type": "budget_unavailable", "code": "budget_unavailable"},
		})
		return
	}
	writeJSON(w, http.StatusPaymentRequired, map[string]any{
		"error": map[string]string{"message": verdict.Reason, "type": "budget_exceeded", "code": "budget_exceeded"},
	})
}

func (v budgetVerdict) rejected() bool {
	return v.Denied || v.Unavailable
}

// beginBudgetCharge records the cost of a logged request in the ledger
// before the store writes start, releasing the request's reservation. The
// charge follows the store's rules: cache hits are free, every other row
// counts toward the monthly usage, and positive costs toward spend_usd.
func (h *Handler) beginBudgetCharge(ctx context.Context, entry store.RequestLog) *budgetPending {
	res := budgetReservationFromContext(ctx)
	if h.budget == nil || entry.APIKeyID == "" {
		res.release()
		return nil
	}
	charge := budgetCharge{Month: budgetMonthStart(entry.Timestamp)}
	if !entry.Cached {
		charge.Monthly = budgetAmount{Cost: entry.Cost, Tokens: int64(entry.TotalTokens)}
		if entry.Cost > 0 {
			charge.Lifetime = entry.Cost
		}
	}
	return h.budget.ledger.beginCharge(budgetScope{Kind: budgetScopeAPIKey, ID: entry.APIKeyID}, res, charge)
}

// finishBudgetCharge completes a charge after the store writes and fires
// budget alerts.
func (h *Handler) finishBudgetCharge(ctx context.Context, keyID string, pending *budgetPending, logErr, spendErr error) {
	if pending == nil {
		return
	}
	limits, lifetime, monthly := pending.finish(logErr == nil, spendErr == nil)
	alerts := h.loadSettings(ctx).alerts
	if alerts == nil || pending.charge.Monthly.Cost <= 0 {
		return
	}
	alerts.CheckBudget(keyID, lifetime, limits.LifetimeCost)
	if limits.MonthlyCost > 0 {
		alerts.CheckBudget(keyID+" (monthly "+pending.charge.Month.Format("2006-01")+")", monthly.Cost, limits.MonthlyCost)
	}
}

// estimateChatBudget estimates the cost and tokens of a chat request from
// the request size and its output limit, priced like the gateway prices the
// response. An unknown price yields a zero cost estimate.
func (h *Handler) estimateChatBudget(req provider.ChatRequest, providerHint string) budgetAmount {
	input := estimateTextTokens(req.Messages) + estimateTextTokens(req.Tools) + 4*len(req.Messages)
	output := defaultEstimatedOutputTokens
	if req.MaxCompletionTokens != nil {
		output = *req.MaxCompletionTokens
	} else if req.MaxTokens != nil {
		output = *req.MaxTokens
	}
	if output < 0 {
		output = 0
	}
	if output > maxEstimatedOutputTokens {
		output = maxEstimatedOutputTokens
	}
	return h.priceEstimate(req.Model, providerHint, input, output)
}

// estimateInputBudget estimates a request that only has input tokens
// (embeddings, rerank).
func (h *Handler) estimateInputBudget(model, providerHint string, input ...any) budgetAmount {
	tokens := 0
	for _, value := range input {
		tokens += estimateTextTokens(value)
	}
	return h.priceEstimate(model, providerHint, tokens, 0)
}

func (h *Handler) priceEstimate(model, providerHint string, input, output int) budgetAmount {
	usage := provider.Usage{PromptTokens: input, CompletionTokens: output, TotalTokens: input + output}
	var cost float64
	switch {
	case providerHint != "":
		cost = provider.EstimateCost(providerHint, model, usage)
	case h.registry != nil:
		if owner, err := h.registry.ResolveModel(model); err == nil {
			cost = provider.EstimateCost(owner.Name(), model, usage)
		}
		if cost == 0 {
			// The routed provider is not known yet: use the highest price
			// any registered provider has for the model.
			for _, name := range h.registry.Names() {
				if c := provider.EstimateCost(name, model, usage); c > cost {
					cost = c
				}
			}
		}
	}
	return budgetAmount{Cost: cost, Tokens: int64(input + output)}
}

// estimateTextTokens approximates the token count of a JSON-encodable value
// as one token per four bytes of its JSON form, rounded up.
func estimateTextTokens(value any) int {
	if value == nil {
		return 0
	}
	if text, ok := value.(string); ok {
		return (len(text) + 3) / 4
	}
	body, err := json.Marshal(value)
	if err != nil || string(body) == "null" {
		return 0
	}
	return (len(body) + 3) / 4
}
