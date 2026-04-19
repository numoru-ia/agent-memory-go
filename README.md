# agent-memory-go

Go library implementing the **tiered memory pattern** for LLM agents:
**Redis** for working memory, **Langfuse** for auditable history, **Mem0 / Qdrant** for long-term semantic memory.

Companion to: [Langfuse + Redis + Mem0 como memoria de agentes en producción](https://numoru.com/contribuciones/langfuse-redis-memoria-agentes).

## Install

```bash
go get github.com/numoru-ia/agent-memory-go
```

## Quick start

```go
import "github.com/numoru-ia/agent-memory-go/memory"

wm := memory.NewWorking(redisClient, memory.WorkingOpts{
    TTL: time.Hour,
    WindowSize: 6,
    Summarizer: memory.LiteLLMSummarizer("http://litellm:4000", "claude-haiku"),
})

sm := memory.NewSemantic(memory.Mem0Backend{URL: "http://mem0:8080"})

audit := memory.NewAudit(langfuseClient)

mgr := memory.Manager{Working: wm, Semantic: sm, Audit: audit}

// Per-turn usage
state, _ := mgr.Load(ctx, sessionID)
facts, _ := mgr.Semantic.Search(ctx, userID, userMessage)
prompt := mgr.BuildPrompt(state, facts, userMessage)
// ... call LLM ...
mgr.Append(ctx, state, memory.Turn{Role: "user", Content: userMessage})
mgr.Append(ctx, state, memory.Turn{Role: "assistant", Content: response})
audit.TraceTurn(ctx, state, userMessage, response)
```

## Layers

| Layer | Latency | TTL | Purpose |
|---|---|---|---|
| Working (Redis) | ~3 ms | 1h (configurable) | Rolling window of last N turns + slot-filling |
| Audit (Langfuse sessions) | async write | 90+ days | Full traces, reproducible sessions |
| Semantic (Mem0/Qdrant) | ~30 ms | indefinite | Consolidated facts per user |

## Heuristics

| Signal | Action |
|---|---|
| Regular turn | Working only |
| "Always/prefer/I am X" | Extract to semantic (Mem0) |
| Structured field (email/RFC/date) | Working slot |
| `windowSize + 1` reached | Compact oldest → summary |
| Session idle >1h | Flush to Audit; extract to Semantic |
| User says "forget" | Delete from Semantic |

## Semantic cache (optional)

```go
cache := memory.NewSemanticCache(redisClient, memory.CacheOpts{
    Threshold: 0.92,
    TTL: 24 * time.Hour,
})
response, err := cache.GetOrCompute(ctx, prompt, func() (string, error) {
    return callLLM(ctx, prompt)
})
```

Typical hit rate: 35-55% in support agents.

## License

Apache 2.0
