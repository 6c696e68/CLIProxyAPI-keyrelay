import { useEffect, useState } from 'react';

type Bar = { label: string; requests: number; tokens: number; height: number };
type Chart = { hint: string; bars: Bar[] };
type EmailRow = { email: string; masked: string; success: number; failed: number; tokens: number };
type Accounts = {
  running: boolean;
  checked: number;
  total: number;
  eligible: number;
  plus: number;
  cooling: number;
  unavailable: number;
  disabled_skipped: number;
  checked_at: string;
};
type Dash = {
  since: string;
  requests: number;
  success: number;
  failed: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  emails: EmailRow[];
  charts: Record<string, Chart>;
  accounts: Accounts | null;
  progress: string;
};

const RANGES = ['hour', 'day', 'week', 'month'] as const;
type Range = (typeof RANGES)[number];

const THEME = [
  '--bg-primary',
  '--bg-secondary',
  '--text-primary',
  '--text-secondary',
  '--text-tertiary',
  '--border-color',
  '--border-hover',
  '--viz-success',
  '--viz-failure',
  '--primary-color',
];

const count = (value: number | undefined) => (value ?? 0).toLocaleString();

function copyEmail(email: string) {
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(email).then(() => true, () => false);
  }
  const area = document.createElement('textarea');
  area.value = email;
  area.setAttribute('readonly', '');
  area.style.position = 'fixed';
  area.style.left = '-9999px';
  document.body.appendChild(area);
  area.select();
  const copied = document.execCommand('copy');
  area.remove();
  return Promise.resolve(copied);
}

function CopyButton({ email }: { email: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="copy"
      onClick={() => {
        void copyEmail(email).then((ok) => {
          if (!ok) return;
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1200);
        });
      }}
    >
      {copied ? 'Copied' : 'Copy'}
    </button>
  );
}

export function App() {
  const [dash, setDash] = useState<Dash | null>(null);
  const [range, setRange] = useState<Range>('hour');

  useEffect(() => {
    try {
      const from = getComputedStyle(parent.document.documentElement);
      for (const name of THEME) {
        const value = from.getPropertyValue(name).trim();
        if (value) document.documentElement.style.setProperty(name, value);
      }
    } catch {
      /* opened outside the management frame */
    }
  }, []);

  useEffect(() => {
    let stopped = false;
    const load = async () => {
      const url = new URL(location.href);
      url.searchParams.set('format', 'json');
      const response = await fetch(url);
      if (!response.ok) return;
      const next = (await response.json()) as Dash;
      if (!stopped) setDash(next);
    };
    void load();
    const timer = window.setInterval(() => void load(), 5000);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, []);

  const chart = dash?.charts?.[range];
  const accounts = dash?.accounts;

  return (
    <div className="page">
      <header>
        <span className="eyebrow">ch-relay-vippro</span>
        <h1>Dashboard</h1>
        <p className="desc">
          Requests and tokens stay on this page. Account checks run in the background.
          {dash?.since ? ` Counting since ${dash.since}.` : ''}
        </p>
      </header>

      <section className="stats" aria-label="Request totals">
        <article className="tile" style={{ ['--tile-accent' as string]: 'var(--text-tertiary)' }}>
          <span>Requests</span>
          <strong>{count(dash?.requests)}</strong>
        </article>
        <article className="tile" style={{ ['--tile-accent' as string]: 'var(--viz-success)' }}>
          <span>Success</span>
          <strong>{count(dash?.success)}</strong>
        </article>
        <article className="tile" style={{ ['--tile-accent' as string]: 'var(--viz-failure)' }}>
          <span>Failed</span>
          <strong>{count(dash?.failed)}</strong>
        </article>
        <article className="tile" style={{ ['--tile-accent' as string]: 'var(--primary-color)' }}>
          <span>Input tokens</span>
          <strong>{count(dash?.input_tokens)}</strong>
        </article>
        <article className="tile" style={{ ['--tile-accent' as string]: 'var(--primary-color)' }}>
          <span>Output tokens</span>
          <strong>{count(dash?.output_tokens)}</strong>
        </article>
        <article className="tile" style={{ ['--tile-accent' as string]: 'var(--primary-color)' }}>
          <span>Total tokens</span>
          <strong>{count(dash?.total_tokens)}</strong>
        </article>
      </section>

      <section className="section">
        <header>
          <span className="eyebrow">Accounts</span>
          <h2>Email usage</h2>
          <p className="desc">Success is green. Errors are red. The busiest email is first.</p>
        </header>
        <div className="tableWrap">
          <table>
            <thead>
              <tr>
                <th>Email</th>
                <th>Request</th>
                <th>Token</th>
              </tr>
            </thead>
            <tbody>
              {(dash?.emails ?? []).length === 0 ? (
                <tr>
                  <td className="empty" colSpan={3}>
                    No requests yet.
                  </td>
                </tr>
              ) : (
                dash?.emails.map((row) => (
                  <tr key={row.email || 'unknown'}>
                    <td>
                      <span className="email">
                        {row.email ? (
                          <>
                            <span>{row.masked}</span>
                            <CopyButton email={row.email} />
                          </>
                        ) : (
                          <span>—</span>
                        )}
                      </span>
                    </td>
                    <td>
                      <span className="ok">{count(row.success)}</span>
                      <span className="split">/</span>
                      <span className="bad">{count(row.failed)}</span>
                    </td>
                    <td>{count(row.tokens)}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </section>

      <section className="section">
        <header className="head">
          <div>
            <span className="eyebrow">Traffic</span>
            <h2>Requests and tokens</h2>
            <p className="desc">{chart?.hint}</p>
          </div>
          <div className="range" role="group" aria-label="Chart range">
            {RANGES.map((item) => (
              <button
                key={item}
                type="button"
                className={range === item ? 'on' : ''}
                aria-pressed={range === item}
                onClick={() => setRange(item)}
              >
                {item[0].toUpperCase() + item.slice(1)}
              </button>
            ))}
          </div>
        </header>
        <div className="panel">
          <div className="bars" role="img" aria-label="Traffic">
            {(chart?.bars ?? []).map((bar) => (
              <div
                key={bar.label}
                className="bar"
                style={{ height: `${bar.height}%` }}
                title={`${bar.label}: ${bar.requests} requests, ${bar.tokens} tokens`}
              />
            ))}
          </div>
        </div>
      </section>

      <section className="section">
        <header>
          <span className="eyebrow">Accounts</span>
          <h2>Last check</h2>
          <p className="desc">
            {dash?.progress || 'The latest snapshot. A new check does not block this page.'}
          </p>
        </header>
        <section className="stats" aria-label="Account snapshot">
          <article className="tile">
            <span>Eligible</span>
            <strong>{accounts ? count(accounts.eligible) : '—'}</strong>
          </article>
          <article className="tile">
            <span>Plus</span>
            <strong>{accounts ? count(accounts.plus) : '—'}</strong>
          </article>
          <article className="tile">
            <span>Cooling</span>
            <strong>{accounts ? count(accounts.cooling) : '—'}</strong>
          </article>
          <article className="tile">
            <span>Unavailable</span>
            <strong>{accounts ? count(accounts.unavailable) : '—'}</strong>
          </article>
          <article className="tile">
            <span>Disabled skipped</span>
            <strong>{accounts ? count(accounts.disabled_skipped) : '—'}</strong>
          </article>
          <article className="tile">
            <span>Checked at</span>
            <strong className="time">{accounts?.checked_at || '—'}</strong>
          </article>
        </section>
      </section>
    </div>
  );
}
