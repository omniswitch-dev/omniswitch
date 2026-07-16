import React, { useState } from 'react';
import { CheckCircle2, XCircle, Shield, ArrowRight, Layers, Zap, Activity, Cpu, Key, HelpCircle } from 'lucide-react';

const categories = [
  { id: 'all', name: 'All Features' },
  { id: 'gateway', name: 'Gateway & Routing' },
  { id: 'security', name: 'Security & Guardrails' },
  { id: 'ops', name: 'Observability & Prompts' },
  { id: 'protocols', name: 'MCP & Agent Protocols' }
];

const comparisonData = [
  {
    area: 'Unified Inference API',
    category: 'gateway',
    desc: 'Unifies API requests across different cloud and local LLM backends.',
    omniswitch: { status: 'partial', detail: '/v1/chat/completions, /v1/models, /v1/embeddings text & core structures.' },
    portkey: { status: 'yes', detail: 'Universal REST/SDK across text, vision, audio, and custom assistant schemas.' },
    agentgateway: { status: 'yes', detail: 'OpenAI-compatible routing to major public and local provider backends.' }
  },
  {
    area: 'Provider Coverage',
    category: 'gateway',
    desc: 'Support for cloud providers (OpenAI, Anthropic, Gemini) and local tools (Ollama, vLLM).',
    omniswitch: { status: 'yes', detail: 'Native OpenAI, Anthropic, Google, Groq, plus any custom OpenAI-compatible endpoint.' },
    portkey: { status: 'yes', detail: 'Extensive provider catalog, managed virtual key rotation, and custom hosts.' },
    agentgateway: { status: 'yes', detail: 'OpenAI, Anthropic, Gemini, Bedrock, Vertex AI, and native custom targets.' }
  },
  {
    area: 'Advanced Routing & Load Balancing',
    category: 'gateway',
    desc: 'Fallback mechanisms, retries, load balancing, and canary/weighted splits.',
    omniswitch: { status: 'yes', detail: 'Weighted variants (A/B testing), conditional rules (CEL), retries, fallbacks, circuit breakers, and shadow routing.' },
    portkey: { status: 'yes', detail: 'Config-driven load balancing, fallbacks, retries, and latency-aware routing.' },
    agentgateway: { status: 'yes', detail: 'Failover, load balancing, and Kubernetes inference-aware scheduling.' }
  },
  {
    area: 'Shadow Routing (Async Compare)',
    category: 'gateway',
    desc: 'Asynchronously mirror traffic to a secondary model to compare outputs without affecting users.',
    omniswitch: { status: 'yes', detail: 'Fully supported natively via config-as-code or header overrides.' },
    portkey: { status: 'no', detail: 'Does not support native shadow model comparisons in the open-source gateway.' },
    agentgateway: { status: 'no', detail: 'Not supported in standard routing rules.' }
  },
  {
    area: 'Real-Time Guardrails',
    category: 'security',
    desc: 'Input/output scanning to prevent prompt injections, toxic content, and data leaks.',
    omniswitch: { status: 'yes', detail: 'Sub-millisecond local guardrail engine for PII, SQL injections, secrets, and regex filters.' },
    portkey: { status: 'yes', detail: '50+ built-in managed guardrails, partner integrations, and custom webhook filters.' },
    agentgateway: { status: 'yes', detail: 'Regex filters, Bedrock Guardrails, and custom validation targets.' }
  },
  {
    area: 'Virtual Key Management',
    category: 'security',
    desc: 'Virtual API keys to secure backend credentials with custom budget limits.',
    omniswitch: { status: 'yes', detail: 'AES-256-GCM encrypted Virtual Key Vault with token, cost, and rate limit quotas.' },
    portkey: { status: 'yes', detail: 'Turnkey virtual keys with project-level governance and cost tracking.' },
    agentgateway: { status: 'yes', detail: 'OIDC, TLS, JWT auth, and credential management.' }
  },
  {
    area: 'Semantic Caching',
    category: 'ops',
    desc: 'Vector-similarity caching to serve queries that are semantically identical, saving cost and time.',
    omniswitch: { status: 'yes', detail: 'Built-in local SQLite exact-match and semantic caching with configurable thresholds.' },
    portkey: { status: 'yes', detail: 'Global semantic cache powered by vector databases.' },
    agentgateway: { status: 'no', detail: 'No primary vector/semantic caching built into the proxy data plane.' }
  },
  {
    area: 'Observability & Tracing',
    category: 'ops',
    desc: 'Logging request/response payloads, tracing agent steps, and exporting metrics.',
    omniswitch: { status: 'yes', detail: 'SQLite logging, trace/session tags, feedback API, Prometheus metrics, and OpenTelemetry (OTLP) export.' },
    portkey: { status: 'yes', detail: 'SaaS logging console, deep traces, LLM analytics dashboard, and OTel support.' },
    agentgateway: { status: 'yes', detail: 'OpenTelemetry integration, native Envoy stats, and protocol telemetry.' }
  },
  {
    area: 'Prompt Management & Versioning',
    category: 'ops',
    desc: 'Centralize, version-control, and dynamically render prompts without code changes.',
    omniswitch: { status: 'yes', detail: 'Declartive prompt templates with version-history and runtime rendering.' },
    portkey: { status: 'yes', detail: 'Prompt Engineering Studio with testing, versioning, and canary rollouts.' },
    agentgateway: { status: 'no', detail: 'Only basic prompt enrichment at the router level.' }
  },
  {
    area: 'Model Context Protocol (MCP) Gateway',
    category: 'protocols',
    desc: 'Securely expose tool calls and local resources to AI agents.',
    omniswitch: { status: 'yes', detail: 'Built-in HTTP MCP target federation and policy-gated tool execution.' },
    portkey: { status: 'yes', detail: 'Centralized authentication and tracking of MCP servers.' },
    agentgateway: { status: 'yes', detail: 'Native MCP tool discovery, versioning, RBAC, and audit logs.' }
  },
  {
    area: 'Agent-to-Agent (A2A) Routing',
    category: 'protocols',
    desc: 'Native routing and identity protocols between autonomous agents.',
    omniswitch: { status: 'no', detail: 'Not currently implemented; focusing on server/gateway boundaries.' },
    portkey: { status: 'no', detail: 'Not supported natively in the gateway data plane.' },
    agentgateway: { status: 'yes', detail: 'Supports Agent-to-Agent protocol, routing between LangChain, CrewAI, etc.' }
  },
  {
    area: 'High Availability & Deployment',
    category: 'ops',
    desc: 'Scalability, zero external dependencies, and deployment structure.',
    omniswitch: { status: 'yes', detail: 'Zero external dependencies (SQLite embedded). Deployable as a single Go binary.' },
    portkey: { status: 'partial', detail: 'Managed SaaS or complex self-hosted stack (requires Redis, Postgres, etc.).' },
    agentgateway: { status: 'yes', detail: 'High-availability Rust proxy deployable in Kubernetes or standalone.' }
  }
];

