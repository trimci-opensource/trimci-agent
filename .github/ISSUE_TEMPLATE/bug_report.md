---
name: Bug report
about: The agent misbehaves (crash, wrong data, stuck sync, noisy logs)
labels: bug
---

<!-- Security issues: do NOT file them here — see SECURITY.md. -->

**Agent version** (`trimci-agent -version`):

**How it runs** (docker / compose / systemd binary / cron `-once`):

**GitLab** (version from `/api/v4/version`, gitlab.com or self-hosted, behind a proxy?):

**What happened / what you expected:**

**Logs** — run with `LOG_LEVEL=debug`, paste the relevant lines. The agent scrubs its own
secrets, but please double-check that no tokens or internal hostnames you consider
sensitive are included:

```
(paste here)
```

**Dashboard state** (Integration health label, e.g. "Agent online" / "Sync error" + text):
