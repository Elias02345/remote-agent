import java.util.Properties

plugins {
    id("com.android.application")
    // The Flutter Gradle Plugin must be applied after the Android and Kotlin Gradle plugins.
    id("dev.flutter.flutter-gradle-plugin")
}

// Release signing comes from android/key.properties, which is gitignored and
// never committed — it names a keystore and holds its passwords.
//
// See app/README.md for how to generate one. Absent, release builds fail
// rather than falling back to the debug key: an APK signed with the debug
// keystore cannot be updated by one signed with the real key later, so
// shipping even one debug-signed build to a device means that device can never
// take an update without uninstalling and losing its paired identity.
val keystoreProperties = Properties().apply {
    val f = rootProject.file("key.properties")
    if (f.exists()) f.inputStream().use { load(it) }
}

android {
    namespace = "io.github.elias02345.claudecode_remote"
    compileSdk = flutter.compileSdkVersion
    ndkVersion = flutter.ndkVersion

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    defaultConfig {
        // com.example.* is the Flutter template's placeholder. It is not a
        // namespace this project owns, Play refuses it outright, and an
        // application id is permanent once anything is installed under it.
        applicationId = "io.github.elias02345.claudecode_remote"
        minSdk = flutter.minSdkVersion
        targetSdk = flutter.targetSdkVersion
        versionCode = flutter.versionCode
        versionName = flutter.versionName
    }

    signingConfigs {
        if (keystoreProperties.isNotEmpty()) {
            create("release") {
                storeFile = keystoreProperties["storeFile"]?.let { rootProject.file(it) }
                storePassword = keystoreProperties["storePassword"] as String?
                keyAlias = keystoreProperties["keyAlias"] as String?
                keyPassword = keystoreProperties["keyPassword"] as String?
            }
        }
    }

    buildTypes {
        release {
            if (keystoreProperties.isEmpty()) {
                // No silent fallback to the debug key. A build that cannot be
                // signed properly must fail loudly at assembly time, not
                // produce an artifact that looks releasable and is not.
                signingConfig = null
            } else {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }
}

// Turns "unsigned APK" into an error with an explanation, at the moment
// someone actually asks for a release build — rather than at install time on a
// user's phone.
tasks.matching { it.name.startsWith("assembleRelease") || it.name.startsWith("bundleRelease") }
    .configureEach {
        doFirst {
            if (keystoreProperties.isEmpty()) {
                throw GradleException(
                    "android/key.properties is missing, so this release build cannot be signed. " +
                        "Create it (storeFile, storePassword, keyAlias, keyPassword) as described in " +
                        "app/README.md, or build a debug variant instead. Debug-signing a release is " +
                        "not offered on purpose: a device that installs a debug-signed build can never " +
                        "take a properly signed update."
                )
            }
        }
    }

kotlin {
    compilerOptions {
        jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
    }
}

flutter {
    source = "../.."
}
