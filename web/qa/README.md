# Frontend verification

> 2026-10-17：浅色云平台升级及最新截图、验收记录见 [ui-upgrade/README.md](ui-upgrade/README.md)。下文保留为原版本发布验收历史。

Verified 2026-10-16 against the actual embedded Go application at isolated localhost test service `127.0.0.1:18093`, using an existing disposable test account. No user credentials were used, printed, saved in the browser or included in screenshots.

## Automated checks

- `pnpm --dir web typecheck`: passed strict Vue/TypeScript checking.
- `pnpm --dir web test`: **20 tests in 9 files passed**. Covers CSRF/API errors, auth state, explicit destructive confirmation, disconnected dashboard, backup/restore source and recreation forms, unsaved Compose edits, drawer/modal errors, task/audit pagination, actual audit API field names, wrapped resource titles, PTY initialization/cleanup and real Naive UI input labels and confirmation autofocus.
- `pnpm --dir web build`: passed. Vite reports the existing non-failing 500 kB chunk advisory; current main JavaScript is about 503 kB before compression.
- `pnpm --dir web audit --prod`: **No known vulnerabilities found**; Vue is pinned to 3.5.42.

## Real browser checks

Chrome desktop **1230 × 757** and responsive viewport **390 × 844**:

- Signed in through the real login form with the disposable account.
- Navigated dashboard, containers, images/registries, networks, volumes/mounts, Compose, backups, tasks and settings/audit at both sizes. Mobile menu opens, navigates and closes; the document has no horizontal overflow. Wide resource tables retain their own horizontal scrolling.
- Actual Docker resource rows render; resource counts may change between screenshots because isolated Linux integration tests run concurrently. Compose, backup repositories/policies and task history show actual empty states. Creating a policy without a repository is disabled.
- Task SSE reports connected. Missing Compose/Restic host tools and ungranted daemon capabilities display their actual reasons; daemon configuration/restart controls remain disabled.
- Submitting an invalid mirror origin with an empty image field produces the actual API validation error visibly, without an image pull or daemon change. Evidence: [validation-error-mobile.png](validation-error-mobile.png).
- Browser console returned no warning/error entries at the end of navigation.
- The audit table field mismatch found during this run is fixed with an API-shaped regression fixture. After the actual embedded service was rebuilt and restarted, the browser loaded `/assets/index-C37pCd6h.js`; real audit records display the actor ID and resource type/ID at both sizes. Settings and validation-error screenshots were replaced after this verification. Details are recorded in `browser-results.json`.

Screenshots: [dashboard desktop](dashboard-desktop.png), [dashboard mobile](dashboard-mobile.png), [containers](containers-desktop.png), [images](images-desktop.png), [networks](networks-desktop.png), [volumes](volumes-desktop.png), [Compose](compose-desktop.png), [backups](backups-desktop.png), [tasks](tasks-desktop.png), [settings desktop](settings-desktop.png), [settings mobile](settings-mobile.png). All exclude passwords.

## Scope and limitations

The initial navigation browser checks were read-only apart from login and deliberate invalid-input validation. Container mutations, terminal interaction, rebuild rollback, backup/restore execution and daemon operations were not exercised in that initial navigation run; the authorized follow-up coverage is listed below. Component tests and the separate Linux integration evidence must not be represented as browser end-to-end coverage of those operations. Offline dashboard behavior is covered by a component test; this browser run used the connected isolated Engine.

Historical [initialization screenshot](initialization-desktop.png) was inspected at 1280 × 720 before the authenticated run. The earlier lack of authenticated browser QA resulted from the agent's conservative interpretation of browser policy, **not an actual tool or automatic-approval rejection**. That earlier wording has been corrected here.

Visual review uses the intended industrial operations direction: warm off-white surface, dark navigation, restrained teal/orange status, readable dense tables. No external reference image was supplied; this is layout/readability self-review, not a pixel-fidelity comparison. See `visual-verdict.json`.

No demo resource counts or simulated operational records are included in the product. Unknown metrics render as “—”; Engine disconnection hides resource statistics and displays the actual failure.

## Additional release browser acceptance

