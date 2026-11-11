import { useEffect, useMemo, useState } from 'react';

export type Bar = { label: string; requests: number; tokens: number; height: number };
export type Chart = { hint: string; bars: Bar[] };
export type EmailRow = { email: string; masked: string; success: number; failed: number; tokens: number };
export type Accounts = {
  running: boolean;
  checked: number;
  total: number;
  eligible: number;
  plus: number;
  cooling: number;
  unavailable: number;
  disabled_skipped: number;
  checked_at: string;
  duration_ms?: number;
};
export type Dash = {
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

const RANGES = [
  { id: 'hour', label: '24h' },
  { id: 'day', label: 'Today' },
  { id: 'week', label: '7d' },
  { id: 'month', label: '30d' },
] as const;
type Range = (typeof RANGES)[number]['id'];

const METRICS = [
  { id: 'requests', label: 'Requests' },
  { id: 'tokens', label: 'Tokens' },
] as const;
type Metric = (typeof METRICS)[number]['id'];

type Sort = 'requests' | 'tokens' | 'email';

const count = (value: number | undefined | null) => Number(value ?? 0).toLocaleString('en-US');

function compact(value: number): string {
  if (!value) return '0';
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}k`;
  return String(Math.round(value));
}

function percent(part: number, total: number): string {
  if (!total) return '—';
  return `${((part / total) * 100).toFixed(1)}%`;
}

function formatTime(value: string): string {
  if (!value) return '—';
  const at = new Date(value);
  if (Number.isNaN(at.valueOf())) return value;
  return at.toLocaleString('en-US', { dateStyle: 'medium', timeStyle: 'short' });
}

function formatDuration(ms: number | undefined): string {
  if (!ms) return '';
  return ms < 1000 ? ` · ${Math.round(ms)}ms` : ` · ${(ms / 1000).toFixed(1)}s`;
}

function copyText(value: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(value).then(
      () => true,
      () => false
    );
  }
  const area = document.createElement('textarea');
  area.value = value;
  area.setAttribute('readonly', '');
  area.style.position = 'fixed';
  area.style.left = '-9999px';
  document.body.appendChild(area);
  area.select();
  const copied = document.execCommand('copy');
  area.remove();
  return Promise.resolve(copied);
}

function CopyButton({ value, label }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="btn btn-ghost btn-xs"
      aria-label={`Copy ${label ?? 'email'}`}
      onClick={() => {
        void copyText(value).then((ok) => {
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

function Metric({ label, value, note, tone }: { label: string; value: string; note?: string; tone?: string }) {
  return (
    <article className="metric" data-tone={tone}>
      <span className="metric-label">{label}</span>
      <strong className="metric-value">{value}</strong>
      {note ? <span className="metric-note">{note}</span> : null}
    </article>
  );
}

function QuietMetric({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="quiet-metric">
      <span className="metric-label">{label}</span>
      <strong className="quiet-value">{value}</strong>
      {note ? <span className="metric-note">{note}</span> : null}
    </div>
  );
}

function TrafficChart({ bars, metric }: { bars: Bar[]; metric: Metric }) {
  const values = bars.map((bar) => (metric === 'tokens' ? bar.tokens : bar.requests));
  const max = Math.max(1, ...values);
  // A tiny maximum would print the same rounded label twice; keep the middle
  // tick blank until the scale has room for it.
  const ticks = [max, max > 3 ? max / 2 : null, 0];
  const first = bars[0]?.label;
  const middle = bars[Math.floor(bars.length / 2)]?.label;
  const last = bars[bars.length - 1]?.label;
  const total = values.reduce((sum, value) => sum + value, 0);
  return (
    <figure className="chart">
      <div className="chart-body">
        <div className="chart-y" aria-hidden="true">
          {ticks.map((tick, index) => (
            <span key={index}>{tick === null ? '' : compact(tick)}</span>
          ))}
        </div>
        <div className="chart-plot">
          <div className="chart-grid" aria-hidden="true">
            <i />
            <i />
            <i />
          </div>
          <div
            className="bars"
            role="img"
            aria-label={`${metric === 'tokens' ? 'Tokens' : 'Requests'} per interval, ${count(total)} in view`}
          >
            {bars.map((bar, index) => {
              const value = metric === 'tokens' ? bar.tokens : bar.requests;
              const height = value ? Math.max(2, (value / max) * 100) : 0;
              return (
                <div
                  key={`${bar.label}-${index}`}
                  className="bar"
                  style={{ height: `${height}%` }}
                  title={`${bar.label}: ${count(bar.requests)} requests · ${count(bar.tokens)} tokens`}
                />
              );
            })}
          </div>
          {!total ? <span className="chart-empty">No traffic in this range yet.</span> : null}
        </div>
      </div>
      <figcaption className="chart-x">
        <span>{first ?? ''}</span>
        <span>{middle ?? ''}</span>
        <span>{last ?? ''}</span>
      </figcaption>
      <p className="chart-total">
        {count(total)} {metric === 'tokens' ? 'tokens' : 'requests'} in view
      </p>
    </figure>
  );
}

export function App() {
  const [dash, setDash] = useState<Dash | null>(null);
  const [range, setRange] = useState<Range>('hour');
  const [metric, setMetric] = useState<Metric>('requests');
  const [query, setQuery] = useState('');
  const [sort, setSort] = useState<Sort>('requests');
  const [error, setError] = useState('');
  const [refreshing, setRefreshing] = useState(false);
  const [updatedAt, setUpdatedAt] = useState('');

  async function load(showSpinner = false) {
    if (showSpinner) setRefreshing(true);
    try {
      if (import.meta.env.DEV) {
        const fixture = await import('./devfixture');
        setDash(fixture.DEMO_DASH);
        setError('');
        setUpdatedAt(new Date().toISOString());
        return;
      }
      const url = new URL(location.href);
      url.searchParams.set('format', 'json');
      const response = await fetch(url, { cache: 'no-store' });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const next = (await response.json()) as Dash;
      if (typeof next.requests !== 'number') throw new Error('Unexpected payload');
      setDash(next);
      setError('');
      setUpdatedAt(new Date().toISOString());
    } catch {
      setError('The last refresh failed. The table below shows the newest snapshot that loaded.');
    } finally {
      if (showSpinner) setRefreshing(false);
    }
  }

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 5000);
    return () => window.clearInterval(timer);
  }, []);

  const accounts = dash?.accounts ?? null;
  const chart = dash?.charts?.[range];

  const emails = useMemo(() => {
    const rows = [...(dash?.emails ?? [])];
    const wanted = query.trim().toLowerCase();
    const filtered = wanted
      ? rows.filter((row) => row.email.toLowerCase().includes(wanted) || row.masked.toLowerCase().includes(wanted))
      : rows;
    return filtered.sort((a, b) => {
      if (sort === 'tokens') return b.tokens - a.tokens;
      if (sort === 'email') return (a.masked || a.email).localeCompare(b.masked || b.email);
      return b.success + b.failed - (a.success + a.failed);
    });
  }, [dash, query, sort]);

  const emailTotal = (dash?.emails ?? []).reduce((sum, row) => sum + row.success + row.failed, 0);

  return (
    <div className="shell">
      <div className="chrome-scrim" aria-hidden="true" />
      <main className="content">
        <header className="page-header">
          <div className="min-w-0">
            <p className="page-kicker">ch-relay-vippro</p>
            <h1 className="page-title">Dashboard</h1>
            <p className="page-copy">
              Requests, tokens, and account health for this relay.
              {dash?.since ? ` Counting since ${formatTime(dash.since)}.` : ''}
            </p>
          </div>
          <div className="page-actions">
            <span className="live" title={updatedAt ? `Last update ${new Date(updatedAt).toLocaleTimeString('en-US')}` : 'Refreshes every five seconds'}>
              <i aria-hidden="true" />
              {refreshing ? 'Refreshing' : 'Live'}
              {updatedAt && !refreshing ? <span className="live-time">· {new Date(updatedAt).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit', second: '2-digit' })}</span> : null}
            </span>
            <button type="button" className="btn btn-secondary btn-sm" disabled={refreshing} onClick={() => void load(true)}>
              {refreshing ? 'Refreshing…' : 'Refresh'}
            </button>
          </div>
        </header>

        {error ? (
          <p className="alert" role="alert">
            {error}{' '}
            <button type="button" className="btn btn-danger btn-xs" onClick={() => void load(true)}>
              Retry
            </button>
          </p>
        ) : null}

        {!dash ? (
          <div className="metric-grid" aria-busy="true">
            {['Requests', 'Success rate', 'Total tokens', 'Eligible accounts'].map((label) => (
              <article className="metric skeleton" key={label}>
                <span className="metric-label">{label}</span>
                <span className="skel-line" />
              </article>
            ))}
          </div>
        ) : (
          <>
            <section className="metric-grid" aria-label="Relay totals">
              <Metric label="Requests" value={count(dash.requests)} note="Every attempt recorded" />
              <Metric label="Success rate" value={percent(dash.success, dash.requests)} note={`${count(dash.success)} of ${count(dash.requests)}`} tone="good" />
              <Metric label="Total tokens" value={compact(dash.total_tokens)} note={`${compact(dash.input_tokens)} in · ${compact(dash.output_tokens)} out`} />
              <Metric
                label="Eligible accounts"
                value={accounts ? count(accounts.eligible) : '—'}
                note={accounts ? `of ${count(accounts.total)} checked` : 'No account check yet'}
              />
            </section>

            <section className="quiet-grid" aria-label="Request detail">
              <QuietMetric label="Succeeded" value={count(dash.success)} />
              <QuietMetric label="Failed" value={count(dash.failed)} />
              <QuietMetric label="Input tokens" value={compact(dash.input_tokens)} />
              <QuietMetric label="Output tokens" value={compact(dash.output_tokens)} />
              <QuietMetric label="Tokens / request" value={dash.requests ? compact(Math.round(dash.total_tokens / dash.requests)) : '—'} />
              <QuietMetric label="Emails seen" value={count(dash.emails.length)} />
            </section>

            <section className="panel">
              <div className="panel-head">
                <div className="min-w-0">
                  <h2 className="section-title">Traffic</h2>
                  <p className="section-copy">{chart?.hint ?? 'Requests over time.'}</p>
                </div>
                <div className="panel-actions">
                  <div className="segmented" role="group" aria-label="Chart range">
                    {RANGES.map((item) => (
                      <button
                        key={item.id}
                        type="button"
                        className={range === item.id ? 'on' : ''}
                        aria-pressed={range === item.id}
                        onClick={() => setRange(item.id)}
                      >
                        {item.label}
                      </button>
                    ))}
                  </div>
                  <div className="segmented" role="group" aria-label="Chart metric">
                    {METRICS.map((item) => (
                      <button
                        key={item.id}
                        type="button"
                        className={metric === item.id ? 'on' : ''}
                        aria-pressed={metric === item.id}
                        onClick={() => setMetric(item.id)}
                      >
                        {item.label}
                      </button>
                    ))}
                  </div>
                </div>
              </div>
              <TrafficChart bars={chart?.bars ?? []} metric={metric} />
            </section>

            <section className="panel">
              <div className="panel-head">
                <div className="min-w-0">
                  <h2 className="section-title">Account health</h2>
                  <p className="section-copy">
                    {accounts?.running
                      ? `Checking ${count(accounts.checked)} of ${count(accounts.total)}.`
                      : accounts
                        ? `Last check ${formatTime(accounts.checked_at)}${formatDuration(accounts.duration_ms)}.`
                        : 'The first background check has not finished yet.'}
                  </p>
                </div>
                {accounts ? <span className={`chip ${accounts.running ? 'chip-warn' : 'chip-good'}`}><i aria-hidden="true" />{accounts.running ? 'Running' : 'Idle'}</span> : null}
              </div>
              {accounts ? (
                <>
                  {accounts.running ? (
                    <div className="progress" role="progressbar" aria-valuenow={accounts.checked} aria-valuemin={0} aria-valuemax={accounts.total || 1}>
                      <i style={{ width: `${accounts.total ? Math.min(100, (accounts.checked / accounts.total) * 100) : 0}%` }} />
                    </div>
                  ) : null}
                  <div className="accounts-grid">
                    <QuietMetric label="Eligible" value={count(accounts.eligible)} note={`${percent(accounts.eligible, accounts.total)} of checked`} />
                    <QuietMetric label="Plus" value={count(accounts.plus)} />
                    <QuietMetric label="Cooling" value={count(accounts.cooling)} />
                    <QuietMetric label="Unavailable" value={count(accounts.unavailable)} />
                    <QuietMetric label="Disabled skipped" value={count(accounts.disabled_skipped)} />
                    <QuietMetric label="Checked" value={`${count(accounts.checked)} / ${count(accounts.total)}`} />
                  </div>
                </>
              ) : (
                <p className="empty-state">No account snapshot yet. The plugin scans in the background and this panel fills in after the first pass.</p>
              )}
            </section>

            <section className="panel">
              <div className="panel-head">
                <div className="min-w-0">
                  <h2 className="section-title">Email usage</h2>
                  <p className="section-copy">Success, failure, and tokens per upstream account. The busiest account is first.</p>
                </div>
                <div className="panel-actions">
                  <label className="search">
                    <span className="sr-only">Search email</span>
                    <input
                      type="search"
                      value={query}
                      placeholder="Search email"
                      onChange={(event) => setQuery(event.target.value)}
                    />
                  </label>
                  <label className="sort">
                    <span className="sr-only">Sort emails</span>
                    <select value={sort} onChange={(event) => setSort(event.target.value as Sort)}>
                      <option value="requests">Most requests</option>
                      <option value="tokens">Most tokens</option>
                      <option value="email">Email A–Z</option>
                    </select>
                  </label>
                </div>
              </div>

              <div className="table-shell only-desktop">
                <div className="table-scroll">
                  <table>
                    <caption className="sr-only">Usage per upstream email</caption>
                    <thead>
                      <tr>
                        <th>Email</th>
                        <th className="num">Requests</th>
                        <th>Mix</th>
                        <th className="num">Tokens</th>
                        <th className="num">Share</th>
                      </tr>
                    </thead>
                    <tbody>
                      {emails.map((row) => {
                        const total = row.success + row.failed;
                        const share = emailTotal ? (total / emailTotal) * 100 : 0;
                        return (
                          <tr key={row.email || `unknown-${row.masked}`}>
                            <td>
                              {row.email ? (
                                <span className="email-cell">
                                  <span className="email-text">
                                    <span className="num">{row.masked}</span>
                                    <span className="sub num">{row.email.length > 34 ? `${row.email.slice(0, 31)}…` : row.email}</span>
                                  </span>
                                  <CopyButton value={row.email} />
                                </span>
                              ) : (
                                <span className="muted">Unattributed</span>
                              )}
                            </td>
                            <td className="num">
                              {count(total)}
                              <span className="sub">
                                <span className="ok">{count(row.success)}</span> ok · <span className={row.failed ? 'bad' : 'muted'}>{count(row.failed)}</span> failed
                              </span>
                            </td>
                            <td>
                              <span className="mix" role="img" aria-label={`${percent(row.success, total)} succeeded`}>
                                <i className="mix-good" style={{ width: `${total ? (row.success / total) * 100 : 0}%` }} />
                                <i className="mix-bad" style={{ width: `${total ? (row.failed / total) * 100 : 0}%` }} />
                              </span>
                            </td>
                            <td className="num">{count(row.tokens)}</td>
                            <td className="num">{share ? `${share.toFixed(1)}%` : '—'}</td>
                          </tr>
                        );
                      })}
                      {!emails.length ? (
                        <tr>
                          <td colSpan={5} className="table-empty">
                            {dash.emails.length ? 'No email matches that search.' : 'No requests yet.'}
                          </td>
                        </tr>
                      ) : null}
                    </tbody>
                  </table>
                </div>
              </div>

              <div className="card-list only-mobile">
                {emails.map((row) => {
                  const total = row.success + row.failed;
                  return (
                    <article className="record" key={row.email || `unknown-${row.masked}`}>
                      <div className="record-head">
                        <span className="num email-text">
                          <strong>{row.masked || 'Unattributed'}</strong>
                          <span className="sub">{row.email || 'No upstream email recorded'}</span>
                        </span>
                        {row.email ? <CopyButton value={row.email} /> : null}
                      </div>
                      <div className="record-grid">
                        <div className="kv"><span>Requests</span><strong className="num">{count(total)}</strong></div>
                        <div className="kv"><span>Success rate</span><strong className="num">{percent(row.success, total)}</strong></div>
                        <div className="kv"><span>Tokens</span><strong className="num">{count(row.tokens)}</strong></div>
                        <div className="kv"><span>Share</span><strong className="num">{emailTotal ? `${((total / emailTotal) * 100).toFixed(1)}%` : '—'}</strong></div>
                      </div>
                      <span className="mix" role="img" aria-label={`${count(row.success)} succeeded, ${count(row.failed)} failed`}>
                        <i className="mix-good" style={{ width: `${total ? (row.success / total) * 100 : 0}%` }} />
                        <i className="mix-bad" style={{ width: `${total ? (row.failed / total) * 100 : 0}%` }} />
                      </span>
                      <p className="sub">
                        <span className="ok">{count(row.success)}</span> ok · <span className={row.failed ? 'bad' : 'muted'}>{count(row.failed)}</span> failed
                      </p>
                    </article>
                  );
                })}
                {!emails.length ? (
                  <p className="record table-empty">{dash.emails.length ? 'No email matches that search.' : 'No requests yet.'}</p>
                ) : null}
              </div>
            </section>

            <footer className="foot">
              <span>Refreshes every 5 seconds.</span>
              <span>{updatedAt ? `Last update ${new Date(updatedAt).toLocaleTimeString('en-US')}.` : ''}</span>
            </footer>
          </>
        )}
      </main>
    </div>
  );
}
