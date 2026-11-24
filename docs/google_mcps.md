# Setting up Google MCP servers in Nestor

This guide explains how to generate the host secrets required by Nestor's Google MCP servers (`CREDENTIALS_KEY`, `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET`, and optionally `GOOGLE_MAPS_API_KEY`), onboard each Nestor user's Google account, and approve the pinned MCP tools.

## 1. Overview

`config/mcp.yaml` configures six Google-hosted remote MCP servers:

| Server in `config/mcp.yaml` | Endpoint | Auth mode | Required host env vars |
|---|---|---|---|
| `google-maps` | `https://mapstools.googleapis.com/mcp` | `shared` (`X-Goog-Api-Key`) | `GOOGLE_MAPS_API_KEY` |
| `google-contacts` | `https://people.googleapis.com/mcp/v1` | `per_user` (`oauth2`) | `CREDENTIALS_KEY`, `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET` |
| `google-calendar` | `https://calendarmcp.googleapis.com/mcp/v1` | `per_user` (`oauth2`) | `CREDENTIALS_KEY`, `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET` |
| `google-docs` | `https://docsmcp.googleapis.com/mcp/v1` | `per_user` (`oauth2`) | `CREDENTIALS_KEY`, `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET` |
| `google-drive` | `https://drivemcp.googleapis.com/mcp/v1` | `per_user` (`oauth2`) | `CREDENTIALS_KEY`, `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET` |
| `google-workspace-search` | `https://workspacemcp.googleapis.com/mcp/v1` | `per_user` (`oauth2`) | `CREDENTIALS_KEY`, `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET` |

- Any server whose required env var is unset or empty is cleanly disabled at startup without affecting the rest of Nestor.
- OAuth refresh tokens are stored **per Nestor user** in Postgres (`user_credentials` under key `google_oauth`), encrypted with AES-256-GCM using `CREDENTIALS_KEY`.

---

## 2. Generate `CREDENTIALS_KEY`

`CREDENTIALS_KEY` is the 32-byte AES-256-GCM key (base64-encoded) that Nestor uses to encrypt and decrypt per-user secrets in `user_credentials`.

Generate a fresh random key on the host:

```sh
openssl rand -base64 32
```

Notes:
- Keep this value backed up securely (e.g. in your password manager). If you lose or change `CREDENTIALS_KEY`, existing rows in `user_credentials` cannot be decrypted and each user will need to re-run `nestor credentials google-auth <user-id>`.
- Never commit `CREDENTIALS_KEY` to git; store it only in `deploy/.env`.

---

## 3. Generate `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET`

These identify your Nestor host's OAuth 2.0 application to Google. Every Nestor user signs into their own Google account against this single OAuth client.

### 3.1 Create/select a GCP project & enroll in Developer Preview