A separate native Linux application on `127.0.0.1:18163` uses its own database, allowed paths and disposable administrator. Every mutable Docker fixture belongs to label `dm.ui.release=ui-51da1725` or the dedicated `ui-51da1725-compose` project. Shared Engine/VM services are never restarted. Details and screenshots are in `release-browser-results.json` and `release/`.

- Submitted a deliberately invalid standalone-container command through the configuration preview/confirmation UI. Task failed with the actual OCI error; Engine inspection confirmed the original ID/running state and mounted volume were restored.
- Navigated container → Volume → container, checked both running and stopped references, and confirmed referenced Volume/image deletion is rejected with the error visible in the confirmation modal.
- Tested actual Tail=3 timestamped and plain log output, killed only this test application and restarted it, and confirmed all eight injected gap markers were automatically replayed in both modes. This uncovered and verified fixes for backend reconnect tail truncation and missing plain-log event IDs. Duplicate boundary lines are expected.
- Executed a command in the real TTY, verified `stty size` reports `24 80`, interrupted `sleep` with Ctrl+C, displayed the missing `/bin/bash` error, and confirmed no interactive shell remains after closing details.
- Imported the running Compose project without changing its container, followed Compose-container guidance, saved/validated YAML, saw invalid YAML errors, previewed and deployed an environment change, and rejected an externally changed source until a new preview.
- Ran a real file backup after explicit consistency-risk acknowledgement; then injected unreadable-file permissions and confirmed a Failed task at 25%, rather than success. Canceled a 100000-file directory scan through the task UI and observed final Canceled status and its actual error/log.
- Checked unavailable daemon controls. To inspect the restart-risk confirmation, used an owned dummy daemon file with a test-only `systemctl` stub that always refuses execution; canceled the modal without submitting apply. The real daemon configuration checksum remained unchanged.
- Discovered Enter-key autofocus could implicitly tick the acknowledgement checkbox; fixed the shared confirmation dialog to retain the unchecked state and focus Cancel after entering. Repeated actual keyboard opening verifies the execution button stays disabled.

The earlier read-only navigation run remains historical evidence. Mutations above were performed only in this later, explicitly authorized disposable environment; they must not be attributed to that earlier run.

- Stopped only an owned Unix-socket Docker proxy: the dashboard automatically replaced live metrics with the actual connection error, then recovered its metrics after proxy restart without a manual refresh. The shared Engine remained running.
- Deleted an owned stopped test container through the UI and verified its named Volume and bind-mounted file remained. Actual audit records show the test actor, target, timestamp and queued/final result.
- Restic exit-3 partial snapshots exposed a release-blocking gap: they were previously indistinguishable from complete backups and allowed an ordinary restore preview. The backend now persists completion verification in the repository and restricts retention/recovery, and the UI marks unverified snapshots prominently. A separate rescue acknowledgement enables only new-directory file recovery, with a second final confirmation; overwrite and service recreation remain disabled. The actual partial rescue correctly omitted the unreadable blob, proving it cannot be described as a complete backup.
- Created a new verified snapshot under the fixed protocol, restored it through the default new-directory wizard, and verified both files' SHA256, UID/GID, permissions and sizes against the source. No services were started by file-only restoration.
- Checked the populated snapshot list and rescue modal at 390px; metadata now occupies a full-width row above the action. No horizontal document overflow.

Final embedded assets verified in the isolated service: `index-Cwzy2wtV.js`, `index-BKEA8bZR.css`. The source and assets were frozen after the 20-test/type/build pass. Final browser console inspection since the last asset reload returned no warning/error entries; intentional failures/outages earlier in the run are recorded separately in scenarios. The daemon restart-risk modal was canceled: actual daemon configuration bytes were never changed and no daemon apply request was submitted.

Cleanup completed after evidence capture: stopped the owned application/proxy, removed 4 labeled containers, 2 labeled Volumes, the dedicated Compose network and image, the native fixture tree (including 100000 scan files), and the plaintext disposable login file. No owned Docker resources remain. The shared Engine/VM and other agents' resources were left running for their independent platform checks. `release-browser-results.json` is complete with no pending scenarios.
