# Echoo on phones

There are two ways to use Echoo on a phone, and both get push notifications:

- **The native apps** (`mobile/`, React Native with Expo) for iOS and Android. Native screens for
  what an agent does on call: the inbox, conversations, replying, notes, status, assignment,
  snooze, priority, notifications, search and push settings. Workspace settings, reports and
  campaigns stay in the web app ("Openen in de browser" in the app).
- **The web app installed to the home screen** (a PWA). Every screen of Echoo, with Web Push.

One app build works with every Echoo installation: the app asks for the address on first start.

## Push notifications

The server sends a push for the notification kinds a user chose under Meldingen (web) or Ik
(app): a customer replies in their conversation, a conversation is assigned to them, someone
mentions them, an SLA deadline is at risk. Low CSAT ratings only appear in the notification list.

| Device | Service | Server configuration |
|---|---|---|
| Browser, installed web app | Web Push (VAPID) | None. The key pair is created on first start and stored encrypted (`push_vapid`, covered by key rotation). |
| iOS app | Apple Push Notification service | `ECHOO_APNS_KEY`, `ECHOO_APNS_KEY_ID`, `ECHOO_APNS_TEAM_ID`, `ECHOO_APNS_TOPIC`, optionally `ECHOO_APNS_ENVIRONMENT=sandbox` |
| Android app | Firebase Cloud Messaging (HTTP v1) | `ECHOO_FCM_CREDENTIALS` |

Echoo talks to Apple and Google directly; there is no relay in between.

**What a push contains.** The title says what happened ("Nieuw antwoord van de klant"). Web Push
is encrypted end to end to the browser (RFC 8291) and adds the conversation number and subject. APNs
and FCM can read what they deliver, so native pushes carry only the conversation number, like
notification mail.

**Which devices.** A device belongs to the session that registered it. Logging out, "log out
everywhere" and revoking a session remove it; a session that merely expires keeps it, because an
agent on call may not open the app for weeks. Deactivated users get nothing. Each user sees and
removes their devices under Meldingen and can send a test notification there.

**Endpoints.** Browsers hand out Web Push endpoints; Echoo only sends to the known push services
(Google, Mozilla, Apple, Microsoft) and never to internal addresses.

With Docker Compose, put the key files in `conf/` next to the other secrets (`chmod 644`, like
`scripts/setup.sh` does: the directory itself is private and the container runs as UID 65532),
mount them with a `docker-compose.override.yml` and point the `_FILE` variables at them in `.env`:

```yaml
services:
  echoo:
    volumes:
      - ./conf/apns.p8:/run/secrets/apns.p8:ro
      - ./conf/firebase.json:/run/secrets/firebase.json:ro
```

### iOS (APNs)

1. In the Apple developer account, create a key with "Apple Push Notifications service (APNs)"
   enabled and download `AuthKey_<KEYID>.p8`.
2. Register the app's bundle identifier (default `nl.echoo.app`, see below) with the Push
   Notifications capability.
3. Configure Echoo:

   ```sh
   ECHOO_APNS_KEY_FILE=/run/secrets/apns.p8
   ECHOO_APNS_KEY_ID=ABC123DEFG
   ECHOO_APNS_TEAM_ID=TEAM123456
   ECHOO_APNS_TOPIC=nl.echoo.app        # the bundle identifier
   # ECHOO_APNS_ENVIRONMENT=sandbox     # only for builds run from Xcode
   ```

### Android (FCM)

1. Create a Firebase project and add an Android app with the package name (default `nl.echoo.app`).
2. Download `google-services.json` into `mobile/` before building (it is not committed).
3. Under Project settings > Service accounts, create a private key and give it to Echoo:

   ```sh
   ECHOO_FCM_CREDENTIALS_FILE=/run/secrets/firebase.json
   ```

## Building the apps

Requirements: Node 24, Xcode (iOS) and/or Android Studio with a JDK (Android). The `ios/` and
`android/` projects are generated from `mobile/app.config.ts` and not committed.

```sh
cd mobile
npm ci
npm run typecheck && npm run lint && npm test

# iOS: generate the project, then build and run it on a simulator or device
npx expo prebuild --platform ios
npx expo run:ios                       # or open ios/Echoo.xcworkspace in Xcode

# Android: google-services.json in mobile/ first, for push
npx expo prebuild --platform android
npx expo run:android
```

Build settings, read by `app.config.ts`:

| Variable | Default | Meaning |
|---|---|---|
| `ECHOO_APP_ID` | `nl.echoo.app` | Bundle identifier and Android package. Must equal `ECHOO_APNS_TOPIC` on the server. |
| `ECHOO_APNS_MODE` | `production` | `development` for builds run from Xcode, which get APNs sandbox tokens (set `ECHOO_APNS_ENVIRONMENT=sandbox` on the server for those). |

For the App Store and Play Store, build a release in Xcode (Product > Archive) or Android Studio
(Build > Generate Signed Bundle), or with EAS (`npx eas-cli build`), which also handles signing.

Push notifications do not arrive in the iOS simulator or an Android emulator without Google Play;
test them on a real device, with the "Testmelding sturen" button under Ik.

### Development against a local server

The app accepts plain http only for the device itself: `http://localhost:8080` in the iOS
simulator and `http://10.0.2.2:8080` in the Android emulator. Run Echoo with that address as
`ECHOO_BASE_URL`, because the server checks the Origin of requests against it.

## The installed web app (PWA)

Open Echoo in the phone's browser and add it to the home screen: Safari > Deel > Zet op
beginscherm (iOS 16.4 or later), or the browser's install prompt on Android. Then turn on
"Op dit apparaat" under Instellingen > Meldingen. On iOS, Web Push only works in the installed
app, not in a Safari tab.
