# The Expense Ledger

A local Go + React/TypeScript expense tracker with a paper-like ’90s design. Nothing is published.

## Run

For the already built app, double-click **start.cmd** in this folder, then open **http://127.0.0.1:8080**. Keep its console window open while using the app. Launch it from Windows Explorer or a regular terminal so the server can reach Google; processes launched in a restricted coding environment may have outbound networking blocked.

Requires Go 1.23+ and Node.js 22.12+. From this folder in PowerShell:

```powershell
.\start.ps1
```

Open **http://127.0.0.1:8080**. The script builds React and runs Go. Press Ctrl+C to stop. Dependencies and a production build are present. Alternatively, run `npm.cmd ci` and `npm.cmd run build` in `frontend`, then `go run .` from the project root.

## User flow

1. **Sign in with Google.** This requests basic identity only: `openid email profile`. Signing in does not read Gmail or grant inbox access.
2. **Choose an inbox.** Use the sign-in email or another Google account. Google requests Gmail permission in a separate authorization step. The backend verifies the account actually returned by Google.
3. **Open the ledger.** Sync starts automatically. Review alerts and categories, then accept expenses to include them in monthly totals.

Users may choose **Continue without Gmail for now** and enter expenses manually or paste alerts. They can connect later in Gmail & settings. The header distinguishes the signed-in user from the connected inbox; choosing another inbox never changes app identity.

Each Google identity has separate expenses, tokens, sync state, and label queues. Each ledger stays bound to its first connected inbox to prevent mixing records. Reconnect that inbox after disconnecting. An inbox connected to another app profile must be disconnected there before linking elsewhere, avoiding competing labels.

Signing out ends the browser session. **Disconnect Gmail** stops imports and removes the saved Gmail token. Background sync can continue after sign-out while the server runs, using previously granted authorization.

## One-time developer setup

End users do not create Cloud projects or handle application secrets. The owner configures one OAuth client:

