const { mkdirSync, writeFileSync } = require('node:fs')
const { join } = require('node:path')

const { IOSConfig, withAppDelegate, withInfoPlist, withXcodeProject } = require('expo/config-plugins')

// iOS 27 terminates apps at launch that do not use the scene-based life cycle. Expo 57 ships the
// scene delegate (ExpoAppSceneDelegate) but its prebuild template still starts React Native from
// the app delegate. This applies what the SDK 58 template does: the scene delegate creates the
// window and starts React Native, the app delegate only creates the factory.

const sceneDelegate = `internal import Expo

@objc(SceneDelegate)
class SceneDelegate: ExpoAppSceneDelegate {
}
`

// Fails the prebuild instead of silently producing an app that crashes on launch.
function replaceOnce(contents, search, replacement, what) {
  if (!search.test(contents)) throw new Error(`withSceneLifecycle: ${what} not found in AppDelegate.swift`)
  return contents.replace(search, replacement)
}

/** @type {import('expo/config-plugins').ConfigPlugin} */
module.exports = function withSceneLifecycle(config) {
  config = withInfoPlist(config, (c) => {
    c.modResults.UIApplicationSceneManifest = {
      UIApplicationSupportsMultipleScenes: false,
      UISceneConfigurations: {
        UIWindowSceneSessionRoleApplication: [
          { UISceneConfigurationName: 'Default Configuration', UISceneDelegateClassName: '$(PRODUCT_MODULE_NAME).SceneDelegate' },
        ],
      },
    }
    return c
  })

  config = withAppDelegate(config, (c) => {
    if (c.modResults.language !== 'swift') throw new Error('withSceneLifecycle: expected a Swift AppDelegate')
    let src = c.modResults.contents
    if (!src.includes('ExpoReactNativeFactoryProvider')) {
      src = replaceOnce(src, /class AppDelegate: ExpoAppDelegate \{/, 'class AppDelegate: ExpoAppDelegate, ExpoReactNativeFactoryProvider {', 'the AppDelegate class')
    }
    if (src.includes('startReactNative(')) {
      src = replaceOnce(src, /\n#if os\(iOS\) \|\| os\(tvOS\)\n\s*window = UIWindow[\s\S]*?#endif\n/, '\n', 'the window setup')
    }
    c.modResults.contents = src
    return c
  })

  return withXcodeProject(config, (c) => {
    const name = IOSConfig.XcodeUtils.getProjectName(c.modRequest.projectRoot)
    const dir = join(c.modRequest.platformProjectRoot, name)
    mkdirSync(dir, { recursive: true })
    writeFileSync(join(dir, 'SceneDelegate.swift'), sceneDelegate)
    const filepath = `${name}/SceneDelegate.swift`
    if (!c.modResults.hasFile(filepath)) {
      IOSConfig.XcodeUtils.addBuildSourceFileToGroup({ filepath, groupName: name, project: c.modResults })
    }
    return c
  })
}
