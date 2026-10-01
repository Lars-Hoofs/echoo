/// <reference types="node" />
import { existsSync } from 'node:fs'

import type { ExpoConfig } from 'expo/config'

// One build serves every Echoo installation: the app asks for the server address on first start.
// ECHOO_APP_ID is the bundle identifier (it must match ECHOO_APNS_TOPIC on the server), and
// ECHOO_APNS_MODE is "development" for builds run from Xcode against the APNs sandbox.
const appId = process.env.ECHOO_APP_ID ?? 'nl.echoo.app'
const apnsMode = process.env.ECHOO_APNS_MODE === 'development' ? 'development' : 'production'
// Firebase's config file for FCM (Android push). Without it the app builds, but gets no pushes.
const googleServices = './google-services.json'

const config: ExpoConfig = {
  name: 'Echoo',
  slug: 'echoo',
  scheme: 'echoo',
  version: '1.0.0',
  orientation: 'portrait',
  icon: './assets/icon.png',
  userInterfaceStyle: 'automatic',
  ios: {
    bundleIdentifier: appId,
    supportsTablet: true,
    infoPlist: { ITSAppUsesNonExemptEncryption: false },
  },
  android: {
    package: appId,
    adaptiveIcon: {
      foregroundImage: './assets/android-icon-foreground.png',
      backgroundColor: '#35e27a',
      monochromeImage: './assets/android-icon-monochrome.png',
    },
    googleServicesFile: existsSync(googleServices) ? googleServices : undefined,
    predictiveBackGestureEnabled: false,
  },
  plugins: [
    './plugins/withSceneLifecycle',
    'expo-router',
    'expo-secure-store',
    [
      'expo-notifications',
      { icon: './assets/notification-icon.png', color: '#35e27a', defaultChannel: 'echoo_urgent', mode: apnsMode },
    ],
    [
      'expo-font',
      {
        fonts: ['./assets/fonts/Urbanist-Light.ttf', './assets/fonts/Urbanist-Regular.ttf', './assets/fonts/Urbanist-Medium.ttf'],
      },
    ],
    [
      'expo-splash-screen',
      {
        image: './assets/splash-icon.png',
        imageWidth: 96,
        backgroundColor: '#ececec',
        dark: { image: './assets/splash-icon.png', backgroundColor: '#0b0c0d' },
      },
    ],
  ],
  experiments: { typedRoutes: true },
}

export default config
