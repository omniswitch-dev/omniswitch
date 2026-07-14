import React from 'react';
import { BookOpen, Terminal, Settings, Shield, Key } from 'lucide-react';
import './index.css';

export default function Docs() {
  return (
    <div className="docs-container">
      <div className="docs-sidebar">
        <h3>Documentation</h3>
        <ul>
          <li><a href="#quickstart" className="active">Quickstart</a></li>
          <li><a href="#configuration">Configuration</a></li>
          <li><a href="#providers">Providers</a></li>
          <li><a href="#guardrails">Guardrails</a></li>
          <li><a href="#vault">Virtual Key Vault</a></li>
        </ul>
      </div>
      
      <div className="docs-content">
        <h1 className="docs-title">OmniSwitch Documentation</h1>
        <p className="docs-intro">Learn how to deploy, configure, and scale OmniSwitch AI Gateway.</p>

        <section id="quickstart" className="docs-section">
          <h2><Terminal size={24} className="section-icon" /> Quickstart</h2>
          <p>Get up and running with OmniSwitch in minutes. You can run OmniSwitch either as a single Go binary or via Docker.</p>
          
          <div className="code-block">
            <div className="code-header">Terminal</div>
            <pre><code>{`# Clone the repository
git clone https://github.com/onlychirag/sentinel-ai-gateway
cd sentinel-ai-gateway

# Start the gateway with an OpenAI Key
OPENAI_API_KEY=sk-your-key go run ./cmd/gateway`}</code></pre>
          </div>

          <p>Once running, your dashboard is available at <code>http://localhost:8080</code> and your AI gateway is ready to receive OpenAI-compatible requests at <code>http://localhost:8080/v1/chat/completions</code>.</p>
        </section>

        <section id="configuration" className="docs-section">
          <h2><Settings size={24} className="section-icon" /> Configuration</h2>
          <p>OmniSwitch can be configured using a declarative YAML file. This allows you to check your configuration into version control (GitOps).</p>
          
          <div className="code-block">
            <div className="code-header">gateway-config.yaml</div>
            <pre><code>{`server:
  listen_addr: ":8080"
  auth_enabled: true
  
cache:
  enabled: true
  ttl: "24h"
  semantic_threshold: 0.95

providers:
  - name: openai
    type: openai
  - name: ollama
    type: custom
    base_url: http://localhost:11434/v1
    models: [llama3.2, mistral]`}</code></pre>
          </div>
          
          <p>Run the gateway with your config: <code>OMNISWITCH_CONFIG=gateway-config.yaml go run ./cmd/gateway</code></p>
        </section>

        <section id="providers" className="docs-section">
          <h2><Globe size={24} className="section-icon" /> Supported Providers</h2>
          <p>OmniSwitch comes with native adapters for the big four, plus a <code>custom</code> provider that lets you connect to any endpoint that follows the OpenAI specification.</p>
          
          <ul className="docs-list">
            <li><strong>OpenAI:</strong> <code>type: openai</code></li>
            <li><strong>Anthropic:</strong> <code>type: anthropic</code></li>
            <li><strong>Google Gemini:</strong> <code>type: google</code></li>
            <li><strong>Groq:</strong> <code>type: groq</code></li>
            <li><strong>Everything Else:</strong> <code>type: custom</code> (Supports Azure, DeepSeek, vLLM, Ollama, Together AI, etc.)</li>
          </ul>
        </section>

        <section id="guardrails" className="docs-section">
          <h2><Shield size={24} className="section-icon" /> Guardrails</h2>
          <p>Guardrails execute locally in microseconds to protect your agents and users. OmniSwitch includes built-in policies evaluated using CEL (Common Expression Language).</p>
          <ul className="docs-list">
            <li><strong>Prompt Injection (Input):</strong> Blocks attempts to bypass system prompts.</li>
            <li><strong>SQL Injection (Input):</strong> Detects malicious SQL queries in user input.</li>
            <li><strong>Toxic Content (Input/Output):</strong> Filters profanity and harmful language.</li>
            <li><strong>Secret Leakage (Output):</strong> Prevents models from outputting AWS keys or API credentials.</li>
            <li><strong>PII Detection (Input/Output):</strong> Flags credit cards, emails, and SSNs.</li>
          </ul>
        </section>

        <section id="vault" className="docs-section">
          <h2><Key size={24} className="section-icon" /> Virtual Key Vault</h2>
          <p>Never hardcode provider API keys in your application. OmniSwitch's Virtual Key Vault encrypts your provider keys in SQLite using AES-256-GCM.</p>
          <p>You can create virtual keys (e.g. <code>sk-omniswitch-123</code>) and distribute those to your teams or agents. Each virtual key can have strict rate limits and token budgets.</p>
          
          <div className="code-block">
            <div className="code-header">Terminal</div>
            <pre><code>{`# Generate a new virtual key with a 60 requests/min limit
curl -X POST http://localhost:8080/api/keys \\
  -H "Content-Type: application/json" \\
  -d '{"name": "production-agent", "rate_limit": 60}'`}</code></pre>
          </div>
        </section>
      </div>
    </div>
  );
}

// Temporary icon for Globe (reusing from App if needed, but defined here to avoid missing imports)
function Globe({ size, className }) {
  return (
    <svg 
      xmlns="http://www.w3.org/2000/svg" 
      width={size} height={size} 
      viewBox="0 24" 
      fill="none" 
      stroke="currentColor" 
      strokeWidth="2" 
      strokeLinecap="round" 
      strokeLinejoin="round" 
      className={className}
    >
      <circle cx="12" cy="12" r="10"></circle>
      <line x1="2" y1="12" x2="22" y2="12"></line>
      <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"></path>
    </svg>
  );
}
