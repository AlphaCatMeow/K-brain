# Live calendar subscription acceptance

Subscriptions consume published ICS URLs; they do not implement Google OAuth, Apple CalDAV or Microsoft Graph account synchronization. A web login page is not a subscription feed. Publishing a calendar can expose it to anyone holding the link; select the account's intended sharing scope.

## Provider inputs

- Google: Settings → Integrate calendar → public or secret iCal address.
- iCloud: calendar sharing → Public Calendar → copy `webcal://` link. The backend normalizes it to HTTPS. Private CalDAV calendars need an account adapter, not this published-feed importer.
- Outlook/Exchange: Settings → Calendar → Shared calendars → Publish calendar → ICS. Tenant policy may disable publication; an Outlook browser URL is not interchangeable with ICS.

Keep private URLs outside reports and source control. Public snapshots only expose the provider hostname; errors are stable codes without response bodies or credential-bearing URLs.

## Procedure

1. Create a subscription using `subscription.create` with `name`, `url`, `refreshMinutes:15`.
2. Wait up to 60 seconds for the backend refresh and query its `subscriptions` status. Require `lastSyncedAt` and no `lastError`; also inspect imported `events` for the calendar ID.
3. Compare at least one timed/all-day entry to the source calendar, including its timezone. Create/change/delete a controlled source event if the account is writable; explicitly refresh and verify stable identities, updates and removal.
4. Restart the backend, query persisted records, then refresh again. Test revocation or a deliberately invalid replacement URL without destroying the last good records.
5. Remove the test subscription when finished. Never report fixture coverage as private-account acceptance.

## 2026-10-06 results

- Real Google official US holiday calendar: `https://calendar.google.com/calendar/ical/en.usa%23holiday%40group.v.calendar.google.com/public/basic.ics`, HTTPS 200, 120685 bytes; actual desktop subscription imported **30** occurrences in the backend's rolling window with no error. Public feed only; no private Google account writeback or source modification tested.
- iCloud and Exchange: no configured test subscriptions or dedicated acceptance URL environment variables were available. Actual account-specific acceptance remains pending the corresponding published ICS URLs. No personal account databases or keychains were inspected.
- Local HTTP regression cases distinguish 401/403 (authentication required), 404/410 (unavailable), 429 (rate limiting), non-ICS HTML and generic transport failure. Malformed refreshes preserve existing events. These cases are protocol tests, not external-provider certification.
