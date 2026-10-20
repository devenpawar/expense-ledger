import { useState } from 'react';

export function DeveloperSetup({ configured, redirectUri, loginRedirectUri, busy, error, onSave }: {
    configured: boolean;
    redirectUri: string;
    loginRedirectUri: string;
    busy: boolean;
    error: string;
    onSave: (credentials: unknown, ownerKey: string) => Promise<void>;
}) {
    const [file, setFile] = useState<File | null>(null);
    const [fileError, setFileError] = useState('');
    const [ownerKey, setOwnerKey] = useState('');
    async function submit(event: React.FormEvent) {
        event.preventDefault();
        setFileError('');
        if (!file) return;
        if (file.size > 64000) { setFileError('Choose the OAuth client JSON file, smaller than 64 KB.'); return; }
        let credentials: unknown;
        try { credentials = JSON.parse(await file.text()); }
        catch { setFileError('This file is not valid JSON. Download the OAuth client JSON from Google Cloud.'); return; }
        await onSave(credentials, ownerKey);
    }
    return <div className="settings-grid">
        <section className="settings-panel">
            <h3>Configure the application once</h3>
            <p>This page is for the person building or running the app. People connecting their Gmail only use <strong>Connect Gmail</strong> and Google's consent screen.</p>
            <ol>
                <li>Open <a href="https://console.cloud.google.com/" target="_blank" rel="noreferrer">Google Cloud Console</a> and enable the Gmail API for your project.</li>
                <li>Configure the OAuth consent screen. While testing, add the Gmail addresses that can use your app as test users.</li>
                <li>Create a <strong>Web application</strong> OAuth client with both authorized redirect URIs:<code>{loginRedirectUri}</code><code>{redirectUri}</code></li>
                <li>Download that client's JSON file and import it here.</li>
            </ol>
            <p>Google Cloud project creation and account consent happen in Google. This app cannot create those credentials or approve inbox access for someone.</p>
        </section>
        <section className="settings-panel">
            <h3>Import Google OAuth configuration</h3>
            <p>Status: <strong>{configured ? 'Configured' : 'Not configured'}</strong></p>
            <p>Import the downloaded Web application JSON once. Credentials stay on this computer and are never included in API responses. No restart is required.</p>
            <form onSubmit={submit}>
                <label htmlFor="developer-key">Local developer key</label>
                <input id="developer-key" className="config-file" type="password" autoComplete="off" required value={ownerKey} onChange={event => setOwnerKey(event.target.value)} disabled={busy}/>
                <p className="small muted">The application creates this key in its data directory as <code>developer-key</code>. Read that local file to authorize setup. Ordinary users do not need it.</p>
                <label htmlFor="oauth-config-file">Google OAuth client JSON</label>
                <input id="oauth-config-file" className="config-file" type="file" accept="application/json,.json" disabled={busy} onChange={event => { setFile(event.target.files?.[0] || null); setFileError(''); }} />
                {(fileError || error) && <p className="message error" role="alert">{fileError || error}</p>}
                <button className="primary" disabled={busy || !file || !ownerKey.trim()}>{busy ? 'Saving configuration…' : 'Save application configuration'}</button>
            </form>
            <p className="small muted">Disconnect Gmail before replacing an existing configuration. Environment variables can still supply the OAuth client for developer-managed installations.</p>
        </section>
    </div>;
}
