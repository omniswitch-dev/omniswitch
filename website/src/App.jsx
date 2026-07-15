import React, { useEffect, useState } from 'react';
import { 
  Shield, Zap, Activity, Lock, Globe, Code2, CheckCircle2, XCircle, 
  Minus, GitBranch, ArrowRight, Terminal, Eye, Layers, Server, 
  Database, Cpu, Users, BookOpen, ChevronRight, ExternalLink, 
  Star, Copy, Menu, X
} from 'lucide-react';
import Docs from './Docs';
import ApiReference from './ApiReference';
import './index.css';

function App() {
  const [currentPage, setCurrentPage] = useState('home');
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

  useEffect(() => {
    // Glow blob
    const blob = document.getElementById('glow-blob');
    const handleMouseMove = (e) => {
      if (blob) {
        blob.animate({
          left: `${e.clientX}px`,
          top: `${e.clientY + window.scrollY}px`
        }, { duration: 3000, fill: "forwards" });
      }
      const img = document.querySelector('.hero-dashboard-img');
      if (!img) return;
      const centerX = window.innerWidth / 2;
      const centerY = window.innerHeight / 2;
      const rotateY = (e.clientX - centerX) / 40;
      const rotateX = (centerY - e.clientY) / 40;
      img.style.transform = `perspective(1000px) rotateX(${rotateX}deg) rotateY(${rotateY}deg)`;
    };
    document.addEventListener('mousemove', handleMouseMove);

    // Scroll reveals
    const observer = new IntersectionObserver((entries) => {
      entries.forEach(entry => {
        if (entry.isIntersecting) entry.target.classList.add('active');
      });
    }, { threshold: 0.1 });
    document.querySelectorAll('.reveal').forEach(el => observer.observe(el));

    // Scroll to top on page change
    window.scrollTo(0, 0);

    return () => {
      document.removeEventListener('mousemove', handleMouseMove);
      observer.disconnect();
    };
  }, [currentPage]);

  const navigate = (page) => {
    setCurrentPage(page);
    setMobileMenuOpen(false);
  };

  return (
    <div className="app-container">
      <div className="bg-grid"></div>
      <div id="glow-blob"></div>
      
      {/* Announcement Bar */}
      <div className="announcement-bar">
        <span>🚀 OmniSwitch v0.1.0 is now open source — </span>
        <a href="https://github.com/onlychirag/sentinel-ai-gateway" target="_blank" rel="noreferrer">
          Star us on GitHub <ArrowRight size={14} />
        </a>
      </div>

      {/* Header */}
      <header>
        <a href="#" onClick={() => navigate('home')} className="logo">
          <Shield className="logo-icon" size={28} />
          OmniSwitch
        </a>
        <nav className={`nav-links ${mobileMenuOpen ? 'mobile-open' : ''}`}>
          <a href="#" onClick={() => navigate('home')} className={currentPage === 'home' ? 'nav-link active-link' : 'nav-link'}>Product</a>
          <a href="#" onClick={() => navigate('docs')} className={currentPage === 'docs' ? 'nav-link active-link' : 'nav-link'}>Docs</a>
          <a href="#" onClick={() => navigate('api')} className={currentPage === 'api' ? 'nav-link active-link' : 'nav-link'}>API Reference</a>
          <a href="#" onClick={() => navigate('about')} className={currentPage === 'about' ? 'nav-link active-link' : 'nav-link'}>About</a>
          <a href="https://github.com/onlychirag/sentinel-ai-gateway" target="_blank" rel="noreferrer" className="nav-link">GitHub <ExternalLink size={12} /></a>
          <a href="https://github.com/onlychirag/sentinel-ai-gateway" target="_blank" rel="noreferrer" className="btn-primary mobile-only-btn">
            <GitBranch size={18} /> Get Started
          </a>
        </nav>
        <div className="nav-actions">
          <a href="https://github.com/onlychirag/sentinel-ai-gateway" target="_blank" rel="noreferrer" className="btn-primary">
            <GitBranch size={18} /> Get Started
          </a>
          <button className="mobile-menu-btn" onClick={() => setMobileMenuOpen(!mobileMenuOpen)}>
            {mobileMenuOpen ? <X size={24} /> : <Menu size={24} />}
          </button>
        </div>
      </header>

      {/* Page Content */}
      {currentPage === 'home' && <Home navigate={navigate} />}
      {currentPage === 'docs' && <Docs />}
      {currentPage === 'api' && <ApiReference />}
      {currentPage === 'about' && <About />}

      {/* Footer */}
      <footer>
        <div className="footer-grid">
          <div className="footer-brand">
            <span className="footer-logo-text">
              <Shield size={22} color="#8e2de2" />
              OmniSwitch
            </span>
            <p className="footer-tagline">The open-source AI gateway for production teams. Route, guard, cache, and observe all your LLM traffic.</p>
            <p className="footer-copy">© 2026 OmniSwitch. Apache 2.0 License.</p>
          </div>
          <div className="footer-col">
            <h4>Product</h4>
            <a href="#" onClick={() => navigate('home')}>Features</a>
            <a href="#" onClick={() => navigate('home')}>Comparison</a>
            <a href="#" onClick={() => navigate('api')}>API Reference</a>
            <a href="https://github.com/onlychirag/sentinel-ai-gateway/releases" target="_blank" rel="noreferrer">Changelog</a>
          </div>
          <div className="footer-col">
            <h4>Resources</h4>
            <a href="#" onClick={() => navigate('docs')}>Documentation</a>
            <a href="#" onClick={() => navigate('docs')}>Quickstart Guide</a>
            <a href="#" onClick={() => navigate('about')}>About</a>
          </div>
          <div className="footer-col">
            <h4>Community</h4>
            <a href="https://github.com/onlychirag/sentinel-ai-gateway" target="_blank" rel="noreferrer">GitHub</a>
            <a href="https://github.com/onlychirag/sentinel-ai-gateway/issues" target="_blank" rel="noreferrer">Report a Bug</a>
            <a href="https://github.com/onlychirag/sentinel-ai-gateway/discussions" target="_blank" rel="noreferrer">Discussions</a>
          </div>
        </div>
      </footer>
    </div>
  );
}

