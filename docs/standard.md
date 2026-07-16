# omniswitch.dev/v1

OmniSwitch's long-term moat is a portable policy and decision format. The current MVP supports two document kinds.

## Policy

```yaml
apiVersion: omniswitch.dev/v1
kind: Policy
metadata:
  name: production-delete
  version: v1
spec:
  match:
    tool: github
    action: delete
    environment: production
  effect: deny
  reason: Repository {{resource.name}} is protected.
```

`spec.match` is compiled into CEL. Advanced users can provide `spec.cel` directly.

## DecisionTrace

```yaml
apiVersion: omniswitch.dev/v1
kind: DecisionTrace
metadata:
  decisionId: dec_...
  timestamp: 2026-07-08T00:00:00Z
spec:
  request: {}
  policy:
    name: production-delete
    version: v1
    hash: sha256:...
  result:
    effect: deny
    allowed: false
    reason: Repository payments-prod is protected.
    evaluationMs: 0.42
  trace:
    - rule: production-delete
      matched: true
      effect: deny
```

This is the basis for `omniswitch replay`, `omniswitch diff`, incident investigation, policy simulation, and future compliance evidence.
