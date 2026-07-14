import React, { useEffect, useState } from 'react';
import { 
  Shield, 
  Zap, 
  Activity, 
  Lock, 
  Globe, 
  Code2, 
  CheckCircle2, 
  XCircle, 
  Minus,
  GitBranch,
  ArrowRight
} from 'lucide-react';
import Docs from './Docs';
import ApiReference from './ApiReference';
import './index.css';

function App() {
  const [currentPage, setCurrentPage] = useState('home');

  // Interactive glowing cursor & scroll animations
  useEffect(() => {
    // 1. Mouse Tracking for 3D card & Glow Blob
    const blob = document.getElementById('glow-blob');
    
    const handleMouseMove = (e) => {
      // Glow Blob
      if (blob) {
        blob.animate({
          left: `${e.clientX}px`,
          top: `${e.clientY + window.scrollY}px`
        }, { duration: 3000, fill: "forwards" });
      }

      // 3D Card
      const img = document.querySelector('.hero-dashboard-img');
      if (!img) return;
      const xAxis = (window.innerWidth / 2 - e.pageX) / 50;
      const yAxis = (window.innerHeight / 2 - e.pageY) / 50;
      img.style.transform = `perspective(1000px) rotateY(${xAxis}deg) rotateX(${yAxis}deg)`;
    };
    
    document.addEventListener('mousemove', handleMouseMove);

    // 2. Intersection Observer for Scroll Reveals
    const observer = new IntersectionObserver((entries) => {
      entries.forEach(entry => {
        if (entry.isIntersecting) {
          entry.target.classList.add('active');
        }
      });
    }, { threshold: 0.1 });

    document.querySelectorAll('.reveal').forEach(el => observer.observe(el));

    return () => {
      document.removeEventListener('mousemove', handleMouseMove);
      observer.disconnect();
    };
  }, [currentPage]);

  return (
    <div className="app-container">
      <div id="glow-blob"></div>
      
      {/* Header */}
      <header>
        <a href="#" className="logo">
          <Shield className="logo-icon" size={28} />
          OmniSwitch
        </a>
        <nav className="nav-links">
          <a href="#" onClick={() => setCurrentPage('home')} className={currentPage === 'home' ? 'nav-link active' : 'nav-link'}>Home</a>
          <a href="#features" onClick={() => setCurrentPage('home')} className="nav-link">Features</a>
          <a href="#" onClick={() => setCurrentPage('docs')} className={currentPage === 'docs' ? 'nav-link active' : 'nav-link'}>Documentation</a>
          <a href="#" onClick={() => setCurrentPage('api')} className={currentPage === 'api' ? 'nav-link active' : 'nav-link'}>API Reference</a>
        </nav>
        <div className="nav-actions">
          <a href="https://github.com/onlychirag/sentinel-ai-gateway" target="_blank" rel="noreferrer" className="btn-secondary">
            <GitBranch size={18} />
            Star on GitHub
          </a>
        </div>
      </header>

      {/* Page Content */}
      {currentPage === 'home' && <Home />}
      {currentPage === 'docs' && <Docs />}
      {currentPage === 'api' && <ApiReference />}

      {/* Footer */}
      <footer>
        <div className="footer-logo">
          <span style={{ fontFamily: 'Outfit', fontWeight: 700, color: 'white', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
            <Shield size={20} color="#8e2de2" />
            OmniSwitch AI
          </span>
        </div>
        <div className="footer-links">
          <a href="https://github.com/onlychirag/sentinel-ai-gateway">GitHub</a>
          <a href="#" onClick={() => setCurrentPage('docs')}>Documentation</a>
          <a href="#" onClick={() => setCurrentPage('api')}>API Reference</a>
          <a href="#">License (Apache 2.0)</a>
        </div>
      </footer>
    </div>
  );
}

function Home() {
  return (
    <>
      {/* Hero Section */}
      <section className="hero">
        <div className="hero-content">
          <div className="hero-badge">v0.1.0 Open Source Release</div>
          <h1 className="hero-title">
            The Missing <span>Control Plane</span> for Agentic AI
          </h1>
          <p className="hero-description">
            A high-performance, local policy enforcement layer and AI gateway. 
            Route, cache, guard, and observe LLM traffic across any OpenAI-compatible provider with zero external dependencies.
          </p>
          <div className="hero-actions">
            <a href="https://github.com/onlychirag/sentinel-ai-gateway" className="btn-primary">
              <GitBranch size={20} />
              View on GitHub
            </a>
            <a href="#docs" className="btn-secondary">
              Read the Docs
            </a>
          </div>
        </div>
        <div className="hero-image-wrapper">
          <img 
            src="/dashboard-mockup.png" 
            alt="OmniSwitch AI Gateway Dashboard" 
            className="hero-dashboard-img" 
          />
        </div>
      </section>

      {/* Features Section */}
      <section id="features" className="features">
        <div className="section-header reveal">
          <h2 className="section-title">Production-Ready Infrastructure</h2>
          <p className="section-subtitle">
            Everything you need to move AI logic out of your application code and into a managed data plane.
          </p>
        </div>
        
        <div className="features-grid">
          <div className="reveal delay-1">
            <FeatureCard 
              icon={<Globe />}
              title="Universal Provider API"
              description="Connect to OpenAI, Anthropic, Google, Groq, or any OpenAI-compatible endpoint (Ollama, vLLM, DeepSeek) through a single unified interface."
            />
          </div>
          <div className="reveal delay-2">
            <FeatureCard 
              icon={<Shield />}
              title="Real-Time Guardrails"
              description="Block prompt injections, SQL injections, toxic content, and secret leakages before they ever reach the model or the user."
            />
          </div>
          <div className="reveal delay-3">
            <FeatureCard 
              icon={<Lock />}
              title="Virtual Key Vault"
              description="Securely store and manage encrypted provider API keys with zero-downtime rotation. Enforce token and cost budgets per virtual key."
            />
          </div>
          <div className="reveal delay-1">
            <FeatureCard 
              icon={<Zap />}
              title="High-Performance Caching"
              description="Built-in exact-match and semantic caching using SQLite to drastically reduce latency and API costs for recurring agent queries."
            />
          </div>
          <div className="reveal delay-2">
            <FeatureCard 
              icon={<Activity />}
              title="Full Observability"
              description="Trace every request, monitor token usage, track costs, and debug agent workflows with a beautiful built-in dark mode dashboard."
            />
          </div>
          <div className="reveal delay-3">
            <FeatureCard 
              icon={<Code2 />}
              title="Lightweight & Portable"
              description="Written in Go. Deploys as a single binary with an embedded SQLite database. Zero external dependencies required."
            />
          </div>
        </div>
      </section>

      {/* Comparison Section */}
      <section id="comparison" className="comparison">
        <div className="section-header reveal">
          <h2 className="section-title">How OmniSwitch Compares</h2>
          <p className="section-subtitle">
            An open-source alternative designed for self-hosted simplicity.
          </p>
        </div>
        
        <div className="comparison-table-wrapper reveal delay-2">
          <table className="comparison-table">
            <thead>
              <tr>
                <th>Feature</th>
                <th>OmniSwitch (OSS)</th>
                <th>Portkey</th>
                <th>AgentGateway.dev</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>OpenAI-Compatible API</td>
                <td><Check /></td>
                <td><Check /></td>
                <td><Check /></td>
              </tr>
              <tr>
                <td>Any Custom Endpoint (Ollama, vLLM)</td>
                <td><Check /></td>
                <td><Check /></td>
                <td><Check /></td>
              </tr>
              <tr>
                <td>Virtual Key Management</td>
                <td><Check /></td>
                <td><Check /></td>
                <td><Check /></td>
              </tr>
              <tr>
                <td>Prompt Guardrails</td>
                <td><Check /> (Regex/CEL)</td>
                <td><Check /> (ML-based)</td>
                <td><Check /></td>
              </tr>
              <tr>
                <td>Shadow Routing (Async Compare)</td>
                <td><Check /> <span style={{fontSize: '0.8rem', color: '#a0a0a0'}}>(Unique)</span></td>
                <td><Cross /></td>
                <td><Cross /></td>
              </tr>
              <tr>
                <td>Zero Dependencies</td>
                <td><Check /> <span style={{fontSize: '0.8rem', color: '#a0a0a0'}}>(SQLite embedded)</span></td>
                <td><Cross /> <span style={{fontSize: '0.8rem', color: '#a0a0a0'}}>(Redis, Postgres)</span></td>
                <td><Cross /></td>
              </tr>
              <tr>
                <td>Completely Free</td>
                <td><Check /></td>
                <td><Cross /> <span style={{fontSize: '0.8rem', color: '#a0a0a0'}}>(Paid tiers)</span></td>
                <td><Check /></td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      {/* CTA Section */}
      <section className="cta">
        <div className="cta-content reveal">
          <h2 className="cta-title">Ready to secure your AI agents?</h2>
          <p className="cta-desc">
            Deploy OmniSwitch in minutes with a single binary. Take back control of your AI infrastructure today.
          </p>
          <a href="https://github.com/onlychirag/sentinel-ai-gateway" className="btn-primary" style={{ padding: '1rem 2.5rem', fontSize: '1.1rem' }}>
            Get Started on GitHub <ArrowRight size={20} />
          </a>
        </div>
      </section>
    </>
  );
}

function FeatureCard({ icon, title, description }) {
  return (
    <div className="feature-card">
      <div className="feature-icon">{icon}</div>
      <h3 className="feature-title">{title}</h3>
      <p className="feature-desc">{description}</p>
    </div>
  );
}

function Check() {
  return <CheckCircle2 className="check-icon" size={20} />;
}

function Cross() {
  return <XCircle className="x-icon" size={20} />;
}

function Dash() {
  return <Minus className="dash-icon" size={20} />;
}

export default App;
