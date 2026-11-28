# Scheduled outbound calls on the ThinkPad

The CRM handles two separate events. **Saving a lead** can register its phone number for outbound routing. **Reaching the scheduled minute** can place a call. Registration runs on a filesystem event. Dialing runs on a user-level systemd timer, with the weekly plan interpreted in Shanghai time (UTC+8).

## Configure a lead

Open **Schedule → New lead** or select an existing lead. Enter the name and phone number, choose a call purpose, select the `schedule` tag, set a local time and repeat days, and record outbound consent. Select **Save lead**. A save that reports *Registration queued* has created a durable request. The registrar still has to verify routing before the number is ready to dial.

| Call purpose | Instructions sent at dial time |
| --- | --- |
| Normal call (`normal` in the API) | The saved template, variables and greeting follow the default CRM rendering path. |
| Follow-up, reminder, survey | The scheduler renders the same default prompt, then appends the latest nonempty **Schedule → Business content** text for the selected purpose. The greeting comes from the default render. |

**Upload CSV** uses the same lead-save and registration path. The UTF-8 file requires `name` and `phone`. Optional columns are `purpose`, `call_time`, `repeat_days` and `outbound_consent`. For a schedule, supply both a time such as `14:30` and days such as `Mon|Wed|Fri`. Omitted purpose defaults to follow-up and omitted consent defaults to false. The importer validates the file before writing, accepts up to 1,000 rows and 2 MiB, and skips existing phone numbers and duplicates within the upload. To change an existing lead, edit it in the panel. [CSV template](../../internal/crm/ui/lead-template.csv) · [Import handler](../../internal/crm/import_csv.go)

## What happens on Save

1. The CRM checks for a saved plan, the `schedule` tag, explicit consent and a supported Chinese mobile number. Deleted and do-not-call leads are excluded. Under the lead lock, the CRM creates a ticket in `~/golive-crm/registration-queue/` and appends the versioned lead event. The ticket identifies the lead and save version. [Save handler](../../internal/crm/leads.go) · [Ticket writer](../../internal/crm/registration.go)
2. The system unit `golive-auto-registration.path` watches for `*.job`. Its worker reads the committed leads through the authenticated CRM API and processes tickets that still match the current version and eligibility. Saves arriving during a run leave tickets for the next activation. A failed or busy run keeps its tickets for retry. [Path unit](../../deploy/services/box/golive-auto-registration.path) · [Worker unit](../../deploy/services/box/golive-auto-registration.service)
3. The worker deduplicates normalized numbers and adds exact destinations to the scheduler policy, Aria and RustPBX. It checks the PBX configuration and previews Aria routing before acknowledging the tickets. The manual-call default remains configured by `ALLOWED_CALLEE`. Enrollment is additive. Removing a schedule or withdrawing consent stops future scheduled dispatch for that lead and leaves previously registered routes in place. [Registrar](../../scripts/add-outbound-recipient.py)

The one-time administrator installer runs from the verified release directory on the box. It reconciles existing eligible schedules, installs the privileged worker, and enables the path watcher. Ordinary lead saves after installation need no administrator command. [Installer](../../deploy/services/box/install-auto-registration.sh)

## What happens at the scheduled minute

The user unit `golive-scheduled-calls.timer` wakes the dispatcher every minute. The dispatcher checks the weekday, Shanghai wall-clock time, consent, do-not-call status, tag and allowed destination. It takes the shared call lock, requires an idle engine and the original local GoLive route, and rechecks the lead before dialing through Aria. [Timer](../../deploy/services/box/golive-scheduled-calls.timer) · [Dispatcher](../../scripts/scheduled-calls.py)

**Plan at most one call per minute across all leads.** The SQLite ledger at `~/golive-crm/scheduled-calls.sqlite` has one claim per minute for the shared line. The dispatcher commits `pending` before sending the dial request and records `accepted` with the call ID when Aria confirms. A missed minute is skipped. A `pending` row can mean the originator received the request but its response was lost. Check the call journal before considering another attempt.

## Diagnose a missed call

Run these read-only checks on the ThinkPad. Registration uses **system** units. Scheduled dialing uses **user** units under the deployment account.

```sh
systemctl is-active golive-auto-registration.path
journalctl -u golive-auto-registration.service --since today --no-pager
systemctl --user list-timers golive-scheduled-calls.timer --all
journalctl --user -u golive-scheduled-calls.service --since today --no-pager
```

First compare the saved weekday and time with the intended Shanghai minute. If the dial log reports an unlisted recipient, check the registration worker for that save. If there is an accepted call ID, use it to find the recording and transcript in the call journal. [Call operations](../../CALL-OPERATIONS.md)

On 2026-11-25, the owner activated the registrar and confirmed the workflow worked. Read-only checks on the ThinkPad confirmed an enabled and active watcher, an empty registration queue, and agreement between the scheduler, Aria routing preview and PBX route for the registered recipient.
