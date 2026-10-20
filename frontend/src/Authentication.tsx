import { useEffect, useState, type ReactNode } from 'react';
import { api } from './api';
import { DeveloperSetup } from './DeveloperSetup';

export type AuthSession = {
    user: { id: string; name: string; email: string } | null;
    inbox: { connected: boolean; email: string; choiceComplete: boolean } | null;
    configured: boolean;
    loginRedirectUri: string;
    gmailRedirectUri: string;
};

function Shell({ children, step }: { children: ReactNode; step?: number }) {
    return <div className="site auth-site">
        <header>
            <div className="title-row"><h1>The Expense Ledger<span className="period">.</span></h1></div>
        </header>
        <div className="auth-rule"/>
        {step && <ol className="auth-steps" aria-label="Getting started"><li aria-current={step === 1 ? 'step' : undefined}><span>01</span> Sign in</li><li aria-current={step === 2 ? 'step' : undefined}><span>02</span> Choose your inbox</li><li><span>03</span> Review your expenses</li></ol>}
        <main>{children}</main>
        <footer><span>The Expense Ledger</span></footer>
    </div>;
}

export function Authentication({ children }: { children: (session: AuthSession, refresh: () => Promise<void>, chooseInbox: () => void, logout: () => Promise<void>) => ReactNode }) {
    const [session, setSession] = useState<AuthSession | null>(null);
    const [error, setError] = useState('');
    const [busy, setBusy] = useState(false);
    const [choosing, setChoosing] = useState(false);
    const [account, setAccount] = useState<'same' | 'another'>('same');
    const [notice, setNotice] = useState('');
    const developer = window.location.pathname === '/developer';

    async function refresh() {
        const next = await api<AuthSession>('/session');
        setSession(next);
    }
    useEffect(() => {
        const query = new URLSearchParams(window.location.search);
        const callbackError = query.get('auth_error') || query.get('gmail_error');
        if (callbackError) {
            setError(callbackError);
            if (query.has('gmail_error')) setChoosing(true);
            window.history.replaceState(null, '', window.location.pathname);
        }
        refresh().catch(err => setError(err.message));
    }, []);
    useEffect(() => {
        if (!session?.user) return;
        const timer = window.setInterval(() => { refresh().catch(() => {}); }, 30000);
        return () => window.clearInterval(timer);
    }, [session?.user?.id]);
    async function run(task: () => Promise<void>) {
        setBusy(true); setError('');
        try { await task(); }
        catch (err) { setError(err instanceof Error ? err.message : 'Please try again.'); }
        finally { setBusy(false); }
    }
    async function logout() {
        await api('/auth/logout', 'POST');
        setSession(previous => previous ? { ...previous, user: null, inbox: null } : previous);
        setChoosing(false); setError(''); setNotice('You have been signed out.');
        await refresh();
    }
    if (!session) return <Shell><section className="auth-loading"><h2>{error ? 'Unable to open the ledger.' : 'Opening your ledger…'}</h2>{error && <p role="alert">{error}</p>}<button disabled={busy} onClick={() => run(refresh)}>Try again</button></section></Shell>;
    if (developer) return <Shell><div className="page-heading"><div><p className="eyebrow">APPLICATION OWNER ONLY</p><h2>Developer setup</h2></div><a href="/">Back to sign-in</a></div>{notice && <div className="message" role="status">{notice}</div>}
        <DeveloperSetup configured={session.configured} redirectUri={session.gmailRedirectUri} loginRedirectUri={session.loginRedirectUri} busy={busy} error={error}
            onSave={(credentials, ownerKey) => run(async () => { await api('/developer/google-config', 'POST', { credentials }, { 'X-Ledger-Owner': ownerKey }); await refresh(); setNotice('Google configuration saved. Return to sign-in to connect through SSO.'); })}/>
    </Shell>;
    if (!session.user) return <Shell step={1}><div className="login-layout">
        <section className="login-intro"><p className="eyebrow">KEEP TRACK, SIMPLY</p><h2>Everyday spending.<br/>A little more clarity.</h2><p>Review your bank alerts, check your spending, and keep expenses in order.</p><div className="login-features"><span>HDFC &amp; Axis alerts</span><span>Duplicate checks</span><span>Monthly spending</span></div></section>
        <section className="login-card"><h3>Sign in to begin</h3><p>Sign in with Google to open your ledger.</p>
            {notice && <p className="message" role="status">{notice}</p>}{error && <p className="message error" role="alert">{error}</p>}
            <button className="google-signin" disabled={busy || !session.configured} onClick={() => run(async () => { const result = await api<{ url: string }>('/auth/google', 'POST'); window.location.assign(result.url); })}><span className="google-initial" aria-hidden="true">G</span>{busy ? 'Opening Google…' : 'Continue with Google'}<span aria-hidden="true">↗</span></button>
            {!session.configured && <p className="auth-unavailable" role="status">Google sign-in isn't available yet.</p>}
            <div className="login-consent-note"><strong>Inbox access is your choice.</strong><p>Signing in won't grant inbox access. Choose which Gmail account to connect next.</p></div>
        </section>
    </div></Shell>;
    if (choosing || !session.inbox?.choiceComplete) return <Shell step={2}><section className="inbox-onboarding">
        <div className="signed-in-as"><span className="identity-initial" aria-hidden="true">{session.user.name.slice(0, 1).toUpperCase()}</span><span>Signed in as <strong>{session.user.email}</strong></span><button className="text-link" disabled={busy} onClick={() => run(logout)}>Sign out</button></div>
        <p className="eyebrow">YOUR SIGN-IN AND YOUR INBOX CAN BE DIFFERENT</p><h2>Where do your bank alerts arrive?</h2><p className="onboarding-lede">Choose the inbox to connect. You’ll approve Gmail access on Google’s next screen.</p>
        {error && <p className="message error" role="alert">{error}</p>}
        {session.inbox?.email && <p className="small muted">This ledger has records for {session.inbox.email}. Reconnect that inbox to keep expenses together.</p>}
        <fieldset className="inbox-options"><legend className="sr-only">Choose an inbox</legend>
            <label className={`inbox-option${account === 'same' ? ' is-selected' : ''}`}><input type="radio" name="inbox" value="same" checked={account === 'same'} onChange={() => setAccount('same')}/><span><strong>Use my sign-in email</strong><span className="inbox-address">{session.user.email}</span><small>Keep sign-in and expense alerts on the same Google account.</small></span></label>
            <label className={`inbox-option${account === 'another' ? ' is-selected' : ''}`}><input type="radio" name="inbox" value="another" checked={account === 'another'} onChange={() => setAccount('another')}/><span><strong>Use another Gmail account</strong><span className="inbox-address">Choose a different account on Google</span><small>Your sign-in stays the same. Only the selected inbox is connected.</small></span></label>
        </fieldset>
        <p className="inbox-permission-note">We use Gmail access to find HDFC and Axis alerts and apply expense labels. Your Gmail password is never entered here. You can disconnect whenever you like.</p>
        <div className="onboarding-actions"><button className="primary" disabled={busy} onClick={() => run(async () => { const result = await api<{ url: string }>('/gmail/connect', 'POST', { account }); window.location.assign(result.url); })}>{busy ? 'Opening Google…' : 'Continue to Google →'}</button>
            <button className="text-link" disabled={busy} onClick={() => run(async () => { if (!session.inbox?.connected) await api('/onboarding/skip', 'POST'); setChoosing(false); await refresh(); })}>{session.inbox?.connected ? 'Back to my ledger' : 'Continue without Gmail for now'}</button></div>
    </section></Shell>;
    return <>{children(session, refresh, () => { setError(''); setAccount('same'); setChoosing(true); window.scrollTo(0, 0); }, logout)}</>;
}
