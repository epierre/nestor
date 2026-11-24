# Creating `GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET`

Follow these steps in the [Google Cloud Console](https://console.cloud.google.com/) to create the OAuth 2.0 client credentials used by Nestor's Google Workspace MCP servers (`google-contacts`, `google-calendar`, `google-docs`, `google-drive`, and `google-workspace-search`).

## 1. Select a project & enable APIs

1. Create or select a Google Cloud project (enrolled in the **Google Workspace Developer Preview Program** for `*.googleapis.com/mcp/v1` endpoints).
2. Enable both the base Google Workspace APIs and their MCP APIs (via **APIs & Services > Library** or `gcloud`):

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

## 2. Configure Google Auth Platform (consent screen)

1. **Branding:** Open **Google Auth Platform > Branding**, set an **App name** (e.g. `Nestor`), **User support email**, and **Developer contact email**.
2. **Audience:** Open **Google Auth Platform > Audience**:
   - Choose **External** for personal `@gmail.com` accounts (or **Internal** for a Google Workspace organization).
   - While the app status is **Testing**, add your `@gmail.com` address (and any other household users) under **Test users**.
   - **7-day token expiry note:** External apps in **Testing** mode automatically expire refresh tokens after **7 days**. Publish to **Production** once configured if you want to avoid re-running `nestor credentials google-auth <user-id>` weekly.
3. **Data Access (Scopes):** Open **Google Auth Platform > Data Access** and add the scopes requested by `nestor credentials google-auth`:
   - `https://www.googleapis.com/auth/userinfo.profile`
   - `https://www.googleapis.com/auth/contacts.readonly`
   - `https://www.googleapis.com/auth/calendar.calendarlist.readonly`
   - `https://www.googleapis.com/auth/calendar.events.freebusy`
   - `https://www.googleapis.com/auth/calendar.events`
   - `https://www.googleapis.com/auth/documents`
   - `https://www.googleapis.com/auth/drive.readonly`
   - `https://www.googleapis.com/auth/drive.file`

## 3. Create the OAuth 2.0 Client

1. Open **Google Auth Platform > Clients** and click **Create Client**.
2. Set **Application type** to **Web application** and give it a name (e.g. `Nestor MCP Client`).
3. Under **Authorized redirect URIs**, click **+ Add URI** and enter:
   ```text
   http://127.0.0.1:8085/callback
   ```
4. Check **"Use this client for an AI-powered agent"** (Google's Workspace MCP servers reject clients without this setting).
5. Accept the Google API Services User Data Policy prompt and click **Create**.
6. Copy the **Client ID** into `GOOGLE_OAUTH_CLIENT_ID` and the **Client Secret** into `GOOGLE_OAUTH_CLIENT_SECRET` in `deploy/.env`.

For full setup (including `CREDENTIALS_KEY`, `nestor credentials google-auth`, and `nestor mcp approve`), see [google_mcps.md](google_mcps.md).