export default function Comparison() {
  const [activeTab, setActiveTab] = useState('all');

  const filteredData = activeTab === 'all' 
    ? comparisonData 
    : comparisonData.filter(d => d.category === activeTab);

  return (
    <div className="api-container">
      <div className="api-header reveal active">
        <div className="section-badge">Comparison</div>
        <h1>OmniSwitch vs. Portkey vs. AgentGateway</h1>
        <p className="docs-intro" style={{ textAlign: 'center', maxWidth: '650px', margin: '0.5rem auto 2rem' }}>
          An objective capability map of open-source and enterprise AI gateway solutions to help you pick the right stack.
        </p>
      </div>

      {/* Category selector */}
      <div className="code-tabs" style={{ justifyContent: 'center', marginBottom: '2.5rem', flexWrap: 'wrap' }}>
        {categories.map(c => (
          <span 
            key={c.id} 
            className={`code-tab ${activeTab === c.id ? 'active' : ''}`}
            onClick={() => setActiveTab(c.id)}
          >
            {c.name}
          </span>
        ))}
      </div>

      {/* Main comparison grid */}
      <div className="reveal active" style={{ display: 'flex', flexDirection: 'column', gap: '1.5rem' }}>
        {filteredData.map((row, idx) => (
          <div key={idx} className="endpoint-card" style={{ borderLeft: '3px solid var(--accent)' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', flexWrap: 'wrap', gap: '1rem', marginBottom: '1rem' }}>
              <div>
                <h3 style={{ fontSize: '1.25rem', color: 'white', fontFamily: 'Outfit' }}>{row.area}</h3>
                <p style={{ color: 'var(--text-secondary)', fontSize: '0.85rem', marginTop: '0.25rem' }}>{row.desc}</p>
              </div>
            </div>

            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: '1rem', marginTop: '1rem' }}>
              {/* OmniSwitch Column */}
              <div style={{ background: 'rgba(142, 45, 226, 0.04)', border: '1px solid rgba(142, 45, 226, 0.15)', padding: '1rem', borderRadius: '8px' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
                  <Shield size={16} style={{ color: 'var(--accent-light)' }} />
                  <span style={{ fontWeight: '700', fontSize: '0.85rem', color: 'white' }}>OmniSwitch</span>
                  <div style={{ marginLeft: 'auto' }}>
                    {row.omniswitch.status === 'yes' && <CheckCircle2 size={16} style={{ color: '#10b981' }} />}
                    {row.omniswitch.status === 'partial' && <HelpCircle size={16} style={{ color: '#f59e0b' }} />}
                    {row.omniswitch.status === 'no' && <XCircle size={16} style={{ color: '#ef4444' }} />}
                  </div>
                </div>
                <p style={{ fontSize: '0.8rem', color: '#c0c0c0', lineHeight: '1.5' }}>{row.omniswitch.detail}</p>
              </div>

              {/* Portkey Column */}
              <div style={{ background: 'rgba(255, 255, 255, 0.01)', border: '1px solid var(--glass-border)', padding: '1rem', borderRadius: '8px' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
                  <span style={{ fontWeight: '600', fontSize: '0.85rem', color: 'var(--text-secondary)' }}>Portkey</span>
                  <div style={{ marginLeft: 'auto' }}>
                    {row.portkey.status === 'yes' && <CheckCircle2 size={16} style={{ color: '#10b981', opacity: 0.8 }} />}
                    {row.portkey.status === 'partial' && <HelpCircle size={16} style={{ color: '#f59e0b', opacity: 0.8 }} />}
                    {row.portkey.status === 'no' && <XCircle size={16} style={{ color: '#ef4444', opacity: 0.6 }} />}
                  </div>
                </div>
                <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', lineHeight: '1.5' }}>{row.portkey.detail}</p>
              </div>

              {/* AgentGateway Column */}
              <div style={{ background: 'rgba(255, 255, 255, 0.01)', border: '1px solid var(--glass-border)', padding: '1rem', borderRadius: '8px' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
                  <span style={{ fontWeight: '600', fontSize: '0.85rem', color: 'var(--text-secondary)' }}>AgentGateway</span>
                  <div style={{ marginLeft: 'auto' }}>
                    {row.agentgateway.status === 'yes' && <CheckCircle2 size={16} style={{ color: '#10b981', opacity: 0.8 }} />}
                    {row.agentgateway.status === 'partial' && <HelpCircle size={16} style={{ color: '#f59e0b', opacity: 0.8 }} />}
                    {row.agentgateway.status === 'no' && <XCircle size={16} style={{ color: '#ef4444', opacity: 0.6 }} />}
                  </div>
                </div>
                <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', lineHeight: '1.5' }}>{row.agentgateway.detail}</p>
              </div>
            </div>
          </div>
        ))}
      </div>

      {/* Parity boundaries detailed section */}
      <div className="reveal active" style={{ marginTop: '4rem', background: 'var(--card-bg)', border: '1px solid var(--glass-border)', borderRadius: '12px', padding: '2.5rem' }}>
        <h3 style={{ fontSize: '1.5rem', color: 'white', fontFamily: 'Outfit', marginBottom: '1rem' }}>Architectural Boundaries</h3>
        <p style={{ color: '#c0c0c0', fontSize: '0.95rem', marginBottom: '1.5rem', lineHeight: '1.7' }}>
          OmniSwitch covers the highest-leverage gateway baseline: secure client auth, declarative routing, semantic caching, embedded credential vaults, and local guardrails. 
          Its biggest strength is the <strong>zero-dependency design</strong>—there is no need to stand up separate databases (like Postgres, Redis, or Qdrant) since it compiles to a single Go binary with an embedded SQLite instance.
        </p>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(280px, 1fr))', gap: '2rem', marginTop: '2rem' }}>
          <div>
            <h4 style={{ color: 'white', fontSize: '1.1rem', marginBottom: '0.5rem', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
              <Zap size={18} style={{ color: 'var(--cyan)' }} /> When to choose OmniSwitch
            </h4>
            <ul className="docs-list" style={{ fontSize: '0.85rem' }}>
              <li>You want complete local data-plane control (zero external telemetry).</li>
              <li>You prefer simple single-binary deployments with zero infrastructure overhead.</li>
              <li>You need advanced features like shadow routing and semantic caching completely local.</li>
            </ul>
          </div>
          <div>
            <h4 style={{ color: 'white', fontSize: '1.1rem', marginBottom: '0.5rem', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
              <Layers size={18} style={{ color: 'var(--accent-light)' }} /> Next roadmap goals
            </h4>
            <ul className="docs-list" style={{ fontSize: '0.85rem' }}>
              <li>Shared storage for multi-instance HA deployments.</li>
              <li>JWT/OIDC credentials & full CEL role authorization.</li>
              <li>External webhook guardrails & streamable HTTP MCP tool execution.</li>
            </ul>
          </div>
        </div>
      </div>
    </div>
  );
}