1. Open the [Google Cloud Console](https://console.cloud.google.com/) and create or select a project for Nestor (for example, `nestor-assistant`).
2. Google's Workspace MCP endpoints (`*.googleapis.com/mcp/v1`) are part of the **Google Workspace Developer Preview Program** — ensure the project/account is enrolled in the Developer Preview Program before testing Workspace MCP calls.

### 3.2 Enable the Workspace & MCP APIs

Enable both the underlying Google APIs and the corresponding MCP APIs in your project:

```sh
gcloud services enable \
  people.googleapis.com \
  calendar-json.googleapis.com \
  calendarmcp.googleapis.com \
  docs.googleapis.com \
  docsmcp.googleapis.com \
  drive.googleapis.com \
  drivemcp.googleapis.com \
  workspacemcp.googleapis.com
```

*(You can also enable these individually under **APIs & Services > Library** in the Cloud Console.)*

### 3.3 Configure Google Auth Platform (OAuth consent screen)

1. Go to **Google Auth Platform > Branding**:
   - Set **App name** (e.g., `Nestor Personal Assistant`), **User support email**, and **Developer contact email**.
2. Go to **Google Auth Platform > Audience**:
   - For a personal `@gmail.com` account, choose **External** (or **Internal** if you are using a Google Workspace organization).
   - While the app is in **Testing** status, add your `@gmail.com` address (and any household members' Google addresses) under **Test users** so Google permits sign-in.
   - **7-day token caveat (External + Testing):** Google automatically expires OAuth refresh tokens after **7 days** for External apps in Testing mode. To avoid weekly re-authentication (`nestor credentials google-auth <user-id>`), publish the app to **Production** once configured (note that requesting sensitive scopes on an unverified Production app shows an "unverified app" warning screen for your own account unless verified).
3. Go to **Google Auth Platform > Data Access** and add the scopes requested by `nestor credentials google-auth`:
   - `https://www.googleapis.com/auth/userinfo.profile`
   - `https://www.googleapis.com/auth/contacts.readonly`
   - `https://www.googleapis.com/auth/calendar.calendarlist.readonly`
   - `https://www.googleapis.com/auth/calendar.events.freebusy`
   - `https://www.googleapis.com/auth/calendar.events`
   - `https://www.googleapis.com/auth/documents`
   - `https://www.googleapis.com/auth/drive.readonly`
   - `https://www.googleapis.com/auth/drive.file`

### 3.4 Create the OAuth 2.0 Client

1. Go to **Google Auth Platform > Clients** and click **Create Client**.
2. Set **Application type** to **Web application**.
3. Give it a name (e.g., `Nestor MCP Client`).
4. Under **Authorized redirect URIs**, click **+ Add URI** and add:
   ```text
   http://127.0.0.1:8085/callback
   ```
   *(This matches the default redirect URI used by `nestor credentials google-auth` in `--manual` mode.)*
5. **Check the box: "Use this client for an AI-powered agent".**
   Google's Workspace MCP servers reject requests from OAuth clients that do not have this flag enabled.
6. Review the Google API Services User Data Policy prompt and click **Create**.
7. Copy the generated **Client ID** (`GOOGLE_OAUTH_CLIENT_ID`) and **Client Secret** (`GOOGLE_OAUTH_CLIENT_SECRET`).

---

## 4. Optional: Generate `GOOGLE_MAPS_API_KEY`

If you also want `google-maps` (`search_places`, `lookup_weather`, `compute_routes`):

1. In the same GCP project, enable the **Maps Grounding Lite / Maps Experience agent tools** (`mapstools.googleapis.com`) and ensure billing is linked (Maps Platform provides monthly free tiers).
2. Go to **APIs & Services > Credentials > Create credentials > API key**.
3. Restrict the API key to `mapstools.googleapis.com` (and optionally your server's public IP) and copy it as `GOOGLE_MAPS_API_KEY`.

---

## 5. Configure `deploy/.env` and restart Nestor

Add the generated values to `deploy/.env` (never commit `deploy/.env`):

```dotenv
CREDENTIALS_KEY=your-base64-32-byte-key
GOOGLE_OAUTH_CLIENT_ID=your-client-id.apps.googleusercontent.com
GOOGLE_OAUTH_CLIENT_SECRET=your-client-secret
GOOGLE_MAPS_API_KEY=your-maps-api-key
```

Start or recreate the `nestor` container so it picks up the environment variables:

```sh
docker compose -f deploy/docker-compose.yml up -d nestor
```

---

## 6. Onboard each user and approve MCP tools

### 6.1 Authorize a user's Google account (`google-auth`)

Run the CLI inside the running `nestor` container for each internal user ID (for single-user v1, user ID `1`):

```sh
docker compose -f deploy/docker-compose.yml exec -it nestor nestor credentials google-auth 1
```

In default `--manual` mode (headless, no exposed ports needed):
1. Copy the printed `https://accounts.google.com/o/oauth2/v2/auth?...` URL into a browser on your laptop or phone and sign in to your Google account.
2. After approving access, Google redirects to `http://127.0.0.1:8085/callback?state=...&code=...` (the browser will show a connection-refused page — that is expected).
3. Copy the **full URL** from the browser address bar and paste it into the `Callback URL:` prompt in your terminal.
4. Nestor verifies `state` and PKCE, exchanges the code for a refresh token, encrypts `{"refresh_token":...}` with `CREDENTIALS_KEY`, and stores it under `(user 1, google_oauth)`.

*(If you run `nestor` directly on a machine with a local browser or SSH port forwarding on port `8085`, you can pass `--listen 127.0.0.1:8085` to capture the callback automatically instead of pasting.)*

To re-authorize later (for example after a 7-day Testing expiry), simply re-run the same command — the encrypted row and nonce are updated and live sessions pick up the new refresh token automatically without restarting Nestor.

### 6.2 Approve newly seen MCP tools

On connect (and every `health_interval: 5m`), Nestor inspects each server's tools and alerts `ADMIN_USER_ID` over Telegram for any tool whose description and schema have not been pinned yet. Every tool stays disabled until you approve it after review:

```sh
# Read-only tools (no Telegram confirmation required at call time)
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-contacts search_contacts
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-contacts get_user_profile
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-calendar list_calendars
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-calendar list_events
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-calendar get_event
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-calendar suggest_time
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-docs read_doc
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-drive search_files
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-drive read_file_content
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-drive list_recent_files
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-drive get_file_metadata
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-drive get_file_permissions
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-workspace-search search_corpus

# Critical write tools (require explicit Telegram approval card before every execution)
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-calendar create_event
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-calendar update_event
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-calendar delete_event
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-calendar respond_to_event
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-docs update_doc
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-drive create_file
docker compose -f deploy/docker-compose.yml exec nestor nestor mcp approve google-drive copy_file
```

Approved tools become active at the next `health_interval` tick (within 5 minutes) or immediately if you restart `nestor`.

---

## 7. Troubleshooting

- **`403` from Workspace MCP endpoints:** Verify that **"Use this client for an AI-powered agent"** is checked on your OAuth client in **Google Auth Platform > Clients**, that both the base API and `*mcp.googleapis.com` API are enabled, and that your GCP project is enrolled in the Google Workspace Developer Preview Program.
- **`redirect_uri_mismatch` during consent:** Ensure `http://127.0.0.1:8085/callback` (exact match, including `127.0.0.1` rather than `localhost`) is listed under **Authorized redirect URIs**.
- **`oauth token response is missing refresh_token`:** Re-run `nestor credentials google-auth <user-id>` (it sets `access_type=offline` and `prompt=consent`); if you previously tested with the same client, revoke app access at [myaccount.google.com/permissions](https://myaccount.google.com/permissions) and run `google-auth` again.
- **`invalid_grant` after 7 days:** Your OAuth consent screen is External in **Testing** mode. Either re-run `nestor credentials google-auth <user-id>` or publish the app to **Production**.
- **`google-workspace-search` returns `403` on a personal `@gmail.com` account:** Some cross-corpus `workspacemcp.googleapis.com` features require a managed Google Workspace organization account. Nestor isolates each server in its own supervisor, so `google-contacts`, `google-calendar`, `google-docs`, and `google-drive` continue working normally even if `google-workspace-search` is unavailable.