1. Create a project in [Google Cloud Console](https://console.cloud.google.com/) and enable the Gmail API.
2. Configure the OAuth consent screen. While testing, add the accounts that will sign in or connect inboxes as test users.
3. Create a **Web application** OAuth client with **both** authorized redirect URIs:

   ```text
   http://127.0.0.1:8080/api/auth/callback
   http://127.0.0.1:8080/api/gmail/callback
   ```

4. Download the client's JSON.
5. Open **http://127.0.0.1:8080/developer**. The server creates an owner key in `data/developer-key`. Read this local file and enter its value to authorize setup. The key is never returned by an API.
6. Import the Google JSON and save. No restart is needed. Return to the login page and choose **Continue with Google**.

Alternatively, set both `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET` before launching. Environment variables take precedence. Disconnect all inboxes before replacing a configured application's credentials. The owner can restore a lost configuration with the developer key.

Google Cloud setup and user consent happen in Google. This repository contains no real OAuth credentials and cannot approve access for an account owner. See Google's [OpenID Connect guide](https://developers.google.com/identity/openid-connect/openid-connect), [server-side OAuth guide](https://developers.google.com/identity/protocols/oauth2/web-server), and [Gmail modification reference](https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/modify).

## Identity and access

The backend exchanges codes directly with Google. Both flows use one-time state, a browser-bound HttpOnly cookie, PKCE, and a nonce. It checks Google's RS256 signature with cached public keys, issuer, client audience, expiry, issue time, verified email, and nonce.

Users are keyed by Google's immutable `sub`, not a changeable email address. Inbox consent is bound to the initiating app session. Same-email mode verifies the returned email; another-account mode keeps the original sign-in identity. Gmail's profile must match the authorization identity before tokens are saved. Logout, disconnect, and configuration changes invalidate relevant pending flows.

Ledger APIs, CSV, manual entry, parsing, review, and Gmail actions require a session. Cookies contain random session IDs, never OAuth tokens. Sessions expire after 12 hours, are revoked on logout, and remain in memory: restarting requires sign-in again. ID tokens are not persisted. Gmail refresh tokens stay on the backend per user.

## Import and review

- Sync runs after connection, on startup for saved connections, and every five minutes while Go runs, including with the browser closed. Review queues immediate labels. Failures retry later. Disconnect cancels an active import.
- Sync follows pagination through up to 1,000 matching bank messages, starting with the newest and including older mail. It reports a cap if more than 1,000 match. Known message IDs are skipped.
- Sender domains must be `hdfcbank.net`, `hdfcbank.com`, `hdfc.bank.in`, `axisbank.com`, or `axis.bank.in`, including subdomains. MIME parsing prefers plain text. Attachments and links are not opened.
- Common successful INR card, account, and UPI debits are extracted. Credits, refunds, failed payments, OTPs, statements, and ambiguous amounts are skipped without labels. Unsupported alerts may be entered manually. Missing dates use receipt date in India; missing merchants become Unknown merchant. Review these fields.
- Amounts use integer paise. Local merchant rules suggest categories. Review can correct merchant, amount, date, category, and note. Settings lets each user create, rename, and remove personal spending labels; removing a label in use requires moving its expenses to another label.
- Message IDs and pasted-body hashes stop repeated imports. Matching bank, masked account, reference, and original amount merge even after edits. Same-day, same-merchant, same-amount alerts without a shared reference stay separate with a duplicate warning; acceptance requires confirmation.
- Only accepted expenses count in monthly totals. Ignored records stay saved to prevent reimporting them. CSV includes all statuses and protects text from spreadsheet formulas.
- Labels follow review: `Expense Ledger/Review`, `Expense Ledger/Accepted`, or `Expense Ledger/Ignored`. Other labels are kept. Records are saved before labels; failures remain queued.

## Local data and existing records

The default `data/` directory contains:

- `google-oauth.json`: imported OAuth client, unless supplied through environment variables.
- `developer-key`: owner key for configuration changes.
- `users/<hashed Google subject>/profile.json`: verified sign-in identity.
- `users/<hashed Google subject>/ledger.json`: that user's expenses, inbox binding, onboarding choice, and label queue.
- `users/<hashed Google subject>/gmail-token.json`: that user's Gmail authorization.

The directory is ignored by Git; override it with `LEDGER_DATA_DIR`. Writes use a flushed temporary file and replacement; failed writes leave in-memory records unchanged. Back up while stopped. Files are plaintext; Windows access follows the folder's ACLs. Keep data, tokens, and developer credentials private.

Older root-level `data/ledger.json` and `data/gmail-token.json` are preserved. They are not assigned to the first sign-in, since ownership cannot be established from that login. New users get separate directories. Legacy records require an explicit owner-controlled migration; no existing records are deleted.

## Validation and development

```powershell
go test ./...
go vet ./...
Set-Location frontend
npm.cmd run build
```

Tests use fake Google responses and signed test tokens. They cover JWT validation, separate scopes, browser/session-bound callbacks, same-email enforcement, another inbox preserving sign-in identity, private APIs, per-user data and CSV isolation, cross-user edits, expiry, logout, owner-key checks, configuration persistence, automation, disconnect, labels, extraction, duplicates, failed writes, and corrupt-data preservation. Live Google sign-in requires OAuth setup.

For frontend development, run Go and `npm.cmd run dev` separately. Open **http://127.0.0.1:5173**; Vite proxies `/api` to Go. Callbacks return to port 8080, so retain a production build.

## Scope

This local app supports separate Google sign-ins and expense data and listens only on **127.0.0.1:8080**. It rejects foreign Hosts, checks mutation origins and headers, and serves its own assets without analytics or external fonts. Google sign-in, signing-key retrieval, and Gmail operations contact Google.

It is not published as an Internet service. Hosted deployment, HTTPS and production cookie settings, secret management, and Google's production setup remain separate work.
