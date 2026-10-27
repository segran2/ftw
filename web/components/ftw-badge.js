// <ftw-badge> — labeled status pill.
//
// Attributes:
//   status — "ok" (default) | "warn" | "error" | "info" | "neutral"
//   size   — "sm" | "md" (default "md")
//
// Default slot renders the label text.
//
// Example:
//   <ftw-badge status="ok">Connected</ftw-badge>
//   <ftw-badge status="warn" size="sm">Degraded</ftw-badge>
//   <ftw-badge status="error">Offline</ftw-badge>

import { FtwElement } from "./ftw-element.js";

class FtwBadge extends FtwElement {
  static styles = `
    :host {
      display: inline-flex;
      align-items: center;
      padding: 0.15rem 0.55rem;
      border-radius: 999px;
      font-family: var(--mono);
      font-size: 0.7rem;
      font-weight: 500;
      line-height: 1.4;
      letter-spacing: 0.15em;
      text-transform: uppercase;
      border: 1px solid var(--line);
      background: var(--ink-raised);
      color: var(--fg-muted);
      white-space: nowrap;
    }
    :host([size="sm"]) {
      padding: 0.05rem 0.4rem;
      font-size: 0.65rem;
    }
    :host([status="ok"]) {
      color: var(--live);
      border-color: color-mix(in srgb, var(--live) 45%, transparent);
      background: color-mix(in srgb, var(--live) 12%, var(--ink-raised));
    }
    :host([status="warn"]) {
      color: var(--alarm);
      border-color: color-mix(in srgb, var(--alarm) 45%, transparent);
      background: color-mix(in srgb, var(--alarm) 12%, var(--ink-raised));
    }
    :host([status="error"]) {
      color: var(--alarm);
      border-color: color-mix(in srgb, var(--alarm) 45%, transparent);
      background: color-mix(in srgb, var(--alarm) 12%, var(--ink-raised));
    }
    :host([status="info"]) {
      color: var(--fg-dim);
      border-color: color-mix(in srgb, var(--fg-dim) 45%, transparent);
      background: color-mix(in srgb, var(--fg-dim) 12%, var(--ink-raised));
    }
  `;

  render() {
    return `<slot></slot>`;
  }
}

customElements.define("ftw-badge", FtwBadge);
