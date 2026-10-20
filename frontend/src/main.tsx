import React, { useEffect, useMemo, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import './style.css';
import { FilterSelect } from './FilterSelect';
import { Authentication, type AuthSession } from './Authentication';
import { api, ApiError } from './api';
import { CategorySettings } from './CategorySettings';
type Transaction = {
    id: string;
    bank: string;
    account: string;
    amount: number;
    date: string;
    merchant: string;
    reference: string;
    category: string;
    status: string;
    note: string;
    duplicateOf?: string;
    sourceIds: string[];
    subject: string;
    excerpt: string;
};
type State = {
    transactions: Transaction[];
    categories: string[];
    customCategories: string[];
    lastSync: string;
    lastScan?: { imported: number; duplicates: number; skipped: number; scanned: number; known: number; limited: boolean; skipReasons?: Record<string, number> };
    labelsPending: number;
    sync?: { running: boolean; lastAttempt: string; lastError: string; nextSync: string; intervalSeconds: number; result: { imported: number; duplicates: number; skipped: number; scanned: number; known?: number; limited: boolean; skipReasons?: Record<string, number> } };
    gmail: {
        configured: boolean;
        connected: boolean;
        redirectUri: string;
        email: string;
    };
};
type Page = 'overview' | 'review' | 'transactions' | 'settings';
const money = (n: number) => new Intl.NumberFormat('en-IN', { style: 'currency', currency: 'INR', minimumFractionDigits: 2 }).format(n / 100);
const today = () => new Date().toLocaleDateString('en-CA', { timeZone: 'Asia/Kolkata' });
const currentMonth = () => { const d = new Date(); return new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Kolkata', year: 'numeric', month: '2-digit' }).format(d).slice(0, 7); };
const monthName = (s: string) => new Date(s + '-01T12:00:00').toLocaleDateString('en-IN', { month: 'long', year: 'numeric' });
const dateLabel = (s: string) => new Date(s + 'T12:00:00').toLocaleDateString('en-IN', { day: '2-digit', month: 'short' });
const skipReasonLabel = (reason: string) => ({
    'not a supported successful debit alert': 'not recognized as a successful debit',
    'no INR amount found': 'no rupee amount found',
    'ambiguous amount; use manual entry': 'multiple amounts found',
    'unsupported bank': 'sender not recognized',
} as Record<string, string>)[reason] || reason;
function useDialog(onClose: () => void, busy: boolean) {
    const latest = useRef({ onClose, busy });
    latest.current = { onClose, busy };
    useEffect(() => {
        const previous = document.activeElement as HTMLElement | null;
        const dialog = document.querySelector<HTMLElement>('.dialog');
        const overflow = document.body.style.overflow;
        document.body.style.overflow = 'hidden';
        (dialog?.querySelector<HTMLElement>('input, textarea') || dialog?.querySelector<HTMLElement>('button'))?.focus();
        function key(e: KeyboardEvent) {
            if (e.key === 'Escape' && !latest.current.busy) {
                e.preventDefault();
                latest.current.onClose();
            }
            if (e.key === 'Tab' && dialog) {
                const els = Array.from(dialog.querySelectorAll<HTMLElement>('button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),summary,a[href]'));
                const first = els[0], last = els[els.length - 1];
                if (e.shiftKey && document.activeElement === first) {
                    e.preventDefault();
                    last?.focus();
                }
                else if (!e.shiftKey && document.activeElement === last) {
                    e.preventDefault();
                    first?.focus();
                }
            }
        }
        document.addEventListener('keydown', key);
        return () => { document.removeEventListener('keydown', key); document.body.style.overflow = overflow; previous?.focus(); };
    }, []);
}
function Ledger({ session, onSessionChange, onChooseInbox, onLogout }: { session: AuthSession; onSessionChange: () => Promise<void>; onChooseInbox: () => void; onLogout: () => Promise<void> }) {
    const [data, setData] = useState<State | null>(null), [error, setError] = useState(''), [notice, setNotice] = useState(''), [busy, setBusy] = useState('');
    const [dismissedSyncError, setDismissedSyncError] = useState('');
    const [page, setPage] = useState<Page>('overview'), [month, setMonth] = useState(currentMonth), [reviewMonth, setReviewMonth] = useState('all'), [bank, setBank] = useState('All banks'), [search, setSearch] = useState(''), [status, setStatus] = useState('all');
    const [editor, setEditor] = useState<Transaction | null>(null), [adding, setAdding] = useState(false), [paste, setPaste] = useState(false);
    const load = async () => {
        try { const d = await api<State>('/state'); setData(d); return d; }
        catch (err) { if (err instanceof ApiError && err.status === 401) await onSessionChange(); throw err; }
    };
    useEffect(() => {
        load().then(d => {
            if (new URLSearchParams(window.location.search).get('gmail') === 'connected' && d.gmail.connected) {
                setPage('review');
                setNotice('Gmail connected. Your alerts import automatically; new expenses appear here for review.');
                window.history.replaceState(null, '', window.location.pathname);
            }
        }).catch(e => setError(e.message));
    }, []);
    useEffect(() => {
        if (!data?.gmail.connected) return;
        const timer = window.setInterval(() => { load().catch(() => {}); }, 5000);
        return () => window.clearInterval(timer);
    }, [data?.gmail.connected]);
    async function run(name: string, fn: () => Promise<void>, reload = true): Promise<boolean> { setBusy(name); setError(''); setNotice(''); try {
        await fn();
        if (reload) await load();
        return true;
    }
    catch (e) {
        setError(e instanceof Error ? e.message : 'Something went wrong');
        await load().catch(() => { });
        return false;
    }
    finally {
        setBusy('');
    } }
    const pending = data?.transactions.filter(t => t.status === 'pending') || [];
    const scan = data?.lastScan || data?.sync?.result;
    const monthly = useMemo(() => data?.transactions.filter(t => t.date.startsWith(month) && (bank === 'All banks' || t.bank === bank)) || [], [data, month, bank]);
    const accepted = monthly.filter(t => t.status === 'accepted'), total = accepted.reduce((a, t) => a + t.amount, 0);
    const latestOtherAcceptedMonth = data?.transactions.filter(t => t.status === 'accepted' && !t.date.startsWith(month)).map(t => t.date.slice(0, 7)).sort().reverse()[0];
    const groups = Object.entries(accepted.reduce<Record<string, number>>((a, t) => { a[t.category] = (a[t.category] || 0) + t.amount; return a; }, {})).sort((a, b) => b[1] - a[1]);
    const categoryUsage = (data?.transactions || []).reduce<Record<string, number>>((usage, t) => { usage[t.category] = (usage[t.category] || 0) + 1; return usage; }, {});
    const months = Array.from(new Set([currentMonth(), month, ...(data?.transactions.map(t => t.date.slice(0, 7)) || [])])).sort().reverse();
    const display = (page === 'review' ? pending : monthly).filter(t => (page !== 'transactions' || status === 'all' || t.status === status) && (page !== 'review' || ((reviewMonth === 'all' || t.date.startsWith(reviewMonth)) && (bank === 'All banks' || t.bank === bank))) && (`${t.merchant} ${t.category} ${t.reference} ${t.amount / 100}`.toLowerCase().includes(search.toLowerCase())));
    const reviewMonths = Array.from(new Set(pending.map(t => t.date.slice(0, 7)))).sort().reverse();
    const gmailErrorKey = data?.sync?.lastError || '';
    const showGmailError = !!data?.gmail.connected && !!data?.sync?.lastError && page !== 'settings' && dismissedSyncError !== gmailErrorKey;
    const previous = new Date(month + '-01T12:00:00');
    previous.setMonth(previous.getMonth() - 1);
    const prevMonth = `${previous.getFullYear()}-${String(previous.getMonth() + 1).padStart(2, '0')}`;
    const prevTotal = (data?.transactions || []).filter(t => t.status === 'accepted' && t.date.startsWith(prevMonth) && (bank === 'All banks' || t.bank === bank)).reduce((a, t) => a + t.amount, 0);
    async function connect() { onChooseInbox(); }
    async function quickAccept(t: Transaction, category: string) {
        await run('Accepting expense', async () => {
            await api('/transactions/' + t.id, 'PATCH', { merchant: t.merchant, category, date: t.date, amount: t.amount, note: t.note, status: 'accepted', allowDuplicate: false });
            setNotice('Expense accepted. Its Gmail label will update automatically.');
        });
    }
    async function sync() { await run('Syncing Gmail', async () => { const r = await api<{
        imported: number;
        duplicates: number;
        skipped: number;
        scanned: number;
        limited: boolean;
        labelError?: string;
    }>('/gmail/sync', 'POST'); setNotice(`Sync complete. ${r.scanned} matching emails scanned, ${r.imported} new expenses, ${r.duplicates} duplicates merged, ${r.skipped} unsupported emails skipped. ${r.limited ? 'Reached the 1,000-message limit. ' : ''}${r.labelError ? 'Expenses saved; labels need a retry: ' + r.labelError : ''}`); }); }
    const nav = (p: Page, label: string) => <a href={'#' + p} aria-current={page === p ? 'page' : undefined} onClick={e => { e.preventDefault(); setPage(p); setSearch(''); setStatus('all'); }}>{label}{p === 'review' && <span className="nav-count">{pending.length}</span>}</a>;
    const newTransaction: Transaction = { id: '', bank: 'HDFC', account: '', amount: 0, date: today(), merchant: '', reference: '', category: 'Other', status: 'pending', note: '', sourceIds: [], subject: '', excerpt: '' };
    return <div className={page === 'review' || page === 'transactions' ? 'site site--table-page' : 'site'}>
  <header><div className="title-row"><h1>The Expense Ledger<span className="period">.</span></h1></div>
  <nav aria-label="Main navigation"><div>{nav('overview', 'Monthly overview')}{nav('review', 'Transaction review')}{nav('transactions', 'All transactions')}{nav('settings', 'Gmail & settings')}</div><a href="/api/export">Export CSV ↗</a></nav></header>
  <div className="account-bar"><span>Signed in: <strong>{session.user?.email}</strong></span><span>Inbox: <strong>{data?.gmail.email && data.gmail.connected ? data.gmail.email : 'Not connected'}</strong></span><button className="text-link" disabled={!!busy} onClick={() => run('Signing out', onLogout, false)}>Sign out</button></div>
  <main>
  {data?.gmail.connected && !('lastScan' in data) && <div className="message" role="status">A transaction matching update is ready. Close the Expense Ledger server window, then open start.cmd again to apply it.</div>}
  {(error || showGmailError) && <div role="alert" className="message error dismissible-message"><div><strong>{error ? 'Couldn’t complete that.' : 'Gmail sync needs attention.'}</strong> {error || data?.sync?.lastError} {showGmailError && <button className="text-link" onClick={() => setPage('settings')}>Manage connection</button>}</div><button className="message-dismiss" type="button" aria-label="Dismiss error" title="Dismiss" onClick={() => { setError(''); setDismissedSyncError(gmailErrorKey); }}>×</button></div>}
  {notice && <div role="status" className="message dismissible-message"><span>{notice}</span><button className="message-dismiss" type="button" aria-label="Dismiss notice" title="Dismiss" onClick={() => setNotice('')}>×</button></div>}
  {!data ? <div className="loading"><h2>{error ? 'The ledger is unavailable.' : 'Opening your ledger…'}</h2><p>{error ? 'Please reload and try again.' : 'Loading your expenses.'}</p><button onClick={() => run('Loading', async () => { await load(); })} disabled={!!busy}>Try again</button></div> : <>
   <div className="page-heading"><div><p className="eyebrow">{page === 'overview' ? 'THE MONTH AT A GLANCE' : page === 'review' ? 'CHECK, CATEGORIZE, FILE' : page === 'transactions' ? 'YOUR RUNNING RECORD' : 'MAILBOX CONNECTION'}</p><h2>{page === 'overview' ? 'Monthly overview' : page === 'review' ? 'Transaction review' : page === 'transactions' ? 'All transactions' : 'Gmail & settings'}</h2></div><div className="actions"><button onClick={() => setAdding(true)} disabled={!!busy}>+ Add expense</button><button className="primary" disabled={!!busy || !!data.sync?.running} onClick={data.gmail.connected ? sync : connect}>{busy || (data.sync?.running ? 'Syncing Gmail...' : data.gmail.connected ? 'Sync Gmail' : 'Connect Gmail')}</button></div></div>
   {page !== 'settings' && <div className="filter-strip"><div className="filter-controls"><FilterSelect id="month" label="Period" value={page === 'review' ? reviewMonth : month} onChange={page === 'review' ? setReviewMonth : setMonth} options={page === 'review' ? [{ value: 'all', label: 'All periods' }, ...reviewMonths.map(m => ({ value: m, label: monthName(m) }))] : months.map(m => ({ value: m, label: monthName(m) }))}/><FilterSelect id="bank" label="Bank" value={bank} onChange={setBank} options={['All banks', 'HDFC', 'Axis'].map(b => ({ value: b, label: b }))}/></div><span className="muted">{page === 'review' ? `${display.length} shown · ${pending.length} awaiting review` : 'Only accepted expenses count toward totals.'}</span></div>}
   {page === 'overview' && <>
    {accepted.length === 0 && latestOtherAcceptedMonth && <p className="message" role="status">No accepted expenses in {monthName(month)}. You have spending in <button className="text-link" onClick={() => setMonth(latestOtherAcceptedMonth)}>{monthName(latestOtherAcceptedMonth)}</button>.</p>}
    <div className="stats"><div><p className="eyebrow">TOTAL SPENT · {monthName(month).toUpperCase()}</p><p className="big-number">{money(total)}</p><p className="muted">{prevTotal ? `${Math.abs((total - prevTotal) / prevTotal * 100).toFixed(0)}% ${total >= prevTotal ? 'more' : 'less'} than ${monthName(prevMonth)}` : 'No accepted spending in the previous month.'}</p></div><div><p className="eyebrow">RECORDED EXPENSES</p><p className="big-number">{accepted.length}<span className="unit"> transactions</span></p><p className="muted">Across {new Set(accepted.map(t => t.bank)).size} bank{new Set(accepted.map(t => t.bank)).size === 1 ? '' : 's'} this month</p></div><div><p className="eyebrow">AWAITING YOUR REVIEW</p><p className="big-number">{pending.length}<span className="unit"> to check</span></p><a href="#review" onClick={e => { e.preventDefault(); setPage('review'); }}>Open the review queue →</a></div></div>
    <div className="columns"><section className="spending"><div className="section-heading"><h3>Where it went</h3><span className="eyebrow">BY CATEGORY</span></div>{groups.length ? <><table className="category-table"><thead><tr><th>Category</th><th className="numeric">Amount</th><th className="numeric">Share</th></tr></thead><tbody>{groups.map(([c, n]) => <tr key={c}><td><span className="category-dot"/>{c}<div className="bar-track"><progress value={n} max={total} aria-label={`${c} spending`}/></div></td><td className="numeric">{money(n)}</td><td className="numeric muted">{Math.round(n / total * 100)}%</td></tr>)}</tbody><tfoot><tr><th>Total</th><th className="numeric">{money(total)}</th><th className="numeric">100%</th></tr></tfoot></table></> : <div className="empty category-empty"><div className="ledger-mark">₹</div><h4>A fresh page.</h4><p>Your category breakdown will appear here<br />as you accept expenses.</p><a href="#review" onClick={e => { e.preventDefault(); setPage('review'); }}>Review transactions →</a></div>}</section>
    <aside><div className="section-heading"><h3>A note on your ledger</h3></div><p>Every expense deserves a second look. Bank alerts arrive in your review queue, with a suggested category.</p><ol><li>Connect your Gmail account.</li><li>Sync HDFC &amp; Axis debit alerts.</li><li>Review, categorize, and accept.</li></ol><p>Matching reference numbers are merged. Possible duplicates are marked for you to check.</p><div className="aside-foot"><span className="dot"/> Only accepted expenses count toward your totals.</div></aside></div>
    <section className="recent"><div className="section-heading"><h3>Recent transactions</h3><a href="#transactions" onClick={e => { e.preventDefault(); setPage('transactions'); }}>View all →</a></div><TransactionTable transactions={monthly.slice(0, 6)} onReview={setEditor} empty="No transactions for this month yet."/></section>
   </>}
   {(page === 'review' || page === 'transactions') && <section className="expense-page">
    {page === 'review' && <div className="review-intro"><div><span className="review-count">{pending.length}</span><span>awaiting review</span></div><p>Check each suggested category, then accept or ignore the expense. Gmail labels follow your choices automatically.</p></div>}
    <div className="table-toolbar"><p>{page === 'review' ? `${display.length} shown from ${pending.length} pending expenses` : `${display.length} matching transactions in ${monthName(month)}.`}</p><div>{page === 'transactions' && <select aria-label="Filter status" value={status} onChange={e => setStatus(e.target.value)}><option value="all">All statuses</option><option value="accepted">Accepted</option><option value="pending">Pending</option><option value="rejected">Ignored</option></select>}<input type="search" aria-label="Search transactions" placeholder="Search merchant, category…" value={search} onChange={e => setSearch(e.target.value)}/></div></div>
    {page === 'review' && data.gmail.connected && data.sync?.running && <p className="message" role="status">Scanning HDFC and Axis emails. New expenses appear here as they are found.</p>}
    {page === 'review' ? <ReviewQueue transactions={display} categories={data.categories} onReview={setEditor} onAccept={quickAccept} busy={!!busy} empty={pending.length ? 'No expenses match these filters.' : 'Nothing waiting. Your review queue is clear.'}/> : <TransactionTable transactions={display} onReview={setEditor} empty="No transactions match these filters." bounded/>}
    <p className="table-note">{page === 'review' ? 'Need to correct the amount, date, or merchant, or ignore an alert? Open Details. Possible duplicates require a closer look.' : 'Amounts in Indian rupees. Open a transaction to correct its details or category.'}</p>
   </section>}
   {page === 'settings' && <div className="settings-grid">
    <section className="settings-panel"><div className="section-heading"><h3>Your Gmail connection</h3><span className="badge">{data.gmail.connected ? 'Connected' : 'Not connected'}</span></div>
     <p>Connect through Google to import HDFC and Axis debit alerts automatically.</p>
     {data.gmail.connected ? <p><strong>Connected inbox:</strong> {data.gmail.email || 'Checking account...'}</p> : <p>You choose the Google account and approve access on Google's sign-in screen. Your Gmail password is never entered in this app.</p>}
     <p><strong>Last successful sync:</strong> {data.lastSync ? new Date(data.lastSync).toLocaleString('en-IN') : 'Not synced yet'}</p>
     {data.lastSync && scan && <p><strong>Last scan:</strong> {scan.scanned} matching emails, {scan.imported} new expenses, {scan.known || 0} already seen, {scan.skipped} skipped. {scan.scanned === 0 ? 'No HDFC or Axis emails matched in this inbox.' : scan.skipped > 0 && scan.imported === 0 ? 'The matched emails were not recognized as supported debit alerts.' : ''}</p>}
     {scan?.skipReasons && Object.keys(scan.skipReasons).length > 0 && <p className="small muted"><strong>Skipped because:</strong> {Object.entries(scan.skipReasons).sort((a, b) => b[1] - a[1]).map(([reason, count]) => `${count} ${skipReasonLabel(reason)}`).join('; ')}.</p>}
     <p><strong>Automatic sync:</strong> {data.gmail.connected ? data.sync?.running ? 'Syncing now...' : 'Every 5 minutes' : 'Starts after you connect'}</p>
     {data.sync?.lastError && <p className="message error" role="alert">{data.sync.lastError}</p>}
     {data.gmail.connected && data.sync?.result.limited && <p className="message">This inbox reached the 1,000-message import limit. Older alerts beyond that limit have not been imported.</p>}
     <div className="actions"><button disabled={!!busy || !!data.sync?.running} onClick={data.gmail.connected ? sync : connect}>{data.sync?.running ? 'Syncing...' : data.gmail.connected ? 'Sync now' : 'Connect Gmail'}</button>
     {data.gmail.connected && <><button disabled={!!busy || !!data.sync?.running} onClick={connect}>Reconnect</button><button disabled={!!busy} onClick={() => run('Disconnecting', async () => { await api('/gmail/disconnect', 'POST'); await onSessionChange(); setNotice('Gmail disconnected. Automatic sync has stopped; existing expenses are kept.'); })}>Disconnect</button></>}</div>
     {!data.gmail.configured && <p className="small muted">Gmail connection isn't available yet.</p>}
    </section>
    {'customCategories' in data ? <CategorySettings categories={data.categories} customCategories={data.customCategories || []} usage={categoryUsage} busy={!!busy} onAdd={name => run('Adding label', async () => { await api('/categories', 'POST', { name }); setNotice(`“${name}” is ready to use.`); })} onRename={(name, newName) => run('Renaming label', async () => { await api('/categories', 'PATCH', { name, newName }); setNotice(`“${name}” was renamed to “${newName}”.`); })} onRemove={(name, replacement) => run('Removing label', async () => { await api('/categories', 'DELETE', { name, replacement }); setNotice(`“${name}” was removed.`); })}/> : <section className="settings-panel"><h3>Your spending labels</h3><p>Restart the Expense Ledger app to use personal spending labels.</p></section>}
    <section className="settings-panel"><h3>From inbox to ledger</h3><ol><li>Connect Gmail and approve access.</li><li>HDFC and Axis debit alerts arrive automatically in your review queue.</li><li>Check the suggested category and accept the expense.</li></ol><p>Accepted expenses appear in your monthly totals. Matching bank references are merged; uncertain duplicates stay in review for you to check.</p><p>Credits, refunds, OTPs, and unsupported alert formats are excluded. You can add an expense manually whenever needed.</p></section>
    <section className="settings-panel"><h3>Email labels</h3><p>Labels follow your review choices automatically: <strong>Review</strong>, <strong>Accepted</strong>, or <strong>Ignored</strong>. Other email labels are kept.</p><p><strong>Label updates waiting:</strong> {data.labelsPending}. Failed updates are retried on the next automatic sync.</p><button disabled={!!busy || !data.gmail.connected || !data.labelsPending || !!data.sync?.running} onClick={() => run('Updating labels', async () => { await api('/gmail/labels', 'POST'); setNotice('Gmail labels are up to date.'); })}>Retry labels now</button></section>
    <section className="settings-panel"><h3>Your data</h3><p>Disconnecting Gmail stops new imports and keeps your existing expenses.</p><p>You can also revoke access from your Google account. Signing out closes your app session. Disconnect Gmail to stop automatic imports. Manual expenses and pasted alerts work without a Gmail connection.</p><div className="actions"><button onClick={() => setPaste(true)}>Paste a bank alert</button><a href="/api/export">Export CSV</a></div></section>
   </div>}


  </>}
  </main><footer><span>The Expense Ledger</span></footer>
  {(editor || adding) && data && <Editor error={error} transaction={editor || newTransaction} categories={data.categories} isNew={adding} busy={!!busy} onClose={() => { setEditor(null); setAdding(false); }} onSave={(t, allowDuplicate) => run('Saving', async () => { if (adding) {
        await api('/transactions', 'POST', t);
    }
    else {
        await api('/transactions/' + t.id, 'PATCH', { merchant: t.merchant, category: t.category, date: t.date, amount: t.amount, note: t.note, status: t.status, allowDuplicate });
    } setEditor(null); setAdding(false); setNotice(adding ? 'Expense added to your review queue.' : 'Transaction saved. Gmail labels update automatically while connected.'); })}/>}
  {paste && <PasteDialog error={error} busy={!!busy} onClose={() => setPaste(false)} onSubmit={(bank, body) => run('Extracting', async () => { await api('/parse', 'POST', { bank, subject: 'Pasted bank alert', body }); setPaste(false); setPage('review'); setNotice('Alert extracted into your review queue. Repeated alerts are kept once.'); })}/>}
 </div>;
}
function ReviewQueue({ transactions, categories, onReview, onAccept, busy, empty }: {
    transactions: Transaction[];
    categories: string[];
    onReview: (t: Transaction) => void;
    onAccept: (t: Transaction, category: string) => Promise<void>;
    busy: boolean;
    empty: string;
}) {
    const [chosenCategories, setChosenCategories] = useState<Record<string, string>>({});
    const grouped = transactions.reduce<Record<string, Transaction[]>>((groups, t) => {
        const key = t.date.slice(0, 7);
        (groups[key] ||= []).push(t);
        return groups;
    }, {});
    if (!transactions.length) return <div className="review-empty"><h3>{empty}</h3><p>{empty.startsWith('Nothing') ? 'New HDFC and Axis alerts will appear here automatically.' : 'Try another period, bank, or search term.'}</p></div>;
    return <div className="table-scroll expense-scroll" role="region" aria-label="Expenses awaiting review" tabIndex={0}>
        <table className="review-table"><thead><tr><th>Date</th><th>Expense</th><th>Bank</th><th>Category</th><th className="numeric">Amount</th><th className="review-actions-heading">Action</th></tr></thead>
            {Object.entries(grouped).sort(([a], [b]) => b.localeCompare(a)).map(([period, entries]) => <tbody key={period}>
                <tr className="review-month-row"><th colSpan={6} scope="rowgroup"><span>{monthName(period)}</span><small>{entries.length} to review</small></th></tr>
                {entries.map(t => <tr key={t.id} className={t.duplicateOf ? 'review-possible-duplicate' : undefined}>
                    <td className="nowrap muted">{dateLabel(t.date)}</td>
                    <td className="review-merchant"><button className="text-link merchant" onClick={() => onReview(t)}>{t.merchant}</button>{t.duplicateOf && <span className="duplicate">Possible duplicate · check details</span>}</td>
                    <td><span className="bank-tag">{t.bank.toUpperCase()}</span>{t.account && <span className="account"> · {t.account}</span>}</td>
                    <td><select className="review-category" aria-label={`Category for ${t.merchant} on ${t.date}`} value={chosenCategories[t.id] ?? t.category} onChange={e => setChosenCategories({ ...chosenCategories, [t.id]: e.target.value })} disabled={busy}>{categories.map(category => <option key={category} value={category}>{category}</option>)}</select></td>
                    <td className="numeric amount">{money(t.amount)}</td>
                    <td><div className="review-row-actions"><button className="text-link" onClick={() => onReview(t)} disabled={busy}>Details</button>{!t.duplicateOf && <button className="review-accept" onClick={() => onAccept(t, chosenCategories[t.id] ?? t.category)} disabled={busy}>Accept</button>}</div></td>
                </tr>)}
            </tbody>)}
        </table>
    </div>;
}
function TransactionTable({ transactions, onReview, empty, bounded = false }: {
    transactions: Transaction[];
    onReview: (t: Transaction) => void;
    empty: string;
    bounded?: boolean;
}) { return <div className={bounded ? 'table-scroll expense-scroll' : 'table-scroll'} role={bounded ? 'region' : undefined} aria-label={bounded ? 'All transactions' : undefined} tabIndex={bounded ? 0 : undefined}><table className="transactions"><thead><tr><th>Date</th><th>Merchant / description</th><th>Bank</th><th>Category</th><th>Status</th><th className="numeric">Amount</th><th><span className="sr-only">Action</span></th></tr></thead><tbody>{transactions.length ? transactions.map(t => <tr key={t.id}><td className="nowrap muted">{dateLabel(t.date)}<span className="subdate">{t.date.slice(0, 4)}</span></td><td><button className="text-link merchant" onClick={() => onReview(t)}>{t.merchant}</button>{t.duplicateOf && <span className="duplicate">Possible duplicate</span>}</td><td><span className="bank-tag">{t.bank.toUpperCase()}</span>{t.account && <span className="account"> · {t.account}</span>}</td><td>{t.category}</td><td><span className={'badge ' + t.status}>{t.status === 'pending' ? 'To review' : t.status === 'rejected' ? 'Ignored' : 'Accepted'}</span></td><td className="numeric amount">{money(t.amount)}</td><td><button className="text-link" onClick={() => onReview(t)}>{t.status === 'pending' ? 'Review' : 'Edit'}</button></td></tr>) : <tr><td colSpan={7}><div className="empty"><p>{empty}</p><p className="small muted">Sync your Gmail or add an expense to start your ledger.</p></div></td></tr>}</tbody></table></div>; }
function Editor({ transaction, categories, isNew, busy, onClose, onSave, error }: {
    error: string;
    transaction: Transaction;
    categories: string[];
    isNew: boolean;
    busy: boolean;
    onClose: () => void;
    onSave: (t: Transaction, allow: boolean) => void;
}) {
    useDialog(onClose, busy);
    const [t, setT] = useState({ ...transaction }), [amount, setAmount] = useState(transaction.amount ? (transaction.amount / 100).toFixed(2) : ''), [allow, setAllow] = useState(false), [validation, setValidation] = useState('');
    function submit(status: string) { if (!/^\d+(\.\d{1,2})?$/.test(amount) || Number(amount) <= 0) {
        setValidation('Enter a positive amount with up to two decimal places.');
        return;
    } if (!t.merchant.trim() || !t.date) {
        setValidation('Merchant and date are required.');
        return;
    } onSave({ ...t, amount: Math.round(Number(amount) * 100), status }, allow); }
    return <div className="overlay"><section className="dialog" role="dialog" aria-modal="true" aria-labelledby="editor-title"><div className="section-heading"><h3 id="editor-title">{isNew ? 'Add an expense' : 'Review transaction'}</h3><button aria-label="Close" onClick={onClose} disabled={busy}>×</button></div>{(validation || error) && <p className="message error" role="alert">{validation || error}</p>}{t.duplicateOf && <p className="message">This looks like another expense with the same date, bank, account, merchant, and amount. Check before accepting.</p>}
 <form onSubmit={e => { e.preventDefault(); submit(isNew ? 'pending' : 'accepted'); }}><div className="form-grid"><label className="wide">Merchant<input autoFocus required maxLength={160} value={t.merchant} onChange={e => setT({ ...t, merchant: e.target.value })}/></label><label>Amount (INR)<input inputMode="decimal" required value={amount} onChange={e => setAmount(e.target.value)} placeholder="0.00"/></label><label>Date<input type="date" required value={t.date} onChange={e => setT({ ...t, date: e.target.value })}/></label><label>Bank<select disabled={!isNew} value={t.bank} onChange={e => setT({ ...t, bank: e.target.value })}><option>HDFC</option><option>Axis</option></select></label><label>Account / card (last 4)<input disabled={!isNew} maxLength={4} pattern="[0-9]{0,4}" value={t.account} onChange={e => setT({ ...t, account: e.target.value })}/></label><label className="wide">Category<select value={t.category} onChange={e => setT({ ...t, category: e.target.value })}>{categories.map(c => <option key={c}>{c}</option>)}</select></label>{isNew && <label className="wide">Bank reference (optional)<input maxLength={100} value={t.reference} onChange={e => setT({ ...t, reference: e.target.value })}/></label>}<label className="wide">Note (optional)<textarea maxLength={1000} rows={2} value={t.note} onChange={e => setT({ ...t, note: e.target.value })}/></label></div>
 {t.excerpt && <details><summary>Original alert &amp; reference</summary><p className="small">{t.subject}</p><blockquote>{t.excerpt}</blockquote><p className="small">Reference: {t.reference || 'Not found'}</p></details>}{t.duplicateOf && <label className="checkbox"><input type="checkbox" checked={allow} onChange={e => setAllow(e.target.checked)}/> I checked: this is a separate expense.</label>}
 <div className="dialog-actions"><button type="button" disabled={busy} onClick={onClose}>Cancel</button>{!isNew && <><button type="button" disabled={busy} onClick={() => submit('rejected')}>Ignore</button><button type="button" disabled={busy} onClick={() => submit('pending')}>Save for later</button></>}<button type="submit" className="primary" disabled={busy || (!isNew && !!t.duplicateOf && !allow)}>{busy ? 'Saving…' : isNew ? 'Add to review' : 'Accept expense'}</button></div></form></section></div>;
}
function PasteDialog({ busy, onClose, onSubmit, error }: {
    error: string;
    busy: boolean;
    onClose: () => void;
    onSubmit: (bank: string, body: string) => void;
}) { useDialog(onClose, busy); const [bank, setBank] = useState('HDFC'), [body, setBody] = useState(''); return <div className="overlay"><section className="dialog" role="dialog" aria-modal="true" aria-labelledby="paste-title"><div className="section-heading"><h3 id="paste-title">Paste a bank alert</h3><button onClick={onClose} disabled={busy} aria-label="Close">×</button></div>{error && <p className="message error" role="alert">{error}</p>}<p>Paste the body of a successful INR debit alert. Review all extracted details before accepting.</p><form onSubmit={e => { e.preventDefault(); onSubmit(bank, body); }}><label>Bank<select value={bank} onChange={e => setBank(e.target.value)}><option>HDFC</option><option>Axis</option></select></label><label>Alert text<textarea autoFocus required rows={8} value={body} onChange={e => setBody(e.target.value)} maxLength={30000}/></label><div className="dialog-actions"><button type="button" disabled={busy} onClick={onClose}>Cancel</button><button className="primary" disabled={busy || !body.trim()}>Extract transaction</button></div></form></section></div>; }
createRoot(document.getElementById('root')!).render(<React.StrictMode><Authentication>{(session, refresh, chooseInbox, logout) => <Ledger key={session.user!.id} session={session} onSessionChange={refresh} onChooseInbox={chooseInbox} onLogout={logout}/>}</Authentication></React.StrictMode>);
