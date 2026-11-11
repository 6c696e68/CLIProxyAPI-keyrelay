// Dev-only fixture so the dashboard can be reviewed without a live plugin.
// Vite removes this module from the production bundle.
import type { Dash } from './App';

const hour = Array.from({ length: 24 }, (_, index) => {
  const at = new Date(Date.now() - (23 - index) * 3_600_000);
  const requests = [12, 8, 5, 4, 3, 6, 18, 42, 88, 120, 96, 74, 110, 148, 132, 90, 72, 66, 84, 102, 78, 44, 26, 14][index];
  return { label: at.toLocaleTimeString('en-US', { hour: 'numeric' }), requests, tokens: requests * 1_180 };
});
const daily = (count: number, seed: number) =>
  Array.from({ length: count }, (_, index) => {
    const at = new Date(Date.now() - (count - 1 - index) * 86_400_000);
    const requests = Math.max(80, 950 + Math.round(430 * Math.sin(index / 2.4 + seed)) + (index > count / 2 ? 320 : 0));
    return { label: at.toISOString().slice(5, 10), requests, tokens: requests * 1_210 };
  });
const scale = (bars: Array<{ label: string; requests: number; tokens: number }>) => {
  const max = Math.max(1, ...bars.map((bar) => bar.requests));
  return bars.map((bar) => ({ ...bar, height: Math.max(2, Math.round((bar.requests * 100) / max)) }));
};

export const DEMO_DASH: Dash = {
  since: '2026-08-14T00:00:00Z',
  requests: 22_612,
  success: 22_104,
  failed: 508,
  input_tokens: 18_432_110,
  output_tokens: 4_180_332,
  total_tokens: 22_617_442,
  emails: [
    { email: 'northline@relay.example', masked: 'nor****@****.example', success: 6_820, failed: 142, tokens: 7_412_000 },
    { email: 'acct.main@openai.example', masked: 'acc***@****.example', success: 5_120, failed: 96, tokens: 5_820_040 },
    { email: 'studio.ops@relay.example', masked: 'stu***@****.example', success: 4_210, failed: 88, tokens: 4_120_900 },
    { email: 'bulk01@chatgpt.example', masked: 'bul***@****.example', success: 3_010, failed: 76, tokens: 2_880_120 },
    { email: 'kite.archive@relay.example', masked: 'kit***@****.example', success: 820, failed: 30, tokens: 842_011 },
    { email: '', masked: '', success: 94, failed: 32, tokens: 88_260 },
  ],
  charts: {
    hour: { hint: 'Each bar is one hour over the last 24 hours.', bars: scale(hour) },
    day: { hint: 'Sum of the last 24 hours.', bars: scale([{ label: 'Today', requests: 22_612, tokens: 22_617_442 }]) },
    week: { hint: 'One bar per day for the last 7 days.', bars: scale(daily(7, 0.4)) },
    month: { hint: 'One bar per day for the last 30 days.', bars: scale(daily(30, 1.1)) },
  },
  accounts: {
    running: false,
    checked: 118,
    total: 118,
    eligible: 96,
    plus: 72,
    cooling: 8,
    unavailable: 22,
    disabled_skipped: 6,
    checked_at: new Date(Date.now() - 240_000).toISOString(),
    duration_ms: 48_210,
  },
  progress: '',
};