/* ===== HOME PAGE ===== */
function Home({ navigate }) {
  const [copied, setCopied] = useState(false);
  const installCmd = 'go install github.com/onlychirag/sentinel-ai-gateway/cmd/gateway@latest';

  const handleCopy = () => {
    navigator.clipboard.writeText(installCmd);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <>
      {/* Hero */}
      <section className="hero">
        <div className="hero-content">
          <div className="hero-badge">
            <Star size={14} /> Open Source &middot; Apache 2.0
          </div>
          <h1 className="hero-title">
            One Gateway for <span>All Your AI Traffic</span>
          </h1>
          <p className="hero-description">
            OmniSwitch is an open-source, high-performance AI gateway. Route to 1600+ models, enforce guardrails, 
            cache responses, manage API keys, and observe everything — all from a single Go binary with zero external dependencies.
          </p>
          <div className="install-bar" onClick={handleCopy}>
            <Terminal size={16} />
            <code>{installCmd}</code>
            <button className="copy-btn">{copied ? <CheckCircle2 size={16} /> : <Copy size={16} />}</button>
          </div>
          <div className="hero-actions">
            <a href="https://github.com/onlychirag/sentinel-ai-gateway" target="_blank" rel="noreferrer" className="btn-primary btn-lg">
              <GitBranch size={20} /> View on GitHub
            </a>
            <a href="#" onClick={() => navigate('docs')} className="btn-secondary btn-lg">
              <BookOpen size={20} /> Read the Docs
            </a>
          </div>
          <div className="hero-stats">
            <div className="stat-item">
              <span className="stat-value">&lt;2ms</span>
              <span className="stat-label">Added Latency</span>
            </div>
            <div className="stat-item">
              <span className="stat-value">1600+</span>
              <span className="stat-label">Models Supported</span>
            </div>
            <div className="stat-item">
              <span className="stat-value">0</span>
              <span className="stat-label">Dependencies</span>
            </div>
            <div className="stat-item">
              <span className="stat-value">27/27</span>
              <span className="stat-label">Tests Passing</span>
            </div>
          </div>
        </div>
        <div className="hero-image-wrapper">
          <img src="/dashboard-mockup.png" alt="OmniSwitch AI Gateway Dashboard" className="hero-dashboard-img" />
        </div>
      </section>

      {/* Providers Marquee */}
      <section className="providers-section">
        <p className="providers-label">Works with every major AI provider</p>
        <div className="providers-marquee">
          <div className="marquee-track">
            {['OpenAI', 'Anthropic', 'Google Gemini', 'Groq', 'Ollama', 'vLLM', 'DeepSeek', 'Mistral', 'Cohere', 'Together AI', 'Azure OpenAI', 'AWS Bedrock'].map((p, i) => (
              <span key={i} className="provider-chip">{p}</span>
            ))}
            {['OpenAI', 'Anthropic', 'Google Gemini', 'Groq', 'Ollama', 'vLLM', 'DeepSeek', 'Mistral', 'Cohere', 'Together AI', 'Azure OpenAI', 'AWS Bedrock'].map((p, i) => (
              <span key={`dup-${i}`} className="provider-chip">{p}</span>
            ))}
          </div>
        </div>
      </section>

      {/* How It Works */}
      <section className="how-it-works">
        <div className="section-header reveal">
          <div className="section-badge">How It Works</div>
          <h2 className="section-title">Integrate in 3 Lines of Code</h2>
          <p className="section-subtitle">OmniSwitch is 100% OpenAI-compatible. Point your existing SDK to OmniSwitch and you're done.</p>
        </div>
        <div className="code-showcase reveal delay-2">
          <div className="code-tabs">
            <span className="code-tab active">Python</span>
            <span className="code-tab">Node.js</span>
            <span className="code-tab">cURL</span>
          </div>
          <div className="code-block">
            <pre><code>{`from openai import OpenAI

# Just change the base_url — everything else stays the same
client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="sk-omniswitch-your-key"
)

response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello, OmniSwitch!"}]
)
print(response.choices[0].message.content)`}</code></pre>
          </div>
        </div>
      </section>

      {/* Features */}
      <section id="features" className="features">
        <div className="section-header reveal">
          <div className="section-badge">Core Modules</div>
          <h2 className="section-title">Everything You Need to Ship AI to Production</h2>
          <p className="section-subtitle">Six production-grade modules, all in a single binary. No Redis. No Postgres. No Kafka.</p>
        </div>
        <div className="features-grid">
          {[
            { icon: <Globe />, title: "AI Gateway", desc: "Unified OpenAI-compatible API across OpenAI, Anthropic, Google, Groq, and any custom endpoint. Automatic provider routing by model name.", color: "#3b82f6" },
            { icon: <Shield />, title: "Guardrails", desc: "Real-time input/output scanning for prompt injection, SQL injection, PII, toxic content, and secret leakage. Sub-millisecond local execution.", color: "#ef4444" },
            { icon: <Lock />, title: "Virtual Key Vault", desc: "AES-256-GCM encrypted credential store. Create virtual API keys with rate limits, token budgets, and zero-downtime rotation.", color: "#f59e0b" },
            { icon: <Zap />, title: "Semantic Cache", desc: "Exact-match and vector-similarity caching in SQLite. Dramatically reduce latency and costs for repeated or similar agent queries.", color: "#10b981" },
            { icon: <Activity />, title: "Observability", desc: "Built-in dashboard with real-time metrics, request logs, cost tracking, and per-provider analytics. Every request is traced end-to-end.", color: "#8b5cf6" },
            { icon: <Layers />, title: "Prompt Management", desc: "Version-controlled prompt templates with variable interpolation. Store, render, and iterate on system prompts without redeploying code.", color: "#ec4899" },
          ].map((f, i) => (
            <div key={i} className={`reveal delay-${(i % 3) + 1}`}>
              <div className="feature-card">
                <div className="feature-icon" style={{ color: f.color, background: `${f.color}15` }}>{f.icon}</div>
                <h3 className="feature-title">{f.title}</h3>
                <p className="feature-desc">{f.desc}</p>
              </div>
            </div>
          ))}
        </div>
      </section>

      {/* Architecture */}
      <section className="architecture">
        <div className="section-header reveal">
          <div className="section-badge">Architecture</div>
          <h2 className="section-title">How OmniSwitch Fits In Your Stack</h2>
        </div>
        <div className="arch-diagram reveal delay-2">
          <div className="arch-layer">
            <div className="arch-box app-box">
              <Cpu size={20} />
              <span>Your App / Agent</span>
            </div>
          </div>
          <div className="arch-arrow">↓</div>
          <div className="arch-layer">
            <div className="arch-box gateway-box">
              <Shield size={20} />
              <span>OmniSwitch Gateway</span>
              <div className="arch-modules">
                <span>Guardrails</span>
                <span>Cache</span>
                <span>Vault</span>
                <span>Logs</span>
              </div>
            </div>
          </div>
          <div className="arch-arrow">↓</div>
          <div className="arch-layer arch-providers">
            <div className="arch-box provider-box"><span>OpenAI</span></div>
            <div className="arch-box provider-box"><span>Anthropic</span></div>
            <div className="arch-box provider-box"><span>Google</span></div>
            <div className="arch-box provider-box"><span>Groq</span></div>
            <div className="arch-box provider-box"><span>Ollama</span></div>
          </div>
        </div>
      </section>

      {/* Comparison */}
      <section id="comparison" className="comparison">
        <div className="section-header reveal">
          <div className="section-badge">Why OmniSwitch</div>
          <h2 className="section-title">Open-Source. Self-Hosted. Zero Lock-In.</h2>
          <p className="section-subtitle">See how OmniSwitch stacks up against other AI gateways.</p>
        </div>
        <div className="comparison-table-wrapper reveal delay-2">
          <table className="comparison-table">
            <thead>
              <tr>
                <th>Capability</th>
                <th className="highlight-col">OmniSwitch</th>
                <th>Portkey</th>
                <th>AgentGateway</th>
              </tr>
            </thead>
            <tbody>
              {[
                ["OpenAI-Compatible API", true, true, true],
                ["Custom Endpoints (Ollama, vLLM)", true, true, true],
                ["Virtual Key Management", true, true, true],
                ["Input/Output Guardrails", true, true, true],
                ["Semantic Caching", true, true, false],
                ["Shadow Routing", true, false, false],
                ["Built-in Dashboard", true, true, false],
                ["Prompt Management", true, true, false],
                ["Zero External Dependencies", true, false, false],
                ["Single Binary Deploy", true, false, true],
                ["100% Free & OSS", true, false, true],
              ].map(([feature, os, pk, ag], i) => (
                <tr key={i}>
                  <td>{feature}</td>
                  <td className="highlight-col">{os ? <Check /> : <Cross />}</td>
                  <td>{pk ? <Check /> : <Cross />}</td>
                  <td>{ag ? <Check /> : <Cross />}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      {/* CTA */}
      <section className="cta">
        <div className="cta-content reveal">
          <h2 className="cta-title">Start Building with OmniSwitch Today</h2>
          <p className="cta-desc">
            Deploy in under 60 seconds. One binary. No vendor lock-in. Full control over your AI infrastructure.
          </p>
          <div className="hero-actions" style={{ justifyContent: 'center' }}>
            <a href="https://github.com/onlychirag/sentinel-ai-gateway" target="_blank" rel="noreferrer" className="btn-primary btn-lg">
              Get Started <ArrowRight size={20} />
            </a>
            <a href="#" onClick={() => navigate('docs')} className="btn-secondary btn-lg">
              Read the Docs
            </a>
          </div>
        </div>
      </section>
    </>
  );
}

/* ===== ABOUT PAGE ===== */
function About() {
  return (
    <div className="about-container">
      <div className="section-header reveal">
        <div className="section-badge">About</div>
        <h2 className="section-title">The Story Behind OmniSwitch</h2>
      </div>
      
      <div className="about-content reveal delay-1">
        <div className="about-text">
          <h3>Why We Built OmniSwitch</h3>
          <p>
            We were building AI-powered applications and hit the same wall every team hits: managing multiple LLM providers, 
            securing API keys, preventing prompt injection, tracking costs, and debugging agent workflows — all while trying to 
            ship features.
          </p>
          <p>
            Existing solutions either required expensive SaaS subscriptions, heavy infrastructure (Redis, Postgres, Kafka), 
            or were too narrow in scope. We wanted something different: a single binary you can run locally or in production 
            that handles <strong>everything</strong> — routing, security, caching, and observability — with zero external dependencies.
          </p>
          <p>
            That's OmniSwitch. A production-grade AI gateway written in Go, powered by SQLite, and licensed under Apache 2.0. 
            It's the infrastructure layer we wished existed when we started.
          </p>
          
          <h3>Our Principles</h3>
          <ul className="about-principles">
            <li><strong>Local-First:</strong> Your data never leaves your network. No SaaS telemetry. No cloud lock-in.</li>
            <li><strong>Zero Dependencies:</strong> One binary, one SQLite file. No Redis, Postgres, or message queues.</li>
            <li><strong>OpenAI-Compatible:</strong> Drop-in replacement. Change one line of code to route through OmniSwitch.</li>
            <li><strong>Security by Default:</strong> Guardrails are on by default. API keys are encrypted at rest. Every request is logged.</li>
            <li><strong>Open Source Forever:</strong> Apache 2.0. No "open core" bait-and-switch. The full product is free.</li>
          </ul>

          <h3>Tech Stack</h3>
          <div className="tech-stack-grid">
            <div className="tech-item"><Server size={20} /> <span><strong>Go</strong> — Core runtime</span></div>
            <div className="tech-item"><Database size={20} /> <span><strong>SQLite</strong> — Storage, cache, logs</span></div>
            <div className="tech-item"><Lock size={20} /> <span><strong>AES-256-GCM</strong> — Key encryption</span></div>
            <div className="tech-item"><Shield size={20} /> <span><strong>CEL</strong> — Policy evaluation</span></div>
          </div>

          <h3>Contributing</h3>
          <p>
            OmniSwitch is built in the open and we welcome contributions. Whether it's a bug fix, a new provider adapter, 
            or an entirely new guardrail engine — we'd love your help.
          </p>
          <div className="about-cta">
            <a href="https://github.com/onlychirag/sentinel-ai-gateway" target="_blank" rel="noreferrer" className="btn-primary">
              <GitBranch size={18} /> Contribute on GitHub
            </a>
            <a href="https://github.com/onlychirag/sentinel-ai-gateway/issues" target="_blank" rel="noreferrer" className="btn-secondary">
              Report an Issue
            </a>
          </div>
        </div>
      </div>
    </div>
  );
}

/* ===== HELPERS ===== */
function Check() { return <CheckCircle2 className="check-icon" size={20} />; }
function Cross() { return <XCircle className="x-icon" size={20} />; }

export default App;
